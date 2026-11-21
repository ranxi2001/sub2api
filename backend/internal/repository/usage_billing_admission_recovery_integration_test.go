//go:build integration

package repository

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Only observations are wrapped: selection, settlement and acknowledgement all
// use the real repository. Worker shutdown cancels a cycle without a 5s sleep.
type admissionRecoveryObserver struct {
	*usageBillingRepository
	selected chan []service.UsageBillingEventKey
	acked    chan service.UsageBillingEventKey
}

func (r *admissionRecoveryObserver) PendingUsageBilling(ctx context.Context, limit int) ([]service.UsageBillingEventKey, error) {
	keys, err := r.usageBillingRepository.PendingUsageBilling(ctx, limit)
	if err == nil {
		select {
		case r.selected <- keys:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return keys, err
}

func (r *admissionRecoveryObserver) AcknowledgeUsageBillingCache(ctx context.Context, key service.UsageBillingEventKey) error {
	if err := r.usageBillingRepository.AcknowledgeUsageBillingCache(ctx, key); err != nil {
		return err
	}
	select {
	case r.acked <- key:
	case <-ctx.Done():
	}
	return nil
}

func startAdmissionRecoveryCycle(t *testing.T, repo *usageBillingRepository) (*service.UsageRecordWorkerPool, *admissionRecoveryObserver) {
	t.Helper()
	observer := &admissionRecoveryObserver{usageBillingRepository: repo}
	observer.selected = make(chan []service.UsageBillingEventKey, 1)
	observer.acked = make(chan service.UsageBillingEventKey, 2)
	worker := service.ProvideUsageRecordWorkerPool(&config.Config{}, observer, nil, nil)
	t.Cleanup(worker.Stop)
	return worker, observer
}

func waitAdmissionRecoveryValue[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("recovery worker did not reach the expected boundary")
		var zero T
		return zero
	}
}

func requireBillingOwnerRefs(t *testing.T, repo *usageBillingRepository, user int64, refs int) {
	t.Helper()
	require.Eventually(t, func() bool {
		repo.admission.mu.Lock()
		defer repo.admission.mu.Unlock()
		owner := repo.admission.owners[strconv.FormatInt(user, 10)]
		return owner != nil && owner.refs == refs
	}, 10*time.Second, time.Millisecond, "the recovery call must reach the held owner before cancellation")
}

type admissionRecoverySnapshot struct {
	State, Failure, Payload, Result, Balance, Quota string
	Attempts, Logs, Claims                          int
	NextAttempt, StagedAt                           time.Time
}

func readAdmissionRecoverySnapshot(t *testing.T, repo *usageBillingRepository, cmd service.UsageBillingCommand) admissionRecoverySnapshot {
	t.Helper()
	var s admissionRecoverySnapshot
	require.NoError(t, repo.db.QueryRowContext(context.Background(), `SELECT
		e.state,COALESCE(e.failure_code,''),e.payload::text,COALESCE(e.result::text,''),
		e.attempts,e.next_attempt_at,e.staged_at,u.balance::text,k.quota_used::text,
		(SELECT count(*) FROM usage_logs WHERE request_id=e.request_id AND api_key_id=e.api_key_id),
		(SELECT count(*) FROM usage_billing_dedup WHERE request_id=e.request_id AND api_key_id=e.api_key_id)
		FROM usage_billing_events e JOIN users u ON u.id=$3 JOIN api_keys k ON k.id=e.api_key_id
		WHERE e.request_id=$1 AND e.api_key_id=$2`, cmd.RequestID, cmd.APIKeyID, cmd.UserID).
		Scan(&s.State, &s.Failure, &s.Payload, &s.Result, &s.Attempts, &s.NextAttempt, &s.StagedAt,
			&s.Balance, &s.Quota, &s.Logs, &s.Claims))
	return s
}

func TestUsageBillingAdmissionCancellationRotatesRecoveryAndSettlesOnce(t *testing.T) {
	ctx := context.Background()
	repo, cmdA, usageA, client := durableBillingFixture(t)
	userB := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.invalid", Balance: 100})
	keyB := mustCreateApiKey(t, client, &service.APIKey{UserID: userB.ID, Quota: 100})
	cmdB, usageB := cmdA, usageA
	cmdB.RequestID, cmdB.UserID, cmdB.APIKeyID = uuid.NewString(), userB.ID, keyB.ID
	usageB.RequestID, usageB.UserID, usageB.APIKeyID = cmdB.RequestID, cmdB.UserID, cmdB.APIKeyID
	require.NoError(t, repo.StageUsageBilling(ctx, &cmdA, &usageA))
	require.NoError(t, repo.StageUsageBilling(ctx, &cmdB, &usageB))
	keyA := service.UsageBillingEventKey{RequestID: cmdA.RequestID, APIKeyID: cmdA.APIKeyID}
	b := service.UsageBillingEventKey{RequestID: cmdB.RequestID, APIKeyID: cmdB.APIKeyID}
	// Make A deterministically oldest and preserve pre-existing retry history.
	_, err := repo.db.ExecContext(ctx, `UPDATE usage_billing_events SET
		next_attempt_at=NOW()-CASE WHEN request_id=$1 THEN INTERVAL '2 seconds' ELSE INTERVAL '1 second' END,
		attempts=CASE WHEN request_id=$1 THEN 4 ELSE 0 END,
		failure_code=CASE WHEN request_id=$1 THEN 'apply_failed' ELSE NULL END`, keyA.RequestID)
	require.NoError(t, err)
	beforeA := readAdmissionRecoverySnapshot(t, repo, cmdA)
	beforeB := readAdmissionRecoverySnapshot(t, repo, cmdB)
	hold, err := repo.admission.acquire(ctx, strconv.FormatInt(cmdA.UserID, 10), repo.db.Stats().MaxOpenConnections)
	require.NoError(t, err)
	t.Cleanup(func() {
		if hold != nil {
			hold()
		}
	})
	worker, observer := startAdmissionRecoveryCycle(t, repo)
	require.Equal(t, []service.UsageBillingEventKey{keyA, b}, waitAdmissionRecoveryValue(t, observer.selected))
	requireBillingOwnerRefs(t, repo, cmdA.UserID, 2)
	worker.Stop()
	requireBillingOwnerRefs(t, repo, cmdA.UserID, 1)
	afterA := readAdmissionRecoverySnapshot(t, repo, cmdA)
	require.Equal(t, "pending", afterA.State)
	require.Equal(t, "apply_failed", afterA.Failure)
	require.Equal(t, 5, afterA.Attempts)
	require.True(t, afterA.NextAttempt.After(beforeA.NextAttempt.Add(29*time.Second)))
	// Compare every other field: contention cannot debit, claim, log or mutate
	// the immutable staged command, result or staging timestamp.
	afterA.Attempts, afterA.NextAttempt = beforeA.Attempts, beforeA.NextAttempt
	require.Equal(t, beforeA, afterA)
	require.Equal(t, beforeB, readAdmissionRecoverySnapshot(t, repo, cmdB))

	// A stays held, but its durable backoff allows B into the very next cycle.
	worker, observer = startAdmissionRecoveryCycle(t, repo)
	require.Equal(t, []service.UsageBillingEventKey{b}, waitAdmissionRecoveryValue(t, observer.selected))
	require.Equal(t, b, waitAdmissionRecoveryValue(t, observer.acked))
	worker.Stop()
	settledB := readAdmissionRecoverySnapshot(t, repo, cmdB)
	require.Equal(t, "complete", settledB.State)
	require.Equal(t, "99.00000000", settledB.Balance)
	require.Equal(t, "1.00000000", settledB.Quota)
	require.Equal(t, 1, settledB.Logs)
	require.Equal(t, 1, settledB.Claims)
	require.Zero(t, settledB.Attempts)

	hold()
	hold = nil
	// Advance only the fixture's retry clock, avoiding a 30s backoff sleep.
	_, err = repo.db.ExecContext(ctx, `UPDATE usage_billing_events SET next_attempt_at=NOW()
		WHERE request_id=$1 AND api_key_id=$2`, keyA.RequestID, keyA.APIKeyID)
	require.NoError(t, err)
	worker, observer = startAdmissionRecoveryCycle(t, repo)
	require.Equal(t, []service.UsageBillingEventKey{keyA}, waitAdmissionRecoveryValue(t, observer.selected))
	require.Equal(t, keyA, waitAdmissionRecoveryValue(t, observer.acked))
	worker.Stop()
	settledA := readAdmissionRecoverySnapshot(t, repo, cmdA)
	require.Equal(t, "complete", settledA.State)
	require.Empty(t, settledA.Failure)
	require.Equal(t, 5, settledA.Attempts)
	require.Equal(t, "99.00000000", settledA.Balance)
	require.Equal(t, "1.00000000", settledA.Quota)
	require.Equal(t, 1, settledA.Logs)
	require.Equal(t, 1, settledA.Claims)
	replayed, err := repo.ApplyStagedUsageBilling(ctx, keyA)
	require.NoError(t, err)
	require.True(t, replayed.Settled)
	require.False(t, replayed.Result.Applied)
	require.False(t, replayed.NeedsCache)
	require.Equal(t, settledA, readAdmissionRecoverySnapshot(t, repo, cmdA))
	require.Equal(t, settledB, readAdmissionRecoverySnapshot(t, repo, cmdB))
	require.Empty(t, repo.admission.owners)
}

func TestUsageBillingAdmissionCancellationCannotOverwriteConcurrentReceipt(t *testing.T) {
	for _, state := range []string{"applied", "complete"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			repo, cmd, usage, _ := durableBillingFixture(t)
			require.NoError(t, repo.StageUsageBilling(ctx, &cmd, &usage))
			key := service.UsageBillingEventKey{RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID}
			hold, err := repo.admission.acquire(ctx, strconv.FormatInt(cmd.UserID, 10), repo.db.Stats().MaxOpenConnections)
			require.NoError(t, err)
			defer hold()
			cycle, cancel := context.WithCancel(ctx)
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				defer close(finished)
				_, err := repo.ApplyStagedUsageBilling(cycle, key)
				finished <- err
			}()
			defer func() {
				cancel()
				// Join the waiter before the fixture database is removed.
				for range finished {
				}
			}()
			requireBillingOwnerRefs(t, repo, cmd.UserID, 2)
			// A different instance can commit while the local waiter is queued;
			// admission is local, while the journal lock remains authoritative.
			other := &usageBillingRepository{db: repo.db}
			result, err := other.ApplyStagedUsageBilling(ctx, key)
			require.NoError(t, err)
			require.True(t, result.Settled)
			if state == "complete" {
				require.NoError(t, other.AcknowledgeUsageBillingCache(ctx, key))
			}
			before := readAdmissionRecoverySnapshot(t, repo, cmd)
			require.Equal(t, state, before.State)
			cancel()
			require.ErrorIs(t, waitAdmissionRecoveryValue(t, finished), context.Canceled)
			requireBillingOwnerRefs(t, repo, cmd.UserID, 1)
			require.Equal(t, before, readAdmissionRecoverySnapshot(t, repo, cmd))
		})
	}
}
