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
