//go:build integration

package repository

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestOAuthDegradationNativePauseSurvivesAdmissionPass(t *testing.T) {
	for _, manualResume := range []bool{false, true} {
		name := "preserve pause"
		if manualResume {
			name = "honor native manual resume"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			repo, _ := degradationPolicyFixture(t)
			f := newAdmissionFixture(t)
			t.Cleanup(func() {
				_, _ = integrationDB.Exec(`DELETE FROM account_auto_config_events WHERE account_id=$1`, f.account.ID)
			})
			// The selected Luna scope has no intersection with this existing map.
			_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET schedulable=true,
 credentials='{"model_mapping":{"gpt-6-astra":"gpt-6-astra"}}'::jsonb WHERE id=$1`, f.account.ID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `UPDATE scheduled_test_plans SET updated_at=NOW()-INTERVAL '2 hours' WHERE id=$1`, f.plan.ID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx, `INSERT INTO scheduled_test_results
 (plan_id,status,started_at,finished_at,created_at,pelican_config,quality_round_id,quality_action,quality_judgment,error_message)
 VALUES($1,'failed',NOW()-INTERVAL '1 minute',NOW()-INTERVAL '50 seconds',NOW()-INTERVAL '50 seconds',
 '{"parallel_count":1}','native-pause-fixture','evaluated','{"verdict":"incorrect"}','state_degraded')`, f.plan.ID)
			require.NoError(t, err)
			require.NoError(t, repo.EvaluateOAuthDegradation(ctx, f.account.ID, time.Now()))
			got, err := repo.GetByID(ctx, f.account.ID)
			require.NoError(t, err)
			require.False(t, got.Schedulable)
			raw, err := json.Marshal(got.Extra[service.OAuthDegradationActionExtraKey])
			require.NoError(t, err)
			var degradation service.OAuthDegradationState
			require.NoError(t, json.Unmarshal(raw, &degradation))
			require.True(t, degradation.NativeControls)
			require.True(t, degradation.NativeSchedulingPaused)
			require.Empty(t, degradation.Models)
			for range 5 {
				require.Equal(t, "observed", f.round("passed"))
			}
			if manualResume {
				got, err = repo.GetByID(ctx, f.account.ID)
				require.NoError(t, err)
				got.Schedulable = true
				require.NoError(t, repo.Update(ctx, got), "the existing account editor can resume scheduling")
			}
			require.Equal(t, "admission_passed", f.round("passed"))
			got, err = repo.GetByID(ctx, f.account.ID)
			require.NoError(t, err)
			require.Equal(t, manualResume, got.Schedulable, "passing admission cannot undo the visible native pause or a later manual resume")
			require.Equal(t, "passed", f.state().Phase)
			require.Equal(t, map[string]any{"gpt-6-astra": "gpt-6-astra"}, got.Credentials["model_mapping"], "admission must not rewrite the original mapping")
		})
	}
}

func TestOAuthDegradationMigratedPauseSurvivesAdmissionPass(t *testing.T) {
	migration, err := dbmigrations.FS.ReadFile("338_oauth_degradation_native_controls.sql")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, action string
	}{
		{"empty intersection", `{"models":["gpt-6-luna"],"limit_concurrency":false}`},
		{"empty scope", `{"models":[],"limit_concurrency":false}`},
		{"object scope", `{"models":{"gpt-6-luna":true},"limit_concurrency":false}`},
		{"mixed scope", `{"models":["gpt-6-luna",5],"limit_concurrency":false}`},
		{"invalid model flag", `{"models":["gpt-6-luna"],"limit_models":"true","limit_concurrency":false}`},
		{"invalid non-model flag", `{"models":["gpt-6-luna"],"limit_models":false,"limit_concurrency":10}`},
		{"invalid action", `"invalid-legacy-state"`},
	} {
		for _, manualResume := range []bool{false, true} {
			name := tc.name + "/preserve pause"
			if manualResume {
				name = tc.name + "/honor native manual resume"
			}
			t.Run(name, func(t *testing.T) {
				ctx := t.Context()
				f := newAdmissionFixture(t)
				// Execute the production migration and all six admissions in one
				// isolated transaction; its legacy-row conversion cannot escape.
				tx := testTx(t)
				_, err := tx.ExecContext(ctx, `UPDATE accounts SET schedulable=true,
 credentials='{"model_mapping":{"gpt-6-astra":"gpt-6-astra"}}'::jsonb,
 extra=jsonb_set(extra,'{oauth_degradation_action}',$2::jsonb) WHERE id=$1`, f.account.ID, tc.action)
				require.NoError(t, err)
				_, err = tx.ExecContext(ctx, string(migration))
				require.NoError(t, err)
				var nativeControls, nativePause, schedulable bool
				var auditScope string
				require.NoError(t, tx.QueryRowContext(ctx, `SELECT
 (extra->'oauth_degradation_action'->>'native_controls')::boolean,
 (extra->'oauth_degradation_action'->>'native_scheduling_paused')::boolean,
 COALESCE((extra->'oauth_degradation_action'->'models')::text,'null'),schedulable
 FROM accounts WHERE id=$1`, f.account.ID).Scan(&nativeControls, &nativePause, &auditScope, &schedulable))
				require.True(t, nativeControls)
				require.True(t, nativePause)
				require.False(t, schedulable)
				var original map[string]json.RawMessage
				if json.Unmarshal([]byte(tc.action), &original) == nil {
					if models, ok := original["models"]; ok {
						require.JSONEq(t, string(models), auditScope, "migration must retain the historical model scope for audit")
					}
				}
				for round := 0; round < 6; round++ {
					if manualResume && round == 5 {
						_, err = tx.ExecContext(ctx, `UPDATE accounts SET schedulable=true WHERE id=$1`, f.account.ID)
						require.NoError(t, err)
					}
					until := time.Now().UTC().Truncate(time.Microsecond).Add(15*time.Minute + time.Duration(round)*time.Second)
					_, err = tx.ExecContext(ctx, `UPDATE scheduled_test_plans SET running_until=$2 WHERE id=$1`, f.plan.ID, until)
					require.NoError(t, err)
					commit := false
					action, err := applyQualityOutcomeTx(ctx, tx, f.plan, until, "passed", &commit)
					require.NoError(t, err)
					require.True(t, commit)
					if round < 5 {
						require.Equal(t, "observed", action)
					} else {
						require.Equal(t, "admission_passed", action)
					}
				}
				var phase string
				require.NoError(t, tx.QueryRowContext(ctx, `SELECT schedulable,extra->'openai_two_fa_admission'->>'phase' FROM accounts WHERE id=$1`, f.account.ID).Scan(&schedulable, &phase))
				require.Equal(t, manualResume, schedulable)
				require.Equal(t, "passed", phase)
			})
		}
	}
}

func TestOAuthDegradationAdmissionIgnoresUnrelatedAuditMarkers(t *testing.T) {
	for _, tc := range []struct{ name, action string }{
		{"legacy marker", `{"models":[],"native_scheduling_paused":true}`},
		{"native non-pausing action", `{"native_controls":true,"models":[]}`},
		{"native concurrency-only action", `{"native_controls":true,"limit_models":false,"limit_concurrency":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdmissionFixture(t)
			_, err := integrationDB.ExecContext(t.Context(), `UPDATE accounts SET extra=jsonb_set(extra,'{oauth_degradation_action}',$2::jsonb) WHERE id=$1`, f.account.ID, tc.action)
			require.NoError(t, err)
			for range 5 {
				require.Equal(t, "observed", f.round("passed"))
			}
			require.Equal(t, "admission_passed", f.round("passed"))
			var schedulable bool
			require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT schedulable FROM accounts WHERE id=$1`, f.account.ID).Scan(&schedulable))
			require.True(t, schedulable, "an unrelated audit marker must not prevent ordinary admission")
		})
	}
}
