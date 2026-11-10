//go:build unit

package repository

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type snapshotReadinessChecker interface {
	CheckSnapshotReadiness(context.Context, []service.SchedulerBucket, time.Duration) error
}

func TestSchedulerReadinessRequiresPublishedSnapshot(t *testing.T) {
	cache, mr := newSchedulerCacheUnitWithRedis(t)
	checker, ok := any(cache).(snapshotReadinessChecker)
	require.True(t, ok, "the production Redis adapter must enforce snapshot readiness")
	ctx := context.Background()
	bucket := service.SchedulerBucket{GroupID: 17, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	require.Error(t, checker.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Minute))
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{{ID: 7, Platform: service.PlatformOpenAI}}))
	require.NoError(t, checker.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Minute))
	mr.Del(schedulerAccountMetaKey("7"))
	require.Error(t, checker.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Minute), "a live Redis connection does not imply complete account metadata")
}

func TestSchedulerReadinessExplicitEmptySnapshot(t *testing.T) {
	for _, name := range []string{"published_empty", "missing_snapshot", "mixed_members", "wrong_generation", "missing_stamp", "stale_stamp", "retired"} {
		t.Run(name, func(t *testing.T) {
			cache, mr := newSchedulerCacheUnitWithRedis(t)
			mr.SetTime(time.Unix(1700000000, 0))
			ctx := t.Context()
			bucket := service.SchedulerBucket{GroupID: 325, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeForced}
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshot(ctx, bucket, token, nil))
			active, err := mr.Get(schedulerBucketKey(schedulerActivePrefix, bucket))
			require.NoError(t, err)
			snapshotKey := schedulerSnapshotKey(bucket, active)
			switch name {
			case "missing_snapshot":
				mr.Del(snapshotKey)
			case "mixed_members":
				require.NoError(t, cache.SetAccount(ctx, &service.Account{ID: 7}))
				require.NoError(t, cache.rdb.ZAdd(ctx, snapshotKey, redis.Z{Member: "7", Score: 1}).Err())
			case "wrong_generation":
				mr.HSet(schedulerBucketKey(schedulerPublishedPrefix, bucket), "version", "old")
			case "missing_stamp":
				mr.Del(schedulerBucketKey(schedulerPublishedPrefix, bucket))
			case "stale_stamp":
				mr.SetTime(time.Unix(1700000061, 0))
			case "retired":
				require.NoError(t, cache.RetireBucket(ctx, bucket))
			}
			err = cache.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Minute)
			if name == "published_empty" {
				require.NoError(t, err, "a deliberately published empty pool is complete without account metadata")
			} else {
				require.ErrorIs(t, err, service.ErrSchedulerCacheNotReady)
			}
		})
	}
}

func TestSchedulerReadinessRejectsStaleRetiredAndIncomplete(t *testing.T) {
	for _, name := range []string{"missing_stamp", "wrong_generation", "old_stamp", "future_stamp", "missing_active", "not_ready", "retired", "missing_members", "invalid_member", "wrong_metadata_type", "empty_metadata", "missing_full_account", "over_limit"} {
		t.Run(name, func(t *testing.T) {
			cache, mr := newSchedulerCacheUnitWithRedis(t)
			mr.SetTime(time.Unix(1700000000, 0))
			ctx := context.Background()
			bucket := service.SchedulerBucket{GroupID: 17, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{{ID: 7}}))
			active, err := mr.Get(schedulerBucketKey(schedulerActivePrefix, bucket))
			require.NoError(t, err)
			stamp := schedulerBucketKey(schedulerPublishedPrefix, bucket)
			switch name {
			case "missing_stamp":
				mr.Del(stamp)
			case "wrong_generation":
				mr.HSet(stamp, "version", "0")
			case "old_stamp":
				mr.SetTime(time.Unix(1700000061, 0))
			case "future_stamp":
				mr.SetTime(time.Unix(1699999999, 0))
			case "missing_active":
				mr.Del(schedulerBucketKey(schedulerActivePrefix, bucket))
			case "not_ready":
				mr.Set(schedulerBucketKey(schedulerReadyPrefix, bucket), "0")
			case "retired":
				require.NoError(t, cache.RetireBucket(ctx, bucket))
			case "missing_members":
				mr.Del(schedulerSnapshotKey(bucket, active))
			case "invalid_member":
				_, err = cache.rdb.ZAdd(ctx, schedulerSnapshotKey(bucket, active), redis.Z{Member: "invalid", Score: 1}).Result()
				require.NoError(t, err)
			case "wrong_metadata_type":
				mr.Del(schedulerAccountMetaKey("7"))
				mr.HSet(schedulerAccountMetaKey("7"), "bad", "type")
			case "empty_metadata":
				mr.Set(schedulerAccountMetaKey("7"), "")
			case "missing_full_account":
				mr.Del(schedulerAccountKey("7"))
			case "over_limit":
				entries := make([]redis.Z, schedulerReadinessMaxAccounts+1)
				for i := range entries {
					entries[i] = redis.Z{Member: strconv.Itoa(i + 1), Score: float64(i)}
				}
				require.NoError(t, cache.rdb.ZAdd(ctx, schedulerSnapshotKey(bucket, active), entries...).Err())
			}
			require.ErrorIs(t, cache.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Minute), service.ErrSchedulerCacheNotReady)
		})
	}
}

