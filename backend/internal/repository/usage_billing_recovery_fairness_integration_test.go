//go:build integration

package repository

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Observe cache invalidations without introducing Redis deltas. PostgreSQL,
// public lane dispatch, recovery selection and cache acknowledgements are real.
type fairnessBillingCache struct {
	service.BillingCache
	mu       sync.Mutex
	balances map[int64]int
	quotas   map[int64]int
}

func (c *fairnessBillingCache) InvalidateUserBalance(_ context.Context, user int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.balances[user]++
	return nil
}

func (c *fairnessBillingCache) DeleteUserPlatformQuotaCache(_ context.Context, user int64, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.quotas[user]++
	return nil
}

func (*fairnessBillingCache) DeductUserBalance(context.Context, int64, float64) error {
	panic("durable recovery must invalidate cache, never replay a monetary delta")
}

func startFairnessRecovery(t *testing.T, repo *usageBillingRepository, cache *service.BillingCacheService) (*service.UsageRecordWorkerPool, *admissionRecoveryObserver) {
	t.Helper()
	observer := &admissionRecoveryObserver{
		usageBillingRepository: repo,
		selected:               make(chan []service.UsageBillingEventKey, 4),
		acked:                  make(chan service.UsageBillingEventKey, 256),
	}
	worker := service.ProvideUsageRecordWorkerPool(&config.Config{}, observer, cache, nil)
	t.Cleanup(worker.Stop)
	return worker, observer
}

func requireFairnessTotals(t *testing.T, repo *usageBillingRepository, cmd service.UsageBillingCommand, count int) {
	t.Helper()
	var balance, keyQuota, daily, weekly, monthly string
	var logs, claims, complete int
	require.NoError(t, repo.db.QueryRowContext(context.Background(), `SELECT u.balance::text,k.quota_used::text,
		q.daily_usage_usd::text,q.weekly_usage_usd::text,q.monthly_usage_usd::text,
		(SELECT count(*) FROM usage_logs WHERE api_key_id=k.id),
		(SELECT count(*) FROM usage_billing_dedup WHERE api_key_id=k.id),
		(SELECT count(*) FROM usage_billing_events WHERE api_key_id=k.id AND state='complete')
		FROM users u JOIN api_keys k ON k.user_id=u.id
		JOIN usage_platform_quota_totals q ON q.quota_id=(SELECT id FROM user_platform_quotas WHERE user_id=u.id AND platform='openai')
		WHERE u.id=$1 AND k.id=$2`, cmd.UserID, cmd.APIKeyID).
		Scan(&balance, &keyQuota, &daily, &weekly, &monthly, &logs, &claims, &complete))
	require.Equal(t, fmt.Sprintf("%.8f", 100-float64(count)/10), balance)
	require.Equal(t, fmt.Sprintf("%.8f", float64(count)/10), keyQuota)
	for _, value := range []string{daily, weekly, monthly} {
		parsed, err := strconv.ParseFloat(value, 64)
		require.NoError(t, err)
		require.InDelta(t, float64(count)/10, parsed, 1e-8)
	}
	require.Equal(t, count, logs)
	require.Equal(t, count, claims)
	require.Equal(t, count, complete)
}

