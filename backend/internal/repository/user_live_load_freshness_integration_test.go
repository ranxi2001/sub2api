//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func userLiveLoadUnlimitedRequest(t *testing.T, c *regionalConcurrencyCache) (service.LiveLeaseTransferRequest, *service.AcquireResult) {
	t.Helper()
	id := time.Now().UnixNano()
	slot, err := service.NewConcurrencyService(c).AcquireUserSlot(context.Background(), id, 0)
	require.NoError(t, err)
	require.True(t, slot.Acquired)
	return service.LiveLeaseTransferRequest{AccountID: id, UserID: id, APIKeyID: id, UserRequestID: slot.RequestID, LeaseID: fmt.Sprint(id)}, slot
}

func userLiveLoadProof(t *testing.T, c *regionalConcurrencyCache, r service.LiveLeaseTransferRequest) string {
	t.Helper()
	var proof string
	require.NoError(t, c.database().QueryRow(`SELECT COALESCE(identity->>'_user_stats_expires_at','missing')
FROM regional_live_handoffs WHERE region_id=$1 AND lease_id=$2`, c.region(), r.LeaseID).Scan(&proof))
	return proof
}

func requireUserLiveLoadUnknown(t *testing.T, c *regionalConcurrencyCache, id int64) {
	t.Helper()
	loads, err := c.GetUsersLoadBatch(context.Background(), []service.UserWithConcurrency{{ID: id}})
	require.Error(t, err)
	require.Nil(t, loads, "unavailable statistics must never serialize a measured zero")
}

func TestUserLiveLoadRegionalCrashedOrLegacyOwnerStaysUnknown(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, proof := range []string{"expired", "missing"} {
			t.Run(fmt.Sprintf("local=%t/%s", local, proof), func(t *testing.T) {
				ctx := context.Background()
				c := userLiveLoadRegionalCache(t, local)
				r, slot := userLiveLoadUnlimitedRequest(t, c)
				ok, err := c.AcquireLiveLeaseTransferring(ctx, r)
				require.NoError(t, err)
				require.True(t, ok)
				slot.ReleaseFunc() // The process then exits without close/abort.
				if proof == "expired" {
					_, err = c.database().Exec(`UPDATE regional_live_handoffs SET identity=jsonb_set(identity,
'{_user_stats_expires_at}',to_jsonb(extract(epoch FROM clock_timestamp())-1)) WHERE region_id=$1 AND lease_id=$2`, c.region(), r.LeaseID)
				} else {
					_, err = c.database().Exec(`UPDATE regional_live_handoffs SET identity=identity-'_user_stats_expires_at'
WHERE region_id=$1 AND lease_id=$2`, c.region(), r.LeaseID)
				}
				require.NoError(t, err)
				before := userLiveLoadProof(t, c, r)
				requireUserLiveLoadUnknown(t, c, r.UserID)
				// Late refresh and retries preserve admission outcomes but cannot
				// create fresh statistics for a permanent orphan or a legacy row.
				ok, err = c.RefreshLiveLease(ctx, r.AccountID, r.UserID, r.APIKeyID, r.LeaseID)
				require.NoError(t, err)
				require.True(t, ok)
				ok, err = c.AcquireLiveLeaseTransferring(ctx, r)
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, before, userLiveLoadProof(t, c, r))
				next := r
				next.AccountID++
				next.ReplacedAccountID = r.AccountID
				ok, err = c.MigrateLiveLeaseAccount(ctx, next)
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, before, userLiveLoadProof(t, c, r))
				requireUserLiveLoadUnknown(t, c, r.UserID)
				ok, err = c.RefreshLiveLease(ctx, r.AccountID, r.UserID, r.APIKeyID, r.LeaseID)
				require.NoError(t, err)
				require.False(t, ok, "old account cannot refresh a migrated owner")
				require.NoError(t, c.ReleaseLiveLease(ctx, next.AccountID, r.UserID, r.APIKeyID, r.LeaseID))
				assertUserLiveLoad(t, c, r.UserID, 0)
				ok, err = c.RefreshLiveLease(ctx, next.AccountID, r.UserID, r.APIKeyID, r.LeaseID)
				require.NoError(t, err)
				require.False(t, ok, "closed statistics cannot be revived")
			})
		}
	}
}

