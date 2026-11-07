//go:build integration

package issue711_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// This driver adds controlled round-trip latency while forwarding every call
// to real PostgreSQL. Lock existence/waiting and Redis state are independently
// asserted below; no query, row, lock or publication result is mocked.
type latencyDriver struct {
	*entsql.Driver
	calls atomic.Int64
}

type latencyTx struct {
	dialect.Tx
	driver *latencyDriver
}

func (d *latencyDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return &latencyTx{Tx: tx, driver: d}, nil
}

func (tx *latencyTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "pg_advisory_xact_lock") {
		tx.driver.calls.Add(1)
		timer := time.NewTimer(2 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return tx.Tx.(interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}).ExecContext(ctx, query, args...)
}

func (tx *latencyTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.Tx.(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}).QueryContext(ctx, query, args...)
}

type fixture struct {
	db     *sql.DB
	repo   [2]service.AccountRepository
	cache  [2]service.SchedulerCache
	redis  [2]*redis.Client
	driver [2]*latencyDriver
	ids    []int64
	bucket service.SchedulerBucket
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("ISSUE711_POSTGRES_DSN")
	addresses := []string{os.Getenv("ISSUE711_REDIS_A"), os.Getenv("ISSUE711_REDIS_B")}
	if dsn == "" || addresses[0] == "" || addresses[1] == "" {
		if os.Getenv("ISSUE711_REQUIRED") != "" {
			t.Fatal("issue711 CI requires PostgreSQL and two independent Redis servers")
		}
		t.Skip("dedicated issue711 CI supplies isolated PostgreSQL and two Redis servers")
	}
	require.NotEqual(t, addresses[0], addresses[1])
	ctx := context.Background()
	f := &fixture{bucket: service.SchedulerBucket{Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}}
	for i := range 2 {
		db, err := sql.Open("postgres", dsn+fmt.Sprintf("&application_name=issue711-writer-%d", i))
		require.NoError(t, err)
		db.SetMaxOpenConns(8)
		t.Cleanup(func() { _ = db.Close() })
		require.NoError(t, db.PingContext(ctx))
		if i == 0 {
			f.db = db
			require.NoError(t, repository.ApplyMigrations(ctx, db))
			_, err = db.ExecContext(ctx, "TRUNCATE accounts, scheduler_outbox, scheduler_outbox_retries RESTART IDENTITY CASCADE")
			require.NoError(t, err)
		}
		f.redis[i] = redis.NewClient(&redis.Options{Addr: addresses[i], ContextTimeoutEnabled: true, MaxRetries: -1})
		t.Cleanup(func() { _ = f.redis[i].Close() })
		require.NoError(t, f.redis[i].FlushDB(ctx).Err())
		f.cache[i] = repository.NewSchedulerCache(f.redis[i])
		f.driver[i] = &latencyDriver{Driver: entsql.OpenDB(dialect.Postgres, db)}
		client := dbent.NewClient(dbent.Driver(f.driver[i]))
		f.repo[i] = repository.NewAccountRepository(client, db, f.cache[i])
	}
	var version int
	require.NoError(t, f.db.QueryRowContext(ctx, "SHOW server_version_num").Scan(&version))
	require.GreaterOrEqual(t, version, 180000)
	require.Less(t, version, 190000)
	rows, err := f.db.QueryContext(ctx, `INSERT INTO accounts (name,platform,type,status,schedulable,credentials,extra)
SELECT 'synthetic-711-' || n, 'openai', 'oauth', 'active', true,
'{"access_token":"synthetic-old","account_scheduling_threshold":90}'::jsonb,
'{"openai_excel_bps":true,"codex_5h_used_percent":10,"codex_usage_observed_at_ns":"100"}'::jsonb
FROM generate_series(1,512) AS n RETURNING id`)
	require.NoError(t, err)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		f.ids = append(f.ids, id)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, f.ids, 512)
	return f
}

