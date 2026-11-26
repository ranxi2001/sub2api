//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestQuality429RoundAccountingAndActions(t *testing.T) {
	for _, action := range []string{service.QualityActionObserveOnly, "disable_scheduling"} {
		t.Run(action, func(t *testing.T) {
			ctx := t.Context()
			reviewRepo, policy := degradationPolicyFixture(t)
			account := degradationAccountFixture(t, 20, nil, nil, time.Minute)
			plans := &scheduledTestPlanRepository{db: integrationDB}
			plan, err := plans.Create(ctx, &service.ScheduledTestPlan{AccountID: account.ID, ModelID: "gpt-6-luna", CronExpression: "*/5 * * * *", Enabled: true, MaxResults: 100,
				PelicanConfig: &service.PelicanTestConfig{QuestionKind: "candy", Prompt: service.CandyPrompt, Quality: &service.QualityPolicy{Action: action}, ParallelCount: 1}})
			require.NoError(t, err)
			for i := 1; i <= 5; i++ {
				now := time.Now().UTC().Truncate(time.Microsecond)
				until := now.Add(15 * time.Minute)
				_, err = integrationDB.ExecContext(ctx, "UPDATE scheduled_test_plans SET next_run_at=$2 WHERE id=$1", plan.ID, now)
				require.NoError(t, err)
				plan, err = plans.GetByID(ctx, plan.ID)
				require.NoError(t, err)
				claimed, err := plans.ClaimPelican(ctx, plan, now, until, now.Add(time.Hour))
				require.NoError(t, err)
				require.True(t, claimed)
				cfg := *plan.PelicanConfig
				cfg.ModelID, cfg.QualityHTTPStatus = plan.ModelID, 429
				samples := []*service.ScheduledTestResult{{Status: "failed", ErrorMessage: "API returned 429: route_rate_limit_exceeded", StartedAt: time.Now(), FinishedAt: time.Now(), PelicanConfig: &cfg}}
				got, err := plans.SaveQuality429Round(ctx, plan, until, samples, "inconclusive")
				require.NoError(t, err)
				if action == "disable_scheduling" && i == 3 {
					require.Equal(t, "scheduling_disabled", got)
				}
				// Redelivery through a fresh repository instance cannot write another sample/action.
				duplicate, err := (&scheduledTestPlanRepository{db: integrationDB}).SaveQuality429Round(ctx, plan, until, samples, "inconclusive")
				require.NoError(t, err)
				require.Equal(t, "stale_run", duplicate)
				var raw, scored int
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE pelican_config->>'quality_429_streak'='3') FROM scheduled_test_results WHERE plan_id=$1 AND quality_judgment IS NULL AND error_message='API returned 429: route_rate_limit_exceeded'`, plan.ID).Scan(&raw, &scored))
				require.Equal(t, i, raw)
				want := 0
				if i >= 3 {
					want = i
				}
				require.Equal(t, want, scored)
				metrics, err := readAccountPerformance(ctx, integrationDB, []int64{account.ID}, time.Now())
				require.NoError(t, err)
				require.EqualValues(t, want, metrics[account.ID].QualitySamples)
				if i >= 3 {
					require.Equal(t, 100.0, *metrics[account.ID].DegradationPercent)
				}
				var schedulable bool
				require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT schedulable FROM accounts WHERE id=$1", account.ID).Scan(&schedulable))
				require.Equal(t, action == service.QualityActionObserveOnly || i < 3, schedulable)
				if i == 3 || i == 5 {
					review := service.OAuthDegradationCandyReview{PlanID: plan.ID, ModelID: plan.ModelID, LeaseUntil: until, PlanUpdatedAt: plan.UpdatedAt, StartedAt: samples[0].StartedAt}
					revision, err := reviewRepo.ReviewOAuthDegradationCandy(ctx, account.ID, review)
					require.NoError(t, err)
					if action == service.QualityActionObserveOnly {
						require.Empty(t, revision)
					} else {
						require.Equal(t, policy.Revision, revision)
					}
					var events int
					require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM account_auto_config_events WHERE account_id=$1 AND kind='degradation_applied'`, account.ID).Scan(&events))
					require.Zero(t, events, "permission for independent retests is not confirmation")
					if i == 5 && revision != "" {
						review.Revision, review.Incorrect = revision, 2
						for range 2 {
							_, err = reviewRepo.ReviewOAuthDegradationCandy(ctx, account.ID, review)
							require.NoError(t, err)
						}
						require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM account_auto_config_events WHERE account_id=$1 AND kind='degradation_applied'`, account.ID).Scan(&events))
						require.Equal(t, 1, events, "authorized action after two independent wrong answers")
					}
				}
				require.NoError(t, plans.FinishPelican(ctx, plan.ID, until, time.Now()))
			}
		})
	}
}
