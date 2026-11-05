package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type retryEventCache struct {
	*outboxCleanupCache
	failAccount  int64
	calls        []int64
	watermarkErr error
}

func (c *retryEventCache) UpdateLastUsed(_ context.Context, updates map[int64]time.Time) error {
	for id := range updates {
		c.calls = append(c.calls, id)
		if id == c.failAccount {
			return errors.New("synthetic cache failure")
		}
	}
	return nil
}

func (c *retryEventCache) SetOutboxWatermark(ctx context.Context, id int64) error {
	if c.watermarkErr != nil {
		return c.watermarkErr
	}
	return c.outboxCleanupCache.SetOutboxWatermark(ctx, id)
}

type retryEventRepo struct {
	*outboxCleanupRepo
	pending     map[int64]SchedulerOutboxRetry
	deferErr    error
	deferCalls  int
	rescheduled []time.Duration
	completeErr error
}

func (r *retryEventRepo) DeferOutboxEvent(_ context.Context, eventID int64) error {
	r.deferCalls++
	if r.deferErr != nil {
		return r.deferErr
	}
	if _, exists := r.pending[eventID]; !exists {
		for _, event := range r.events {
			if event.ID == eventID {
				r.pending[eventID] = SchedulerOutboxRetry{Event: event}
				return nil
			}
		}
		return errors.New("original event missing")
	}
	return nil
}

func (r *retryEventRepo) ClaimOutboxRetry(_ context.Context, _ time.Duration) (*SchedulerOutboxRetry, error) {
	var selected int64
	for id := range r.pending {
		if selected == 0 || id < selected {
			selected = id
		}
	}
	if selected == 0 {
		return nil, nil
	}
	retry := r.pending[selected]
	retry.Generation++
	retry.Attempts++
	r.pending[selected] = retry
	return &retry, nil
}

func (r *retryEventRepo) CompleteOutboxRetry(_ context.Context, eventID, generation int64) error {
	if r.completeErr != nil {
		return r.completeErr
	}
	if r.pending[eventID].Generation != generation {
		return errors.New("ownership changed")
	}
	delete(r.pending, eventID)
	return nil
}

func (r *retryEventRepo) RescheduleOutboxRetry(_ context.Context, eventID, generation int64, delay time.Duration) error {
	if r.pending[eventID].Generation != generation {
		return errors.New("ownership changed")
	}
	r.rescheduled = append(r.rescheduled, delay)
	return nil
}

func retryLastUsedEvent(id int64) SchedulerOutboxEvent {
	return SchedulerOutboxEvent{ID: id, EventType: SchedulerOutboxEventAccountLastUsed,
		CreatedAt: time.Now().Add(-time.Hour),
		Payload:   map[string]any{"last_used": map[string]any{strconv.FormatInt(id, 10): float64(123)}},
	}
}

func newRetryEventFixture() (*retryEventCache, *retryEventRepo, *SchedulerSnapshotService) {
	cache := &retryEventCache{outboxCleanupCache: &outboxCleanupCache{}, failAccount: 2}
	repo := &retryEventRepo{
		outboxCleanupRepo: &outboxCleanupRepo{
			events: []SchedulerOutboxEvent{retryLastUsedEvent(1), retryLastUsedEvent(2), retryLastUsedEvent(3)},
			rows:   []int64{1, 2, 3}, lockAcquired: true,
		},
		pending: make(map[int64]SchedulerOutboxRetry),
	}
	return cache, repo, NewSchedulerSnapshotService(cache, repo, nil, nil, nil)
}

func TestSchedulerOutboxRetryDefersBeforeWatermarkAndSurvivesOriginalCleanup(t *testing.T) {
	cache, repo, svc := newRetryEventFixture()
	svc.pollOutbox()
	require.Equal(t, []int64{1, 2, 3}, cache.calls, "a failed event must not block its successor")
	require.Equal(t, int64(3), cache.watermark)
	require.Empty(t, repo.rows, "ordinary outbox cleanup can remove the copied original")
	require.Contains(t, repo.pending, int64(2))
	require.Equal(t, retryLastUsedEvent(2).Payload, repo.pending[2].Event.Payload)

	// A new process has no local retry state and the original has been cleaned.
	// Only the durable retry repository is needed to recover the failed change.
	cache.failAccount = 0
	restarted := NewSchedulerSnapshotService(cache, repo, nil, nil, nil)
	restarted.pollOutboxRetry(repo)
	require.Empty(t, repo.pending)
	require.Equal(t, []int64{1, 2, 3, 2}, cache.calls)
	require.Equal(t, int64(3), cache.watermark, "retry completion must not move the main watermark backwards")
}

