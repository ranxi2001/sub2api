package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newGroupAntiDistillationTestCache(t *testing.T) (*groupAntiDistillationCache, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	server.SetTime(time.Unix(1_800_000_000, 0))
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache, ok := NewGroupAntiDistillationCache(client).(*groupAntiDistillationCache)
	require.True(t, ok)
	return cache, server
}

func TestGroupAntiDistillationCacheTwentySamplesEighteenZeros(t *testing.T) {
	cache, server := newGroupAntiDistillationTestCache(t)
	ctx := context.Background()
	for i := 0; i < 19; i++ {
		triggered, _, err := cache.Observe(ctx, 7, fmt.Sprint(i), i >= 2, 5*time.Minute, time.Time{})
		require.NoError(t, err)
		require.False(t, triggered, "19 valid samples cannot trigger")
	}
	remaining, err := cache.PauseRemaining(ctx, 7)
	require.NoError(t, err)
	require.Zero(t, remaining)
	triggered, until, err := cache.Observe(ctx, 7, "last", true, 10*time.Minute, time.Time{})
	require.NoError(t, err)
	require.True(t, triggered)
	require.Equal(t, time.Unix(1_800_000_000, 0).Add(10*time.Minute), until)
	remaining, err = cache.PauseRemaining(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, 10*time.Minute, remaining, "the triggering observation determines the duration")
	remaining, err = cache.PauseRemaining(ctx, 8)
	require.NoError(t, err)
	require.Zero(t, remaining, "users are isolated")
	require.False(t, server.Exists(groupAntiDistillationRedisKeys(7)[0]), "trigger consumes the sample window")
}

func TestGroupAntiDistillationCacheWindowSlidesAndDeduplicates(t *testing.T) {
	cache, _ := newGroupAntiDistillationTestCache(t)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		triggered, _, err := cache.Observe(ctx, 7, fmt.Sprint(i), i >= 3, 5*time.Minute, time.Time{})
		require.NoError(t, err)
		require.False(t, triggered, "17 of 20 zeros cannot trigger")
	}
	// Replaying an earlier positive result as zero must not replace its sample.
	triggered, _, err := cache.Observe(ctx, 7, "0", true, 5*time.Minute, time.Time{})
	require.NoError(t, err)
	require.False(t, triggered)
	keys := groupAntiDistillationRedisKeys(7)
	require.EqualValues(t, 20, cache.rdb.LLen(ctx, keys[0]).Val())
	triggered, _, err = cache.Observe(ctx, 7, "20", true, 5*time.Minute, time.Time{})
	require.NoError(t, err)
	require.True(t, triggered, "the oldest positive slides out, leaving 18 of 20 zeros")
}

func TestGroupAntiDistillationCachePauseNotRenewedAndOldInflightIgnored(t *testing.T) {
	cache, server := newGroupAntiDistillationTestCache(t)
	ctx := context.Background()
	var until time.Time
	for i := 0; i < 20; i++ {
		_, observedUntil, err := cache.Observe(ctx, 7, fmt.Sprint(i), true, time.Minute, time.Time{})
		require.NoError(t, err)
		until = observedUntil
	}
	keys := groupAntiDistillationRedisKeys(7)
	firstTTL := server.TTL(keys[2])
	for i := 0; i < 30; i++ {
		triggered, _, err := cache.Observe(ctx, 7, fmt.Sprint(20+i), true, 10*time.Minute, time.Time{})
		require.NoError(t, err)
		require.False(t, triggered)
	}
	require.Equal(t, firstTTL, server.TTL(keys[2]), "an active pause never renews")
	require.False(t, server.Exists(keys[0]), "in-flight completions do not accumulate during a pause")
	server.FastForward(time.Minute + time.Second)
	server.SetTime(until.Add(time.Second))
	remaining, err := cache.PauseRemaining(ctx, 7)
	require.NoError(t, err)
	require.Zero(t, remaining)
	for i := 0; i < 30; i++ {
		triggered, _, err := cache.Observe(ctx, 7, fmt.Sprint(100+i), true, time.Minute, until.Add(-time.Millisecond))
		require.NoError(t, err)
		require.False(t, triggered)
	}
	require.False(t, server.Exists(keys[0]), "late completions from before the pause ended remain excluded")
	triggered, _, err := cache.Observe(ctx, 7, "new-request", true, time.Minute, until.Add(time.Millisecond))
	require.NoError(t, err)
	require.False(t, triggered)
	require.EqualValues(t, 1, cache.rdb.LLen(ctx, keys[0]).Val(), "recovery starts a fresh window")
}

func TestGroupAntiDistillationCacheConcurrentTriggerIsAtomic(t *testing.T) {
	cache, _ := newGroupAntiDistillationTestCache(t)
	ctx := context.Background()
	var triggers atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			triggered, _, err := cache.Observe(ctx, 7, fmt.Sprint(i), true, 5*time.Minute, time.Time{})
			if err != nil {
				t.Error(err)
				return
			}
			if triggered {
				triggers.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 1, triggers.Load(), "exactly one atomic trigger owns notification eligibility")
}

func TestGroupAntiDistillationCacheBoundsStateAndHashesRequestIDs(t *testing.T) {
	cache, server := newGroupAntiDistillationTestCache(t)
	ctx := context.Background()
	keys := groupAntiDistillationRedisKeys(7)
	for _, key := range keys {
		require.Contains(t, key, "{7}", "all Lua keys must share a Redis cluster hash slot")
	}
	const requestID = "private-request-reference"
	triggered, _, err := cache.Observe(ctx, 7, requestID, false, time.Minute, time.Time{})
	require.NoError(t, err)
	require.False(t, triggered)
	digest := sha256.Sum256([]byte(requestID))
	members, err := cache.rdb.ZRange(ctx, keys[1], 0, -1).Result()
	require.NoError(t, err)
	require.Equal(t, []string{hex.EncodeToString(digest[:])}, members)
	for i := 0; i < 300; i++ {
		_, _, err := cache.Observe(ctx, 7, fmt.Sprint(i), false, time.Minute, time.Time{})
		require.NoError(t, err)
	}
	require.EqualValues(t, 20, cache.rdb.LLen(ctx, keys[0]).Val())
	require.EqualValues(t, 256, cache.rdb.ZCard(ctx, keys[1]).Val())
	require.Equal(t, 24*time.Hour, server.TTL(keys[0]))
	require.Equal(t, 24*time.Hour, server.TTL(keys[1]))
	server.FastForward(24*time.Hour + time.Second)
	require.False(t, server.Exists(keys[0]))
	require.False(t, server.Exists(keys[1]))
}

func TestGroupAntiDistillationCacheFailuresNeverEstablishPause(t *testing.T) {
	cache, server := newGroupAntiDistillationTestCache(t)
	ctx := context.Background()
	require.NoError(t, cache.rdb.Close())
	remaining, err := cache.PauseRemaining(ctx, 7)
	require.Error(t, err)
	require.Zero(t, remaining)
	triggered, until, err := cache.Observe(ctx, 7, "request", true, time.Minute, time.Time{})
	require.Error(t, err)
	require.False(t, triggered)
	require.True(t, until.IsZero())
	require.Empty(t, server.Keys())
	unavailable := NewGroupAntiDistillationCache(nil)
	remaining, err = unavailable.PauseRemaining(ctx, 7)
	require.Error(t, err)
	require.Zero(t, remaining)
	triggered, until, err = unavailable.Observe(ctx, 7, "request", true, time.Minute, time.Time{})
	require.Error(t, err)
	require.False(t, triggered)
	require.True(t, until.IsZero())
}
