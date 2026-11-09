//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// The repository manages its own statements, so fixtures are committed through the shared
// client and removed afterwards (plans and results cascade with their groups).
func createPelicanGroupTestGroups(t *testing.T, labels ...string) []*service.Group {
	t.Helper()
	client := testEntClient(t)
	suffix := time.Now().UnixNano()
	groups := make([]*service.Group, 0, len(labels))
	ids := make([]int64, 0, len(labels))
	for _, label := range labels {
		group := mustCreateGroup(t, client, &service.Group{Name: fmt.Sprintf("pelican-group-test-%s-%d", label, suffix)})
		groups = append(groups, group)
		ids = append(ids, group.ID)
	}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM groups WHERE id = ANY($1)`, pq.Array(ids))
	})
	return groups
}

func newPelicanGroupTestPlan(groupID int64, enabled bool, next time.Time) *service.PelicanGroupTestPlan {
	return &service.PelicanGroupTestPlan{
		GroupID: groupID, ModelID: "gpt-6-astra", CronExpression: "*/30 * * * *", Enabled: enabled, NextRunAt: &next,
		PelicanConfig: &service.PelicanTestConfig{QuestionKind: "pelican", Prompt: "draw a pelican", ReasoningEffort: "high", ParallelCount: 2, ModelID: "gpt-6-astra"},
	}
}

func TestPelicanGroupTestRepo_PlansDueAndLease(t *testing.T) {
	ctx := context.Background()
	repo := NewPelicanGroupTestRepository(integrationDB)
	groups := createPelicanGroupTestGroups(t, "active", "disabled", "deleted")
	active, disabled, deleted := groups[0], groups[1], groups[2]
	_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET status = $2 WHERE id = $1`, disabled.ID, service.StatusDisabled)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE groups SET deleted_at = NOW() WHERE id = $1`, deleted.ID)
	require.NoError(t, err)

	now := time.Now().Truncate(time.Microsecond)
	past := now.Add(-time.Minute)
	due, err := repo.CreatePlan(ctx, newPelicanGroupTestPlan(active.ID, true, past))
	require.NoError(t, err)
	paused, err := repo.CreatePlan(ctx, newPelicanGroupTestPlan(active.ID, false, past))
	require.NoError(t, err)
	future, err := repo.CreatePlan(ctx, newPelicanGroupTestPlan(active.ID, true, now.Add(time.Hour)))
	require.NoError(t, err)
	_, err = repo.CreatePlan(ctx, newPelicanGroupTestPlan(disabled.ID, true, past))
	require.NoError(t, err)
	_, err = repo.CreatePlan(ctx, newPelicanGroupTestPlan(deleted.ID, true, past))
	require.NoError(t, err)

	require.Equal(t, active.Name, due.GroupName)
	require.Equal(t, service.PlatformAnthropic, due.GroupPlatform)
	require.Equal(t, service.StatusActive, due.GroupStatus)
	require.Equal(t, "draw a pelican", due.PelicanConfig.Prompt)
	require.Equal(t, 2, due.PelicanConfig.ParallelCount)
	require.Nil(t, due.LastResult)

	listed, err := repo.ListPlans(ctx)
	require.NoError(t, err)
	statuses := map[int64]string{}
	for _, plan := range listed {
		statuses[plan.GroupID] = plan.GroupStatus
	}
	require.Equal(t, service.StatusDisabled, statuses[disabled.ID])
	require.Equal(t, "deleted", statuses[deleted.ID], "soft-deleted groups are flagged so admins can remove their plans")

	dueIDs := func() []int64 {
		t.Helper()
		plans, err := repo.ListDue(ctx, now)
		require.NoError(t, err)
		ids := []int64{}
		for _, plan := range plans {
			for _, group := range groups {
				if plan.GroupID == group.ID {
					ids = append(ids, plan.ID)
				}
			}
		}
		return ids
	}
	require.Equal(t, []int64{due.ID}, dueIDs(), "paused, future, disabled-group and deleted-group plans are skipped")

	// A scheduled claim needs the plan unchanged since it was listed, then holds the lease.
	until := now.Add(15 * time.Minute)
	next := now.Add(30 * time.Minute).Truncate(time.Microsecond)
	stale := *due
	stale.UpdatedAt = due.UpdatedAt.Add(-time.Second)
	claimed, err := repo.Claim(ctx, &stale, now, until, &next)
	require.NoError(t, err)
	require.False(t, claimed, "an edit after listing invalidates the claim")
	claimed, err = repo.Claim(ctx, due, now, until, &next)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = repo.Claim(ctx, due, now, until, nil)
	require.NoError(t, err)
	require.False(t, claimed, "a manual run waits for the scheduled one")
	require.Empty(t, dueIDs(), "a running plan is not due")
	running, err := repo.GetPlan(ctx, due.ID)
	require.NoError(t, err)
	require.WithinDuration(t, next, *running.NextRunAt, time.Millisecond)
	require.NotNil(t, running.RunningUntil)

	finished := now.Add(2 * time.Minute)
	require.NoError(t, repo.Finish(ctx, due.ID, until.Add(time.Second), finished), "a stale lease owner cannot finish")
	running, err = repo.GetPlan(ctx, due.ID)
	require.NoError(t, err)
	require.NotNil(t, running.RunningUntil)
	require.NoError(t, repo.Finish(ctx, due.ID, until, finished))
	done, err := repo.GetPlan(ctx, due.ID)
	require.NoError(t, err)
	require.Nil(t, done.RunningUntil)
	require.WithinDuration(t, finished, *done.LastRunAt, time.Millisecond)

	// A manual claim ignores the schedule and keeps next_run_at.
	claimed, err = repo.Claim(ctx, future, now, until, nil)
	require.NoError(t, err)
	require.True(t, claimed)
	afterManual, err := repo.GetPlan(ctx, future.ID)
	require.NoError(t, err)
	require.WithinDuration(t, *future.NextRunAt, *afterManual.NextRunAt, time.Millisecond)

	// Edits replace the schedule and config; unknown plans report nil.
	edited := *paused
	edited.ModelID, edited.CronExpression, edited.Enabled = "claude-opus-5-5", "0 * * * *", true
	edited.PelicanConfig = &service.PelicanTestConfig{QuestionKind: "pelican", Prompt: "new prompt", ReasoningEffort: "low", ParallelCount: 1, ModelID: "claude-opus-5-5"}
	updated, err := repo.UpdatePlan(ctx, &edited)
	require.NoError(t, err)
	require.Equal(t, "claude-opus-5-5", updated.ModelID)
	require.Equal(t, "0 * * * *", updated.CronExpression)
	require.True(t, updated.Enabled)
	require.Equal(t, "new prompt", updated.PelicanConfig.Prompt)
	require.True(t, updated.UpdatedAt.After(paused.UpdatedAt) || updated.UpdatedAt.Equal(paused.UpdatedAt))
	missing := edited
	missing.ID = -1
	gone, err := repo.UpdatePlan(ctx, &missing)
	require.NoError(t, err)
	require.Nil(t, gone)
	gotMissing, err := repo.GetPlan(ctx, -1)
	require.NoError(t, err)
	require.Nil(t, gotMissing)

	removed, err := repo.DeletePlan(ctx, paused.ID)
	require.NoError(t, err)
	require.True(t, removed)
	removed, err = repo.DeletePlan(ctx, paused.ID)
	require.NoError(t, err)
	require.False(t, removed)
}

func TestPelicanGroupTestRepo_ResultsHistory(t *testing.T) {
	ctx := context.Background()
	repo := NewPelicanGroupTestRepository(integrationDB)
	group := createPelicanGroupTestGroups(t, "history")[0]
	plan, err := repo.CreatePlan(ctx, newPelicanGroupTestPlan(group.ID, true, time.Now().Add(time.Hour)))
	require.NoError(t, err)
	other, err := repo.CreatePlan(ctx, newPelicanGroupTestPlan(group.ID, true, time.Now().Add(time.Hour)))
	require.NoError(t, err)

	started := time.Date(2026, 9, 28, 4, 0, 0, 0, time.UTC)
	save := func(planID int64, result service.PelicanGroupTestResult) *service.PelicanGroupTestResult {
		t.Helper()
		result.PlanID, result.StartedAt, result.FinishedAt = planID, started, started.Add(time.Minute)
		result.PelicanConfig = &service.PelicanTestConfig{QuestionKind: "pelican", ModelID: "gpt-6-astra", ReasoningEffort: "high"}
		saved, err := repo.CreateResult(ctx, &result)
		require.NoError(t, err)
		require.Positive(t, saved.ID)
		return saved
	}
	none := save(plan.ID, service.PelicanGroupTestResult{Status: "failed", ErrorMessage: "no_available_account: no available accounts"})
	failedOver := save(plan.ID, service.PelicanGroupTestResult{
		Status: "success", ResponseText: "<svg data-n=\"2\"></svg>", LatencyMs: 4200, AccountID: 88, AccountName: "pool-b",
		Attempts: []service.PelicanGroupTestAttempt{{AccountID: 87, AccountName: "pool-a", Error: "API returned 429"}},
	})
	otherResult := save(other.ID, service.PelicanGroupTestResult{Status: "success", ResponseText: "<svg></svg>", AccountID: 90, AccountName: "pool-c"})

	// Group results take their IDs from the account-test sequence, so showcase sources never collide.
	var nextShared int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT nextval('scheduled_test_results_id_seq')`).Scan(&nextShared))
	require.Greater(t, nextShared, otherResult.ID)

	listed, err := repo.GetPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.NotNil(t, listed.LastResult)
	require.Equal(t, failedOver.ID, listed.LastResult.ID)
	require.Equal(t, "pool-b", listed.LastResult.AccountName)
	require.Equal(t, []service.PelicanGroupTestAttempt{{AccountID: 87, AccountName: "pool-a", Error: "API returned 429"}}, listed.LastResult.Attempts)
	require.Empty(t, listed.LastResult.ResponseText, "the plan overview never carries HTML")

	page, total, err := repo.ListResults(ctx, plan.ID, 0, 10)
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Equal(t, []int64{failedOver.ID, none.ID}, []int64{page[0].ID, page[1].ID})
	require.Empty(t, page[0].ResponseText)
	require.Equal(t, group.Name, page[0].GroupName)
	require.Equal(t, group.ID, page[0].GroupID)
	require.Zero(t, page[1].AccountID, "no account was routed")
	require.Equal(t, []service.PelicanGroupTestAttempt{}, page[1].Attempts)
	require.Equal(t, "gpt-6-astra", page[1].PelicanConfig.ModelID)
	page, total, err = repo.ListResults(ctx, plan.ID, 1, 1)
	require.NoError(t, err)
	require.Len(t, page, 1, "the next page skips earlier results")
	require.Equal(t, none.ID, page[0].ID)
	require.EqualValues(t, 2, total)
	page, total, err = repo.ListResults(ctx, plan.ID, 2, 1)
	require.NoError(t, err)
	require.Empty(t, page)
	require.EqualValues(t, 2, total)
	page, total, err = repo.ListResults(ctx, 0, 0, 500)
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	seen := map[int64]bool{}
	for _, result := range page {
		seen[result.ID] = true
	}
	require.True(t, seen[otherResult.ID] && seen[failedOver.ID], "plan 0 lists every plan")

	full, err := repo.GetResult(ctx, failedOver.ID)
	require.NoError(t, err)
	require.Equal(t, `<svg data-n="2"></svg>`, full.ResponseText)
	require.EqualValues(t, 4200, full.LatencyMs)
	require.Equal(t, started, full.StartedAt.UTC())
	missing, err := repo.GetResult(ctx, -1)
	require.NoError(t, err)
	require.Nil(t, missing)

	require.NoError(t, repo.PruneResults(ctx, plan.ID, 1))
	page, total, err = repo.ListResults(ctx, plan.ID, 0, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, page, 1)
	require.Equal(t, failedOver.ID, page[0].ID)

	_, err = integrationDB.ExecContext(ctx, `UPDATE pelican_group_test_results SET created_at = NOW() - INTERVAL '8 days' WHERE id = $1`, otherResult.ID)
	require.NoError(t, err)
	require.NoError(t, repo.PruneExpiredResults(ctx, time.Now().Add(-7*24*time.Hour)))
	gone, err := repo.GetResult(ctx, otherResult.ID)
	require.NoError(t, err)
	require.Nil(t, gone)
	kept, err := repo.GetResult(ctx, failedOver.ID)
	require.NoError(t, err)
	require.NotNil(t, kept)

	removed, err := repo.DeletePlan(ctx, plan.ID)
	require.NoError(t, err)
	require.True(t, removed)
	gone, err = repo.GetResult(ctx, failedOver.ID)
	require.NoError(t, err)
	require.Nil(t, gone, "results go with their plan")
}

