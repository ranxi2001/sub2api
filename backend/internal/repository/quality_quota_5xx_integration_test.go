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

func TestIssue817QuotaSkipPreservesPending5xxExecution(t *testing.T) {
	for _, previousRun := range []string{"never_run", "previous_completed_run"} {
		t.Run(previousRun, func(t *testing.T) {
			ctx := context.Background()
			f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: "disable_scheduling", TriggerOnUpstream5xx: true})
			require.NoError(t, f.plans.FinishPelican(ctx, f.plan.ID, f.until, time.Now()))
			now := time.Now().UTC().Truncate(time.Microsecond)
			var lastRun *time.Time
			if previousRun == "previous_completed_run" {
				prior := now.Add(-time.Hour)
				lastRun = &prior
			}
			extra, err := json.Marshal(map[string]any{"codex_5h_used_percent": 100, "codex_usage_updated_at": now.Format(time.RFC3339Nano), "codex_5h_reset_at": now.Add(time.Hour).Format(time.RFC3339Nano)})
			require.NoError(t, err)
			_, err = integrationDB.Exec(`UPDATE accounts SET extra=$2::jsonb WHERE id=$1`, f.account, string(extra))
			require.NoError(t, err)
			_, err = integrationDB.Exec(`UPDATE scheduled_test_plans SET last_run_at=$2,next_run_at=$3 WHERE id=$1`, f.plan.ID, lastRun, now.Add(-time.Minute))
			require.NoError(t, err)

			templates := NewQualityRuleTemplateRepository(integrationEntClient, integrationDB)
			template, err := templates.Create(ctx, &service.QualityRuleTemplate{AccountFilter: service.QualityRuleAccountFilter{Statuses: []string{"active"}},
				ModelID: f.plan.ModelID, CronExpression: f.plan.CronExpression, Enabled: true, MaxResults: f.plan.MaxResults, PelicanConfig: f.plan.PelicanConfig})
			require.NoError(t, err)
			t.Cleanup(func() { _, _ = templates.Delete(context.Background(), template.ID) })
			_, err = integrationDB.Exec(`INSERT INTO quality_rule_template_accounts(template_id,account_id,plan_id) VALUES($1,$2,$3)`, template.ID, f.account, f.plan.ID)
			require.NoError(t, err)

			redis := miniredis.RunT(t)
			client := queuetest.NewClient(redis.Addr())
			t.Cleanup(func() { _ = client.Close() })
			queue := qualityqueue.NewRedis(client)
			results := NewScheduledTestResultRepository(integrationDB)
			scheduled := service.ProvideScheduledTestService(f.plans, results, templates, nil)
			var calls atomic.Int32
			runner := service.NewQuotaTriggerRunnerForTesting(scheduled, NewAccountRepository(integrationEntClient, integrationDB, nil), queue, []string{f.plan.ModelID},
				func(_ context.Context, accountID int64, _ string, cfg *service.PelicanTestConfig) (*service.ScheduledTestResult, error) {
					calls.Add(1)
					if accountID != f.account {
						return nil, service.ErrAccountNotFound
					}
					return &service.ScheduledTestResult{Status: "success", StartedAt: time.Now(), FinishedAt: time.Now(), PelicanConfig: cfg,
						QualityJudgment: &service.QualityJudgment{Verdict: "correct"}}, nil
				})
			t.Cleanup(runner.Stop)

			// The failure is pending before a timer claims and skips this plan.
			signalAt := now.Add(-time.Second).Truncate(time.Millisecond)
			require.NoError(t, queue.Enqueue(ctx, f.account, signalAt))
			plan, err := f.plans.GetByID(ctx, f.plan.ID)
			require.NoError(t, err)
			require.False(t, plan.ManualQualityRun)
			require.False(t, runner.RunQuotaScheduledPlanForTesting(ctx, plan))
			require.Zero(t, calls.Load(), "quota-skipped cron must not execute a probe")
			skipped, err := f.plans.GetByID(ctx, f.plan.ID)
			require.NoError(t, err)
			require.Nil(t, skipped.RunningUntil)
			require.True(t, skipped.NextRunAt.After(now), "skip preserves the claimed next cron")
			pending, err := queue.Pending(ctx, now.Add(-20*time.Minute))
			require.NoError(t, err)
			require.Len(t, pending, 1)
			// The provider window recovers before the queued failure is handled.
			// A skipped timer still cannot count as evidence covering that failure.
			_, err = integrationDB.Exec(`UPDATE accounts SET extra=jsonb_set(extra,'{codex_5h_used_percent}','20') WHERE id=$1`, f.account)
			require.NoError(t, err)

			// Run the existing worker, including its real coverage check, claim
			// fence, result persistence and Acknowledge. Do not start a cron.
			runner.StartQuotaTriggerWorkerForTesting()
			require.Eventually(t, func() bool {
				pending, err := queue.Pending(ctx, now.Add(-20*time.Minute))
				return err == nil && len(pending) == 0
			}, 5*time.Second, 20*time.Millisecond, "trigger worker must finish and acknowledge its signal")
			require.Equal(t, int32(1), calls.Load(), "pending 5xx signal must execute after quota-skipped cron before acknowledgement")
			require.Equal(t, lastRun, skipped.LastRunAt, "zero-probe skip must preserve the previous executed round")
			stored, err := results.ListByPlanID(ctx, f.plan.ID, 10)
			require.NoError(t, err)
			require.Len(t, stored, 1)
			require.Equal(t, "upstream_5xx", stored[0].PelicanConfig.TriggerSource)
			completed, err := f.plans.GetByID(ctx, f.plan.ID)
			require.NoError(t, err)
			require.NotNil(t, completed.LastRunAt)
			require.False(t, completed.LastRunAt.Before(signalAt))
			require.True(t, skipped.NextRunAt.Equal(*completed.NextRunAt), "trigger retains the future cron schedule")
		})
	}
}