func TestUsageBillingRecoveryPassesLockedPrefixAndRevisitsReceipts(t *testing.T) {
	ctx := context.Background()
	repo, cmdA, usageA, client := durableBillingFixture(t)
	cmdA.PlatformQuotaPlatform, cmdA.PlatformQuotaCost, cmdA.PlatformUsageAt = "openai", 0.1, usageA.CreatedAt
	keysA := stageBillingBatchFixture(t, repo, cmdA, usageA, 65)
	userB := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.invalid", Balance: 100})
	keyB := mustCreateApiKey(t, client, &service.APIKey{UserID: userB.ID, Quota: 100})
	cmdB, usageB := cmdA, usageA
	cmdB.UserID, cmdB.APIKeyID = userB.ID, keyB.ID
	usageB.UserID, usageB.APIKeyID = userB.ID, keyB.ID
	b := stageBillingBatchFixture(t, repo, cmdB, usageB, 1)[0]
	cmdB.RequestID = b.RequestID
	_, err := repo.db.ExecContext(ctx, `INSERT INTO user_platform_quotas(user_id,platform,daily_limit_usd,weekly_limit_usd,monthly_limit_usd)
		VALUES($1,'openai',100,100,100),($2,'openai',100,100,100)`, cmdA.UserID, cmdB.UserID)
	require.NoError(t, err)
	// Equal timestamps exercise the complete keyset tie breaker. All 64 queued
	// A receipts sort before B, independently of goroutine/transaction timing.
	_, err = repo.db.ExecContext(ctx, `UPDATE usage_billing_events SET staged_at=NOW()-INTERVAL '3 seconds',
		next_attempt_at=NOW()-CASE WHEN api_key_id=$1 THEN INTERVAL '2 seconds' ELSE INTERVAL '1 second' END`, cmdA.APIKeyID)
	require.NoError(t, err)
	cache := &fairnessBillingCache{balances: map[int64]int{}, quotas: map[int64]int{}}
	cacheService := service.NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(cacheService.Stop)

	// An independent pinned connection holds the trigger's advisory barrier.
	// The real batch reaches this AFTER taking all 64 event locks and effects;
	// its changes remain invisible until we explicitly release the barrier.
	name := "recovery_fairness_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	barrier, err := repo.db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = barrier.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext($1))`, name)
		_ = barrier.Close()
		_, _ = repo.db.ExecContext(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON usage_logs; DROP FUNCTION IF EXISTS %s()`, name, name))
	})
	var barrierPID int
	require.NoError(t, barrier.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&barrierPID))
	_, err = barrier.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext($1))`, name)
	require.NoError(t, err)
	_, err = repo.db.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.api_key_id=%d AND NEW.request_id<>'%s' THEN PERFORM pg_advisory_xact_lock(hashtext('%s')); END IF;
		RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON usage_logs FOR EACH ROW EXECUTE FUNCTION %s()`,
		name, cmdA.APIKeyID, keysA[0].RequestID, name, name, name))
	require.NoError(t, err)
	owner := strconv.FormatInt(cmdA.UserID, 10)
	hold, err := repo.admission.acquire(ctx, owner, repo.db.Stats().MaxOpenConnections)
	require.NoError(t, err)
	live, cancelLive := context.WithCancel(ctx)
	var liveCalls sync.WaitGroup
	type outcome struct {
		key service.UsageBillingEventKey
		r   *service.UsageBillingRecovery
		err error
	}
	results := make(chan outcome, len(keysA))
	t.Cleanup(func() {
		cancelLive()
		if hold != nil {
			hold()
		}
		_, _ = barrier.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext($1))`, name)
		liveCalls.Wait()
	})
	deliver := func(key service.UsageBillingEventKey) {
		liveCalls.Add(1)
		go func() {
			defer liveCalls.Done()
			r, err := repo.ApplyStagedUsageBilling(live, key)
			results <- outcome{key, r, err}
		}()
	}
	deliver(keysA[0])
	requireBillingOwnerRefs(t, repo, cmdA.UserID, 2)
	for i, key := range keysA[1:] {
		deliver(key)
		require.Eventually(t, func() bool {
			repo.stagedLanes.mu.Lock()
			defer repo.stagedLanes.mu.Unlock()
			return len(repo.stagedLanes.owners[owner]) == i+1
		}, 3*time.Second, time.Millisecond)
	}
	hold()
	hold = nil
	first := waitAdmissionRecoveryValue(t, results)
	require.NoError(t, first.err)
	require.Equal(t, keysA[0], first.key)
	require.True(t, first.r.Result.Applied)
	require.NoError(t, cacheService.InvalidateUserBalance(ctx, cmdA.UserID))
	require.NoError(t, cacheService.InvalidateDurablePlatformQuota(ctx, cmdA.UserID, "openai"))
	require.NoError(t, repo.AcknowledgeUsageBillingCache(ctx, first.key))
	blocked := func() bool {
		var waiting bool
		err := repo.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, barrierPID).Scan(&waiting)
		return err == nil && waiting
	}
	require.Eventually(t, blocked, 3*time.Second, time.Millisecond, "live batch must reach the independent SQL barrier")
	var unlocked int
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT count(*) FROM
		(SELECT 1 FROM usage_billing_events WHERE api_key_id=$1 AND state='pending' FOR UPDATE SKIP LOCKED) e`, cmdA.APIKeyID).Scan(&unlocked))
	require.Zero(t, unlocked, "all 64 pending A receipts must be locked by the live batch")
	before := make(map[service.UsageBillingEventKey]admissionRecoverySnapshot)
	for _, key := range keysA[1:] {
		cmdA.RequestID = key.RequestID
		before[key] = readAdmissionRecoverySnapshot(t, repo, cmdA)
		require.Equal(t, "pending", before[key].State)
		require.Zero(t, before[key].Claims)
		require.Zero(t, before[key].Logs)
		require.Equal(t, "99.90000000", before[key].Balance)
	}
	worker, observer := startFairnessRecovery(t, repo, cacheService)
	selected := waitAdmissionRecoveryValue(t, observer.selected)
	require.Len(t, selected, 64)
	for _, key := range selected {
		require.Contains(t, before, key)
	}
	selected = waitAdmissionRecoveryValue(t, observer.selected)
	require.Equal(t, []service.UsageBillingEventKey{b}, selected, "BILLING_RECOVERY_LOCKED_PREFIX: B must be reached in at most two bounded cycles while A stays locked")
	require.Equal(t, b, waitAdmissionRecoveryValue(t, observer.acked))
	worker.Stop()
	require.True(t, blocked(), "B must settle before releasing A's live batch")
	// With B complete and A still pending at its original position, reaching
	// the end must wrap to the skipped rows without changing their retry data.
	revisited, err := repo.PendingUsageBilling(ctx, 64)
	require.NoError(t, err)
	require.ElementsMatch(t, keysA[1:], revisited)
	for key, snapshot := range before {
		cmdA.RequestID = key.RequestID
		require.Equal(t, snapshot, readAdmissionRecoverySnapshot(t, repo, cmdA), "busy recovery cannot mutate or charge locked A")
	}
	requireFairnessTotals(t, repo, cmdB, 1)

	_, err = barrier.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext($1))`, name)
	require.NoError(t, err)
	settled := make(map[service.UsageBillingEventKey]*service.UsageBillingRecovery)
	for range keysA[1:] {
		out := waitAdmissionRecoveryValue(t, results)
		require.NoError(t, out.err)
		require.NotContains(t, settled, out.key)
		require.True(t, out.r.Result.Applied)
		settled[out.key] = out.r
	}
	for i, key := range keysA[1:] {
		require.Contains(t, settled, key)
		require.InDelta(t, 100-float64(i+2)/10, *settled[key].Result.NewBalance, 1e-8, "live lane order must survive recovery selection")
	}
	worker, observer = startFairnessRecovery(t, repo, cacheService)
	for range 64 {
		key := waitAdmissionRecoveryValue(t, observer.acked)
		require.Contains(t, before, key)
	}
	worker.Stop()
	requireFairnessTotals(t, repo, cmdA, 65)
	for _, key := range append(keysA, b) {
		replayed, err := repo.ApplyStagedUsageBilling(ctx, key)
		require.NoError(t, err)
		require.True(t, replayed.Settled)
		require.False(t, replayed.Result.Applied)
		require.False(t, replayed.NeedsCache)
	}
	requireFairnessTotals(t, repo, cmdA, 65)
	requireFairnessTotals(t, repo, cmdB, 1)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	require.Equal(t, map[int64]int{cmdA.UserID: 65, cmdB.UserID: 1}, cache.balances)
	require.Equal(t, cache.balances, cache.quotas)
}