func (f *fixture) publish(ctx context.Context, writer int, ids []int64, before func()) error {
	token, err := f.cache[writer].CaptureBucketWriteToken(ctx, f.bucket)
	if err != nil {
		return err
	}
	return f.repo[writer].(service.LatestSchedulerAccountsRepository).WithLatestSchedulerAccounts(ctx, ids, func(accounts []service.Account) error {
		if before != nil {
			before()
		}
		return f.cache[writer].SetSnapshot(ctx, f.bucket, token, accounts)
	})
}

func assertAccount(t *testing.T, cache service.SchedulerCache, id int64) {
	t.Helper()
	account, err := cache.GetAccount(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, account)
	require.False(t, account.Schedulable, "stale rebuild restored a disabled account")
	require.Equal(t, service.StatusDisabled, account.Status)
	require.Equal(t, "synthetic quality disable", account.ErrorMessage)
	require.Equal(t, false, account.Extra["openai_excel_bps"], "stale rebuild restored BPS")
	require.Equal(t, float64(100), account.Extra["codex_5h_used_percent"])
	require.Equal(t, "200", account.Extra[service.CodexUsageObservationVersionKey])
	require.Equal(t, "synthetic-new", account.Credentials["access_token"])
}

func disable(t *testing.T, f *fixture) {
	t.Helper()
	_, err := f.db.ExecContext(context.Background(), `UPDATE accounts SET status='disabled', schedulable=false,
error_message='synthetic quality disable', credentials=credentials || '{"access_token":"synthetic-new"}'::jsonb,
extra=extra || '{"openai_excel_bps":false,"codex_5h_used_percent":100,"codex_usage_observed_at_ns":"200"}'::jsonb WHERE id=$1`, f.ids[0])
	require.NoError(t, err)
}

