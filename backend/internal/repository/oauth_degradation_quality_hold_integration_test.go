//go:build integration

package repository

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOAuthDegradationHoldSurvivesQualityRecovery(t *testing.T) {
	for _, holdBeforePending := range []bool{true, false} {
		name := "degradation_after_pending"
		if holdBeforePending {
			name = "degradation_before_pending"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			repo, cfg := degradationPolicyFixture(t)
			cfg.UpgradeEnabled, cfg.SuccessesPerStep, cfg.CooldownSeconds = true, 1, 0
			cfg.DegradationAction.TargetConcurrency = 2
			limitModels := false
			cfg.DegradationAction.LimitModels = &limitModels
			var groupID int64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name,platform,status) VALUES('native-hold-test','openai','active') RETURNING id`).Scan(&groupID))
			t.Cleanup(func() {
				_, err := integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, groupID)
				require.NoError(t, err)
			})
			cfg.UpgradeGroupIDs = []int64{groupID}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE settings SET value=$2 WHERE key=$1`, service.SettingKeyOAuthAutoConfig, string(raw))
			require.NoError(t, err)
			f := newQualityBPSFixture(t, `{"unrelated":"kept"}`, &service.QualityPolicy{
				Action: service.QualityActionRemoveModel, TriggerOnUpstream5xx: true,
				RemoveModels: []string{"gpt-6-astra"}, RecoveryConcurrency: 4, AutoRestore: true,
			})
			t.Cleanup(func() {
				_, err := integrationDB.ExecContext(ctx, `DELETE FROM account_auto_config_events WHERE account_id=$1`, f.account)
				require.NoError(t, err)
			})
			f.exec(`UPDATE accounts SET concurrency=17 WHERE id=$1`)
			f.exec(`INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,50)`, groupID)
			require.NoError(t, repo.ApplyQuality5xx(ctx, f.account))
			require.NoError(t, f.plans.FinishPelican(ctx, f.plan.ID, f.until, time.Now()))
			f.claim()
			degrade := func() {
				// Use the same persisted completed-round evidence as the actual
				// degradation evaluator, rather than inserting the hold itself.
				var evidencePlanID int64
				require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO scheduled_test_plans(account_id,model_id,cron_expression,enabled,pelican_config,updated_at)
 VALUES($1,'gpt-6-luna','*/30 * * * *',true,'{"quality":{}}',NOW()-INTERVAL '2 hours') RETURNING id`, f.account).Scan(&evidencePlanID))
				for i := range 6 {
					_, err := integrationDB.ExecContext(ctx, `INSERT INTO scheduled_test_results(plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment,error_message)
 VALUES($1,'failed',NOW()-INTERVAL '1 minute',NOW()-INTERVAL '50 seconds',NOW()-INTERVAL '50 seconds','{"parallel_count":1}',$2,'evaluated','{"verdict":"incorrect"}','state_degraded')`, evidencePlanID, fmt.Sprintf("%s-%d-%d", name, f.account, i))
					require.NoError(t, err)
				}
				require.NoError(t, repo.EvaluateOAuthDegradation(ctx, f.account, time.Now()))
				a, err := repo.GetByID(ctx, f.account)
				require.NoError(t, err)
				require.Equal(t, 2, a.Concurrency)
				require.Contains(t, a.Extra, service.OAuthDegradationActionExtraKey)
			}
			if holdBeforePending {
				degrade()
			}
			f.apply("pending")
			if !holdBeforePending {
				degrade()
			}
			require.Equal(t, "recovery_started", f.apply("passed"))
			ramp := f.extra()[service.AutoConfigConcurrencyExtraKey].(map[string]any)
			require.Equal(t, true, ramp["degradation_hold"], "quality block/arm must retain the independent hold")
			for range 3 {
				require.NoError(t, repo.RecordConcurrencyResult(ctx, service.AccountConcurrencyResult{
					AccountID: f.account, Success: true, StartedAt: time.Now().Add(time.Second),
				}, cfg))
			}
			a, err := repo.GetByID(ctx, f.account)
			require.NoError(t, err)
			require.Equal(t, 2, a.Concurrency, "completed customer requests cannot restart automatic upgrades")
			a.Concurrency = 5
			require.NoError(t, repo.Update(ctx, a))
			a, err = repo.GetByID(ctx, f.account)
			require.NoError(t, err)
			_, held := a.Extra[service.AutoConfigConcurrencyExtraKey]
			require.False(t, held, "an explicit native concurrency edit still releases the hold")
		})
	}
}
