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

type admissionFixture struct {
	t       *testing.T
	account *service.Account
	plan    *service.ScheduledTestPlan
	plans   service.ScheduledTestPlanRepository
	policy  service.OpenAITwoFAAdmissionConfig
	groups  []int64
}

func newAdmissionFixture(t *testing.T) *admissionFixture {
	t.Helper()
	ctx := t.Context()
	f := &admissionFixture{t: t, plans: NewScheduledTestPlanRepository(integrationDB), policy: service.DefaultOpenAITwoFAAdmissionConfig()}
	t.Cleanup(func() {
		ctx := context.Background()
		if f.account != nil {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, f.account.ID)
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, f.account.ID)
		}
		for _, id := range f.groups {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id=$1`, id)
		}
	})
	for i := range 3 {
		var id int64
		name := fmt.Sprintf("2fa-admission-%d-%d", time.Now().UnixNano(), i)
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups(name,platform,status) VALUES($1,'openai','active') RETURNING id`, name).Scan(&id))
		f.groups = append(f.groups, id)
	}
	f.policy.Enabled, f.policy.IsolationGroupID = true, f.groups[2]
	now := time.Now().UTC()
	plan := &service.ScheduledTestPlan{ModelID: f.policy.ModelID, CronExpression: "* * * * *", Enabled: true, MaxResults: 100, NextRunAt: &now,
		PelicanConfig: &service.PelicanTestConfig{QuestionKind: "candy", Prompt: service.CandyPrompt, ModelID: f.policy.ModelID, ReasoningEffort: "low", ParallelCount: 1,
			Quality: &service.QualityPolicy{Action: service.QualityActionOpenAITwoFAAdmission, ExpectedAnswer: "21", Admission: &f.policy}}}
	f.account = &service.Account{Name: "2fa-admission-test", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Concurrency: 1, Schedulable: false,
		Extra: map[string]any{"keep": "value", service.OpenAITwoFAAdmissionExtraKey: service.OpenAITwoFAAdmissionState{Policy: f.policy, Phase: "pending"}}, InitialAdmissionPlan: plan}

	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	require.NoError(t, repo.CreateWithAccountGroups(ctx, f.account, []service.AccountGroup{{GroupID: f.groups[0], Priority: 7}, {GroupID: f.groups[1], Priority: 9}}))
	plans, err := f.plans.ListByAccountID(ctx, f.account.ID)
	require.NoError(t, err)
	require.Len(t, plans, 1)
	f.plan = plans[0]
	require.Equal(t, f.plan.ID, f.state().PlanID)
	return f
}

func (f *admissionFixture) state() service.OpenAITwoFAAdmissionState {
	f.t.Helper()
	var raw []byte
	require.NoError(f.t, integrationDB.QueryRowContext(f.t.Context(), `SELECT extra->'openai_two_fa_admission' FROM accounts WHERE id=$1`, f.account.ID).Scan(&raw))
	var s service.OpenAITwoFAAdmissionState
	require.NoError(f.t, json.Unmarshal(raw, &s))
	return s
}

func (f *admissionFixture) round(outcome string) string {
	f.t.Helper()
	ctx := f.t.Context()
	require.NoError(f.t, f.plans.TriggerQuality(ctx, f.plan.ID))
	plan, err := f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(f.t, err)
	now := time.Now().Add(time.Second).Truncate(time.Microsecond)
	until := now.Add(15 * time.Minute)
	claimed, err := f.plans.ClaimPelican(ctx, plan, now, until, now.Add(time.Minute))
	require.NoError(f.t, err)
	require.True(f.t, claimed)
	action, err := f.plans.ApplyQualityOutcome(ctx, plan, until, outcome)
	require.NoError(f.t, err)
	before := f.state()
	// Lost-response retries cannot count the same round a second time.
	_, err = f.plans.ApplyQualityOutcome(ctx, plan, until, outcome)
	require.NoError(f.t, err)
	require.Equal(f.t, before, f.state())
	finished := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(f.t, f.plans.FinishPelican(ctx, plan.ID, until, finished))
	f.plan, err = f.plans.GetByID(ctx, plan.ID)
	require.NoError(f.t, err)
	require.Equal(f.t, finished.Add(time.Duration(f.policy.WaitSeconds)*time.Second), f.plan.NextRunAt.UTC())
	require.True(f.t, f.plan.Enabled, "admission and isolation never stop background testing")
	return action
}