func TestSchedulerOutboxRetryPersistenceFailureCommitsOnlyPrefixAndBacksOff(t *testing.T) {
	cache, repo, svc := newRetryEventFixture()
	repo.deferErr = errors.New("synthetic durable queue outage")
	svc.pollOutbox()
	require.Equal(t, int64(1), cache.watermark)
	require.Equal(t, []int64{2, 3}, repo.rows, "failed and later events must remain durable")
	require.Equal(t, []int64{1, 2}, cache.calls)
	require.Empty(t, repo.pending)

	svc.pollOutbox()
	require.Equal(t, []int64{1, 2}, cache.calls, "polling during backoff must not repeat expensive handling")
	require.Equal(t, 1, repo.deferCalls)

	repo.deferErr = nil
	svc.outboxEventRetryMu.Lock()
	svc.outboxEventRetry.retryAt = time.Now().Add(-time.Second)
	svc.outboxEventRetryMu.Unlock()
	svc.pollOutbox()
	require.Equal(t, int64(3), cache.watermark)
	require.Equal(t, []int64{1, 2, 2, 3}, cache.calls)
	require.Contains(t, repo.pending, int64(2))
}

func TestSchedulerOutboxRetryWatermarkFailureKeepsCopyAndDoesNotCleanup(t *testing.T) {
	cache, repo, svc := newRetryEventFixture()
	cache.watermarkErr = errors.New("synthetic watermark outage")
	svc.pollOutbox()
	require.Zero(t, cache.watermark)
	require.Empty(t, repo.deleteCalls)
	require.Contains(t, repo.pending, int64(2))

	cache.watermarkErr = nil
	svc.pollOutbox()
	require.Equal(t, int64(3), cache.watermark)
	require.Len(t, repo.pending, 1, "replaying an ambiguous checkpoint must not duplicate its retry task")
}

func TestSchedulerOutboxRetryFailureReschedulesAndAcknowledgementFailureKeepsTask(t *testing.T) {
	cache, repo, svc := newRetryEventFixture()
	svc.pollOutbox()
	for range 10 {
		svc.pollOutboxRetry(repo)
	}
	require.Len(t, repo.pending, 1)
	require.Equal(t, 5*time.Second, repo.rescheduled[0])
	require.Equal(t, 5*time.Minute, repo.rescheduled[len(repo.rescheduled)-1])

	cache.failAccount = 0
	repo.completeErr = errors.New("synthetic ack outage")
	svc.pollOutboxRetry(repo)
	require.Len(t, repo.pending, 1)
	repo.completeErr = nil
	svc.pollOutboxRetry(repo)
	require.Empty(t, repo.pending)
}

func TestSchedulerOutboxRetryUnavailableStillObservesLagAtCommittedPrefix(t *testing.T) {
	cache, repo, _ := newRetryEventFixture()
	cache.listBucketErr = errors.New("synthetic full rebuild failure")
	repo.deferErr = errors.New("synthetic durable queue outage")
	cfg := &config.Config{Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{
		OutboxLagWarnSeconds: 1, OutboxLagRebuildSeconds: 1, OutboxLagRebuildFailures: 1,
	}}}
	svc := NewSchedulerSnapshotService(cache, repo, nil, nil, cfg)
	svc.pollOutbox()
	svc.pollOutbox()
	require.Equal(t, []int64{1, 1}, repo.firstCreatedAfterID)
	require.Equal(t, 1, cache.listBucketCalls, "failed lag rebuild must retain its own cooldown")
	require.Equal(t, int64(1), cache.watermark, "rebuild failure must never acknowledge a lost event")
}

type retryAccountRepository struct {
	AccountRepository
	old     Account
	current []Account
}

func (r *retryAccountRepository) GetByID(context.Context, int64) (*Account, error) {
	a := r.old
	return &a, nil
}

func (r *retryAccountRepository) WithLatestSchedulerAccounts(_ context.Context, _ []int64, publish func([]Account) error) error {
	return publish(r.current)
}

func (r *retryAccountRepository) ListSchedulableUngroupedByPlatform(context.Context, string) ([]Account, error) {
	return []Account{r.old}, nil
}

type retryAccountCache struct {
	*outboxCleanupCache
	busy      bool
	account   *Account
	snapshot  []Account
	deletedID int64
}

func (c *retryAccountCache) SetAccount(_ context.Context, account *Account) error {
	c.account = account
	return nil
}

func (c *retryAccountCache) DeleteAccount(_ context.Context, id int64) error {
	c.deletedID = id
	return nil
}