func TestSchedulerReadinessPublicationDoesNotRefreshRejectedGeneration(t *testing.T) {
	cache, mr := newSchedulerCacheUnitWithRedis(t)
	ctx := context.Background()
	mr.SetTime(time.Unix(1700000000, 0))
	bucket := service.SchedulerBucket{GroupID: 17, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{{ID: 7}}))
	version, err := mr.Get(schedulerBucketKey(schedulerActivePrefix, bucket))
	require.NoError(t, err)
	mr.SetTime(time.Unix(1700000061, 0))
	require.NoError(t, cache.activateSnapshotVersion(ctx, bucket, token, version))
	require.NoError(t, cache.activateSnapshotVersion(ctx, bucket, token, "0"))
	require.ErrorIs(t, cache.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Minute), service.ErrSchedulerCacheNotReady)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{{ID: 7}}))
	require.NoError(t, cache.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Minute))
	require.NoError(t, cache.RetireBucket(ctx, bucket))
	require.ErrorIs(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{{ID: 7}}), service.ErrSchedulerBucketRetired)
	require.ErrorIs(t, cache.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Minute), service.ErrSchedulerCacheNotReady)
}

func TestSchedulerReadinessRealRedis(t *testing.T) {
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server is required for this isolated adapter check")
	}
	socket := filepath.Join(t.TempDir(), "redis.sock")
	cmd := exec.Command(binary, "--port", "0", "--unixsocket", socket, "--unixsocketperm", "700", "--save", "", "--appendonly", "no")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	client := redis.NewClient(&redis.Options{Network: "unix", Addr: socket, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	require.Eventually(t, func() bool { return client.Ping(context.Background()).Err() == nil }, 3*time.Second, 10*time.Millisecond)
	writer, ok := NewSchedulerCache(client).(*schedulerCache)
	require.True(t, ok)
	// Separate adapter instances sharing one Redis model the full publisher and gateway probe.
	reader, ok := NewSchedulerCache(client).(snapshotReadinessChecker)
	require.True(t, ok)
	ctx := context.Background()
	bucket := service.SchedulerBucket{GroupID: 17, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	require.Error(t, reader.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Second))
	token, err := writer.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, writer.SetSnapshot(ctx, bucket, token, []service.Account{{ID: 7}}))
	require.NoError(t, reader.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Second))
	// Simulate a stalled publisher using the same Redis clock, without wall-clock sleeps.
	now, err := client.Time(ctx).Result()
	require.NoError(t, err)
	require.NoError(t, client.HSet(ctx, schedulerBucketKey(schedulerPublishedPrefix, bucket), "published_ms", now.Add(-2*time.Second).UnixMilli()).Err())
	require.Error(t, reader.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Second))
	require.NoError(t, writer.SetSnapshot(ctx, bucket, token, []service.Account{{ID: 7}}))
	require.NoError(t, reader.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Second))
	require.NoError(t, client.Del(ctx, schedulerAccountMetaKey("7")).Err())
	require.Error(t, reader.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Second))
	require.NoError(t, writer.SetAccount(ctx, &service.Account{ID: 7}))
	require.NoError(t, reader.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Second))
	require.NoError(t, writer.RetireBucket(ctx, bucket))
	require.Error(t, reader.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Second))
	token, err = writer.ReopenBucket(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, writer.SetSnapshot(ctx, bucket, token, []service.Account{{ID: 7}}))
	require.NoError(t, reader.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Second))
	// Publishing no candidates is complete; losing the version-local sorted
	// set or mixing its empty marker with real accounts is not.
	require.NoError(t, writer.SetSnapshot(ctx, bucket, token, nil))
	require.NoError(t, reader.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Second))
	active, err := client.Get(ctx, schedulerBucketKey(schedulerActivePrefix, bucket)).Result()
	require.NoError(t, err)
	snapshotKey := schedulerSnapshotKey(bucket, active)
	require.NoError(t, client.ZAdd(ctx, snapshotKey, redis.Z{Member: "7", Score: 1}).Err())
	require.ErrorIs(t, reader.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Second), service.ErrSchedulerCacheNotReady)
	require.NoError(t, client.ZRem(ctx, snapshotKey, "7").Err())
	require.NoError(t, reader.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Second))
	require.NoError(t, client.Del(ctx, snapshotKey).Err())
	require.ErrorIs(t, reader.CheckSnapshotReadiness(ctx, []service.SchedulerBucket{bucket}, time.Second), service.ErrSchedulerCacheNotReady)
}

func TestSchedulerReadinessAllScopesAndRedisFailure(t *testing.T) {
	cache, mr := newSchedulerCacheUnitWithRedis(t)
	ctx := context.Background()
	buckets := []service.SchedulerBucket{{GroupID: 17, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}, {GroupID: 18, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}}
	token, err := cache.CaptureBucketWriteToken(ctx, buckets[0])
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, buckets[0], token, []service.Account{{ID: 7}}))
	require.Error(t, cache.CheckSnapshotReadiness(ctx, buckets, time.Minute), "one ready bucket cannot hide another missing one")
	token, err = cache.CaptureBucketWriteToken(ctx, buckets[1])
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, buckets[1], token, []service.Account{{ID: 8}}))
	require.NoError(t, cache.CheckSnapshotReadiness(ctx, buckets, time.Minute))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, cache.CheckSnapshotReadiness(canceled, buckets, time.Minute), context.Canceled)
	mr.SetError("synthetic-private-redis-diagnostic")
	err = cache.CheckSnapshotReadiness(ctx, buckets, time.Minute)
	require.ErrorIs(t, err, service.ErrSchedulerCacheNotReady)
	require.NotContains(t, err.Error(), "synthetic-private")
}
