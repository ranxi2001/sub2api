package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type templatePlanRepo struct {
	ScheduledTestPlanRepository
	plans  map[int64]*ScheduledTestPlan
	nextID int64
}

func newTemplatePlanRepo() *templatePlanRepo {
	return &templatePlanRepo{plans: map[int64]*ScheduledTestPlan{}}
}

func (r *templatePlanRepo) add(plan *ScheduledTestPlan) (*ScheduledTestPlan, bool) {
	scope := qualityRuleScope(plan.PelicanConfig)
	for _, existing := range r.plans {
		if existing.AccountID == plan.AccountID && qualityRuleScope(existing.PelicanConfig) == scope {
			return nil, false
		}
	}
	r.nextID++
	stored := *plan
	stored.ID = r.nextID
	r.plans[stored.ID] = &stored
	return &stored, true
}

func (r *templatePlanRepo) ListQualityPlans(context.Context) ([]*ScheduledTestPlan, error) {
	out := []*ScheduledTestPlan{}
	for _, plan := range r.plans {
		out = append(out, plan)
	}
	return out, nil
}

func (r *templatePlanRepo) GetByID(_ context.Context, id int64) (*ScheduledTestPlan, error) {
	plan, ok := r.plans[id]
	if !ok {
		return nil, ErrQualityTemplateNotFound
	}
	copied := *plan
	return &copied, nil
}

func (r *templatePlanRepo) Update(_ context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	stored := *plan
	r.plans[plan.ID] = &stored
	return &stored, nil
}

func (r *templatePlanRepo) Delete(_ context.Context, id int64) error {
	delete(r.plans, id)
	return nil
}

type fakeTemplateRepo struct {
	presets   []*QualityRulePreset
	plans     *templatePlanRepo
	templates map[int64]*QualityRuleTemplate
	accounts  []QualityTemplateAccount
	links     map[int64]map[int64]int64 // template -> account -> plan (0 once the plan is gone)
	filters   []QualityRuleAccountFilter
	nextID    int64
}

func (r *fakeTemplateRepo) ListPresets(context.Context) ([]*QualityRulePreset, error) {
	return r.presets, nil
}

func (r *fakeTemplateRepo) CreatePreset(_ context.Context, preset *QualityRulePreset) (*QualityRulePreset, error) {
	stored := *preset
	stored.ID = int64(len(r.presets) + 1)
	stored.PelicanConfig = cloneQualityPelicanConfig(preset.PelicanConfig)
	r.presets = append(r.presets, &stored)
	return &stored, nil
}

func newFakeTemplateRepo(plans *templatePlanRepo, accounts ...QualityTemplateAccount) *fakeTemplateRepo {
	return &fakeTemplateRepo{plans: plans, templates: map[int64]*QualityRuleTemplate{}, accounts: accounts, links: map[int64]map[int64]int64{}}
}

func (r *fakeTemplateRepo) snapshot(t *QualityRuleTemplate) *QualityRuleTemplate {
	copied := *t
	copied.PelicanConfig = cloneQualityPelicanConfig(t.PelicanConfig)
	copied.PlanIDs = []int64{}
	for _, planID := range r.links[t.ID] {
		if _, ok := r.plans.plans[planID]; ok {
			copied.PlanIDs = append(copied.PlanIDs, planID)
		}
	}
	return &copied
}

func (r *fakeTemplateRepo) List(context.Context) ([]*QualityRuleTemplate, error) {
	out := []*QualityRuleTemplate{}
	for id := int64(1); id <= r.nextID; id++ {
		if t, ok := r.templates[id]; ok {
			out = append(out, r.snapshot(t))
		}
	}
	return out, nil
}

func (r *fakeTemplateRepo) Get(_ context.Context, id int64) (*QualityRuleTemplate, error) {
	t, ok := r.templates[id]
	if !ok {
		return nil, ErrQualityTemplateNotFound
	}
	return r.snapshot(t), nil
}

