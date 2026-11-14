//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/qualityqueue"
	"github.com/Wei-Shaw/sub2api/internal/pkg/qualityqueue/queuetest"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

// Observe a completed production claim, without replacing its SQL or any of
// the repository's optional ownership/401/quota interfaces.
type issue817ObservedPlans struct {
	*scheduledTestPlanRepository
	attempts chan bool
}

func (r *issue817ObservedPlans) ClaimPelican(ctx context.Context, plan *service.ScheduledTestPlan, now, until, next time.Time) (bool, error) {
	claimed, err := r.scheduledTestPlanRepository.ClaimPelican(ctx, plan, now, until, next)
	if plan.TriggerSource == "upstream_5xx" {
		select {
		case r.attempts <- claimed:
		default:
		}
	}
	return claimed, err
}

func TestIssue817ManualWorkerInterleaving(t *testing.T) {
	ctx := context.Background()
	f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: service.QualityActionObserveOnly, TriggerOnUpstream5xx: true})
	require.NoError(t, f.plans.FinishPelican(ctx, f.plan.ID, f.until, time.Now()))
	_, err := integrationDB.Exec(`UPDATE scheduled_test_plans SET last_run_at=NULL WHERE id=$1`, f.plan.ID)
	require.NoError(t, err)
	plan, err := f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	plan.PelicanConfig.QuestionKind = "candy"
	plan.PelicanConfig.Prompt = "manual fixture: return the configured answer"
	plan.PelicanConfig.Quality.ExpectedAnswer = "fixture-answer"
	_, err = f.plans.Update(ctx, plan)
	require.NoError(t, err)
	require.NoError(t, f.plans.TriggerQuality(ctx, plan.ID))
	manual, err := f.plans.GetByID(ctx, plan.ID)
	require.NoError(t, err)

	plans := &issue817ObservedPlans{scheduledTestPlanRepository: f.plans.(*scheduledTestPlanRepository), attempts: make(chan bool, 8)}
	redis := miniredis.RunT(t)
	client := queuetest.NewClient(redis.Addr())
	t.Cleanup(func() { _ = client.Close() })
	queue := qualityqueue.NewRedis(client)
	results := NewScheduledTestResultRepository(integrationDB)
	scheduled := service.ProvideScheduledTestService(plans, results, nil, nil)
	var calls atomic.Int32
	runner := service.NewQuotaTriggerRunnerForTesting(scheduled, NewAccountRepository(integrationEntClient, integrationDB, nil), queue, []string{plan.ModelID},
		func(_ context.Context, _ int64, _ string, cfg *service.PelicanTestConfig) (*service.ScheduledTestResult, error) {
			calls.Add(1)
			return &service.ScheduledTestResult{Status: "success", ResponseText: "fixture-answer", StartedAt: time.Now(), FinishedAt: time.Now(), PelicanConfig: cfg}, nil
		})
	t.Cleanup(runner.Stop)
	signalAt := time.Now().Add(-time.Second).Truncate(time.Millisecond)
	require.NoError(t, queue.Enqueue(ctx, f.account, signalAt))
	runner.StartQuotaTriggerWorkerForTesting()
	select {
	case claimed := <-plans.attempts:
		require.False(t, claimed, "5xx worker must not consume a pending manual request with substituted configuration")
	case <-time.After(5 * time.Second):
		t.Fatal("existing trigger worker did not reach the real repository claim")
	}
	require.Zero(t, calls.Load())
	current, err := f.plans.GetByID(ctx, plan.ID)
	require.NoError(t, err)
	require.True(t, current.ManualQualityRun)
	require.Equal(t, manual.ManualQualityRunID, current.ManualQualityRunID)
	require.Equal(t, manual.NextRunAt, current.NextRunAt)
	require.Nil(t, current.LastRunAt)
	pending, err := queue.Pending(ctx, signalAt.Add(-time.Minute))
	require.NoError(t, err)
	require.Len(t, pending, 1)

	// The actual scheduled path consumes the manual lease and saves the original
	// configuration. Only that completed round may let the worker ACK the 5xx.
	require.True(t, runner.RunQuotaScheduledPlanForTesting(ctx, current))
	require.Eventually(t, func() bool {
		pending, err := queue.Pending(ctx, signalAt.Add(-time.Minute))
		return err == nil && len(pending) == 0
	}, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, int32(1), calls.Load())
	stored, err := results.ListByPlanID(ctx, plan.ID, 10)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Equal(t, "scheduled", stored[0].PelicanConfig.TriggerSource)
	require.Equal(t, "candy", stored[0].PelicanConfig.QuestionKind)
	require.Equal(t, plan.PelicanConfig.Prompt, stored[0].PelicanConfig.Prompt)
	require.Equal(t, "fixture-answer", stored[0].PelicanConfig.Quality.ExpectedAnswer)
	current, err = f.plans.GetByID(ctx, plan.ID)
	require.NoError(t, err)
	require.False(t, current.ManualQualityRun)
	require.Empty(t, current.ManualQualityRunID)
}