func TestOpenAITwoFAAdmissionAtomicPassAndIsolation(t *testing.T) {
	f := newAdmissionFixture(t)
	ctx := t.Context()
	var concurrency int
	var schedulable bool
	for range 5 {
		require.Equal(t, "observed", f.round("passed"))
	}
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT concurrency,schedulable FROM accounts WHERE id=$1`, f.account.ID).Scan(&concurrency, &schedulable))
	require.Equal(t, 1, concurrency)
	require.False(t, schedulable)
	require.Equal(t, "admission_passed", f.round("passed"))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT concurrency,schedulable FROM accounts WHERE id=$1`, f.account.ID).Scan(&concurrency, &schedulable))
	require.Equal(t, 2, concurrency)
	require.True(t, schedulable)
	// Later successful probes cannot reset an ordinary/manual concurrency change.
	_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET concurrency=7 WHERE id=$1`, f.account.ID)
	require.NoError(t, err)
	require.Equal(t, "observed", f.round("passed"))
	for range 5 {
		require.Equal(t, "observed", f.round("answer_29"))
	}
	require.Equal(t, "admission_isolated", f.round("answer_29"))
	var groupIDs []int64
	rows, err := integrationDB.QueryContext(ctx, `SELECT group_id FROM account_groups WHERE account_id=$1`, f.account.ID)
	require.NoError(t, err)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		groupIDs = append(groupIDs, id)
	}
	require.NoError(t, rows.Close())
	require.Equal(t, []int64{f.groups[2]}, groupIDs)
	for range 7 {
		require.Equal(t, "observed", f.round("passed"))
	}
	require.Equal(t, "isolated", f.state().Phase)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT concurrency,schedulable FROM accounts WHERE id=$1`, f.account.ID).Scan(&concurrency, &schedulable))
	require.Equal(t, 7, concurrency)
	require.True(t, schedulable)
	var events int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM scheduler_outbox WHERE account_id=$1 AND event_type='account_groups_changed' AND payload->'group_ids' @> $2::jsonb`, f.account.ID, "["+qualityIsolationID(f.groups[0])+","+qualityIsolationID(f.groups[1])+","+qualityIsolationID(f.groups[2])+"]").Scan(&events))
	require.Positive(t, events)
}

func TestOpenAITwoFAAdmissionUnavailableGroupCannotUndoIsolation(t *testing.T) {
	f := newAdmissionFixture(t)
	_, err := integrationDB.ExecContext(t.Context(), `UPDATE groups SET status='disabled' WHERE id=$1`, f.groups[2])
	require.NoError(t, err)
	for range 5 {
		f.round("answer_29")
	}
	require.Equal(t, "admission_isolation_pending", f.round("answer_29"))
	for range 6 {
		require.Equal(t, "admission_isolation_pending", f.round("passed"))
	}
	require.Equal(t, "isolation_pending", f.state().Phase)
	_, err = integrationDB.ExecContext(t.Context(), `UPDATE groups SET status='active' WHERE id=$1`, f.groups[2])
	require.NoError(t, err)
	require.Equal(t, "admission_isolated", f.round("passed"))
	require.Equal(t, "isolated", f.state().Phase)
	var schedulable bool
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), `SELECT schedulable FROM accounts WHERE id=$1`, f.account.ID).Scan(&schedulable))
	require.False(t, schedulable, "an account isolated before admission must never open customer scheduling")
}

func TestOpenAITwoFAAdmissionCustomerUpgradeStartsAfterPassAndStopsAfterIsolation(t *testing.T) {
	f := newAdmissionFixture(t)
	ctx := t.Context()
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	cfg := service.DefaultOAuthAutoConfig()
	cfg.UpgradeEnabled, cfg.UpgradeGroupIDs, cfg.Revision, cfg.SuccessesPerStep = true, append([]int64(nil), f.groups...), "2fa-upgrade-test", 2
	cfg.UpdatedAt = time.Now().UTC().Add(-time.Minute)
	var previous string
	previousErr := integrationDB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, service.SettingKeyOAuthAutoConfig).Scan(&previous)
	t.Cleanup(func() {
		ctx := context.Background()
		if previousErr == nil {
			_, _ = integrationDB.ExecContext(ctx, `UPDATE settings SET value=$2 WHERE key=$1`, service.SettingKeyOAuthAutoConfig, previous)
		} else {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM settings WHERE key=$1`, service.SettingKeyOAuthAutoConfig)
		}
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM account_auto_config_events WHERE account_id=$1`, f.account.ID)
	})
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, service.SettingKeyOAuthAutoConfig, string(raw))
	require.NoError(t, err)
	old := time.Now().UTC().Add(-time.Second)
	for range 6 {
		f.round("passed")
	}
	for range 3 {
		require.NoError(t, repo.RecordConcurrencyResult(ctx, service.AccountConcurrencyResult{AccountID: f.account.ID, StartedAt: old, Success: true}, cfg))
	}
	var concurrency int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT concurrency FROM accounts WHERE id=$1`, f.account.ID).Scan(&concurrency))
	require.Equal(t, 2, concurrency)
	// Only customer completions after admission fill the ordinary upgrade tier.
	for range 2 {
		require.NoError(t, repo.RecordConcurrencyResult(ctx, service.AccountConcurrencyResult{AccountID: f.account.ID, StartedAt: time.Now().UTC(), Success: true}, cfg))
	}
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT concurrency FROM accounts WHERE id=$1`, f.account.ID).Scan(&concurrency))
	require.Equal(t, 3, concurrency)
	for range 6 {
		f.round("answer_29")
	}
	for range 4 {
		require.NoError(t, repo.RecordConcurrencyResult(ctx, service.AccountConcurrencyResult{AccountID: f.account.ID, StartedAt: time.Now().UTC().Add(time.Hour), Success: true}, cfg))
	}
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT concurrency FROM accounts WHERE id=$1`, f.account.ID).Scan(&concurrency))
	require.Equal(t, 3, concurrency)
}

func TestOpenAITwoFAAdmissionKeepsImportScheduling(t *testing.T) {
	for _, manualResume := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual_resume=%t", manualResume), func(t *testing.T) {
			f := newAdmissionFixture(t)
			ctx := t.Context()
			_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(extra,'{openai_two_fa_admission,keep_scheduling_on_pass}','true'), schedulable=$2 WHERE id=$1`, f.account.ID, manualResume)
			require.NoError(t, err)
			for range f.policy.PassCount - 1 {
				require.Equal(t, "observed", f.round("passed"))
			}
			require.Equal(t, "admission_passed", f.round("passed"))
			var schedulable bool
			var concurrency int
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT schedulable,concurrency FROM accounts WHERE id=$1", f.account.ID).Scan(&schedulable, &concurrency))
			require.Equal(t, manualResume, schedulable)
			require.Equal(t, f.policy.PassedConcurrency, concurrency)
		})
	}
}
