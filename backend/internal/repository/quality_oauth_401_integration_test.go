//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestQualityOAuth401SharedBatchCountsBeforeErrorRedaction(t *testing.T) {
	f := oauth401Fixture(t, 1)
	ctx := context.Background()
	require.NoError(t, f.plans.FinishPelican(ctx, f.plan.ID, f.until, time.Now()))
	f.plan.PelicanConfig.QuestionKind = service.QualityRandomQuestionKind
	f.plan.PelicanConfig.Quality.ExpectedAnswer = ""
	var err error
	f.plan, err = f.plans.Update(ctx, f.plan)
	require.NoError(t, err)
	f.claim()
	now := time.Now().UTC().Truncate(time.Microsecond)
	batch := &service.QualitySharedBatch{ID: "oauth401-" + uuid.NewString(), StartedAt: now.Add(-time.Minute),
		StartDeadline: now.Add(time.Minute), ClosesAt: now.Add(11 * time.Minute),
		Questions: []service.QualityBatchQuestion{{Prompt: "Synthetic question", Expected: "42",
			Question: &service.QualityRandomQuestion{ID: "oauth401-question", Family: "paths", Version: "fixture"}}}}
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM quality_probe_batches WHERE id=$1`, batch.ID) })
	repo := f.plans.(*scheduledTestPlanRepository)
	_, err = repo.StartQualityBatch(ctx, batch, f.plan, f.until)
	require.NoError(t, err)
	cfg := *f.plan.PelicanConfig
	cfg.ModelID, cfg.RandomQuestion = f.plan.ModelID, batch.Questions[0].Question
	cfg.QualityBatch = &service.QualityBatchEvidence{ID: batch.ID, StartedAt: batch.StartedAt, StartDeadline: batch.StartDeadline, ClosesAt: batch.ClosesAt}
	result := &service.ScheduledTestResult{PlanID: f.plan.ID, Status: "failed", ErrorMessage: "API returned 401: synthetic-provider-body", DirectOAuth401: true, DirectOAuthRevoked401: true,
		StartedAt: now, FinishedAt: now, QualityRoundID: f.until.Format(time.RFC3339Nano), PelicanConfig: &cfg}
	run := &service.QualityBatchRun{Apply: true, Plan: f.plan, Until: f.until, Results: []*service.ScheduledTestResult{result}}
	require.NoError(t, repo.CollectQualityBatch(ctx, run))
	var raw []byte
	require.NoError(t, integrationDB.QueryRow(`SELECT snapshot FROM quality_probe_batch_runs WHERE batch_id=$1 AND plan_id=$2`, batch.ID, f.plan.ID).Scan(&raw))
	var stored service.QualityBatchRun
	require.NoError(t, json.Unmarshal(raw, &stored))
	require.Empty(t, stored.Results[0].ErrorMessage)
	require.NotNil(t, stored.OAuth401)
	require.True(t, stored.OAuth401.AllDirect401, "safe evidence survives provider-body redaction")
	require.True(t, stored.OAuth401.HasDirectRevoked401, "trusted revocation survives only as internal classification")
	_, err = integrationDB.Exec(`UPDATE quality_probe_batches SET start_deadline=clock_timestamp()-interval '1 second' WHERE id=$1`, batch.ID)
	require.NoError(t, err)
	// A new primary can finish without provider response bodies or another call.
	restarted := NewScheduledTestPlanRepository(integrationDB).(*scheduledTestPlanRepository)
	require.NoError(t, restarted.FinalizeQualityBatches(ctx))
	var plans, accounts int
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM scheduled_test_plans WHERE id=$1`, f.plan.ID).Scan(&plans))
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM accounts WHERE id=$1`, f.account).Scan(&accounts))
	require.Zero(t, plans)
	require.Equal(t, 1, accounts)
	require.NoError(t, restarted.FinalizeQualityBatches(ctx), "deleted batch runs do not replay")
}

func oauth401Fixture(t *testing.T, threshold int) *qualityBPSFixture {
	t.Helper()
	f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: service.QualityActionObserveOnly, OAuth401MaxRounds: threshold})
	_, err := integrationDB.Exec(`UPDATE accounts SET status='error',schedulable=false,error_message='OpenAI token revoked (401)' WHERE id=$1`, f.account)
	require.NoError(t, err)
	return f
}

func oauth401Evidence() *service.QualityOAuth401Round {
	return &service.QualityOAuth401Round{AllDirect401: true, All401OrUnavailable: true}
}

func oauth401Complete(t *testing.T, f *qualityBPSFixture, evidence *service.QualityOAuth401Round) bool {
	t.Helper()
	// Constructing a fresh repository for every round models a process restart.
	r := NewScheduledTestPlanRepository(integrationDB).(service.QualityOAuth401Repository)
	deleted, err := r.CompleteQualityOAuth401Round(context.Background(), f.plan, f.until, evidence)
	require.NoError(t, err)
	return deleted
}

func oauth401Next(t *testing.T, f *qualityBPSFixture) {
	t.Helper()
	require.NoError(t, f.plans.FinishPelican(context.Background(), f.plan.ID, f.until, time.Now()))
	oauth401ClaimDue(t, f)
}

func oauth401ClaimDue(t *testing.T, f *qualityBPSFixture) {
	t.Helper()
	ctx := context.Background()
	// Model cron becoming due without editing the rule version. TriggerQuality
	// is a manual operation that updates updated_at and would reset the streak.
	_, err := integrationDB.Exec(`UPDATE scheduled_test_plans SET next_run_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.plan.ID)
	require.NoError(t, err)
	plan, err := f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	now := time.Now().Add(time.Second).Truncate(time.Microsecond)
	f.until = now.Add(15 * time.Minute)
	claimed, err := f.plans.ClaimPelican(ctx, plan, now, f.until, now.Add(30*time.Minute))
	require.NoError(t, err)
	require.True(t, claimed)
	f.plan = plan
}

