//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/regional/slots"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func userLiveLoadRegionalCache(t *testing.T, local bool) *regionalConcurrencyCache {
	t.Helper()
	p := &slots.Primary{DB: integrationDB, Limit: func(context.Context, *sql.Tx, slots.Resource) (int, error) { return 2, nil }}
	var l *slots.Local
	if local {
		l = &slots.Local{DB: integrationDB, Region: fmt.Sprintf("user-load-%d", time.Now().UnixNano())}
		p = nil
	}
	base := NewConcurrencyCache(testRedis(t), 15, 30)
	cache, err := NewRegionalConcurrencyCache(base, p, l)
	require.NoError(t, err)
	return cache.(*regionalConcurrencyCache)
}

func assertUserLiveLoad(t *testing.T, c *regionalConcurrencyCache, id int64, expected int) {
	t.Helper()
	n, err := c.GetUserConcurrency(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, expected, n)
	loads, err := c.GetUsersLoadBatch(context.Background(), []service.UserWithConcurrency{{ID: id}})
	require.NoError(t, err)
	require.Equal(t, expected, loads[id].CurrentConcurrency)
}

func TestUserLiveLoadRegionalUnlimitedLifecycle(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprintf("local=%t", local), func(t *testing.T) {
			ctx := context.Background()
			c := userLiveLoadRegionalCache(t, local)
			id := time.Now().UnixNano()
			svc := service.NewConcurrencyService(c)
			slot, err := svc.AcquireUserSlot(ctx, id, 0)
			require.NoError(t, err)
			require.True(t, slot.Acquired)
			assertUserLiveLoad(t, c, id, 1)
			r := service.LiveLeaseTransferRequest{AccountID: id, UserID: id, APIKeyID: id, UserRequestID: slot.RequestID, LeaseID: fmt.Sprint(id)}
			for range 2 {
				ok, err := c.AcquireLiveLeaseTransferring(ctx, r)
				require.NoError(t, err)
				require.True(t, ok)
				assertUserLiveLoad(t, c, id, 1)
			}
			slot.ReleaseFunc() // SDP returns; the call is still active.
			slot.ReleaseFunc()
			assertUserLiveLoad(t, c, id, 1)
			next := r
			next.ReplacedAccountID = r.AccountID
			next.AccountID++
			next.UserRequestID = "" // Migration must retain the joint identity.
			ok, err := c.MigrateLiveLeaseAccount(ctx, next)
			require.NoError(t, err)
			require.True(t, ok)
			assertUserLiveLoad(t, c, id, 1)
			require.NoError(t, c.ReleaseLiveLeaseAccount(ctx, next.AccountID, r.LeaseID))
			assertUserLiveLoad(t, c, id, 1)
			for range 2 {
				require.NoError(t, c.ReleaseLiveLease(ctx, next.AccountID, id, id, r.LeaseID))
				assertUserLiveLoad(t, c, id, 0)
			}
			// Statistics never create user budget holders or consume local grants.
			n, err := c.count(ctx, "user", id)
			require.NoError(t, err)
			require.Zero(t, n)
		})
	}
}

func TestUserLiveLoadRegionalFailureCancellationAndFailOpen(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprintf("local=%t", local), func(t *testing.T) {
			ctx := context.Background()
			c := userLiveLoadRegionalCache(t, local)
			id := time.Now().UnixNano()
			svc := service.NewConcurrencyService(c)
			slot, err := svc.AcquireUserSlot(ctx, id, 0)
			require.NoError(t, err)
			r := service.LiveLeaseTransferRequest{AccountID: id, AccountMax: 1, AccountRequestID: "missing", UserID: id, APIKeyID: id, UserRequestID: slot.RequestID, LeaseID: fmt.Sprint(id)}
			ok, err := c.AcquireLiveLeaseTransferring(ctx, r)
			require.ErrorIs(t, err, service.ErrLiveLeaseSourceLost)
			require.False(t, ok)
			assertUserLiveLoad(t, c, id, 1)
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			_, err = c.AcquireLiveLeaseTransferring(cancelled, r)
			require.Error(t, err)
			slot.ReleaseFunc()
			assertUserLiveLoad(t, c, id, 0)
			r.AccountMax = 0
			r.AccountRequestID = ""
			r.LeaseID += "-abort"
			ok, err = c.AcquireLiveLeaseTransferring(ctx, r)
			require.NoError(t, err)
			require.True(t, ok)
			assertUserLiveLoad(t, c, id, 1)
			for range 2 {
				require.NoError(t, c.AbortLiveLeaseTransfer(ctx, r))
				assertUserLiveLoad(t, c, id, 0)
			}
			_, err = c.AcquireLiveLeaseTransferring(ctx, r)
			require.ErrorIs(t, err, service.ErrLiveLeaseTransferFenced)
			// A failed tracking write must not prevent SQL Live admission, and a
			// failed read must report unavailable rather than manufacture zero.
			base := c.ConcurrencyCache.(*concurrencyCache)
			require.NoError(t, base.rdb.Close())
			untracked, err := svc.AcquireUserSlot(ctx, id, 0)
			require.NoError(t, err)
			require.True(t, untracked.Acquired)
			r.LeaseID += "-offline"
			r.UserRequestID = untracked.RequestID
			ok, err = c.AcquireLiveLeaseTransferring(ctx, r)
			require.NoError(t, err)
			require.True(t, ok)
			_, err = c.GetUsersLoadBatch(ctx, []service.UserWithConcurrency{{ID: id}})
			require.Error(t, err)
			untracked.ReleaseFunc()
			require.NoError(t, c.ReleaseLiveLease(ctx, id, id, id, r.LeaseID))
		})
	}
}
