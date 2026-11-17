//go:build unit

package repository

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type snapshotReadHook struct {
	direct    int
	pipelines [][]redis.Cmder
}

func (h *snapshotReadHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}
func (h *snapshotReadHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error { h.direct++; return next(ctx, cmd) }
}
func (h *snapshotReadHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		h.pipelines = append(h.pipelines, cmds)
		return next(ctx, cmds)
	}
}

func TestSchedulerSnapshotPipelinesBoundedFieldsAndFreshLastUsed(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	cache.mgetChunkSize = 2
	bucket := service.SchedulerBucket{GroupID: 91, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	accounts := []service.Account{{ID: 801}, {ID: 802}, {ID: 803}}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, accounts))
	latest := time.Unix(1700000000, 0).UTC()
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{803: latest}))
	hook := &snapshotReadHook{}
	cache.rdb.AddHook(hook)
	result, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, result, 3)
	for i, account := range result {
		require.Equal(t, accounts[i].ID, account.ID)
	}
	require.Equal(t, latest, *result[2].LastUsedAt)
	require.Equal(t, 3, hook.direct)  // membership ZRANGE + two bounded metadata existence checks
	require.Len(t, hook.pipelines, 3) // control pair + two bounded field pairs
	for _, pair := range hook.pipelines[1:] {
		require.Len(t, pair, 2)
		for _, command := range pair {
			require.Equal(t, "mget", command.Name())
			require.LessOrEqual(t, len(command.Args())-1, 2)
		}
	}
	// Side-key changes must be visible without publishing a new bucket version.
	newer := latest.Add(time.Minute)
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{803: newer}))
	result, hit, err = cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, newer, *result[2].LastUsedAt)
	require.NoError(t, cache.rdb.Del(ctx, schedulerAccountMetaKey(strconv.Itoa(802))).Err())
	_, hit, err = cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.False(t, hit) // missing metadata cannot return a partial candidate pool
}

func TestSchedulerSnapshotPipelineControlErrors(t *testing.T) {
	for _, kind := range []string{"missing_ready", "not_ready", "missing_active", "wrong_ready", "wrong_active", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			cache := newSchedulerCacheUnit(t)
			ctx := context.Background()
			bucket := service.SchedulerBucket{GroupID: 92, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
			ready := schedulerBucketKey(schedulerReadyPrefix, bucket)
			active := schedulerBucketKey(schedulerActivePrefix, bucket)
			switch kind {
			case "missing_ready":
				require.NoError(t, cache.rdb.LPush(ctx, active, "bad").Err())
			case "not_ready":
				require.NoError(t, cache.rdb.Set(ctx, ready, "0", 0).Err())
				require.NoError(t, cache.rdb.LPush(ctx, active, "bad").Err())
			case "missing_active", "wrong_active":
				require.NoError(t, cache.rdb.Set(ctx, ready, "1", 0).Err())
				if kind == "wrong_active" {
					require.NoError(t, cache.rdb.LPush(ctx, active, "bad").Err())
				}
			case "wrong_ready":
				require.NoError(t, cache.rdb.LPush(ctx, ready, "bad").Err())
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			_, hit, err := cache.GetSnapshot(ctx, bucket)
			require.False(t, hit)
			if kind == "wrong_ready" || kind == "wrong_active" || kind == "cancelled" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
