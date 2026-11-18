//go:build integration

package repository

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/qualityqueue"
	"github.com/Wei-Shaw/sub2api/internal/pkg/qualityqueue/queuetest"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

// Fault only at the persistence boundary; every successful operation uses SQL.
type issue817ResultFault struct {
	service.ScheduledTestResultRepository
	failCreate int
	failPrune  bool
	creates    int
	prunes     int
}

func (r *issue817ResultFault) Create(ctx context.Context, result *service.ScheduledTestResult) (*service.ScheduledTestResult, error) {
	r.creates++
	if r.failCreate < 0 || r.creates == r.failCreate {
		return nil, errors.New("issue817 injected Create failure")
	}
	return r.ScheduledTestResultRepository.Create(ctx, result)
}

func (r *issue817ResultFault) PruneOldResults(ctx context.Context, planID int64, keep int) error {
	r.prunes++
	if r.failPrune {
		return errors.New("issue817 injected prune failure after Create")
	}
	return r.ScheduledTestResultRepository.PruneOldResults(ctx, planID, keep)
}

// Embed the concrete repository so optional quota/401/ownership methods remain
// real. Count actions and finishes to detect persistence-driven side effects.
type issue817SavePlans struct {
	*scheduledTestPlanRepository
	lease    time.Time
	next     time.Time
	actions  int
	finishes int
}

func (r *issue817SavePlans) ClaimPelican(ctx context.Context, plan *service.ScheduledTestPlan, now, until, next time.Time) (bool, error) {
	claimed, err := r.scheduledTestPlanRepository.ClaimPelican(ctx, plan, now, until, next)
	if claimed {
		r.lease = until
		r.next = next
	}
	return claimed, err
}

func (r *issue817SavePlans) ApplyQualityOutcome(ctx context.Context, plan *service.ScheduledTestPlan, until time.Time, outcome string) (string, error) {
	r.actions++
	return r.scheduledTestPlanRepository.ApplyQualityOutcome(ctx, plan, until, outcome)
}

func (r *issue817SavePlans) FinishPelican(ctx context.Context, id int64, until, finished time.Time) error {
	r.finishes++
	return r.scheduledTestPlanRepository.FinishPelican(ctx, id, until, finished)
}

func (r *issue817SavePlans) FinishTriggeredQuality(ctx context.Context, plan *service.ScheduledTestPlan, until, finished, next time.Time) error {
	r.finishes++
	return r.scheduledTestPlanRepository.FinishTriggeredQuality(ctx, plan, until, finished, next)
}

