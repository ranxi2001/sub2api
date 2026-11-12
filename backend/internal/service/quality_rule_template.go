package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// ErrQualityTemplateSkipped means an account was not linked this round: it is already
// linked, the rule scope was taken concurrently, the template changed since it was read,
// or the account is gone. Nothing is recorded, so the next sync looks at it again.
var ErrQualityTemplateSkipped = errors.New("quality rule template account skipped")

var ErrQualityTemplateNotFound = errors.New("quality rule template not found")

var errQualityTemplatesUnavailable = errors.New("quality rule templates are not configured")

// QualityRuleAccountFilter mirrors the quality-ops account selector. Group is "" (all
// groups), "ungrouped" or a group id; any listed status matches (none = every status).
type QualityRuleAccountFilter struct {
	Group    string   `json:"group,omitempty"`
	Type     string   `json:"type,omitempty"`
	Search   string   `json:"search,omitempty"`
	Statuses []string `json:"statuses,omitempty"`
}

// QualityRuleTemplate is a quality rule saved for an account filter (「分组规则」).
// Each matching account gets its own scheduled plan; accounts that start matching
// later get one on the next sync.
type QualityRuleTemplate struct {
	PelicanConfig  *PelicanTestConfig       `json:"pelican_config"`
	LastSyncedAt   *time.Time               `json:"last_synced_at"`
	AccountFilter  QualityRuleAccountFilter `json:"account_filter"`
	PlanIDs        []int64                  `json:"plan_ids"`
	ModelID        string                   `json:"model_id"`
	CronExpression string                   `json:"cron_expression"`
	CreatedAt      time.Time                `json:"created_at"`
	UpdatedAt      time.Time                `json:"updated_at"`
	ID             int64                    `json:"id"`
	MaxResults     int                      `json:"max_results"`
	Enabled        bool                     `json:"enabled"`
}

// QualityTemplateAccount is the account data a template sync needs.
type QualityTemplateAccount struct {
	Extra    map[string]any
	Platform string
	Type     string
	ID       int64
}

// QualityTemplateUpdate reports how an edit reached the template's existing rules.
type QualityTemplateUpdate struct {
	Template *QualityRuleTemplate `json:"template"`
	Updated  int                  `json:"updated"`
	Failed   int                  `json:"failed"`
	Created  int                  `json:"created"`
}

// QualityRuleTemplateRepository stores group rules and the accounts each one has covered.
type QualityRuleTemplateRepository interface {
	// Presets are configuration snapshots in separate storage, never sync inputs.
	ListPresets(ctx context.Context) ([]*QualityRulePreset, error)
	CreatePreset(ctx context.Context, preset *QualityRulePreset) (*QualityRulePreset, error)
	List(ctx context.Context) ([]*QualityRuleTemplate, error)
	Get(ctx context.Context, id int64) (*QualityRuleTemplate, error)
	// ForPlan returns the current owning template, or nil for an independent plan.
	ForPlan(ctx context.Context, planID int64) (*QualityRuleTemplate, error)
	Create(ctx context.Context, template *QualityRuleTemplate) (*QualityRuleTemplate, error)
	Update(ctx context.Context, template *QualityRuleTemplate) (*QualityRuleTemplate, error)
	// Delete removes the template and returns the rules it had created that still exist.
	Delete(ctx context.Context, id int64) ([]int64, error)
	// MatchingAccounts uses the same predicates as the admin account list.
	MatchingAccounts(ctx context.Context, filter QualityRuleAccountFilter) ([]QualityTemplateAccount, error)
	LinkedAccountIDs(ctx context.Context, id int64) (map[int64]struct{}, error)
	// CreateLinkedPlan records the account link and creates its plan in one transaction,
	// or returns ErrQualityTemplateSkipped.
	CreateLinkedPlan(ctx context.Context, template *QualityRuleTemplate, plan *ScheduledTestPlan) (*ScheduledTestPlan, error)
	MarkSynced(ctx context.Context, id int64, at time.Time) error
}

var qualityTemplateStatuses = map[string]struct{}{
	StatusActive: {}, StatusDisabled: {}, StatusError: {}, "inactive": {},
	"rate_limited": {}, "temp_unschedulable": {}, "unschedulable": {},
}