func TestIssue711BatchedPublicationContention(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	a, b := make(chan error, 1), make(chan error, 1)
	ids := slices.Clone(f.ids)
	slices.Reverse(ids)
	ids = append(ids, ids[0], 0, -1)
	go func() {
		a <- f.publish(ctx, 0, ids, func() {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("writer A never acquired publication locks")
	}
	var held int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid
WHERE a.application_name='issue711-writer-0' AND l.locktype='advisory' AND l.granted`).Scan(&held))
	require.Equal(t, len(f.ids), held, "every distinct account fence must exist in real PostgreSQL")
	// DB writes can commit while the first publication holds its locks. The
	// second publisher must wait and re-read those committed fields afterward.
	disable(t, f)
	go func() { b <- f.publish(ctx, 1, f.ids, nil) }()
	require.Eventually(t, func() bool {
		var waiting int
		err := f.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name='issue711-writer-1'
AND wait_event_type='Lock' AND wait_event='advisory'`).Scan(&waiting)
		return err == nil && waiting > 0
	}, 5*time.Second, 10*time.Millisecond, "writer B must actually wait on PostgreSQL advisory locks")
	select {
	case err := <-b:
		t.Fatalf("writer B published before the shared account fence was released: %v", err)
	default:
	}
	close(release)
	require.NoError(t, <-a)
	require.NoError(t, <-b)
	assertAccount(t, f.cache[1], f.ids[0])
	require.NoError(t, f.publish(ctx, 0, f.ids, nil))
	for i := range 2 {
		assertAccount(t, f.cache[i], f.ids[0])
		accounts, hit, err := f.cache[i].GetSnapshot(ctx, f.bucket)
		require.NoError(t, err)
		require.True(t, hit)
		require.Len(t, accounts, len(f.ids))
		require.Equal(t, "1", f.redis[i].Get(ctx, "sched:ready:"+f.bucket.String()).Val())
		require.NotEmpty(t, f.redis[i].Get(ctx, "sched:active:"+f.bucket.String()).Val())
	}
	// A published twice, B once; this is actual production SQL, not a timing
	// threshold. On the pinned defect A makes 1024 and B makes 512 calls.
	require.Equal(t, int64(2), f.driver[0].calls.Load(), "lock acquisition round trips must be bounded per publication")
	require.Equal(t, int64(1), f.driver[1].calls.Load(), "lock acquisition round trips must be bounded per publication")
	var left int
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid
WHERE a.application_name LIKE 'issue711-writer-%' AND l.locktype='advisory'`).Scan(&left))
	require.Zero(t, left, "completed transactions must release all account locks")
}

// Delay the result of a genuine full DB query, after rows have been read but
// before the production snapshot wrapper acquires publication locks.
type staleReadRepository struct {
	service.AccountRepository
	entered, release chan struct{}
}

func (r *staleReadRepository) ListSchedulableByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	accounts, err := r.AccountRepository.ListSchedulableByPlatform(ctx, platform)
	close(r.entered)
	select {
	case <-r.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return accounts, err
}
func (r *staleReadRepository) WithLatestSchedulerAccounts(ctx context.Context, ids []int64, publish func([]service.Account) error) error {
	return r.AccountRepository.(service.LatestSchedulerAccountsRepository).WithLatestSchedulerAccounts(ctx, ids, publish)
}

func TestIssue711StaleRebuildCannotRestoreAccount(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	gated := &staleReadRepository{AccountRepository: f.repo[0], entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-gated.release:
		default:
			close(gated.release)
		}
	}()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.Scheduling.DbFallbackEnabled = true
	snapshot := service.NewSchedulerSnapshotService(f.cache[0], nil, gated, nil, cfg)
	done := make(chan error, 1)
	go func() {
		_, _, err := snapshot.ListSchedulableAccounts(ctx, nil, service.PlatformOpenAI, false)
		done <- err
	}()
	select {
	case <-gated.entered:
	case <-ctx.Done():
		t.Fatal("full rebuild never read its initial account rows")
	}
	disable(t, f)
	fresh, err := f.repo[1].GetByID(ctx, f.ids[0])
	require.NoError(t, err)
	// Newer state is already published to BOTH local caches before A resumes.
	for i := range 2 {
		s := service.NewSchedulerSnapshotService(f.cache[i], nil, f.repo[i], nil, cfg)
		require.NoError(t, s.UpdateAccountInCache(ctx, fresh))
		assertAccount(t, f.cache[i], f.ids[0])
	}
	close(gated.release)
	require.NoError(t, <-done)
	assertAccount(t, f.cache[0], f.ids[0])
	accounts, hit, err := f.cache[0].GetSnapshot(ctx, f.bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, accounts, 512)
	for _, account := range accounts {
		if account.ID == f.ids[0] {
			require.False(t, account.IsSchedulable(), "candidate metadata must retain the committed quality disable")
		}
	}
}

func TestIssue711PublicationFailureKeepsActiveAndFencesOldWriter(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	require.NoError(t, f.publish(ctx, 0, f.ids, nil))
	activeKey := "sched:active:" + f.bucket.String()
	before, err := f.redis[0].Get(ctx, activeKey).Result()
	require.NoError(t, err)
	disable(t, f)
	fresh, err := f.repo[0].GetByID(ctx, f.ids[0])
	require.NoError(t, err)
	require.NoError(t, f.cache[0].SetAccount(ctx, fresh))
	token, err := f.cache[0].CaptureBucketWriteToken(ctx, f.bucket)
	require.NoError(t, err)
	// Actual Redis failure, not an error-returning mock cache.
	require.NoError(t, f.redis[0].Do(ctx, "CLIENT", "PAUSE", 600, "WRITE").Err())
	short, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	err = f.repo[0].(service.LatestSchedulerAccountsRepository).WithLatestSchedulerAccounts(short, f.ids, func(accounts []service.Account) error {
		return f.cache[0].SetSnapshot(short, f.bucket, token, accounts)
	})
	cancel()
	require.Error(t, err, "paused Redis publication must fail within its context")
	require.NoError(t, f.redis[0].Set(ctx, "issue711:write-resumed", "1", 0).Err(), "wait for the real Redis write pause to end")
	after, err := f.redis[0].Get(ctx, activeKey).Result()
	require.NoError(t, err)
	require.Equal(t, before, after, "failed publication must preserve the last active snapshot")
	accounts, hit, err := f.cache[0].GetSnapshot(ctx, f.bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, accounts, 512)
	assertAccount(t, f.cache[0], f.ids[0])
	require.NoError(t, f.publish(ctx, 0, f.ids, nil), "retry must successfully publish committed state")
	beforeVersion, err := strconv.ParseInt(before, 10, 64)
	require.NoError(t, err)
	afterVersion, err := f.redis[0].Get(ctx, activeKey).Int64()
	require.NoError(t, err)
	require.Greater(t, afterVersion, beforeVersion)
	old, err := f.cache[0].CaptureBucketWriteToken(ctx, f.bucket)
	require.NoError(t, err)
	require.NoError(t, f.cache[0].RetireBucket(ctx, f.bucket))
	_, err = f.cache[0].ReopenBucket(ctx, f.bucket)
	require.NoError(t, err)
	require.ErrorIs(t, f.cache[0].SetSnapshot(ctx, f.bucket, old, []service.Account{*fresh}), service.ErrSchedulerBucketWriteFenced)
	assertAccount(t, f.cache[0], f.ids[0])
}

func TestIssue711TwoWritersDrainAccountChanges(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.Scheduling.OutboxPollIntervalSeconds = 1
	cfg.Gateway.Scheduling.DbFallbackEnabled = false
	for i := range 2 {
		s := service.NewSchedulerSnapshotService(f.cache[i], repository.NewSchedulerOutboxRepository(f.db), f.repo[i], nil, cfg)
		s.Start()
		t.Cleanup(s.Stop)
	}
	// Several poll windows of real repository changes continually enqueue
	// account_changed, with both snapshot writers live on their own Redis.
	var initial [2]int64
	require.Eventually(t, func() bool {
		for i := range 2 {
			value, err := f.redis[i].Get(ctx, "sched:active:"+f.bucket.String()).Int64()
			if err != nil {
				return false
			}
			initial[i] = value
		}
		return true
	}, 15*time.Second, 50*time.Millisecond, "both writers must establish their initial snapshots")
	for n := range 20 {
		require.NoError(t, f.repo[n%2].UpdateExtra(ctx, f.ids[n], map[string]any{"openai_excel_bps": n%2 == 0}))
		time.Sleep(180 * time.Millisecond)
	}
	var last int64
	require.NoError(t, f.db.QueryRowContext(ctx, "SELECT MAX(id) FROM scheduler_outbox").Scan(&last))
	require.Positive(t, last)
	require.Eventually(t, func() bool {
		for i := range 2 {
			watermark, err := f.cache[i].GetOutboxWatermark(ctx)
			if err != nil || watermark < last {
				return false
			}
		}
		return true
	}, 25*time.Second, 100*time.Millisecond, "both isolated Redis writers must drain actual account_changed events")
	for i := range 2 {
		accounts, hit, err := f.cache[i].GetSnapshot(ctx, f.bucket)
		require.NoError(t, err)
		require.True(t, hit)
		require.Len(t, accounts, 512)
		version, err := f.redis[i].Get(ctx, "sched:active:"+f.bucket.String()).Int64()
		require.NoError(t, err)
		require.Greater(t, version, initial[i], "account_changed must publish a newer bucket version on each Redis")
		for n := range 20 {
			a, err := f.cache[i].GetAccount(ctx, f.ids[n])
			require.NoError(t, err)
			require.Equal(t, n%2 == 0, a.Extra["openai_excel_bps"])
		}
	}
	var retries int
	require.NoError(t, f.db.QueryRowContext(ctx, "SELECT count(*) FROM scheduler_outbox_retries").Scan(&retries))
	require.Zero(t, retries, "successful synthetic account changes must not accumulate retries")
}
