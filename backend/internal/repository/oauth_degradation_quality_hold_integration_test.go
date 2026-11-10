//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOAuthDegradationHoldSurvivesQualityRecovery(t *testing.T) {
	for _, tc := range []struct {
		name            string
		target          int
		expectedOutcome string
	}{
		{"unchanged_native_concurrency", 4, "recovery_started"},
		{"lowered_native_concurrency", 2, "restore_conflict"},
	} {
		for _, holdBeforePending := range []bool{true, false} {
			name := tc.name + "/degradation_after_pending"
			if holdBeforePending {
				name = tc.name + "/degradation_before_pending"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				repo, cfg := degradationPolicyFixture(t)
				cfg.UpgradeEnabled, cfg.SuccessesPerStep, cfg.CooldownSeconds = true, 1, 0
				cfg.DegradationAction.TargetConcurrency = tc.target
				limitModels := false
				cfg.DegradationAction.LimitModels = &limitModels
				var groupID int64
				require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name,platform,status) VALUES($1,'openai','active') RETURNING id`, fmt.Sprintf("native-hold-%s-%d", name, time.Now().UnixNano())).Scan(&groupID))
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
					for i := range 6 {
						_, err := integrationDB.ExecContext(ctx, `INSERT INTO scheduled_test_results(plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment,error_message)
 VALUES($1,'failed',clock_timestamp(),clock_timestamp(),clock_timestamp(),'{"parallel_count":1}',$2,'evaluated','{"verdict":"incorrect"}','state_degraded')`, f.plan.ID, fmt.Sprintf("%s-%d-%d", name, f.account, i))
						require.NoError(t, err)
					}
					require.NoError(t, repo.EvaluateOAuthDegradation(ctx, f.account, time.Now()))
					a, err := repo.GetByID(ctx, f.account)
					require.NoError(t, err)
					require.Equal(t, tc.target, a.Concurrency)
					require.Contains(t, a.Extra, service.OAuthDegradationActionExtraKey)
				}
				if holdBeforePending {
					degrade()
				}
				f.apply("pending")
				if !holdBeforePending {
					degrade()
				}
				// 5xx protection already captured concurrency 4. A same-value
				// degradation hold must survive both block and arm; lowering to 2
				// retains the separate policy's ownership conflict in either order.
				require.Equal(t, tc.expectedOutcome, f.apply("passed"))
				ramp := f.extra()[service.AutoConfigConcurrencyExtraKey].(map[string]any)
				require.Equal(t, true, ramp["degradation_hold"], "quality block/arm must retain the independent hold")
				for range 3 {
					require.NoError(t, repo.RecordConcurrencyResult(ctx, service.AccountConcurrencyResult{
						AccountID: f.account, Success: true, StartedAt: time.Now().Add(time.Second),
					}, cfg))
				}
				a, err := repo.GetByID(ctx, f.account)
				require.NoError(t, err)
				require.Equal(t, tc.target, a.Concurrency, "completed customer requests cannot restart automatic upgrades")
				a.Concurrency = 5
				require.NoError(t, repo.Update(ctx, a))
				a, err = repo.GetByID(ctx, f.account)
				require.NoError(t, err)
				raw, err = json.Marshal(a.Extra[service.AutoConfigConcurrencyExtraKey])
				require.NoError(t, err)
				var native service.AutoConfigConcurrencyState
				require.NoError(t, json.Unmarshal(raw, &native))
				require.False(t, native.DegradationHold, "native edits release the degradation hold while retaining ordinary ramp bookkeeping")
				require.Equal(t, 5, a.Concurrency)
			})
		}
	}
}
