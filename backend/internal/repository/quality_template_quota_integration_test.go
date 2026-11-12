//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIssue817TemplateCandidateQuotaAndRecovery(t *testing.T) {
	ctx := context.Background()
	var group int64
	require.NoError(t, integrationDB.QueryRow(`INSERT INTO groups(name,platform) VALUES($1,'openai') RETURNING id`, fmt.Sprintf("issue817-%d", time.Now().UnixNano())).Scan(&group))
	var ids []int64
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM quality_rule_templates WHERE account_filter->>'group'=$1`, strconv.FormatInt(group, 10))
		for _, id := range ids {
			_, _ = integrationDB.Exec(`DELETE FROM scheduled_test_plans WHERE account_id=$1`, id)
			_, _ = integrationDB.Exec(`DELETE FROM scheduler_outbox WHERE account_id=$1`, id)
			_, _ = integrationDB.Exec(`DELETE FROM account_groups WHERE account_id=$1`, id)
			_, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, id)
		}
		_, _ = integrationDB.Exec(`DELETE FROM groups WHERE id=$1`, group)
	})
	add := func(platform, kind, extra string) int64 {
		var id int64
		require.NoError(t, integrationDB.QueryRow(`INSERT INTO accounts(name,platform,type,status,schedulable,extra)
 VALUES('issue817-fixture',$1,$2,'active',true,$3::jsonb) RETURNING id`, platform, kind, extra).Scan(&id))
		ids = append(ids, id)
		_, err := integrationDB.Exec(`INSERT INTO account_groups(account_id,group_id,priority) VALUES($1,$2,1)`, id, group)
		require.NoError(t, err)
		return id
	}
	now := time.Now()
	snapshot := func(window string, used float64, observed, reset time.Time) string {
		extra := map[string]any{"codex_usage_updated_at": observed.Format(time.RFC3339Nano), "codex_" + window + "_used_percent": used}
		if !reset.IsZero() {
			extra["codex_"+window+"_reset_at"] = reset.Format(time.RFC3339Nano)
		}
		data, err := json.Marshal(extra)
		require.NoError(t, err)
		return string(data)
	}
	five := add("openai", "oauth", snapshot("5h", 100, now, now.Add(time.Hour)))
	week := add("openai", "oauth", snapshot("7d", 100, now, now.Add(time.Hour)))
	healthy := add("openai", "oauth", snapshot("5h", 99, now, now.Add(time.Hour)))
	unknown := add("openai", "oauth", `{}`)
	stale := add("openai", "oauth", snapshot("7d", 100, now.Add(-3*time.Hour), time.Time{}))
	recovered := add("openai", "oauth", snapshot("5h", 100, now, now.Add(-time.Minute)))
	apiKey := add("openai", "apikey", snapshot("7d", 100, now, now.Add(time.Hour)))
	unrelated := add("anthropic", "oauth", snapshot("7d", 100, now, now.Add(time.Hour)))
	templates := NewQualityRuleTemplateRepository(integrationEntClient, integrationDB)
	filter := service.QualityRuleAccountFilter{Group: strconv.FormatInt(group, 10), Statuses: []string{"active"}}
	matchingIDs := func(filter service.QualityRuleAccountFilter) []int64 {
		rows, err := templates.MatchingAccounts(ctx, filter)
		require.NoError(t, err)
		out := make([]int64, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.ID)
		}
		return out
	}
	require.ElementsMatch(t, []int64{healthy, unknown, stale, recovered, apiKey, unrelated}, matchingIDs(filter), "normal-only candidates must exclude known 5h and weekly exhaustion")
	filter.Statuses = []string{"active", "rate_limited"}
	require.ElementsMatch(t, ids, matchingIDs(filter), "explicit other statuses retain probe intent")
	filter.Statuses = nil
	require.ElementsMatch(t, ids, matchingIDs(filter))
	filter.Statuses = []string{"active"}
	_, err := integrationDB.Exec(`UPDATE accounts SET extra=jsonb_set(extra,'{codex_5h_reset_at}',to_jsonb($2::text)) WHERE id=$1`, five, now.Add(-time.Minute).Format(time.RFC3339Nano))
	require.NoError(t, err)
	require.Contains(t, matchingIDs(filter), five, "window rollover makes the account a candidate again")
	require.NotContains(t, matchingIDs(filter), week)

	// Exercise the persisted link, rather than copying a filter into plan JSON.
	template, err := templates.Create(ctx, &service.QualityRuleTemplate{AccountFilter: filter, ModelID: "fixture-model", CronExpression: "*/5 * * * *", Enabled: true, MaxResults: 50,
		PelicanConfig: &service.PelicanTestConfig{Prompt: "fixture", ReasoningEffort: "medium", ParallelCount: 1, Quality: &service.QualityPolicy{Action: service.QualityActionObserveOnly}}})
	require.NoError(t, err)
	next := time.Now().Add(time.Minute)
	plan, err := templates.CreateLinkedPlan(ctx, template, &service.ScheduledTestPlan{AccountID: healthy, ModelID: template.ModelID, CronExpression: template.CronExpression, Enabled: true, MaxResults: 50, NextRunAt: &next, PelicanConfig: template.PelicanConfig})
	require.NoError(t, err)
	owner, err := templates.ForPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, filter, owner.AccountFilter)
	template.AccountFilter.Statuses = []string{"rate_limited"}
	_, err = templates.Update(ctx, template)
	require.NoError(t, err)
	owner, err = templates.ForPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"rate_limited"}, owner.AccountFilter.Statuses, "execution uses the current selection")
	_, err = templates.Delete(ctx, template.ID)
	require.NoError(t, err)
	owner, err = templates.ForPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Nil(t, owner, "detached rules retain independent behavior")
}

func TestIssue817ManualTriggerIsOneShotAndLeaseFenced(t *testing.T) {
	f := newQualityBPSFixture(t, `{}`, &service.QualityPolicy{Action: "disable_scheduling", AutoRestore: true})
	ctx := context.Background()
	require.NoError(t, f.plans.FinishPelican(ctx, f.plan.ID, f.until, time.Now()))
	stale, err := f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	require.False(t, stale.ManualQualityRun)
	require.NoError(t, f.plans.TriggerQuality(ctx, f.plan.ID))
	manual, err := f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	require.True(t, manual.ManualQualityRun)
	data, err := json.Marshal(manual)
	require.NoError(t, err)
	require.NotContains(t, string(data), "_manual_quality_run", "marker must not become a public configuration field")
	now := time.Now().Add(time.Second).Truncate(time.Microsecond)
	until := now.Add(15 * time.Minute)
	claimed, err := f.plans.ClaimPelican(ctx, stale, now, until, now.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, claimed, "old selection cannot consume a new manual trigger")
	current, err := f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	require.True(t, current.ManualQualityRun)
	claimed, err = f.plans.ClaimPelican(ctx, manual, now, until, now.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, claimed)
	require.True(t, manual.ManualQualityRun, "claimed run keeps its explicit intent")
	current, err = f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	require.False(t, current.ManualQualityRun, "next automatic round cannot inherit the bypass")
	require.Error(t, f.plans.TriggerQuality(ctx, f.plan.ID), "manual runs cannot bypass the active lease")
	require.NoError(t, f.plans.FinishPelican(ctx, f.plan.ID, until, time.Now()))
}

func TestIssue817QuotaSkipPreserves401AndOwnership(t *testing.T) {
	f := oauth401Fixture(t, 6)
	ctx := context.Background()
	require.False(t, oauth401Complete(t, f, oauth401Evidence()))
	require.Equal(t, 1, oauth401Failures(t, f))
	oauth401Next(t, f)
	var before []byte
	require.NoError(t, integrationDB.QueryRow(`SELECT COALESCE((SELECT state FROM account_quality_states WHERE plan_id=$1),'{}'::jsonb)`, f.plan.ID).Scan(&before))
	// A stale worker must not release a newer lease or touch its streak.
	repo := f.plans.(service.QualityQuotaSkipRepository)
	require.NoError(t, repo.FinishQuotaSkippedPelican(ctx, f.plan, f.until.Add(-time.Minute), time.Now()))
	current, err := f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	require.NotNil(t, current.RunningUntil)
	require.NoError(t, repo.FinishQuotaSkippedPelican(ctx, f.plan, f.until, time.Now()))
	current, err = f.plans.GetByID(ctx, f.plan.ID)
	require.NoError(t, err)
	require.Nil(t, current.RunningUntil)
	require.Equal(t, 1, oauth401Failures(t, f))
	var after []byte
	require.NoError(t, integrationDB.QueryRow(`SELECT COALESCE((SELECT state FROM account_quality_states WHERE plan_id=$1),'{}'::jsonb)`, f.plan.ID).Scan(&after))
	require.JSONEq(t, string(before), string(after))
	oauth401ClaimDue(t, f)
	require.Equal(t, 1, oauth401Failures(t, f), "unexecuted quota skip must not reset the prior 401 streak")
	require.False(t, oauth401Complete(t, f, oauth401Evidence()))
	require.Equal(t, 2, oauth401Failures(t, f))
}
