package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func displayRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1, ContextTimeoutEnabled: true})
	t.Cleanup(func() { _ = client.Close() })
	return server, client
}
func seedDisplay(t *testing.T, c *accountPerformanceDisplayCache, j performanceDisplayJob, at time.Time) {
	t.Helper()
	n := 42.0
	raw, err := json.Marshal(performanceDisplaySnapshot{AccountID: j.id, Minutes: j.minutes, At: at, TPS: &n, TPSSamples: 9})
	require.NoError(t, err)
	require.NoError(t, c.redis.Set(context.Background(), c.key(j), raw, performanceDisplayMaxAge).Err())
}
func expectDisplayMetadata(mock sqlmock.Sqlmock, minutes string, paused bool) {
	mock.ExpectQuery("SELECT value FROM settings").WithArgs(service.SettingKeyAccountPerformanceWindowMinutes).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(minutes))
	mock.ExpectQuery("SELECT a.id,COALESCE").WithArgs("{7}").WillReturnRows(sqlmock.NewRows([]string{"id", "config", "revision", "paused", "generation", "at", "reason"}).AddRow(7, `{"enabled":true}`, 12, paused, 3, nil, ""))
}

func TestAccountPerformanceDisplayColdReadNeverWaitsForHistory(t *testing.T) {
	_, client := displayRedis(t)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { mock.ExpectClose(); require.NoError(t, db.Close()) }()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	c := newAccountPerformanceDisplayCache(client, "fixture", func(ctx context.Context, ids []int64, minutes int, now time.Time) (map[int64]service.AccountPerformance, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		n := 42.0
		return map[int64]service.AccountPerformance{7: {AccountID: 7, AvgTPS: &n, TPSSamples: 9, Paused: false}}, nil
	})
	defer c.stop()
	repo := &accountPerformanceRepository{db: db, display: c}
	request, cancel := context.WithCancel(context.Background())
	expectDisplayMetadata(mock, "60", false)
	got, err := repo.Read(request, []int64{7}, time.Now())
	require.NoError(t, err)
	require.Equal(t, "pending", got[7].StatisticsStatus)
	require.Nil(t, got[7].AvgTPS)
	cancel() // A departed browser cannot cancel shared refresh work.
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	expectDisplayMetadata(mock, "60", true)
	got, err = repo.Read(context.Background(), []int64{7}, time.Now())
	require.NoError(t, err)
	require.True(t, got[7].Paused, "pause state is live even while the cache is warming")
	require.Equal(t, int32(1), calls.Load())
	close(release)
	require.Eventually(t, func() bool {
		return client.Exists(context.Background(), c.key(performanceDisplayJob{7, 60})).Val() == 1
	}, time.Second, time.Millisecond)
	expectDisplayMetadata(mock, "60", true)
	got, err = repo.Read(context.Background(), []int64{7}, time.Now())
	require.NoError(t, err)
	require.Equal(t, "ready", got[7].StatisticsStatus)
	require.Equal(t, 42.0, *got[7].AvgTPS)
	require.True(t, got[7].Paused)
	require.Equal(t, int64(12), got[7].Revision)
	require.Equal(t, 60*time.Minute, got[7].WindowEnd.Sub(got[7].WindowStart))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountPerformanceDisplayReplicasCoalesceRefreshAndFenceLease(t *testing.T) {
	server, client := displayRedis(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	load := func(ctx context.Context, ids []int64, minutes int, now time.Time) (map[int64]service.AccountPerformance, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return map[int64]service.AccountPerformance{7: {AccountID: 7}}, nil
	}
	a := newAccountPerformanceDisplayCache(client, "shared", load)
	b := newAccountPerformanceDisplayCache(client, "shared", load)
	defer a.stop()
	defer b.stop()
	jobs := []performanceDisplayJob{{7, 60}}
	done := make(chan struct{})
	go func() { a.refresh(jobs); close(done) }()
	<-started
	b.refresh(jobs)
	require.Equal(t, int32(1), calls.Load())
	// A delayed former owner cannot publish into or delete a successor's lease.
	server.FastForward(performanceDisplayLease + time.Second)
	require.NoError(t, client.Set(context.Background(), a.prefix+"refresh", "successor", performanceDisplayLease).Err())
	close(release)
	<-done
	require.Equal(t, "successor", client.Get(context.Background(), a.prefix+"refresh").Val())
	require.Zero(t, client.Exists(context.Background(), a.key(jobs[0])).Val())
}

func TestAccountPerformanceDisplayExpiryFailureBackoffAndWindowIsolation(t *testing.T) {
	_, client := displayRedis(t)
	var calls atomic.Int32
	c := newAccountPerformanceDisplayCache(client, "fixture", func(context.Context, []int64, int, time.Time) (map[int64]service.AccountPerformance, error) {
		calls.Add(1)
		return nil, errors.New("synthetic timeout")
	})
	defer c.stop()
	now := time.Now()
	j := performanceDisplayJob{7, 60}
	seedDisplay(t, c, j, now.Add(-90*time.Second))
	c.refresh([]performanceDisplayJob{j})
	c.refresh([]performanceDisplayJob{j})
	require.Equal(t, int32(1), calls.Load(), "failure backoff also suppresses peer retries")
	data, retry, err := c.lookup(context.Background(), []performanceDisplayJob{j}, now)
	require.NoError(t, err)
	require.True(t, retry[7])
	require.Equal(t, 42.0, *data[7].TPS, "failed refresh preserves old evidence")
	data, _, err = c.lookup(context.Background(), []performanceDisplayJob{{7, 15}}, now)
	require.NoError(t, err)
	require.Empty(t, data)
	data, _, err = c.lookup(context.Background(), []performanceDisplayJob{j}, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.Empty(t, data, "hard age expires despite surviving Redis TTL")
	other := newAccountPerformanceDisplayCache(client, "other-database", c.load)
	defer other.stop()
	data, _, err = other.lookup(context.Background(), []performanceDisplayJob{j}, now)
	require.NoError(t, err)
	require.Empty(t, data)
}

func TestAccountPerformanceDisplayRedisOutageDoesNotFallThroughToHistory(t *testing.T) {
	server, client := displayRedis(t)
	var calls atomic.Int32
	c := newAccountPerformanceDisplayCache(client, "fixture", func(context.Context, []int64, int, time.Time) (map[int64]service.AccountPerformance, error) {
		calls.Add(1)
		return nil, nil
	})
	defer c.stop()
	server.Close()
	_, _, err := c.lookup(context.Background(), []performanceDisplayJob{{7, 60}}, time.Now())
	require.Error(t, err)
	c.refresh([]performanceDisplayJob{{7, 60}})
	require.Zero(t, calls.Load())
}

func TestAccountPerformanceDisplayBoundedQueueDeduplicatesAndStops(t *testing.T) {
	_, client := displayRedis(t)
	started := make(chan struct{})
	var calls atomic.Int32
	c := newAccountPerformanceDisplayCache(client, "fixture", func(ctx context.Context, _ []int64, _ int, _ time.Time) (map[int64]service.AccountPerformance, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	c.enqueue(performanceDisplayJob{1, 60})
	<-started
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := int64(1); id <= 1024; id++ {
				c.enqueue(performanceDisplayJob{id, 60})
			}
		}()
	}
	wg.Wait()
	c.mu.Lock()
	n := len(c.pending)
	c.mu.Unlock()
	require.LessOrEqual(t, n, performanceDisplayQueueSize)
	require.Equal(t, int32(1), calls.Load())
	c.stop()
	c.enqueue(performanceDisplayJob{2048, 60})
	require.Equal(t, int32(1), calls.Load())
}

func TestAccountPerformanceDisplayRefreshUsesReadOnlyDatabaseBudget(t *testing.T) {
	_, client := displayRedis(t)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { mock.ExpectClose(); require.NoError(t, db.Close()) }()
	repo, ok := NewAccountPerformanceRepository(db, client, &config.Config{Database: config.DatabaseConfig{DBName: "fixture"}}).(*accountPerformanceRepository)
	require.True(t, ok)
	defer repo.Stop()
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL statement_timeout='4500ms'; SET LOCAL lock_timeout='500ms'; SET LOCAL work_mem='8MB'; SET LOCAL max_parallel_workers_per_gather=0").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("WITH usage").WithArgs("{7}", sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(performanceRow(service.PerformanceConfig{}))
	mock.ExpectCommit()
	repo.display.refresh([]performanceDisplayJob{{7, 60}})
	require.Equal(t, int64(1), client.Exists(context.Background(), repo.display.key(performanceDisplayJob{7, 60})).Val())
	require.NoError(t, mock.ExpectationsWereMet())
}