func (c *retryAccountCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (bool, error) {
	return !c.busy, nil
}

func (c *retryAccountCache) SetSnapshot(_ context.Context, _ SchedulerBucket, _ SchedulerBucketWriteToken, accounts []Account) error {
	c.snapshot = accounts
	return nil
}

func TestSchedulerOutboxRetryBusyBucketKeepsBothOldAndNewEventDurable(t *testing.T) {
	accountID := int64(101)
	event := SchedulerOutboxEvent{ID: 2, EventType: SchedulerOutboxEventAccountChanged, AccountID: &accountID}
	accounts := &retryAccountRepository{old: Account{ID: accountID, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}}
	accounts.current = []Account{accounts.old}
	cache := &retryAccountCache{outboxCleanupCache: &outboxCleanupCache{}, busy: true}
	repo := &retryEventRepo{
		outboxCleanupRepo: &outboxCleanupRepo{events: []SchedulerOutboxEvent{event, retryLastUsedEvent(3)}},
		pending:           map[int64]SchedulerOutboxRetry{1: {Event: SchedulerOutboxEvent{ID: 1, EventType: SchedulerOutboxEventAccountChanged, AccountID: &accountID}}},
	}
	svc := NewSchedulerSnapshotService(cache, repo, accounts, nil, nil)
	svc.pollOutboxRetry(repo)
	require.Contains(t, repo.pending, int64(1), "busy bucket is not completed compensation")
	require.Len(t, repo.rescheduled, 1)
	svc.pollOutbox()
	require.Equal(t, int64(3), cache.watermark)
	require.Contains(t, repo.pending, int64(2), "new main event must not disappear behind the retry's bucket lock")

	cache.busy = false
	svc.pollOutboxRetry(repo)
	svc.pollOutboxRetry(repo)
	require.Empty(t, repo.pending)
	require.Len(t, cache.snapshot, 1)
}

func TestSchedulerOutboxRetryPublishesCurrentAccountEvenAfterStaleReadAndBusyRebuild(t *testing.T) {
	id := int64(101)
	accounts := &retryAccountRepository{old: Account{
		ID: id, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true,
		Concurrency: 100, Credentials: map[string]any{"access_token": "synthetic-old"},
	}}
	current := accounts.old
	current.Schedulable = false
	current.Concurrency = 1
	current.Credentials = map[string]any{"access_token": "synthetic-new"}
	accounts.current = []Account{current}
	cache := &retryAccountCache{outboxCleanupCache: &outboxCleanupCache{}, busy: true}
	repo := &retryEventRepo{outboxCleanupRepo: &outboxCleanupRepo{}, pending: map[int64]SchedulerOutboxRetry{
		1: {Event: SchedulerOutboxEvent{ID: 1, EventType: SchedulerOutboxEventAccountChanged, AccountID: &id}},
	}}
	svc := NewSchedulerSnapshotService(cache, repo, accounts, nil, nil)
	svc.pollOutboxRetry(repo)
	require.NotNil(t, cache.account)
	require.False(t, cache.account.Schedulable)
	require.Equal(t, 1, cache.account.Concurrency)
	require.Equal(t, "synthetic-new", cache.account.Credentials["access_token"])
	require.Contains(t, repo.pending, int64(1))
}

func TestSchedulerOutboxRetrySnapshotDoesNotRestoreMovedDisabledOrDeletedMembers(t *testing.T) {
	old := Account{ID: 101, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, GroupIDs: []int64{10}}
	for _, state := range []string{"moved", "disabled", "deleted"} {
		t.Run(state, func(t *testing.T) {
			current := old
			accounts := &retryAccountRepository{old: old, current: []Account{current}}
			switch state {
			case "moved":
				accounts.current[0].GroupIDs = []int64{20}
			case "disabled":
				accounts.current[0].Schedulable = false
			case "deleted":
				accounts.current = nil
			}
			cache := &retryAccountCache{outboxCleanupCache: &outboxCleanupCache{}}
			svc := NewSchedulerSnapshotService(cache, nil, accounts, nil, nil)
			ctx := context.WithValue(context.Background(), schedulerOutboxRetryPublicationKey{}, true)
			bucket := SchedulerBucket{GroupID: 10, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
			require.NoError(t, svc.cache.SetSnapshot(ctx, bucket, SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, []Account{old}))
			require.Empty(t, cache.snapshot)
		})
	}
}

func TestSchedulerOutboxRetryOrdinaryFeedbackKeepsItsExistingMergeSemantics(t *testing.T) {
	old := Account{ID: 101, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}
	current := old
	current.Schedulable = false
	accounts := &retryAccountRepository{current: []Account{current}}
	cache := &retryAccountCache{outboxCleanupCache: &outboxCleanupCache{}}
	svc := NewSchedulerSnapshotService(cache, nil, accounts, nil, nil)
	require.NoError(t, svc.cache.SetAccount(context.Background(), &old))
	require.True(t, cache.account.Schedulable, "this PR must not silently replace ordinary request feedback semantics")
}