func TestIssue817SaveResultFailureDoesNotAcknowledge(t *testing.T) {
	for _, source := range []string{"scheduled", "upstream_5xx"} {
		for _, previous := range []bool{false, true} {
			for _, fault := range []struct {
				name       string
				failCreate int
				failPrune  bool
			}{
				{name: "all_creates", failCreate: -1},
				{name: "first_create", failCreate: 1},
				{name: "partial_create", failCreate: 2},
				{name: "prune_only", failPrune: true},
				{name: "saved_success"},
			} {
				history := "never_run"
				if previous {
					history = "previous_completed_run"
				}
				t.Run(source+"/"+history+"/"+fault.name, func(t *testing.T) {
					ctx := context.Background()
					f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: "disable_scheduling", TriggerOnUpstream5xx: true, OAuth401MaxRounds: 3})
					require.NoError(t, f.plans.FinishPelican(ctx, f.plan.ID, f.until, time.Now()))
					now := time.Now().UTC().Truncate(time.Microsecond)
					var lastRun *time.Time
					if previous {
						prior := now.Add(-time.Hour)
						lastRun = &prior
						// A completed prior 401 round must survive a failed save.
						_, err := integrationDB.Exec(`UPDATE quality_oauth_401_streaks SET failures=1,last_round_until=$2,claimed_until=$2 WHERE plan_id=$1`, f.plan.ID, prior)
						require.NoError(t, err)
					}
					_, err := integrationDB.Exec(`UPDATE scheduled_test_plans SET last_run_at=$2,next_run_at=$3,
 pelican_config=jsonb_set(pelican_config,'{model_ids}','["gpt-6-astra","gpt-6-sol"]'::jsonb) WHERE id=$1`, f.plan.ID, lastRun, now.Add(-time.Minute))
					require.NoError(t, err)
					// The real multi-model 5xx path chooses candy, with no live calls.
					plan, err := f.plans.GetByID(ctx, f.plan.ID)
					require.NoError(t, err)
					plan.PelicanConfig.QuestionKind = "candy"
					plan.PelicanConfig.Prompt = service.CandyPrompt
					plan.PelicanConfig.Quality.ExpectedAnswer = "21"
					plan, err = f.plans.Update(ctx, plan)
					require.NoError(t, err)
					// Update changes the config version: seed prior ledger against it.
					if previous {
						_, err = integrationDB.Exec(`UPDATE quality_oauth_401_streaks SET config_updated_at=$2,
 config_revision=(SELECT md5(jsonb_build_array(pelican_config,model_id,cron_expression)::text) FROM scheduled_test_plans WHERE id=$1) WHERE plan_id=$1`, plan.ID, plan.UpdatedAt)
						require.NoError(t, err)
					}
					plans := &issue817SavePlans{scheduledTestPlanRepository: f.plans.(*scheduledTestPlanRepository)}
					results := &issue817ResultFault{ScheduledTestResultRepository: NewScheduledTestResultRepository(integrationDB), failCreate: fault.failCreate, failPrune: fault.failPrune}
					redis := miniredis.RunT(t)
					client := queuetest.NewClient(redis.Addr())
					t.Cleanup(func() { _ = client.Close() })
					queue := qualityqueue.NewRedis(client)
					t.Cleanup(func() { _ = queue.Close() })
					scheduled := service.ProvideScheduledTestService(plans, results, nil, nil)
					var calls atomic.Int32
					execute := func(_ context.Context, _ int64, _ string, cfg *service.PelicanTestConfig) (*service.ScheduledTestResult, error) {
						calls.Add(1)
						return &service.ScheduledTestResult{Status: "failed", ErrorMessage: "answer_mismatch", StartedAt: now, FinishedAt: now,
							PelicanConfig: cfg, QualityJudgment: &service.QualityJudgment{Verdict: "incorrect"}}, nil
					}
					accounts := NewAccountRepository(integrationEntClient, integrationDB, nil)
					runner := service.NewQuotaTriggerRunnerForTesting(scheduled, accounts, queue, []string{"gpt-6-astra", "gpt-6-sol"}, execute)
					signal := qualityqueue.Signal{AccountID: f.account, ObservedAt: now.Add(-time.Second).Truncate(time.Millisecond)}
					require.NoError(t, queue.Enqueue(ctx, signal.AccountID, signal.ObservedAt))
					if source == "scheduled" {
						require.Equal(t, fault.failCreate == 0, runner.RunQuotaScheduledPlanForTesting(ctx, plan))
					}
					attemptErr := runner.RunQualityTriggerForTesting(ctx, signal)
					current, err := plans.GetByID(ctx, plan.ID)
					require.NoError(t, err)
					pending, err := queue.Pending(ctx, now.Add(-20*time.Minute))
					require.NoError(t, err)
					stored, err := results.ListByPlanID(ctx, plan.ID, 10)
					require.NoError(t, err)
					require.Equal(t, int32(2), calls.Load())
					require.Equal(t, 2, results.creates, "never retry a Create or replay a probe to save history")
					require.Equal(t, 1, plans.actions, "apply the actual quality outcome once")
					account, err := accounts.GetByID(ctx, f.account)
					require.NoError(t, err)
					require.False(t, account.Schedulable, "the original quality action really committed before persistence")
					for _, result := range stored {
						require.Equal(t, "scheduling_disabled", result.QualityAction)
						require.Equal(t, source, result.PelicanConfig.TriggerSource)
						require.Equal(t, plans.lease.Format(time.RFC3339Nano), result.QualityRoundID)
					}
					require.NotNil(t, current.NextRunAt)
					require.True(t, plans.next.Equal(*current.NextRunAt), "keep the next cron computed by the claim")
					if fault.failCreate != 0 {
						require.Error(t, attemptErr)
						require.Len(t, pending, 1)
						require.Equal(t, signal.AccountID, pending[0].AccountID)
						require.True(t, signal.ObservedAt.Equal(pending[0].ObservedAt))
						if fault.failCreate < 0 {
							require.Empty(t, stored)
						} else {
							require.Len(t, stored, 1, "keep the sample that was actually inserted")
						}
						if lastRun == nil {
							require.Nil(t, current.LastRunAt)
						} else {
							require.NotNil(t, current.LastRunAt)
							require.True(t, lastRun.Equal(*current.LastRunAt), "failed persistence cannot cover the pending signal")
						}
						require.NotNil(t, current.RunningUntil)
						require.True(t, plans.lease.Equal(*current.RunningUntil), "preserve the exact claimed lease")
						require.Zero(t, plans.finishes)
						var failures int
						var completed *time.Time
						require.NoError(t, integrationDB.QueryRow(`SELECT failures,last_round_until FROM quality_oauth_401_streaks WHERE plan_id=$1`, plan.ID).Scan(&failures, &completed))
						if previous {
							require.Equal(t, 1, failures, "partial persistence must not reset the prior 401 evidence")
							require.NotNil(t, completed)
							require.True(t, lastRun.Equal(*completed))
						} else {
							require.Nil(t, completed, "failed saves must not complete a 401 round")
						}
						// Fresh runner models another worker/process while the lease is
						// live. Deliberately make storage healthy; no replay is allowed.
						results.failCreate = 0
						fresh := service.NewQuotaTriggerRunnerForTesting(scheduled, accounts, queue, []string{"gpt-6-astra", "gpt-6-sol"}, execute)
						require.Error(t, fresh.RunQualityTriggerForTesting(ctx, signal))
						require.Equal(t, int32(2), calls.Load())
						require.Equal(t, 1, plans.actions)
						require.Equal(t, 2, results.creates)
						pending, err = queue.Pending(ctx, now.Add(-20*time.Minute))
						require.NoError(t, err)
						require.Len(t, pending, 1)
						require.Equal(t, signal.AccountID, pending[0].AccountID)
						require.True(t, signal.ObservedAt.Equal(pending[0].ObservedAt))
					} else {
						require.NoError(t, attemptErr)
						require.Empty(t, pending)
						require.Len(t, stored, 2)
						require.Nil(t, current.RunningUntil)
						require.NotNil(t, current.LastRunAt)
						require.False(t, current.LastRunAt.Before(signal.ObservedAt))
						require.Equal(t, 1, plans.finishes)
						require.Equal(t, 2, results.prunes)
					}
				})
			}
		}
	}
}