func normalizeQualityRuleAccountFilter(filter QualityRuleAccountFilter) (QualityRuleAccountFilter, error) {
	out := QualityRuleAccountFilter{Group: strings.TrimSpace(filter.Group), Type: strings.TrimSpace(filter.Type), Search: strings.TrimSpace(filter.Search)}
	if out.Group != "" && out.Group != "ungrouped" {
		id, err := strconv.ParseInt(out.Group, 10, 64)
		if err != nil || id <= 0 {
			return out, fmt.Errorf("invalid group filter")
		}
		out.Group = strconv.FormatInt(id, 10)
	}
	if len(out.Type) > 50 {
		return out, fmt.Errorf("invalid account type filter")
	}
	if utf8.RuneCountInString(out.Search) > 100 {
		return out, fmt.Errorf("account search must be at most 100 characters")
	}
	seen := make(map[string]struct{}, len(filter.Statuses))
	for _, status := range filter.Statuses {
		status = strings.TrimSpace(status)
		if _, ok := qualityTemplateStatuses[status]; !ok {
			return out, fmt.Errorf("invalid account status filter")
		}
		if _, dup := seen[status]; !dup {
			seen[status] = struct{}{}
			out.Statuses = append(out.Statuses, status)
		}
	}
	return out, nil
}

// QualityTemplateGroupID converts the filter group to the account list's group argument.
func QualityTemplateGroupID(filter QualityRuleAccountFilter) int64 {
	switch filter.Group {
	case "":
		return 0
	case "ungrouped":
		return AccountListGroupUngrouped
	}
	id, _ := strconv.ParseInt(filter.Group, 10, 64)
	return id
}

func qualityRuleScope(cfg *PelicanTestConfig) string {
	if cfg == nil || cfg.Quality == nil {
		return ""
	}
	switch cfg.Quality.Action {
	case QualityActionEnableBPS:
		return "bps"
	case QualityActionObserveOnly:
		return "observation"
	}
	return "quarantine"
}

// Same rule as the quality-ops account picker: probes only run on real OpenAI OAuth accounts.
func qualityProbeSupportsAccount(account QualityTemplateAccount) bool {
	if account.Platform != PlatformOpenAI || (account.Type != AccountTypeOAuth && account.Type != AccountTypeSetupToken) {
		return false
	}
	synthetic, _ := account.Extra["synthetic_ui_test"].(bool)
	return !synthetic
}

// Every plan gets its own copy: validation writes the model into the config.
func cloneQualityPelicanConfig(cfg *PelicanTestConfig) *PelicanTestConfig {
	if cfg == nil {
		return nil
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil
	}
	var out PelicanTestConfig
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return &out
}

func (t *QualityRuleTemplate) planFor(accountID int64) *ScheduledTestPlan {
	return &ScheduledTestPlan{
		AccountID:      accountID,
		ModelID:        t.ModelID,
		CronExpression: t.CronExpression,
		Enabled:        t.Enabled,
		MaxResults:     t.MaxResults,
		PelicanConfig:  cloneQualityPelicanConfig(t.PelicanConfig),
	}
}

// prepareQualityTemplate validates the template exactly as a plan created from it would be.
func prepareQualityTemplate(t *QualityRuleTemplate) error {
	filter, err := normalizeQualityRuleAccountFilter(t.AccountFilter)
	if err != nil {
		return err
	}
	t.AccountFilter = filter
	if t.PelicanConfig == nil || t.PelicanConfig.Quality == nil || t.PelicanConfig.Quality.Action == QualityActionOpenAITwoFAAdmission {
		return fmt.Errorf("group rules must be quality rules")
	}
	config, err := reusableQualityConfig(t.PelicanConfig)
	if err != nil {
		return err
	}
	config.RuleName = t.PelicanConfig.RuleName
	t.PelicanConfig = config
	sample := t.planFor(0)
	if _, err := nextPlanRun(sample, time.Now()); err != nil {
		return fmt.Errorf("invalid test schedule: %w", err)
	}
	t.ModelID = sample.ModelID
	t.MaxResults = sample.MaxResults
	t.PelicanConfig = sample.PelicanConfig
	return nil
}

