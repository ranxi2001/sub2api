package service

import (
	"context"
	"slices"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const schedulerOutboxRetryLease = outboxEventTimeout + time.Minute

type schedulerOutboxRetryPublicationKey struct{}
type schedulerOutboxRetrySimpleKey struct{}
type schedulerOutboxStrictKey struct{}

func isStrictOutboxPublication(ctx context.Context) bool {
	strict, _ := ctx.Value(schedulerOutboxStrictKey{}).(bool)
	return strict || isOutboxRetryPublication(ctx)
}

func isOutboxRetryPublication(ctx context.Context) bool {
	marked, _ := ctx.Value(schedulerOutboxRetryPublicationKey{}).(bool)
	return marked
}

func filterOutboxRetryBucketAccounts(ctx context.Context, bucket SchedulerBucket, accounts []Account) []Account {
	simple, _ := ctx.Value(schedulerOutboxRetrySimpleKey{}).(bool)
	filtered := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if !account.IsSchedulable() {
			continue
		}
		if bucket.Mode == SchedulerModeMixed {
			if account.Platform != bucket.Platform && (account.Platform != PlatformAntigravity || !account.IsMixedSchedulingEnabled()) {
				continue
			}
		} else if account.Platform != bucket.Platform {
			continue
		}
		if !simple && ((bucket.GroupID > 0 && !slices.Contains(account.GroupIDs, bucket.GroupID)) ||
			(bucket.GroupID == 0 && len(account.GroupIDs) > 0)) {
			continue
		}
		filtered = append(filtered, account)
	}
	return filtered
}

func (s *SchedulerSnapshotService) deferOutboxEventDurably(event SchedulerOutboxEvent, handleErr error) bool {
	retries, ok := s.outboxRepo.(SchedulerOutboxRetryRepository)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := retries.DeferOutboxEvent(ctx, event.ID); err != nil {
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox retry persistence failed: id=%d type=%s err=%v", event.ID, event.EventType, err)
		return false
	}
	logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox event deferred durably: id=%d type=%s err=%v", event.ID, event.EventType, handleErr)
	return true
}

func (s *SchedulerSnapshotService) runOutboxRetryWorker(retries SchedulerOutboxRetryRepository, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.pollOutboxRetry(retries)
		}
	}
}

func (s *SchedulerSnapshotService) pollOutboxRetry(retries SchedulerOutboxRetryRepository) {
	if !s.writesSnapshots() || s.cache == nil {
		return
	}
	claimCtx, claimCancel := context.WithTimeout(context.Background(), 5*time.Second)
	retry, err := retries.ClaimOutboxRetry(claimCtx, schedulerOutboxRetryLease)
	claimCancel()
	if err != nil {
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox retry claim failed: %v", err)
		return
	}
	if retry == nil {
		return
	}
	eventCtx, eventCancel := context.WithTimeout(context.Background(), outboxEventTimeout)
	eventCtx = context.WithValue(eventCtx, schedulerOutboxRetryPublicationKey{}, true)
	eventCtx = context.WithValue(eventCtx, schedulerOutboxRetrySimpleKey{}, s.isRunModeSimple())
	var handleErr error
	if retry.Event.EventType == SchedulerOutboxEventFullRebuild {
		// Keep the authoritative-publication context and run after any earlier
		// rebuild. A normal coalesced run may use request-feedback publication
		// semantics and cannot acknowledge this delayed authoritative retry.
		s.fullRebuildRunMu.Lock()
		handleErr = s.rebuildFullSnapshot(eventCtx, "outbox_retry")
		s.fullRebuildRunMu.Unlock()
	} else {
		handleErr = s.handleOutboxEvent(eventCtx, retry.Event, make(map[batchSeenKey]struct{}))
	}
	eventCancel()
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if handleErr != nil {
		delay := outboxRebuildRetryDelay(retry.Attempts)
		if err := retries.RescheduleOutboxRetry(finishCtx, retry.Event.ID, retry.Generation, delay); err != nil {
			logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox retry reschedule failed: id=%d generation=%d err=%v", retry.Event.ID, retry.Generation, err)
			return
		}
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox retry handle failed: id=%d type=%s attempts=%d retry_in=%s err=%v", retry.Event.ID, retry.Event.EventType, retry.Attempts, delay, handleErr)
		return
	}
	if err := retries.CompleteOutboxRetry(finishCtx, retry.Event.ID, retry.Generation); err != nil {
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox retry completion failed: id=%d generation=%d err=%v", retry.Event.ID, retry.Generation, err)
		return
	}
	logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox retry recovered: id=%d type=%s attempts=%d", retry.Event.ID, retry.Event.EventType, retry.Attempts)
}

func (s *SchedulerSnapshotService) deleteOutboxAccount(ctx context.Context, accountID int64) error {
	if isOutboxRetryPublication(ctx) {
		if _, ok := s.cache.(*codexOrderedSchedulerCache); ok {
			// Reconfirm an earlier missing-account read under the publication lock.
			// SetAccount deletes only if the fresh source still reports it missing.
			return s.cache.SetAccount(ctx, &Account{ID: accountID})
		}
	}
	return s.cache.DeleteAccount(ctx, accountID)
}

// Used only when durable deferral is unavailable or fails. The event itself
// remains in the original outbox; a restart may retry immediately without
// losing a change or treating an incomplete rebuild as compensation.
type schedulerOutboxEventRetry struct {
	eventID  int64
	failures int
	retryAt  time.Time
}

func (s *SchedulerSnapshotService) outboxEventRetryReady(watermark int64) bool {
	s.outboxEventRetryMu.Lock()
	defer s.outboxEventRetryMu.Unlock()
	if watermark >= s.outboxEventRetry.eventID {
		s.outboxEventRetry = schedulerOutboxEventRetry{}
	}
	return !time.Now().Before(s.outboxEventRetry.retryAt)
}

func (s *SchedulerSnapshotService) deferOutboxEvent(eventID int64) (int, time.Duration) {
	s.outboxEventRetryMu.Lock()
	defer s.outboxEventRetryMu.Unlock()
	if s.outboxEventRetry.eventID != eventID {
		s.outboxEventRetry = schedulerOutboxEventRetry{eventID: eventID}
	}
	s.outboxEventRetry.failures++
	delay := outboxRebuildRetryDelay(s.outboxEventRetry.failures)
	s.outboxEventRetry.retryAt = time.Now().Add(delay)
	return s.outboxEventRetry.failures, delay
}

func (s *SchedulerSnapshotService) clearOutboxEventRetryThrough(eventID int64) {
	s.outboxEventRetryMu.Lock()
	defer s.outboxEventRetryMu.Unlock()
	if eventID >= s.outboxEventRetry.eventID {
		s.outboxEventRetry = schedulerOutboxEventRetry{}
	}
}