func oauth401Failures(t *testing.T, f *qualityBPSFixture) int {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRow(`SELECT failures FROM quality_oauth_401_streaks WHERE plan_id=$1`, f.plan.ID).Scan(&count))
	return count
}

func TestQualityOAuth401SixRoundsSurvivePruningAndDeleteOnlyRule(t *testing.T) {
	f := oauth401Fixture(t, 6)
	ctx := context.Background()
	var before []byte
	require.NoError(t, integrationDB.QueryRow(`SELECT to_jsonb(a) FROM accounts a WHERE id=$1`, f.account).Scan(&before))
	results := NewScheduledTestResultRepository(integrationDB)
	for round := 1; round <= 6; round++ {
		// History retains a single row, much less than the cleanup threshold.
		_, err := results.Create(ctx, &service.ScheduledTestResult{PlanID: f.plan.ID, Status: "failed", ErrorMessage: "API returned 401: synthetic",
			StartedAt: time.Now(), FinishedAt: time.Now(), QualityRoundID: f.until.Format(time.RFC3339Nano), PelicanConfig: f.plan.PelicanConfig})
		require.NoError(t, err)
		require.NoError(t, results.PruneOldResults(ctx, f.plan.ID, 1))
		deleted := oauth401Complete(t, f, oauth401Evidence())
		require.Equal(t, round == 6, deleted)
		if round < 6 {
			require.Equal(t, round, oauth401Failures(t, f))
			require.False(t, oauth401Complete(t, f, oauth401Evidence()), "a replay cannot count another round")
			require.Equal(t, round, oauth401Failures(t, f))
			oauth401Next(t, f)
		}
	}
	var after []byte
	require.NoError(t, integrationDB.QueryRow(`SELECT to_jsonb(a) FROM accounts a WHERE id=$1`, f.account).Scan(&after))
	require.JSONEq(t, string(before), string(after), "cleanup must not change the account")
	for _, table := range []string{"scheduled_test_plans", "scheduled_test_results", "quality_oauth_401_streaks"} {
		column := "plan_id"
		if table == "scheduled_test_plans" {
			column = "id"
		}
		var n int
		require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM `+table+` WHERE `+column+`=$1`, f.plan.ID).Scan(&n))
		require.Zero(t, n)
	}
}

func TestQualityOAuth401ResetAndTypeCredentialFences(t *testing.T) {
	for _, scenario := range []string{"success", "other-error", "apikey", "relogin", "account-recovered", "paused", "edited", "expired", "unfinished-round"} {
		t.Run(scenario, func(t *testing.T) {
			f := oauth401Fixture(t, 2)
			require.False(t, oauth401Complete(t, f, oauth401Evidence()))
			oauth401Next(t, f)
			evidence := oauth401Evidence()
			var err error
			switch scenario {
			case "success", "other-error":
				evidence = &service.QualityOAuth401Round{}
			case "apikey":
				_, err = integrationDB.Exec(`UPDATE accounts SET type='apikey' WHERE id=$1`, f.account)
			case "relogin":
				_, err = integrationDB.Exec(`UPDATE accounts SET credentials='{"access_token":"synthetic-replacement"}'::jsonb WHERE id=$1`, f.account)
			case "account-recovered":
				_, err = integrationDB.Exec(`UPDATE accounts SET status='active',error_message='' WHERE id=$1`, f.account)
			case "paused":
				_, err = integrationDB.Exec(`UPDATE scheduled_test_plans SET enabled=false WHERE id=$1`, f.plan.ID)
			case "edited":
				_, err = integrationDB.Exec(`UPDATE scheduled_test_plans SET updated_at=clock_timestamp() WHERE id=$1`, f.plan.ID)
			case "expired":
				_, err = integrationDB.Exec(`UPDATE scheduled_test_plans SET running_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.plan.ID)
			case "unfinished-round":
				// Claiming after a crash must not carry a streak across an unobserved round.
				oauth401Next(t, f)
				require.Zero(t, oauth401Failures(t, f))
			}
			require.NoError(t, err)
			require.False(t, oauth401Complete(t, f, evidence))
			var n int
			require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM scheduled_test_plans WHERE id=$1`, f.plan.ID).Scan(&n))
			require.Equal(t, 1, n)
			if scenario == "success" || scenario == "other-error" || scenario == "apikey" || scenario == "relogin" || scenario == "account-recovered" {
				require.Zero(t, oauth401Failures(t, f))
			}
		})
	}
}

func TestQualityOAuth401UnavailableRequiresCurrentRevoked401(t *testing.T) {
	for _, message := range []string{"OpenAI token revoked (401)", "token revoked (403)", "HTTP 401 transient", "model unavailable"} {
		t.Run(message, func(t *testing.T) {
			f := oauth401Fixture(t, 1)
			_, err := integrationDB.Exec(`UPDATE accounts SET error_message=$2 WHERE id=$1`, f.account, message)
			require.NoError(t, err)
			deleted := oauth401Complete(t, f, &service.QualityOAuth401Round{All401OrUnavailable: true})
			require.False(t, deleted, "account error text alone never establishes trusted revocation")
			require.Zero(t, oauth401Failures(t, f))
		})
	}
}

func TestQualityOAuth401Untrusted401ThenUnavailableNeverDeletes(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		body   string
		direct bool
	}{
		{"plugin", `{"error":{"code":"token_invalidated"}}`, false},
		{"borrowed-ticket", `{"error":{"code":"token_invalidated"}}`, false},
		{"negated", `{"error":{"message":"not invalid_grant; token not revoked"}}`, true},
		{"quoted", `{"error":{"message":"{\"error\":{\"code\":\"invalid_grant\"}}"}}`, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := oauth401Fixture(t, 2)
			_, err := integrationDB.Exec(`UPDATE accounts SET error_message=$2 WHERE id=$1`, f.account, "Authentication failed (401): "+scenario.body)
			require.NoError(t, err)
			// Transport provenance is tested on real plugin/ticket request paths
			// in service. Their generic SetError text is deliberately retained.
			result := &service.ScheduledTestResult{Status: "failed", ErrorMessage: "API returned 401: " + scenario.body, DirectOAuth401: scenario.direct}
			require.False(t, oauth401Complete(t, f, service.QualityOAuth401Evidence(f.plan, []*service.ScheduledTestResult{result})))
			for round := 0; round < 4; round++ {
				oauth401Next(t, f)
				result = &service.ScheduledTestResult{Status: "skipped", ErrorMessage: "model_unavailable"}
				require.False(t, oauth401Complete(t, f, service.QualityOAuth401Evidence(f.plan, []*service.ScheduledTestResult{result})))
				require.Zero(t, oauth401Failures(t, f))
			}
			_, err = f.plans.GetByID(context.Background(), f.plan.ID)
			require.NoError(t, err, "untrusted 401 followed by more than the threshold of skipped rounds keeps the rule")
		})
	}
}

func TestQualityOAuth401UnavailableUsesOnlyCurrentCredentialTrust(t *testing.T) {
	for _, scenario := range []string{"retained", "mixed", "not-revoked", "credentials-changed", "unfinished-round", "inconclusive"} {
		t.Run(scenario, func(t *testing.T) {
			f := oauth401Fixture(t, 3)
			direct := oauth401Evidence()
			direct.HasDirectRevoked401 = scenario != "not-revoked"
			if scenario == "mixed" {
				direct.AllDirect401 = false
			}
			require.False(t, oauth401Complete(t, f, direct))
			require.Equal(t, 1, oauth401Failures(t, f))
			if scenario == "credentials-changed" {
				_, err := integrationDB.Exec(`UPDATE accounts SET credentials='{"access_token":"synthetic-new"}'::jsonb WHERE id=$1`, f.account)
				require.NoError(t, err)
			}
			oauth401Next(t, f)
			if scenario == "unfinished-round" {
				oauth401Next(t, f)
			} else if scenario == "inconclusive" {
				require.False(t, oauth401Complete(t, f, &service.QualityOAuth401Round{}))
				oauth401Next(t, f)
			}
			skipped := &service.QualityOAuth401Round{All401OrUnavailable: true}
			require.False(t, oauth401Complete(t, f, skipped))
			if scenario == "retained" || scenario == "mixed" {
				require.Equal(t, 2, oauth401Failures(t, f))
				oauth401Next(t, f)
				require.True(t, oauth401Complete(t, f, skipped), "persisted own-credential evidence survives a repository restart")
			} else {
				require.Zero(t, oauth401Failures(t, f), "old revocation trust cannot cross a changed credential or interrupted streak")
			}
		})
	}
}

func TestQualityOAuth401SavedRuleVersionRestartsStreak(t *testing.T) {
	for _, scenario := range []string{"pause-resume", "disable-reenable-cleanup"} {
		t.Run(scenario, func(t *testing.T) {
			f := oauth401Fixture(t, 6)
			ctx := context.Background()
			direct := oauth401Evidence()
			direct.HasDirectRevoked401 = true
			for round := 1; round <= 5; round++ {
				require.False(t, oauth401Complete(t, f, direct))
				require.Equal(t, round, oauth401Failures(t, f))
				if round < 5 {
					oauth401Next(t, f)
				}
			}
			require.NoError(t, f.plans.FinishPelican(ctx, f.plan.ID, f.until, time.Now()))
			oldVersion := f.plan.UpdatedAt
			var oldConfigHash string
			require.NoError(t, integrationDB.QueryRow(`SELECT config_revision FROM quality_oauth_401_streaks WHERE plan_id=$1`, f.plan.ID).Scan(&oldConfigHash))
			// Both writes use the actual repository save, then a fresh DB read.
			// The final config hash equals the original, but the version differs.
			for _, disabled := range []bool{true, false} {
				plan, err := f.plans.GetByID(ctx, f.plan.ID)
				require.NoError(t, err)
				if scenario == "pause-resume" {
					plan.Enabled = !disabled
				} else if disabled {
					plan.PelicanConfig.Quality.OAuth401MaxRounds = 0
				} else {
					plan.PelicanConfig.Quality.OAuth401MaxRounds = 6
				}
				_, err = f.plans.Update(ctx, plan)
				require.NoError(t, err)
				f.plan, err = f.plans.GetByID(ctx, f.plan.ID)
				require.NoError(t, err)
				require.True(t, f.plan.UpdatedAt.After(oldVersion))
				oldVersion = f.plan.UpdatedAt
			}
			var currentConfigHash string
			require.NoError(t, integrationDB.QueryRow(`SELECT md5(jsonb_build_array(pelican_config,model_id,cron_expression)::text) FROM scheduled_test_plans WHERE id=$1`, f.plan.ID).Scan(&currentConfigHash))
			require.Equal(t, oldConfigHash, currentConfigHash, "the final configuration is identical; only the saved version fences the old streak")
			oauth401ClaimDue(t, f)
			require.Zero(t, oauth401Failures(t, f))
			var trusted bool
			require.NoError(t, integrationDB.QueryRow(`SELECT own_revoked_401 FROM quality_oauth_401_streaks WHERE plan_id=$1`, f.plan.ID).Scan(&trusted))
			require.False(t, trusted)
			require.False(t, oauth401Complete(t, f, oauth401Evidence()))
			require.Equal(t, 1, oauth401Failures(t, f), "the next completed round is 1/6, not 6/6")
		})
	}
}

func TestQualityOAuth401PreservesOtherRuleOwnership(t *testing.T) {
	f := oauth401Fixture(t, 1)
	ctx := context.Background()
	other, err := f.plans.Create(ctx, &service.ScheduledTestPlan{AccountID: f.account, ModelID: "other-model", CronExpression: "*/30 * * * *", MaxResults: 100,
		PelicanConfig: &service.PelicanTestConfig{Quality: &service.QualityPolicy{Action: service.QualityActionEnableBPS}}})
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO account_quality_states(plan_id,state) VALUES($1,'{"action":"isolate_models"}')`, other.ID)
	require.NoError(t, err)
	marker, err := json.Marshal(map[string]any{"other-model": map[string]any{"plan_id": other.ID, "reason": fmt.Sprintf("quality_rule:%d", other.ID)}})
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE accounts SET extra=jsonb_build_object('quality_model_isolation',$2::jsonb) WHERE id=$1`, f.account, string(marker))
	require.NoError(t, err)
	var beforeAccount, beforePlan, beforeState []byte
	require.NoError(t, integrationDB.QueryRow(`SELECT to_jsonb(a) FROM accounts a WHERE id=$1`, f.account).Scan(&beforeAccount))
	require.NoError(t, integrationDB.QueryRow(`SELECT to_jsonb(p) FROM scheduled_test_plans p WHERE id=$1`, other.ID).Scan(&beforePlan))
	require.NoError(t, integrationDB.QueryRow(`SELECT state FROM account_quality_states WHERE plan_id=$1`, other.ID).Scan(&beforeState))
	require.True(t, oauth401Complete(t, f, oauth401Evidence()))
	var afterAccount, afterPlan, afterState []byte
	require.NoError(t, integrationDB.QueryRow(`SELECT to_jsonb(a) FROM accounts a WHERE id=$1`, f.account).Scan(&afterAccount))
	require.NoError(t, integrationDB.QueryRow(`SELECT to_jsonb(p) FROM scheduled_test_plans p WHERE id=$1`, other.ID).Scan(&afterPlan))
	require.NoError(t, integrationDB.QueryRow(`SELECT state FROM account_quality_states WHERE plan_id=$1`, other.ID).Scan(&afterState))
	require.JSONEq(t, string(beforeAccount), string(afterAccount))
	require.JSONEq(t, string(beforePlan), string(afterPlan))
	require.JSONEq(t, string(beforeState), string(afterState))
}

func TestQualityOAuth401KeepsOwnershipAndTemplateDeletionMarker(t *testing.T) {
	f := oauth401Fixture(t, 1)
	// A live ownership marker is checked even without a durable snapshot.
	marker, err := json.Marshal(map[string]any{"sol": map[string]any{"plan_id": f.plan.ID, "reason": fmt.Sprintf("quality_rule:%d", f.plan.ID)}})
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE accounts SET extra=jsonb_build_object('quality_model_isolation',$2::jsonb) WHERE id=$1`, f.account, string(marker))
	require.NoError(t, err)
	// Use the same durable model-isolation ownership guard as native DELETE.
	_, err = integrationDB.Exec(`INSERT INTO account_quality_states(plan_id,state) VALUES($1,'{"action":"isolate_models"}')`, f.plan.ID)
	require.NoError(t, err)
	require.False(t, oauth401Complete(t, f, oauth401Evidence()))
	require.Equal(t, 1, oauth401Failures(t, f))
	var state string
	require.NoError(t, integrationDB.QueryRow(`SELECT state->>'action' FROM account_quality_states WHERE plan_id=$1`, f.plan.ID).Scan(&state))
	require.Equal(t, "isolate_models", state)
	// Removing ownership through an operator workflow is separate from cleanup.
	_, err = integrationDB.Exec(`DELETE FROM account_quality_states WHERE plan_id=$1`, f.plan.ID)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE accounts SET extra='{}' WHERE id=$1`, f.account)
	require.NoError(t, err)
	var template int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO quality_rule_templates(model_id,cron_expression,pelican_config) VALUES('sol','*/5 * * * *','{"quality":{"action":"observe_only"}}') RETURNING id`).Scan(&template))
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM quality_rule_templates WHERE id=$1`, template) })
	_, err = integrationDB.Exec(`INSERT INTO quality_rule_template_accounts(template_id,account_id,plan_id) VALUES($1,$2,$3)`, template, f.account, f.plan.ID)
	require.NoError(t, err)
	oauth401Next(t, f)
	require.True(t, oauth401Complete(t, f, oauth401Evidence()))
	var linked, nullPlan bool
	require.NoError(t, integrationDB.QueryRow(`SELECT true,plan_id IS NULL FROM quality_rule_template_accounts WHERE template_id=$1 AND account_id=$2`, template, f.account).Scan(&linked, &nullPlan))
	require.True(t, linked)
	require.True(t, nullPlan, "template association survives to prevent recreation")
}