func (s *ScheduledTestService) ListQualityTemplates(ctx context.Context) ([]*QualityRuleTemplate, error) {
	if s.templateRepo == nil {
		return []*QualityRuleTemplate{}, nil
	}
	return s.templateRepo.List(ctx)
}

func (s *ScheduledTestService) GetQualityTemplate(ctx context.Context, id int64) (*QualityRuleTemplate, error) {
	if s.templateRepo == nil {
		return nil, errQualityTemplatesUnavailable
	}
	return s.templateRepo.Get(ctx, id)
}

// CreateQualityTemplate saves the group rule and immediately covers the accounts that
// match now. It returns how many rules were created.
func (s *ScheduledTestService) CreateQualityTemplate(ctx context.Context, t *QualityRuleTemplate) (*QualityRuleTemplate, int, error) {
	if s.templateRepo == nil {
		return nil, 0, errQualityTemplatesUnavailable
	}
	if err := prepareQualityTemplate(t); err != nil {
		return nil, 0, err
	}
	created, err := s.templateRepo.Create(ctx, t)
	if err != nil {
		return nil, 0, err
	}
	count := 0
	if created.Enabled {
		if count, err = s.syncQualityTemplate(ctx, created, nil); err != nil {
			// The runner retries every minute; the template itself is saved.
			logger.LegacyPrintf("service.scheduled_test", "quality template %d initial sync: %v", created.ID, err)
		}
	}
	return s.reloadQualityTemplate(ctx, created), count, nil
}

// UpdateQualityTemplate saves the template and applies the same settings to every rule it
// created (pausing the template pauses them). A changed filter only affects accounts that
// start matching from now on.
func (s *ScheduledTestService) UpdateQualityTemplate(ctx context.Context, t *QualityRuleTemplate) (*QualityTemplateUpdate, error) {
	if s.templateRepo == nil {
		return nil, errQualityTemplatesUnavailable
	}
	if err := prepareQualityTemplate(t); err != nil {
		return nil, err
	}
	saved, err := s.templateRepo.Update(ctx, t)
	if err != nil {
		return nil, err
	}
	// Re-read after the update commits so rules created by a concurrent sync are included.
	saved = s.reloadQualityTemplate(ctx, saved)
	result := &QualityTemplateUpdate{}
	for _, planID := range saved.PlanIDs {
		plan, err := s.planRepo.GetByID(ctx, planID)
		if err != nil {
			result.Failed++
			continue
		}
		plan.ModelID = saved.ModelID
		plan.CronExpression = saved.CronExpression
		plan.MaxResults = saved.MaxResults
		plan.Enabled = saved.Enabled
		plan.PelicanConfig = cloneQualityPelicanConfig(saved.PelicanConfig)
		supported, supportErr := s.filterQualityTemplatePlan(ctx, plan)
		if supportErr != nil {
			result.Failed++
			continue
		}
		if !supported {
			plan.Enabled = false
		}
		// A template may have no supported models for this linked account.
		// Keep it paused rather than falsely classifying or deleting the rule.
		next, validationErr := nextPlanRun(plan, time.Now())
		if validationErr != nil {
			result.Failed++
			continue
		}
		plan.NextRunAt = &next
		if _, err := s.planRepo.Update(ctx, plan); err != nil {
			logger.LegacyPrintf("service.scheduled_test", "quality template %d update plan %d: %v", saved.ID, planID, err)
			result.Failed++
			continue
		}
		result.Updated++
	}
	if saved.Enabled {
		if result.Created, err = s.syncQualityTemplate(ctx, saved, nil); err != nil {
			logger.LegacyPrintf("service.scheduled_test", "quality template %d sync after edit: %v", saved.ID, err)
		}
	}
	result.Template = s.reloadQualityTemplate(ctx, saved)
	return result, nil
}