func TestIssue817ConnectivitySaveFailureDoesNotCompleteOrRecover(t *testing.T) {
	for _, fault := range []string{"create", "prune", "saved_success"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			var accountID int64
			require.NoError(t, integrationDB.QueryRow(`INSERT INTO accounts(name,platform,type,status,schedulable,extra)
 VALUES('issue817 synthetic','anthropic','oauth','error',true,'{"synthetic_ui_test":true}') RETURNING id`).Scan(&accountID))
			t.Cleanup(func() {
				_, _ = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1`, accountID)
				_, _ = integrationDB.Exec(`DELETE FROM scheduled_test_plans WHERE account_id=$1`, accountID)
				_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, accountID)
			})
			plans := NewScheduledTestPlanRepository(integrationDB)
			now := time.Now().UTC().Truncate(time.Microsecond)
			previous, due := now.Add(-time.Hour), now.Add(-time.Minute)
			plan, err := plans.Create(ctx, &service.ScheduledTestPlan{AccountID: accountID, ModelID: "offline", CronExpression: "*/30 * * * *", Enabled: true, AutoRecover: true, MaxResults: 10, NextRunAt: &due})
			require.NoError(t, err)
			_, err = integrationDB.Exec(`UPDATE scheduled_test_plans SET last_run_at=$2 WHERE id=$1`, plan.ID, previous)
			require.NoError(t, err)
			results := &issue817ResultFault{ScheduledTestResultRepository: NewScheduledTestResultRepository(integrationDB), failPrune: fault == "prune"}
			if fault == "create" {
				results.failCreate = 1
			}
			scheduled := service.ProvideScheduledTestService(plans, results, nil, nil)
			accounts := NewAccountRepository(integrationEntClient, integrationDB, nil)
			runner := service.NewConnectivityRunnerForTesting(scheduled, accounts)
			require.Equal(t, fault != "create", runner.RunQuotaScheduledPlanForTesting(ctx, plan))
			current, err := plans.GetByID(ctx, plan.ID)
			require.NoError(t, err)
			account, err := accounts.GetByID(ctx, accountID)
			require.NoError(t, err)
			stored, err := results.ListByPlanID(ctx, plan.ID, 10)
			require.NoError(t, err)
			require.Equal(t, 1, results.creates)
			if fault == "create" {
				require.Empty(t, stored)
				require.True(t, previous.Equal(*current.LastRunAt))
				require.True(t, due.Equal(*current.NextRunAt))
				require.Equal(t, "error", account.Status, "unsaved success cannot recover the account")
				require.Zero(t, results.prunes)
			} else {
				require.Len(t, stored, 1)
				require.Equal(t, "success", stored[0].Status)
				require.True(t, current.LastRunAt.After(previous))
				require.True(t, current.NextRunAt.After(now))
				require.Equal(t, "active", account.Status)
				require.Equal(t, 1, results.prunes)
			}
		})
	}
}