func TestIssue817ManualUpdateInterleaving(t *testing.T) {
	ctx := context.Background()
	f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: service.QualityActionObserveOnly})
	require.NoError(t, f.plans.FinishPelican(ctx, f.plan.ID, f.until, time.Now()))
	old, err := f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	svc := service.NewScheduledTestService(f.plans, NewScheduledTestResultRepository(integrationDB))
	require.NoError(t, svc.TriggerQuality(ctx, old.ID))
	manual, err := f.plans.GetByID(ctx, old.ID)
	require.NoError(t, err)
	old.PelicanConfig.RuleName = "edited after manual scheduling"
	// Use UpdatePlan, including its normal next-cron recomputation; the saved
	// runtime marker must come from the database, not this earlier snapshot.
	updated, err := svc.UpdatePlan(ctx, old)
	require.NoError(t, err)
	require.True(t, updated.ManualQualityRun, "configuration save must preserve the newer pending manual request")
	require.Equal(t, manual.NextRunAt, updated.NextRunAt)
	require.NotEmpty(t, updated.ManualQualityRunID)
	require.Equal(t, manual.ManualQualityRunID, updated.ManualQualityRunID)
	require.Equal(t, old.PelicanConfig.RuleName, updated.PelicanConfig.RuleName)
	require.NoError(t, svc.TriggerQuality(ctx, old.ID))
	newest, err := f.plans.GetByID(ctx, old.ID)
	require.NoError(t, err)
	require.NotEqual(t, updated.ManualQualityRunID, newest.ManualQualityRunID)
	updated, err = svc.UpdatePlan(ctx, updated)
	require.NoError(t, err)
	require.Equal(t, newest.ManualQualityRunID, updated.ManualQualityRunID, "an older pending snapshot cannot replace the newer generation")
	require.Equal(t, newest.NextRunAt, updated.NextRunAt)

	// A save prepared while the request was pending must not resurrect it once
	// a worker has consumed it. Pausing remains an explicit cancellation.
	now := time.Now().Add(time.Second).Truncate(time.Microsecond)
	until := now.Add(15 * time.Minute)
	claimed, err := f.plans.ClaimPelican(ctx, updated, now, until, now.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = svc.UpdatePlan(ctx, updated)
	require.NoError(t, err)
	current, err := f.plans.GetByID(ctx, old.ID)
	require.NoError(t, err)
	require.False(t, current.ManualQualityRun)
	require.Empty(t, current.ManualQualityRunID)
	require.NoError(t, f.plans.FinishPelican(ctx, old.ID, until, time.Now()))
	require.NoError(t, svc.TriggerQuality(ctx, old.ID))
	current.Enabled = false
	paused, err := svc.UpdatePlan(ctx, current)
	require.NoError(t, err)
	require.False(t, paused.ManualQualityRun)
}