func TestUserLiveLoadRegionalFreshnessRenewalIsFencedAndReadOnly(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprintf("local=%t", local), func(t *testing.T) {
			ctx := context.Background()
			c := userLiveLoadRegionalCache(t, local)
			r, slot := userLiveLoadUnlimitedRequest(t, c)
			ok, err := c.AcquireLiveLeaseTransferring(ctx, r)
			require.NoError(t, err)
			require.True(t, ok)
			defer slot.ReleaseFunc()
			before := userLiveLoadProof(t, c, r)
			assertUserLiveLoad(t, c, r.UserID, 1)
			require.Equal(t, before, userLiveLoadProof(t, c, r), "sampling cannot extend lifetime")
			ok, err = c.RefreshLiveLease(ctx, r.AccountID+1, r.UserID, r.APIKeyID, r.LeaseID)
			require.NoError(t, err)
			require.False(t, ok)
			require.Equal(t, before, userLiveLoadProof(t, c, r))
			ok, err = c.RefreshLiveLease(ctx, r.AccountID, r.UserID, r.APIKeyID, r.LeaseID)
			require.NoError(t, err)
			require.True(t, ok)
			require.NotEqual(t, before, userLiveLoadProof(t, c, r))
			require.NoError(t, c.AbortLiveLeaseTransfer(ctx, r))
			assertUserLiveLoad(t, c, r.UserID, 0)
			ok, err = c.RefreshLiveLease(ctx, r.AccountID, r.UserID, r.APIKeyID, r.LeaseID)
			require.NoError(t, err)
			require.False(t, ok)
			assertUserLiveLoad(t, c, r.UserID, 0)
		})
	}
}

// Pause a real Redis pipeline either before or after its commands execute. SQL
// transitions run concurrently while the reader holds its first SQL snapshot.
type userLiveLoadPauseHook struct {
	started chan struct{}
	resume  chan struct{}
	used    atomic.Bool
	after   bool
}

func (h *userLiveLoadPauseHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *userLiveLoadPauseHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return next
}

func (h *userLiveLoadPauseHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		if !h.used.CompareAndSwap(false, true) {
			return next(ctx, cmds)
		}
		var err error
		if h.after {
			err = next(ctx, cmds)
		}
		close(h.started)
		select {
		case <-h.resume:
		case <-ctx.Done():
			return ctx.Err()
		}
		if h.after {
			return err
		}
		return next(ctx, cmds)
	}
}

func TestUserLiveLoadRegionalConcurrentHandoffAndCloseSamples(t *testing.T) {
	for _, local := range []bool{false, true} {
		for _, transition := range []string{"handoff-oldrelease", "handoff-oldrelease-close", "migration", "close", "abort", "oldrelease", "expiry"} {
			for _, afterRedis := range []bool{false, true} {
				t.Run(fmt.Sprintf("local=%t/%s/afterRedis=%t", local, transition, afterRedis), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					c := userLiveLoadRegionalCache(t, local)
					r, slot := userLiveLoadUnlimitedRequest(t, c)
					defer slot.ReleaseFunc()
					if transition != "handoff-oldrelease" && transition != "handoff-oldrelease-close" {
						ok, err := c.AcquireLiveLeaseTransferring(ctx, r)
						require.NoError(t, err)
						require.True(t, ok)
					}
					hook := &userLiveLoadPauseHook{started: make(chan struct{}), resume: make(chan struct{}), after: afterRedis}
					base, baseOK := c.ConcurrencyCache.(*concurrencyCache)
					require.True(t, baseOK)
					base.rdb.AddHook(hook)
					type sample struct {
						loads map[int64]*service.UserLoadInfo
						err   error
					}
					done := make(chan sample, 1)
					go func() {
						loads, err := c.GetUsersLoadBatch(ctx, []service.UserWithConcurrency{{ID: r.UserID}})
						done <- sample{loads: loads, err: err}
					}()
					select {
					case <-hook.started:
					case <-ctx.Done():
						t.Fatal("reader did not reach real Redis pipeline")
					}
					stableCount := 1
					switch transition {
					case "handoff-oldrelease", "handoff-oldrelease-close":
						ok, err := c.AcquireLiveLeaseTransferring(ctx, r)
						require.NoError(t, err)
						require.True(t, ok)
						slot.ReleaseFunc()
						if transition == "handoff-oldrelease-close" {
							require.NoError(t, c.ReleaseLiveLease(ctx, r.AccountID, r.UserID, r.APIKeyID, r.LeaseID))
							stableCount = 0
						}
					case "migration":
						next := r
						next.AccountID++
						next.ReplacedAccountID = r.AccountID
						ok, err := c.MigrateLiveLeaseAccount(ctx, next)
						require.NoError(t, err)
						require.True(t, ok)
						slot.ReleaseFunc()
					case "close":
						require.NoError(t, c.ReleaseLiveLease(ctx, r.AccountID, r.UserID, r.APIKeyID, r.LeaseID))
						stableCount = 0
					case "abort":
						require.NoError(t, c.AbortLiveLeaseTransfer(ctx, r))
						stableCount = 0
					case "oldrelease":
						slot.ReleaseFunc()
					case "expiry":
						_, err := c.database().ExecContext(ctx, `UPDATE regional_live_handoffs SET identity=jsonb_set(identity,
'{_user_stats_expires_at}',to_jsonb(extract(epoch FROM clock_timestamp())-1)) WHERE region_id=$1 AND lease_id=$2`, c.region(), r.LeaseID)
						require.NoError(t, err)
					}
					close(hook.resume)
					result := <-done
					if transition == "oldrelease" {
						require.NoError(t, result.err)
						require.Equal(t, 1, result.loads[r.UserID].CurrentConcurrency)
					} else {
						require.Error(t, result.err, "a changed SQL proof invalidates the sampled number")
						require.Nil(t, result.loads)
					}
					if transition == "expiry" {
						requireUserLiveLoadUnknown(t, c, r.UserID)
					} else {
						assertUserLiveLoad(t, c, r.UserID, stableCount)
					}
				})
			}
		}
	}
}

