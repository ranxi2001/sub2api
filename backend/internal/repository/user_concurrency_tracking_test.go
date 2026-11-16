package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestUnlimitedUserConcurrencySharesExistingLimitCounters(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 900)
	svc := service.NewConcurrencyService(cache)
	ctx := context.Background()
	first, err := svc.AcquireUserSlot(ctx, 42, 0)
	require.NoError(t, err)
	second, err := svc.AcquireUserSlot(ctx, 42, 0)
	require.NoError(t, err)
	require.True(t, first.Acquired)
	require.True(t, second.Acquired)
	require.NoError(t, client.ZScore(ctx, userActiveIndexKey, "42").Err())
	loads, err := svc.GetUsersLoadBatch(ctx, []service.UserWithConcurrency{{ID: 42, MaxConcurrency: 0}})
	require.NoError(t, err)
	require.Equal(t, 2, loads[42].CurrentConcurrency)
	// Setting a limit observes already-running requests from the same set.
	blocked, err := svc.AcquireUserSlot(ctx, 42, 2)
	require.NoError(t, err)
	require.False(t, blocked.Acquired)
	first.ReleaseFunc()
	second.ReleaseFunc()
	count, err := cache.GetUserConcurrency(ctx, 42)
	require.NoError(t, err)
	require.Zero(t, count)
	limited, err := svc.AcquireUserSlot(ctx, 42, 1)
	require.NoError(t, err)
	require.True(t, limited.Acquired)
	limited.ReleaseFunc()
}

func TestUnlimitedUserLiveTransferCountsExactlyOnce(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, ok := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	require.True(t, ok)
	svc := service.NewConcurrencyService(cache)
	ctx := context.Background()
	slot, err := svc.AcquireUserSlot(ctx, 42, 0)
	require.NoError(t, err)
	request := service.LiveLeaseTransferRequest{AccountID: 10, UserID: 42, APIKeyID: 20, UserRequestID: slot.RequestID, LeaseID: "user-stats-live"}
	acquired, err := cache.AcquireLiveLeaseTransferring(ctx, request)
	require.NoError(t, err)
	require.True(t, acquired)
	count, err := cache.GetUserConcurrency(ctx, 42)
	require.NoError(t, err)
	require.Equal(t, 1, count, "ordinary stats slot must move into the Live lease")
	slot.ReleaseFunc()
	count, err = cache.GetUserConcurrency(ctx, 42)
	require.NoError(t, err)
	require.Equal(t, 1, count, "releasing the old owner must preserve the Live lease")
	require.NoError(t, cache.ReleaseLiveLease(ctx, 10, 42, 20, request.LeaseID))
	count, err = cache.GetUserConcurrency(ctx, 42)
	require.NoError(t, err)
	require.Zero(t, count)
}