func (r *fakeTemplateRepo) ForPlan(ctx context.Context, planID int64) (*QualityRuleTemplate, error) {
	for id, links := range r.links {
		for _, linkedPlan := range links {
			if linkedPlan == planID {
				return r.Get(ctx, id)
			}
		}
	}
	return nil, nil
}

func (r *fakeTemplateRepo) Create(ctx context.Context, t *QualityRuleTemplate) (*QualityRuleTemplate, error) {
	r.nextID++
	stored := *t
	stored.ID = r.nextID
	stored.UpdatedAt = time.Unix(r.nextID, 0)
	r.templates[stored.ID] = &stored
	r.links[stored.ID] = map[int64]int64{}
	return r.Get(ctx, stored.ID)
}

func (r *fakeTemplateRepo) Update(ctx context.Context, t *QualityRuleTemplate) (*QualityRuleTemplate, error) {
	current, ok := r.templates[t.ID]
	if !ok {
		return nil, ErrQualityTemplateNotFound
	}
	stored := *t
	stored.UpdatedAt = current.UpdatedAt.Add(time.Second)
	r.templates[t.ID] = &stored
	return r.Get(ctx, t.ID)
}

func (r *fakeTemplateRepo) Delete(_ context.Context, id int64) ([]int64, error) {
	if _, ok := r.templates[id]; !ok {
		return nil, ErrQualityTemplateNotFound
	}
	ids := []int64{}
	for _, planID := range r.links[id] {
		if planID != 0 {
			ids = append(ids, planID)
		}
	}
	delete(r.templates, id)
	delete(r.links, id)
	return ids, nil
}

func (r *fakeTemplateRepo) MatchingAccounts(_ context.Context, filter QualityRuleAccountFilter) ([]QualityTemplateAccount, error) {
	r.filters = append(r.filters, filter)
	return r.accounts, nil
}

func (r *fakeTemplateRepo) LinkedAccountIDs(_ context.Context, id int64) (map[int64]struct{}, error) {
	out := map[int64]struct{}{}
	for accountID := range r.links[id] {
		out[accountID] = struct{}{}
	}
	return out, nil
}

func (r *fakeTemplateRepo) CreateLinkedPlan(_ context.Context, t *QualityRuleTemplate, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	current, ok := r.templates[t.ID]
	if !ok || !current.Enabled || !current.UpdatedAt.Equal(t.UpdatedAt) {
		return nil, ErrQualityTemplateSkipped
	}
	if _, linked := r.links[t.ID][plan.AccountID]; linked {
		return nil, ErrQualityTemplateSkipped
	}
	created, ok := r.plans.add(plan)
	if !ok {
		return nil, ErrQualityTemplateSkipped
	}
	r.links[t.ID][plan.AccountID] = created.ID
	return created, nil
}

func (r *fakeTemplateRepo) MarkSynced(_ context.Context, id int64, at time.Time) error {
	if t, ok := r.templates[id]; ok {
		t.LastSyncedAt = &at
	}
	return nil
}

func probeTemplate() *QualityRuleTemplate {
	return &QualityRuleTemplate{
		AccountFilter:  QualityRuleAccountFilter{Group: " 7 ", Statuses: []string{"active", "active"}},
		ModelID:        "gpt-5.4",
		CronExpression: "*/30 * * * *",
		Enabled:        true,
		PelicanConfig: &PelicanTestConfig{
			QuestionKind:    OpenAICodexStateProbeQuestionKind,
			ReasoningEffort: "medium",
			ParallelCount:   1,
			Quality:         &QualityPolicy{Action: "disable_scheduling", AutoRestore: true},
		},
	}
}

