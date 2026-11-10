//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type retiredGroupBatchCache struct {
	*groupLifecycleTestCache
	beforeCapture func(SchedulerBucket)
	deletedIDs    []int64
}

func (c *retiredGroupBatchCache) CaptureBucketWriteToken(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	if c.beforeCapture != nil {
		c.beforeCapture(bucket)
	}
	return c.groupLifecycleTestCache.CaptureBucketWriteToken(ctx, bucket)
}

func (c *retiredGroupBatchCache) DeleteAccount(_ context.Context, id int64) error {
	c.deletedIDs = append(c.deletedIDs, id)
	return nil
}

type retiredGroupBatchAccounts struct{ *groupLifecycleTestAccountRepo }

func (r *retiredGroupBatchAccounts) GetByID(context.Context, int64) (*Account, error) {
	return nil, ErrAccountNotFound
}

func newRetiredGroupBatchFixture(t *testing.T, groupID int64) (*retiredGroupBatchCache, *groupLifecycleTestGroupRepo, *retiredGroupBatchAccounts, *SchedulerSnapshotService) {
	t.Helper()
	historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "obsolete"}
	cache := &retiredGroupBatchCache{groupLifecycleTestCache: newGroupLifecycleTestCache(historical)}
	for _, bucket := range schedulerCanonicalBuckets(groupID) {
		// Seed retirement without counting a lifecycle reconciliation.
		require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), bucket))
	}
	groups := &groupLifecycleTestGroupRepo{}
	accounts := &retiredGroupBatchAccounts{groupLifecycleTestAccountRepo: &groupLifecycleTestAccountRepo{}}
	svc := newGroupLifecycleTestService(cache, accounts, groups, config.RunModeStandard)
	return cache, groups, accounts, svc
}

func TestSchedulerRetiredGroupBatchReconcilesOnce(t *testing.T) {
	for _, state := range []string{"missing", "inactive"} {
		for _, publication := range []string{"main", "retry"} {
			t.Run(state+"/"+publication, func(t *testing.T) {
				const groupID int64 = 711
				cache, groups, accounts, svc := newRetiredGroupBatchFixture(t, groupID)
				if state == "inactive" {
					groups.set(&Group{ID: groupID, Status: StatusDisabled, Hydrated: true}, nil)
				}
				ctx := context.WithValue(context.Background(), schedulerOutboxStrictKey{}, true)
				if publication == "retry" {
					ctx = context.WithValue(context.Background(), schedulerOutboxRetryPublicationKey{}, true)
				}
				event := SchedulerOutboxEvent{EventType: SchedulerOutboxEventAccountChanged, AccountID: ptrInt64(101),
					Payload: map[string]any{"group_ids": []any{groupID}}}

				require.NoError(t, svc.handleOutboxEvent(ctx, event, make(map[batchSeenKey]struct{})))

				require.Equal(t, 1, groups.callCount(), "retired group authority must be read once per bucket preparation")
				acquires, releases, lists := cache.lifecycleCounts()
				require.Equal(t, 1, acquires)
				require.Equal(t, 1, releases)
				require.Equal(t, 1, lists)
				want := append(schedulerCanonicalBuckets(groupID), cache.listBuckets...)
				require.ElementsMatch(t, want, cache.retiredBuckets(), "retire canonical and historical buckets exactly once")
				captures, reopens := cache.captureAndReopenCounts()
				require.Equal(t, schedulerCanonicalBucketCount(), captures, "every bucket must still capture its current epoch")
				require.Zero(t, reopens)
				require.Zero(t, accounts.callCount())
				require.Equal(t, []int64{101}, cache.deletedIDs)
			})
		}
	}
}

func TestSchedulerRetiredGroupBatchRechecksNextCall(t *testing.T) {
	const groupID int64 = 712
	cache, groups, accounts, svc := newRetiredGroupBatchFixture(t, groupID)
	ctx := context.WithValue(context.Background(), schedulerOutboxStrictKey{}, true)
	buckets := schedulerCanonicalBuckets(groupID)
	require.NoError(t, svc.rebuildBuckets(ctx, buckets, "inactive"))
	groups.set(&Group{ID: groupID, Status: StatusActive, Hydrated: true}, nil)

	require.NoError(t, svc.rebuildBuckets(ctx, buckets, "active"))

	require.Equal(t, 2, groups.callCount(), "completed retirement must not be cached across calls or events")
	require.Len(t, cache.tokens(), len(buckets))
	require.Positive(t, accounts.callCount())
	for _, bucket := range buckets {
		_, published := cache.counts(bucket)
		require.Equal(t, 1, published, bucket.String())
	}
}

func TestSchedulerRetiredGroupBatchDoesNotCacheFailure(t *testing.T) {
	const groupID int64 = 713
	cache, groups, _, svc := newRetiredGroupBatchFixture(t, groupID)
	wantErr := errors.New("synthetic partial retirement failure")
	cache.retireErr, cache.retireErrAt = wantErr, 1
	ctx := context.WithValue(context.Background(), schedulerOutboxStrictKey{}, true)
	buckets := schedulerCanonicalBuckets(groupID)[:2]

	tasks, err := svc.prepareBucketWriteTasks(ctx, buckets)

	require.ErrorIs(t, err, wantErr, "later success must not acknowledge an earlier failed reconciliation")
	require.Empty(t, tasks)
	require.Equal(t, 2, groups.callCount(), "failed lifecycle work must be retried for the next retired bucket")
	acquires, releases, lists := cache.lifecycleCounts()
	require.Equal(t, 2, acquires)
	require.Equal(t, 2, releases)
	require.Equal(t, 2, lists)
}

func TestSchedulerRetiredGroupBatchCapturesConcurrentReopen(t *testing.T) {
	const groupID int64 = 714
	cache, groups, _, svc := newRetiredGroupBatchFixture(t, groupID)
	ctx := context.WithValue(context.Background(), schedulerOutboxStrictKey{}, true)
	buckets := schedulerCanonicalBuckets(groupID)[:2]
	cache.beforeCapture = func(bucket SchedulerBucket) {
		if bucket == buckets[1] {
			// A concurrent lifecycle owner reactivates this bucket after the
			// first inactive decision, before its own epoch capture.
			groups.set(&Group{ID: groupID, Status: StatusActive, Hydrated: true}, nil)
			_, err := cache.retirementRaceCache.ReopenBucket(ctx, bucket)
			require.NoError(t, err)
		}
	}

	tasks, err := svc.prepareBucketWriteTasks(ctx, buckets)

	require.NoError(t, err)
	require.Len(t, tasks, 1, "successful captures must take precedence over a previous inactive decision")
	require.Equal(t, buckets[1], tasks[0].bucket)
	require.NoError(t, cache.SetSnapshot(ctx, tasks[0].bucket, tasks[0].token, nil))
	// Retirement/reopening after capture must still fence the prepared token.
	require.NoError(t, cache.retirementRaceCache.RetireBucket(ctx, buckets[1]))
	_, err = cache.retirementRaceCache.ReopenBucket(ctx, buckets[1])
	require.NoError(t, err)
	require.ErrorIs(t, cache.SetSnapshot(ctx, tasks[0].bucket, tasks[0].token, nil), ErrSchedulerBucketWriteFenced)
	require.Equal(t, 1, groups.callCount())
}