// DeleteQualityTemplate stops following the filter. With deletePlans the rules it created
// are deleted too; otherwise they stay as ordinary per-account rules.
func (s *ScheduledTestService) DeleteQualityTemplate(ctx context.Context, id int64, deletePlans bool) (int, error) {
	if s.templateRepo == nil {
		return 0, errQualityTemplatesUnavailable
	}
	planIDs, err := s.templateRepo.Delete(ctx, id)
	if err != nil || !deletePlans {
		return 0, err
	}
	deleted := 0
	for _, planID := range planIDs {
		if err := s.planRepo.Delete(ctx, planID); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

// SyncQualityTemplates gives every enabled group rule to accounts that newly match it.
func (s *ScheduledTestService) SyncQualityTemplates(ctx context.Context) (int, error) {
	if s.templateRepo == nil {
		return 0, nil
	}
	templates, err := s.templateRepo.List(ctx)
	if err != nil {
		return 0, err
	}
	var taken map[qualityScopeKey]struct{}
	total := 0
	for _, t := range templates {
		if !t.Enabled {
			continue
		}
		if taken == nil {
			if taken, err = s.qualityScopesTaken(ctx); err != nil {
				return 0, err
			}
		}
		created, err := s.syncQualityTemplate(ctx, t, taken)
		total += created
		if err != nil {
			logger.LegacyPrintf("service.scheduled_test", "quality template %d sync: %v", t.ID, err)
		}
	}
	return total, nil
}

type qualityScopeKey struct {
	scope     string
	accountID int64
}

func (s *ScheduledTestService) qualityScopesTaken(ctx context.Context) (map[qualityScopeKey]struct{}, error) {
	plans, err := s.planRepo.ListQualityPlans(ctx)
	if err != nil {
		return nil, err
	}
	taken := make(map[qualityScopeKey]struct{}, len(plans))
	for _, plan := range plans {
		taken[qualityScopeKey{accountID: plan.AccountID, scope: qualityRuleScope(plan.PelicanConfig)}] = struct{}{}
	}
	return taken, nil
}

// Accounts already linked, holding a rule of the same kind, or unsupported by the probe are
// skipped. Only linked accounts are remembered, so a skipped account is looked at again.
func (s *ScheduledTestService) syncQualityTemplate(ctx context.Context, t *QualityRuleTemplate, taken map[qualityScopeKey]struct{}) (int, error) {
	if taken == nil {
		var err error
		if taken, err = s.qualityScopesTaken(ctx); err != nil {
			return 0, err
		}
	}
	accounts, err := s.templateRepo.MatchingAccounts(ctx, t.AccountFilter)
	if err != nil {
		return 0, err
	}
	linked, err := s.templateRepo.LinkedAccountIDs(ctx, t.ID)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	scope, probe := qualityRuleScope(t.PelicanConfig), isOpenAICodexStateProbePlan(t.PelicanConfig)
	created := 0
	var firstErr error
	for _, account := range accounts {
		key := qualityScopeKey{accountID: account.ID, scope: scope}
		if _, ok := linked[account.ID]; ok {
			continue
		}
		if _, ok := taken[key]; ok {
			continue
		}
		if probe && !qualityProbeSupportsAccount(account) {
			continue
		}
		plan := t.planFor(account.ID)
		supported, supportErr := s.filterQualityTemplatePlan(ctx, plan)
		if supportErr != nil {
			if firstErr == nil {
				firstErr = supportErr
			}
			continue
		}
		if !supported {
			continue
		}
		next, err := nextPlanRun(plan, now)
		if err != nil {
			return created, fmt.Errorf("invalid test schedule: %w", err)
		}
		plan.NextRunAt = &next
		if _, err := s.templateRepo.CreateLinkedPlan(ctx, t, plan); err != nil {
			if !errors.Is(err, ErrQualityTemplateSkipped) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		taken[key] = struct{}{}
		created++
	}
	if err := s.templateRepo.MarkSynced(ctx, t.ID, now); err != nil && firstErr == nil {
		firstErr = err
	}
	return created, firstErr
}

func (s *ScheduledTestService) reloadQualityTemplate(ctx context.Context, t *QualityRuleTemplate) *QualityRuleTemplate {
	if fresh, err := s.templateRepo.Get(ctx, t.ID); err == nil {
		return fresh
	}
	return t
}