const pelicanGroupTestsMigration = "253_pelican_group_tests.sql"

// 253 turns the groups picked for the showcase before group tests into paused plans, so
// they stay on the gallery after the upgrade.
func TestPelicanGroupTestRepo_CostsSurviveRetentionAndPlanDeletion(t *testing.T) {
	ctx := context.Background()
	tz := useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
	repo := &pelicanGroupTestRepository{db: integrationDB, configuredTimezone: tz}
	groups := createPelicanGroupTestGroups(t, "costs", "isolated")
	a, err := repo.CreatePlan(ctx, newPelicanGroupTestPlan(groups[0].ID, true, time.Now()))
	require.NoError(t, err)
	b, err := repo.CreatePlan(ctx, newPelicanGroupTestPlan(groups[0].ID, true, time.Now()))
	require.NoError(t, err)
	other, err := repo.CreatePlan(ctx, newPelicanGroupTestPlan(groups[1].ID, true, time.Now()))
	require.NoError(t, err)
	today := tz.startOfDay(time.Now())
	// Local 00:30 is still yesterday in the database's UTC session.
	finished := today.Add(30 * time.Minute).UTC()
	save := func(planID int64, cost *float64, at time.Time, partial bool) *service.PelicanGroupTestResult {
		t.Helper()
		saved, err := repo.CreateResult(ctx, &service.PelicanGroupTestResult{PlanID: planID, GroupID: groups[1].ID, Status: "failed", StartedAt: at.Add(-time.Minute), FinishedAt: at, CostUSD: cost, CostIncomplete: partial})
		require.NoError(t, err)
		return saved
	}
	oldCost, firstCost, secondCost, zero := 1.25, 0.1, 0.2, 0.0
	save(a.ID, &oldCost, today.Add(-time.Second), false)
	first := save(a.ID, &firstCost, finished, false)
	save(b.ID, &secondCost, finished, true)
	save(a.ID, nil, finished, false)
	save(other.ID, &zero, finished, false)
	detail, err := repo.GetResult(ctx, first.ID)
	require.NoError(t, err)
	require.NotNil(t, detail.CostUSD)
	require.InDelta(t, firstCost, *detail.CostUSD, 1e-10)
	page, _, err := repo.ListResults(ctx, b.ID, 0, 20)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.InDelta(t, secondCost, *page[0].CostUSD, 1e-10)
	require.True(t, page[0].CostIncomplete)
	last, err := repo.GetPlan(ctx, b.ID)
	require.NoError(t, err)
	require.InDelta(t, secondCost, *last.LastResult.CostUSD, 1e-10)
	require.True(t, last.LastResult.CostIncomplete)

	// Concurrent parallel samples must atomically add to the same daily row.
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cost := 0.01
			_, err := repo.CreateResult(ctx, &service.PelicanGroupTestResult{PlanID: a.ID, Status: "success", StartedAt: finished, FinishedAt: finished, CostUSD: &cost})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	check := func() {
		t.Helper()
		got, err := repo.GetPlan(ctx, b.ID)
		require.NoError(t, err)
		require.InDelta(t, 0.42, got.TodayCostUSD, 1e-10)
		require.InDelta(t, 1.67, got.TotalCostUSD, 1e-10)
		require.True(t, got.TodayCostIncomplete)
		require.True(t, got.TotalCostIncomplete)
		isolated, err := repo.GetPlan(ctx, other.ID)
		require.NoError(t, err)
		require.Zero(t, isolated.TotalCostUSD)
		require.False(t, isolated.TotalCostIncomplete)
	}
	check()
	require.NoError(t, repo.PruneResults(ctx, a.ID, 2))
	check()
	require.NoError(t, repo.PruneExpiredResults(ctx, time.Now().Add(time.Hour)))
	check()
	deleted, err := repo.DeletePlan(ctx, a.ID)
	require.NoError(t, err)
	require.True(t, deleted)
	check()
	// Replaying the migration must neither fabricate legacy costs nor reset totals.
	migration, err := dbmigrations.FS.ReadFile("261_pelican_group_test_costs.sql")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	check()
	plans, err := repo.ListPlans(ctx)
	require.NoError(t, err)
	for _, plan := range plans {
		if plan.ID == b.ID {
			require.InDelta(t, 1.67, plan.TotalCostUSD, 1e-10)
		}
	}
	due, err := repo.ListDue(ctx, time.Now().AddDate(0, 0, 1))
	require.NoError(t, err)
	for _, plan := range due {
		if plan.ID == b.ID {
			require.Zero(t, plan.TodayCostUSD)
			require.False(t, plan.TodayCostIncomplete)
			require.InDelta(t, 1.67, plan.TotalCostUSD, 1e-10)
		}
	}
}

