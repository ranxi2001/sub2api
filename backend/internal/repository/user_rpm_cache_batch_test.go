package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestUserRPMBatchReadsLimiterCountersWithoutIncrementing(t *testing.T) {
	server := miniredis.RunT(t)
	server.SetTime(time.Date(2026, 11, 17, 12, 0, 30, 0, time.UTC))
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	cache := &userRPMCacheImpl{rdb: client}
	ctx := context.Background()
	for range 3 {
		_, err := cache.IncrementUserRPM(ctx, 42)
		require.NoError(t, err)
	}
	before := server.Keys()
	counts, err := cache.GetUserRPMBatch(ctx, []int64{42, 43, 42})
	require.NoError(t, err)
	require.Equal(t, map[int64]int{42: 3, 43: 0}, counts)
	require.Equal(t, before, server.Keys())
	count, err := cache.GetUserRPM(ctx, 42)
	require.NoError(t, err)
	require.Equal(t, 3, count)
	server.SetTime(time.Date(2026, 11, 17, 12, 1, 0, 0, time.UTC))
	counts, err = cache.GetUserRPMBatch(ctx, []int64{42})
	require.NoError(t, err)
	require.Equal(t, 0, counts[42])
	require.NoError(t, client.Close())
	counts, err = cache.GetUserRPMBatch(ctx, []int64{42})
	require.Error(t, err)
	require.Nil(t, counts)
}