func openAIOAuthAccount(id int64) QualityTemplateAccount {
	return QualityTemplateAccount{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
}

func plansByAccount(r *templatePlanRepo) map[int64]*ScheduledTestPlan {
	out := map[int64]*ScheduledTestPlan{}
	for _, plan := range r.plans {
		out[plan.AccountID] = plan
	}
	return out
}

func newTemplateService(plans *templatePlanRepo, templates *fakeTemplateRepo) *ScheduledTestService {
	return ProvideScheduledTestService(plans, &pelicanResults{}, templates, nil)
}

func TestProvideScheduledTestServiceWiresTemplateRepository(t *testing.T) {
	plans := newTemplatePlanRepo()
	templates := newFakeTemplateRepo(plans)
	svc := ProvideScheduledTestService(plans, &pelicanResults{}, templates, nil)
	require.Same(t, templates, svc.templateRepo)
}

func TestQualityTemplateCoversMatchingAndLaterAccounts(t *testing.T) {
	plans := newTemplatePlanRepo()
	// Account 2 already has a quarantine rule; 3 is not an OpenAI OAuth account; 4 is a UI test account.
	_, _ = plans.add(&ScheduledTestPlan{AccountID: 2, PelicanConfig: &PelicanTestConfig{Quality: &QualityPolicy{Action: "remove_groups"}}})
	templates := newFakeTemplateRepo(plans,
		openAIOAuthAccount(1), openAIOAuthAccount(2),
		QualityTemplateAccount{ID: 3, Platform: "anthropic", Type: AccountTypeOAuth},
		QualityTemplateAccount{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{"synthetic_ui_test": true}},
	)
	svc := newTemplateService(plans, templates)

	template, created, err := svc.CreateQualityTemplate(context.Background(), probeTemplate())
	require.NoError(t, err)
	require.Equal(t, 1, created)
	require.Equal(t, QualityRuleAccountFilter{Group: "7", Statuses: []string{"active"}}, templates.filters[0])
	require.Len(t, template.PlanIDs, 1)
	require.NotNil(t, template.LastSyncedAt)
	first := plansByAccount(plans)[1]
	require.NotNil(t, first)
	require.NotNil(t, first.NextRunAt)
	require.True(t, first.Enabled)
	require.False(t, first.AutoRecover)
	require.Equal(t, 100, first.MaxResults)
	require.Equal(t, "gpt-5.4", first.PelicanConfig.ModelID)
	require.Equal(t, "disable_scheduling", first.PelicanConfig.Quality.Action)

	// A new account in the group is picked up by the next runner tick, exactly once.
	templates.accounts = append(templates.accounts, openAIOAuthAccount(5))
	added, err := svc.SyncQualityTemplates(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, added)
	added, err = svc.SyncQualityTemplates(context.Background())
	require.NoError(t, err)
	require.Zero(t, added)

	// Each plan owns its config: editing one never leaks into another.
	byAccount := plansByAccount(plans)
	require.NotSame(t, byAccount[1].PelicanConfig, byAccount[5].PelicanConfig)

	// A rule the admin deletes by hand is not recreated.
	require.NoError(t, svc.DeletePlan(context.Background(), byAccount[5].ID))
	added, err = svc.SyncQualityTemplates(context.Background())
	require.NoError(t, err)
	require.Zero(t, added)
	require.Nil(t, plansByAccount(plans)[5])
}

func TestQualityTemplateUpdatePropagatesAndPauseStopsFollowing(t *testing.T) {
	plans := newTemplatePlanRepo()
	templates := newFakeTemplateRepo(plans, openAIOAuthAccount(1), openAIOAuthAccount(2))
	svc := newTemplateService(plans, templates)
	template, created, err := svc.CreateQualityTemplate(context.Background(), probeTemplate())
	require.NoError(t, err)
	require.Equal(t, 2, created)

	template.CronExpression = "0 * * * *"
	template.ModelID = "gpt-5.5"
	template.Enabled = false
	result, err := svc.UpdateQualityTemplate(context.Background(), template)
	require.NoError(t, err)
	require.Equal(t, 2, result.Updated)
	require.Zero(t, result.Failed)
	require.Zero(t, result.Created)
	for _, plan := range plans.plans {
		require.False(t, plan.Enabled)
		require.Equal(t, "0 * * * *", plan.CronExpression)
		require.Equal(t, "gpt-5.5", plan.PelicanConfig.ModelID)
	}

	templates.accounts = append(templates.accounts, openAIOAuthAccount(3))
	added, err := svc.SyncQualityTemplates(context.Background())
	require.NoError(t, err)
	require.Zero(t, added, "a paused group rule must not follow new accounts")

	// Resuming turns the old rules back on and covers the account that joined meanwhile.
	resumed, err := svc.GetQualityTemplate(context.Background(), template.ID)
	require.NoError(t, err)
	resumed.Enabled = true
	result, err = svc.UpdateQualityTemplate(context.Background(), resumed)
	require.NoError(t, err)
	require.Equal(t, 2, result.Updated)
	require.Equal(t, 1, result.Created)
	require.Len(t, result.Template.PlanIDs, 3)
	for _, plan := range plans.plans {
		require.True(t, plan.Enabled)
	}
}

func TestQualityTemplateSyncSkipsStaleTemplateVersion(t *testing.T) {
	plans := newTemplatePlanRepo()
	templates := newFakeTemplateRepo(plans)
	svc := newTemplateService(plans, templates)
	template, _, err := svc.CreateQualityTemplate(context.Background(), probeTemplate())
	require.NoError(t, err)
	stale := *template
	_, err = templates.Update(context.Background(), template)
	require.NoError(t, err)
	templates.accounts = []QualityTemplateAccount{openAIOAuthAccount(1)}
	created, err := svc.syncQualityTemplate(context.Background(), &stale, nil)
	require.NoError(t, err)
	require.Zero(t, created)
	require.Empty(t, plans.plans)
}

func TestQualityTemplateDeleteKeepsOrRemovesRules(t *testing.T) {
	for _, deletePlans := range []bool{false, true} {
		plans := newTemplatePlanRepo()
		templates := newFakeTemplateRepo(plans, openAIOAuthAccount(1), openAIOAuthAccount(2))
		svc := newTemplateService(plans, templates)
		template, _, err := svc.CreateQualityTemplate(context.Background(), probeTemplate())
		require.NoError(t, err)
		deleted, err := svc.DeleteQualityTemplate(context.Background(), template.ID, deletePlans)
		require.NoError(t, err)
		if deletePlans {
			require.Equal(t, 2, deleted)
			require.Empty(t, plans.plans)
		} else {
			require.Zero(t, deleted)
			require.Len(t, plans.plans, 2)
		}
		_, err = svc.GetQualityTemplate(context.Background(), template.ID)
		require.ErrorIs(t, err, ErrQualityTemplateNotFound)
	}
}

func TestQualityTemplateValidation(t *testing.T) {
	plans := newTemplatePlanRepo()
	svc := newTemplateService(plans, newFakeTemplateRepo(plans))
	for name, change := range map[string]func(*QualityRuleTemplate){
		"not a quality rule": func(t *QualityRuleTemplate) { t.PelicanConfig.Quality = nil },
		"bad cron":           func(t *QualityRuleTemplate) { t.CronExpression = "nope" },
		"bad group":          func(t *QualityRuleTemplate) { t.AccountFilter.Group = "abc" },
		"bad status":         func(t *QualityRuleTemplate) { t.AccountFilter.Statuses = []string{"deleted"} },
		"probe parallel":     func(t *QualityRuleTemplate) { t.PelicanConfig.ParallelCount = 2 },
	} {
		template := probeTemplate()
		change(template)
		_, _, err := svc.CreateQualityTemplate(context.Background(), template)
		require.Error(t, err, name)
	}
	require.Empty(t, plans.plans)

	unwired := NewScheduledTestService(plans, &pelicanResults{})
	_, _, err := unwired.CreateQualityTemplate(context.Background(), probeTemplate())
	require.Error(t, err)
	added, err := unwired.SyncQualityTemplates(context.Background())
	require.NoError(t, err)
	require.Zero(t, added)
}

func TestQualityTemplateGroupID(t *testing.T) {
	require.Equal(t, int64(0), QualityTemplateGroupID(QualityRuleAccountFilter{}))
	require.Equal(t, int64(AccountListGroupUngrouped), QualityTemplateGroupID(QualityRuleAccountFilter{Group: "ungrouped"}))
	require.Equal(t, int64(12), QualityTemplateGroupID(QualityRuleAccountFilter{Group: "12"}))
}