func TestMigration253ConvertsShowcaseGroupsToPausedPlans(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	createGroup := func(name string) int64 {
		t.Helper()
		var id int64
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO groups (name, platform, rate_multiplier, status)
 VALUES ($1, 'openai', 1, 'active') RETURNING id`, fmt.Sprintf("migration-253-%s-%d", name, time.Now().UnixNano())).Scan(&id))
		return id
	}
	withItems, empty, deleted, planned := createGroup("items"), createGroup("empty"), createGroup("deleted"), createGroup("planned")
	_, err := tx.ExecContext(ctx, `UPDATE groups SET deleted_at = NOW() WHERE id = $1`, deleted)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO pelican_showcase_items (group_id, source_result_id, model_id, reasoning_effort, response_text, generated_at)
 VALUES ($1, 1, 'gpt-old', 'low', '<svg></svg>', NOW() - INTERVAL '1 hour'), ($1, 2, 'gpt-6-astra', 'high', '<svg></svg>', NOW())`, withItems)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO pelican_group_test_plans (group_id, model_id, enabled, pelican_config)
 VALUES ($1, 'kept-model', true, '{"prompt":"kept"}')`, planned)
	require.NoError(t, err)

	setConfig := func(value string) {
		t.Helper()
		_, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('pelican_showcase_config', $1)
 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, value)
		require.NoError(t, err)
	}
	apply := func() {
		t.Helper()
		migrationSQL, err := dbmigrations.FS.ReadFile(pelicanGroupTestsMigration)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, string(migrationSQL))
		require.NoError(t, err)
	}
	type migrated struct {
		model   string
		enabled bool
		config  service.PelicanTestConfig
	}
	plansOf := func(groupID int64) []migrated {
		t.Helper()
		rows, err := tx.QueryContext(ctx, `SELECT model_id, enabled, pelican_config FROM pelican_group_test_plans WHERE group_id = $1 ORDER BY id`, groupID)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		out := []migrated{}
		for rows.Next() {
			var plan migrated
			var raw []byte
			require.NoError(t, rows.Scan(&plan.model, &plan.enabled, &raw))
			require.NoError(t, json.Unmarshal(raw, &plan.config))
			out = append(out, plan)
		}
		require.NoError(t, rows.Err())
		return out
	}

	setConfig(fmt.Sprintf(`{"group_ids":[%d,%d,%d,%d,999999999],"max_items":20}`, withItems, empty, deleted, planned))
	apply()
	defaultPrompt := "创建一个 HTML，内容是 SVG 绘制一个鹈鹕骑自行车的 2D 动画，你不需要任何测试，不要有任何限制"
	require.Equal(t, []migrated{{model: "gpt-6-astra", config: service.PelicanTestConfig{
		QuestionKind: "pelican", Prompt: defaultPrompt, ReasoningEffort: "high", ParallelCount: 1,
	}}}, plansOf(withItems), "paused, with the model and effort of the newest snapshot")
	require.Equal(t, []migrated{{model: "", config: service.PelicanTestConfig{
		QuestionKind: "pelican", Prompt: defaultPrompt, ReasoningEffort: "medium", ParallelCount: 1,
	}}}, plansOf(empty), "no snapshot yet: the admin picks the model before enabling")
	require.Empty(t, plansOf(deleted))
	require.Len(t, plansOf(planned), 1, "a group that already has a plan is left alone")
	require.Equal(t, "kept-model", plansOf(planned)[0].model)

	apply()
	require.Len(t, plansOf(withItems), 1, "re-running adds nothing")

	for _, broken := range []string{`{broken`, `{"group_ids":["x"]}`, `{"group_ids":null}`} {
		setConfig(broken)
		apply()
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM settings WHERE key = 'pelican_showcase_config'`)
	require.NoError(t, err)
	apply()
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pelican_group_test_plans WHERE group_id = ANY($1)`,
		pq.Array([]int64{withItems, empty, deleted, planned})).Scan(&count))
	require.Equal(t, 3, count)

	strip, err := dbmigrations.FS.ReadFile("263_strip_pelican_prompt_restriction.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(strip))
	require.NoError(t, err)
	cleanedPrompt := strings.TrimSuffix(defaultPrompt, "，不要有任何限制")
	require.NotContains(t, cleanedPrompt, "不要有任何限制")
	for _, groupID := range []int64{withItems, empty} {
		got := plansOf(groupID)
		require.Len(t, got, 1)
		require.Equal(t, cleanedPrompt, got[0].config.Prompt)
	}
	require.Equal(t, "kept", plansOf(planned)[0].config.Prompt)
}