type userLiveLoadCleanupFailureCache struct {
	*concurrencyCache
}

func (c *userLiveLoadCleanupFailureCache) ReleaseTrackedUserSlot(context.Context, int64, string) error {
	return errors.New("statistics cleanup unavailable")
}

func TestUserLiveLoadRegionalClosedSourceExcludedAfterCleanupFailure(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprintf("local=%t", local), func(t *testing.T) {
			ctx := context.Background()
			c := userLiveLoadRegionalCache(t, local)
			r, slot := userLiveLoadUnlimitedRequest(t, c)
			base, ok := c.ConcurrencyCache.(*concurrencyCache)
			require.True(t, ok)
			c.ConcurrencyCache = &userLiveLoadCleanupFailureCache{concurrencyCache: base}
			acquired, err := c.AcquireLiveLeaseTransferring(ctx, r)
			require.NoError(t, err)
			require.True(t, acquired)
			require.NoError(t, c.ReleaseLiveLease(ctx, r.AccountID, r.UserID, r.APIKeyID, r.LeaseID))
			// SQL is closed while the exact old Redis source still exists. It
			// cannot reappear as an ordinary request or double-count a new one.
			require.NoError(t, base.rdb.ZScore(ctx, userSlotKey(r.UserID), slot.RequestID).Err())
			assertUserLiveLoad(t, c, r.UserID, 0)
			other, err := service.NewConcurrencyService(c).AcquireUserSlot(ctx, r.UserID, 0)
			require.NoError(t, err)
			require.True(t, other.Acquired)
			assertUserLiveLoad(t, c, r.UserID, 1)
			require.NoError(t, base.ReleaseTrackedUserSlot(ctx, r.UserID, other.RequestID))
			require.NoError(t, base.ReleaseTrackedUserSlot(ctx, r.UserID, slot.RequestID))
			assertUserLiveLoad(t, c, r.UserID, 0)
		})
	}
}

func TestUserLiveLoadRegionalSQLDependencyFailureIsUnknown(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprintf("local=%t", local), func(t *testing.T) {
			c := userLiveLoadRegionalCache(t, local)
			db, err := sql.Open("postgres", integrationPostgresDSN)
			require.NoError(t, err)
			require.NoError(t, db.Close())
			if local {
				c.local.DB = db
			} else {
				c.primary.DB = db
			}
			r, slot := userLiveLoadUnlimitedRequest(t, c)
			defer slot.ReleaseFunc()
			requireUserLiveLoadUnknown(t, c, r.UserID)
		})
	}
}