func TestUsageBillingRecoveryRetainsCanceledAndOverflowedEvents(t *testing.T) {
	for _, mode := range []string{"canceled", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			repo, cmd, usage, _ := durableBillingFixture(t)
			keys := stageBillingBatchFixture(t, repo, cmd, usage, 2)
			owner := strconv.FormatInt(cmd.UserID, 10)
			hold, err := repo.admission.acquire(ctx, owner, repo.db.Stats().MaxOpenConnections)
			require.NoError(t, err)
			live, cancelLive := context.WithCancel(ctx)
			finished := make(chan error, 1)
			t.Cleanup(func() {
				cancelLive()
				if hold != nil {
					hold()
				}
				for range finished {
				}
			})
			go func() {
				defer close(finished)
				_, err := repo.ApplyStagedUsageBilling(live, keys[0])
				finished <- err
			}()
			requireBillingOwnerRefs(t, repo, cmd.UserID, 2)
			cmd.RequestID = keys[1].RequestID
			before := readAdmissionRecoverySnapshot(t, repo, cmd)
			if mode == "canceled" {
				queued, cancelQueued := context.WithCancel(ctx)
				defer cancelQueued()
				done := make(chan error, 1)
				go func() { _, err := repo.ApplyStagedUsageBilling(queued, keys[1]); done <- err }()
				require.Eventually(t, func() bool {
					repo.stagedLanes.mu.Lock()
					defer repo.stagedLanes.mu.Unlock()
					return len(repo.stagedLanes.owners[owner]) == 1
				}, 3*time.Second, time.Millisecond)
				cancelQueued()
				require.ErrorIs(t, waitAdmissionRecoveryValue(t, done), context.Canceled)
			} else {
				// Inject only occupied queue slots to reach the production cap
				// without creating 4096 unrelated financial events or goroutines.
				repo.stagedLanes.mu.Lock()
				repo.stagedLanes.owners[owner] = make([]*stagedBillingRequest, stagedBillingMaxPending)
				repo.stagedLanes.mu.Unlock()
				_, err := repo.ApplyStagedUsageBilling(ctx, keys[1])
				repo.stagedLanes.mu.Lock()
				repo.stagedLanes.owners[owner] = nil
				repo.stagedLanes.mu.Unlock()
				require.ErrorIs(t, err, errStagedBillingQueueFull)
			}
			require.Equal(t, before, readAdmissionRecoverySnapshot(t, repo, cmd), "unclaimed delivery stays durable without effects")
			hold()
			hold = nil
			require.NoError(t, waitAdmissionRecoveryValue(t, finished))
			worker, observer := startFairnessRecovery(t, repo, nil)
			seen := map[service.UsageBillingEventKey]bool{}
			for range keys {
				key := waitAdmissionRecoveryValue(t, observer.acked)
				require.False(t, seen[key])
				seen[key] = true
			}
			worker.Stop()
			require.Len(t, seen, len(keys))
			for _, key := range keys {
				cmd.RequestID = key.RequestID
				settled := readAdmissionRecoverySnapshot(t, repo, cmd)
				require.Equal(t, "complete", settled.State)
				require.Equal(t, "99.80000000", settled.Balance)
				require.Equal(t, "0.20000000", settled.Quota)
				require.Equal(t, 1, settled.Logs)
				require.Equal(t, 1, settled.Claims)
				r, err := repo.ApplyStagedUsageBilling(ctx, key)
				require.NoError(t, err)
				require.False(t, r.Result.Applied)
				require.False(t, r.NeedsCache)
				require.Equal(t, settled, readAdmissionRecoverySnapshot(t, repo, cmd))
			}
		})
	}
}

func TestUsageBillingRecoveryScanBoundsSweepAndWraps(t *testing.T) {
	ctx := context.Background()
	repo, cmd, usage, _ := durableBillingFixture(t)
	keys := stageBillingBatchFixture(t, repo, cmd, usage, 2)
	first, err := repo.PendingUsageBilling(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, keys[:1], first)
	// Neither selected old event is settled. New traffic must wait for the
	// next finite sweep, rather than keeping skipped old rows behind a cursor.
	newKey := stageBillingBatchFixture(t, repo, cmd, usage, 1)[0]
	second, err := repo.PendingUsageBilling(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, keys[1:], second)
	wrapped, err := repo.PendingUsageBilling(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, first, wrapped)
	second, err = repo.PendingUsageBilling(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, keys[1:], second)
	newPage, err := repo.PendingUsageBilling(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, []service.UsageBillingEventKey{newKey}, newPage)
}