func TestIssue817ManualGenerationAndLeaseFencing(t *testing.T) {
	ctx := context.Background()
	f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: service.QualityActionObserveOnly})
	require.NoError(t, f.plans.FinishPelican(ctx, f.plan.ID, f.until, time.Now()))
	require.NoError(t, f.plans.TriggerQuality(ctx, f.plan.ID))
	old, err := f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	require.NotEmpty(t, old.ManualQualityRunID)
	require.NoError(t, f.plans.TriggerQuality(ctx, f.plan.ID))
	newer, err := f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	require.NotEqual(t, old.ManualQualityRunID, newer.ManualQualityRunID)
	// Preserve identical timestamps to exercise generation CAS independently
	// of database clock resolution, skew or a stale selection's time fields.
	_, err = integrationDB.Exec(`UPDATE scheduled_test_plans SET updated_at=$2,next_run_at=$3 WHERE id=$1`, old.ID, old.UpdatedAt, old.NextRunAt)
	require.NoError(t, err)
	now := time.Now().Add(time.Second).Truncate(time.Microsecond)
	until := now.Add(15 * time.Minute)
	claimed, err := f.plans.ClaimPelican(ctx, old, now, until, now.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, claimed, "an older manual generation cannot consume a newer request")
	newer, err = f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	data, err := json.Marshal(newer)
	require.NoError(t, err)
	require.NotContains(t, string(data), "_manual_quality_run")
	require.NotContains(t, string(data), newer.ManualQualityRunID)
	claimed, err = f.plans.ClaimPelican(ctx, newer, now, until, now.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, claimed)
	require.True(t, newer.ManualQualityRun)
	require.Error(t, f.plans.TriggerQuality(ctx, newer.ID))
	// Model lease expiry, then accept a new request while the old worker is
	// still alive. Its finish must leave the newer pending request intact.
	expiredUntil := time.Now().Add(-time.Second).Truncate(time.Microsecond)
	_, err = integrationDB.Exec(`UPDATE scheduled_test_plans SET running_until=$2 WHERE id=$1`, newer.ID, expiredUntil)
	require.NoError(t, err)
	require.NoError(t, f.plans.TriggerQuality(ctx, newer.ID))
	pending, err := f.plans.GetByID(ctx, newer.ID)
	require.NoError(t, err)
	finished := time.Now().Truncate(time.Microsecond)
	require.NoError(t, f.plans.FinishPelican(ctx, newer.ID, expiredUntil, finished))
	current, err := f.plans.GetByID(ctx, newer.ID)
	require.NoError(t, err)
	require.Equal(t, pending.ManualQualityRunID, current.ManualQualityRunID)
	require.True(t, current.ManualQualityRun)
	require.Equal(t, pending.NextRunAt, current.NextRunAt)
	require.Nil(t, current.RunningUntil, "the old expired lease really finished; this is not a mismatched-lease no-op")
	require.NotNil(t, current.LastRunAt)
	// PostgreSQL preserves the instant, but its driver may reconstruct a
	// different Location object. Compare exact instants without a tolerance.
	require.True(t, finished.Equal(*current.LastRunAt), "finish must persist the exact completion instant")

	// Legacy bool-only requests remain claimable, with no public config field.
	_, err = integrationDB.Exec(`UPDATE scheduled_test_plans SET pelican_config=pelican_config-'_manual_quality_run_id' WHERE id=$1`, newer.ID)
	require.NoError(t, err)
	legacy, err := f.plans.GetByID(ctx, newer.ID)
	require.NoError(t, err)
	require.True(t, legacy.ManualQualityRun)
	require.Empty(t, legacy.ManualQualityRunID)
	legacyNow := time.Now().Add(time.Second).Truncate(time.Microsecond)
	claimed, err = f.plans.ClaimPelican(ctx, legacy, legacyNow, legacyNow.Add(15*time.Minute), legacyNow.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, claimed)
}
