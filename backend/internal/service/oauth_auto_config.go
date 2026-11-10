package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

const SettingKeyOAuthAutoConfig = "smart_ops_oauth_auto_config"
const AutoConfigConcurrencyExtraKey = "auto_config_concurrency"

// Initial account fields, BPS defaults, model billing and concurrency upgrades are independent.
type OAuthAutoConfig struct {
	OpenAIAPIKeyFallback                           OpenAIAPIKeyFallbackConfig `json:"openai_apikey_fallback"`
	DegradationAction                              OAuthDegradationConfig     `json:"degradation_action"`
	OpenAITwoFAAdmission                           OpenAITwoFAAdmissionConfig `json:"openai_two_fa_admission"`
	OpenAITOTPRotateOn429                          bool                       `json:"openai_totp_rotate_on_429"`
	OpenAIDisableSchedulingOn429                   bool                       `json:"openai_disable_scheduling_on_429"`
	OpenAIDisableSchedulingOn4297dThresholdPercent float64                    `json:"openai_disable_scheduling_on_429_7d_threshold_percent"`
	OpenAIWSMode                                   *string                    `json:"openai_ws_mode,omitempty"`
	OpenAIWSSSEAcceleration                        *bool                      `json:"openai_ws_sse_acceleration,omitempty"`
	QualityRule                                    *OAuthInitialQualityRule   `json:"quality_rule,omitempty"`
	ModelMappings                                  []OAuthModelMappingRule    `json:"model_mappings"`
	SelectedModels                                 []string                   `json:"selected_models"`
	ModelBilling                                   ModelBillingConfig         `json:"model_billing"`
	ExcelBPS                                       ExcelBPSDefaults           `json:"excel_bps"`
	UpdatedAt                                      time.Time                  `json:"updated_at"`
	Enabled                                        bool                       `json:"enabled"`
	Platform                                       string                     `json:"platform"`
	Priority                                       int                        `json:"priority"`
	LoadFactor                                     int                        `json:"load_factor"`
	Concurrency                                    int                        `json:"concurrency"`
	GroupIDs                                       []int64                    `json:"group_ids"`
	UpgradeEnabled                                 bool                       `json:"upgrade_enabled"`
	UpgradeGroupIDs                                []int64                    `json:"upgrade_group_ids"`
	SuccessesPerStep                               int                        `json:"successes_per_step"`
	UpgradeStep                                    int                        `json:"upgrade_step"`
	MaxConcurrency                                 int                        `json:"max_concurrency"`
	CooldownSeconds                                int                        `json:"cooldown_seconds"`
	CostMultiplier                                 float64                    `json:"cost_multiplier"`
	Revision                                       string                     `json:"revision"`
}

func (c *OAuthAutoConfig) UnmarshalJSON(data []byte) error {
	type configJSON OAuthAutoConfig
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, exists := fields[OpenAIDisableSchedulingOn4297dThresholdExtraKey]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("7d scheduling threshold must be a number")
	}
	return json.Unmarshal(data, (*configJSON)(c))
}

func DefaultOAuthAutoConfig() OAuthAutoConfig {
	wsMode, wsSSEAcceleration := OpenAIWSIngressModeCtxPool, false
	return OAuthAutoConfig{OpenAIAPIKeyFallback: DefaultOpenAIAPIKeyFallbackConfig(), OpenAIDisableSchedulingOn4297dThresholdPercent: DefaultOpenAIDisableSchedulingOn4297dThresholdPercent, DegradationAction: DefaultOAuthDegradationConfig(), OpenAIWSMode: &wsMode, OpenAIWSSSEAcceleration: &wsSSEAcceleration, OpenAITwoFAAdmission: DefaultOpenAITwoFAAdmissionConfig(), ModelMappings: defaultOAuthModelMappings(PlatformOpenAI), ModelBilling: DefaultModelBillingConfig(), ExcelBPS: DefaultExcelBPSDefaults(), Platform: PlatformOpenAI, Priority: 50, LoadFactor: 1, Concurrency: 3, CostMultiplier: 0.07, GroupIDs: []int64{}, UpgradeGroupIDs: []int64{}, SuccessesPerStep: 20, UpgradeStep: 1, MaxConcurrency: 100, CooldownSeconds: 60}
}
func ValidateOAuthAutoConfig(c OAuthAutoConfig) error {
	if err := c.OpenAIAPIKeyFallback.Validate(); err != nil {
		return err
	}
	if threshold := c.OpenAIDisableSchedulingOn4297dThresholdPercent; math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 || threshold > 100 {
		return infraerrors.BadRequest("AUTO_CONFIG_INVALID", "7d scheduling threshold must be a finite number between 0 and 100")
	}
	if err := c.DegradationAction.Validate(); err != nil {
		return err
	}
	if err := validateOAuthInitialWSMode(c.OpenAIWSMode); err != nil {
		return err
	}
	if err := c.OpenAITwoFAAdmission.Validate(); err != nil {
		return infraerrors.BadRequest("AUTO_CONFIG_ADMISSION_INVALID", err.Error())
	}
	if _, err := c.QualityRule.plan(c.Platform); err != nil {
		return err
	}
	if err := validateOAuthModelMappings(c.ModelMappings); err != nil {
		return err
	}
	if err := validateOAuthSelectedModels(c.SelectedModels); err != nil {
		return err
	}
	if err := validateModelBillingConfig(c.ModelBilling); err != nil {
		return err
	}
	if err := validateExcelBPSDefaults(c.ExcelBPS); err != nil {
		return err
	}
	bad := func(s string) error { return infraerrors.BadRequest("AUTO_CONFIG_INVALID", s) }
	if !slices.Contains([]string{PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformAntigravity, PlatformGrok}, c.Platform) {
		return bad("unsupported OAuth platform")
	}
	if c.Priority < 0 || c.Priority > 10000 || c.LoadFactor < 1 || c.LoadFactor > 10000 || c.Concurrency < 1 || c.Concurrency > 10000 {
		return bad("priority must be 0–10000; concurrency and load factor must be 1–10000")
	}
	if _, ok := accountCostMultiplierNumber(c.CostMultiplier); !ok {
		return bad("cost multiplier must be a finite number between 0 and 1000000")
	}
	if c.SuccessesPerStep < 1 || c.SuccessesPerStep > 100000 || c.UpgradeStep < 1 || c.UpgradeStep > 1000 || c.MaxConcurrency < 1 || c.MaxConcurrency > 10000 || c.CooldownSeconds < 1 || c.CooldownSeconds > 86400 {
		return bad("invalid concurrency upgrade settings")
	}
	if c.Enabled && len(c.GroupIDs) == 0 {
		return bad("select initial account groups")
	}
	if c.UpgradeEnabled && len(c.UpgradeGroupIDs) == 0 {
		return bad("select concurrency upgrade groups")
	}
	for _, ids := range [][]int64{c.GroupIDs, c.UpgradeGroupIDs} {
		if len(ids) > 100 {
			return bad("at most 100 groups allowed")
		}
		seen := map[int64]bool{}
		for _, id := range ids {
			if id <= 0 || seen[id] {
				return bad("group IDs must be positive and unique")
			}
			seen[id] = true
		}
	}
	return nil
}
func GetOAuthAutoConfig(ctx context.Context, repo SettingRepository) (OAuthAutoConfig, error) {
	c := DefaultOAuthAutoConfig()
	if repo == nil {
		return c, nil
	}
	raw, err := repo.GetValue(ctx, SettingKeyOAuthAutoConfig)
	if errors.Is(err, ErrSettingNotFound) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if raw != "" {
		// Legacy settings keep their routing until mappings are explicitly saved.
		c.ModelMappings = nil
		// Absent WS fields leave imported transport settings unchanged until saved.
		c.OpenAIWSMode, c.OpenAIWSSSEAcceleration = nil, nil
		if err = json.Unmarshal([]byte(raw), &c); err != nil {
			return c, err
		}
		if c.CostMultiplier == 0 {
			var stored struct {
				CostMultiplier *float64 `json:"cost_multiplier"`
			}
			if err := json.Unmarshal([]byte(raw), &stored); err != nil {
				return c, err
			}
			if stored.CostMultiplier == nil {
				c.CostMultiplier = DefaultOAuthAutoConfig().CostMultiplier
			}
		}
	}
	return c, ValidateOAuthAutoConfig(c)
}
func (s *SettingService) GetOAuthAutoConfig(ctx context.Context) (OAuthAutoConfig, error) {
	if s == nil {
		return DefaultOAuthAutoConfig(), nil
	}
	return GetOAuthAutoConfig(ctx, s.settingRepo)
}
func (s *adminServiceImpl) ApplyOAuthAutoConfig(ctx context.Context, input *CreateAccountInput) error {
	if input == nil {
		return ErrAccountNilInput
	}
	input.InitialQualityPlan = nil
	input.InitialAdmissionPlan = nil
	input.Credentials = maps.Clone(input.Credentials)
	delete(input.Credentials, OAuthInitialModelSelectionKey)
	// Imported/user-supplied markers must not fabricate automatic-configuration history.
	input.Extra = maps.Clone(input.Extra)
	delete(input.Extra, "auto_config_initial_revision")
	delete(input.Extra, OpenAITwoFAAdmissionExtraKey)
	delete(input.Extra, OAuthDegradationActionExtraKey)
	// Global initialization is an administrator policy. Observers explicitly
	// choose their own groups and must not inherit global groups or field overrides.
	if _, observer := ObserverGroupIDs(ctx); observer {
		return ValidateObserverGroupBindings(ctx, input.GroupIDs)
	}
	if input.Type != AccountTypeOAuth || s.settingService == nil {
		return nil
	}
	c, err := s.settingService.GetOAuthAutoConfig(ctx)
	if err != nil {
		return fmt.Errorf("read automatic configuration: %w", err)
	}
	if !c.Enabled || c.Platform != input.Platform {
		return nil
	}
	if err := ValidateObserverGroupBindings(ctx, c.GroupIDs); err != nil {
		return err
	}
	// Validate at use time as a configured group may have been deleted or moved.
	for _, id := range c.GroupIDs {
		g, err := s.groupRepo.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if g.Platform != c.Platform || g.Status != StatusActive {
			return infraerrors.BadRequest("AUTO_CONFIG_GROUP_INVALID", "automatic configuration group is unavailable for this platform")
		}
	}
	plan, err := c.QualityRule.plan(c.Platform)
	if err != nil {
		return err
	}
	input.InitialQualityPlan = plan
	applyOAuthInitialModels(input, c.ModelMappings, c.SelectedModels)
	input.Priority = c.Priority
	lf := c.LoadFactor
	input.LoadFactor = &lf
	input.Concurrency = c.Concurrency
	input.GroupIDs = append([]int64(nil), c.GroupIDs...)
	input.Extra = maps.Clone(input.Extra)
	if input.Extra == nil {
		input.Extra = map[string]any{}
	}
	// No credentials or account identity is stored in the marker.
	input.Extra["auto_config_initial_revision"] = c.Revision
	input.Extra[AccountCostMultiplierExtraKey] = c.CostMultiplier
	if input.Platform == PlatformOpenAI {
		input.Extra[OpenAITOTPRotateOn429ExtraKey] = c.OpenAITOTPRotateOn429
		input.Extra[OpenAIDisableSchedulingOn429ExtraKey] = c.OpenAIDisableSchedulingOn429
		input.Extra[OpenAIDisableSchedulingOn4297dThresholdExtraKey] = c.OpenAIDisableSchedulingOn4297dThresholdPercent
		applyOAuthInitialWS(input.Extra, c)
	}
	return nil
}
func (s *AccountOpsService) GetOAuthAutoConfig(ctx context.Context) (OAuthAutoConfig, error) {
	return GetOAuthAutoConfig(ctx, s.settings)
}
func (s *AccountOpsService) SaveOAuthAutoConfig(ctx context.Context, c OAuthAutoConfig) (OAuthAutoConfig, error) {
	s.autoConfigMu.Lock()
	defer s.autoConfigMu.Unlock()
	c.ModelBilling = normalizeModelBillingConfig(c.ModelBilling)
	if c.ModelMappings == nil {
		c.ModelMappings = []OAuthModelMappingRule{}
	}
	if err := ValidateOAuthAutoConfig(c); err != nil {
		return c, err
	}
	if err := s.validateOpenAIAPIKeyFallbackAccounts(ctx, c.OpenAIAPIKeyFallback); err != nil {
		return c, err
	}
	if c.QualityRule != nil {
		// Keep only the normalized configuration, never source runtime metadata.
		plan, err := c.QualityRule.plan(c.Platform)
		if err != nil {
			return c, err
		}
		c.QualityRule = &OAuthInitialQualityRule{ModelID: plan.ModelID, CronExpression: plan.CronExpression,
			Enabled: plan.Enabled, MaxResults: plan.MaxResults, PelicanConfig: plan.PelicanConfig}
	}
	if s.autoGroups == nil {
		return c, errors.New("group repository unavailable")
	}
	// Existing scopes may reference a group deleted/disabled since saving. Keep
	// those stable IDs; only newly selected IDs must resolve to active groups.
	if err := s.validateModelBillingGroupSelections(ctx, c.ModelBilling); err != nil {
		return c, err
	}
	if err := s.validateDegradationGroupSelections(ctx, c.DegradationAction); err != nil {
		return c, err
	}

	if c.Enabled {
		for _, id := range c.GroupIDs {
			g, err := s.autoGroups.GetByID(ctx, id)
			if err != nil {
				return c, err
			}
			if g.Status != StatusActive || g.Platform != c.Platform {
				return c, infraerrors.BadRequest("AUTO_CONFIG_GROUP_INVALID", "select active groups for the selected platform")
			}
		}
	}
	if c.UpgradeEnabled {
		for _, id := range c.UpgradeGroupIDs {
			g, err := s.autoGroups.GetByID(ctx, id)
			if err != nil {
				return c, err
			}
			if g.Status != StatusActive {
				return c, infraerrors.BadRequest("AUTO_CONFIG_GROUP_INVALID", "select active upgrade groups")
			}
		}
	}
	if c.OpenAITwoFAAdmission.Enabled {
		g, err := s.autoGroups.GetByID(ctx, c.OpenAITwoFAAdmission.IsolationGroupID)
		if err != nil {
			return c, err
		}
		if g.Status != StatusActive || g.Platform != PlatformOpenAI {
			return c, infraerrors.BadRequest("AUTO_CONFIG_ADMISSION_GROUP_INVALID", "select an active OpenAI isolation group")
		}
	}
	if c.ExcelBPS.AutoMoveOn403 {
		validator := &adminServiceImpl{groupRepo: s.autoGroups}
		if err := validator.validateExcelBPS403GroupSettings(ctx, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: c.ExcelBPS.extra()}); err != nil {
			return c, err
		}
	}
	expectedRevision := c.Revision
	c.Revision = uuid.NewString()
	c.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	if cas, ok := s.settings.(interface {
		CompareAndSetOAuthAutoConfig(context.Context, string, string) error
	}); ok {
		err = cas.CompareAndSetOAuthAutoConfig(ctx, string(raw), expectedRevision)
	} else {
		current, readErr := GetOAuthAutoConfig(ctx, s.settings)
		if readErr != nil {
			return c, readErr
		}
		if current.Revision != expectedRevision {
			return c, infraerrors.Conflict("AUTO_CONFIG_CONFLICT", "automatic configuration changed; reload before saving")
		}
		err = s.settings.Set(ctx, SettingKeyOAuthAutoConfig, string(raw))
	}
	if err != nil {
		return c, err
	}
	s.autoConfig.Store(c)
	s.autoBlocked.Store(false)
	return c, nil
}

func (s *AccountOpsService) validateDegradationGroupSelections(ctx context.Context, cfg OAuthDegradationConfig) error {
	if !cfg.LimitGroups {
		return nil
	}
	current, err := GetOAuthAutoConfig(ctx, s.settings)
	if err != nil {
		return err
	}
	for _, id := range cfg.GroupIDs {
		// Keep saved IDs even if the group has since been disabled or removed.
		// Removing it from this template must not restore an applied restriction.
		if slices.Contains(current.DegradationAction.GroupIDs, id) {
			continue
		}
		group, err := s.autoGroups.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if group == nil || group.Status != StatusActive {
			return infraerrors.BadRequest("OAUTH_DEGRADATION_GROUP_INVALID", "select active groups to remove after degradation")
		}
	}
	return nil
}

func (s *AccountOpsService) validateModelBillingGroupSelections(ctx context.Context, cfg ModelBillingConfig) error {
	scoped := false
	for _, rule := range cfg.Rules {
		scoped = scoped || len(rule.GroupIDs) > 0
	}
	if !scoped {
		return nil
	}
	current, err := GetOAuthAutoConfig(ctx, s.settings)
	if err != nil {
		return err
	}
	previous := make(map[string][]int64, len(current.ModelBilling.Rules))
	for _, rule := range current.ModelBilling.Rules {
		previous[modelBillingRuleKey(rule.Model)] = rule.GroupIDs
	}
	checked := map[int64]bool{}
	for _, rule := range cfg.Rules {
		for _, id := range rule.GroupIDs {
			if slices.Contains(previous[modelBillingRuleKey(rule.Model)], id) || checked[id] {
				continue
			}
			group, err := s.autoGroups.GetByID(ctx, id)
			if err != nil {
				return err
			}
			if group == nil || group.Status != StatusActive {
				return infraerrors.BadRequest("MODEL_BILLING_GROUP_INVALID", "select active model billing groups")
			}
			checked[id] = true
		}
	}
	return nil
}

type AccountConcurrencyResult struct {
	Reset     bool
	AccountID int64
	StartedAt time.Time
	Success   bool
}
type AccountConcurrencyRepository interface {
	RecordConcurrencyResult(context.Context, AccountConcurrencyResult, OAuthAutoConfig) error
}
type AutoConfigConcurrencyState struct {
	DegradationHold bool       `json:"degradation_hold,omitempty"`
	Revision        string     `json:"revision"`
	Concurrency     int        `json:"concurrency"`
	Successes       int        `json:"successes"`
	Required        int        `json:"required"`
	Maximum         int        `json:"maximum"`
	Step            int        `json:"step"`
	PausedUntil     time.Time  `json:"paused_until"`
	LastUpgradeAt   *time.Time `json:"last_upgrade_at,omitempty"`
	LastFailureAt   *time.Time `json:"last_failure_at,omitempty"`
	// RecoveryTarget is a quality-rule-owned ceiling. Native success ramping
	// may advance only up to this captured pre-quarantine value.
	RecoveryTarget int `json:"recovery_target,omitempty"`
}

// AdvanceConcurrency is pure. A fresh cycle starts after a rule/manual change.
func AdvanceConcurrency(state AutoConfigConcurrencyState, current int, c OAuthAutoConfig, result AccountConcurrencyResult, now time.Time) (AutoConfigConcurrencyState, int) {
	if state.DegradationHold {
		return state, current
	}
	if state.Revision != c.Revision || state.Concurrency != current {
		state = AutoConfigConcurrencyState{Revision: c.Revision, Concurrency: current}
	}
	state.Required = c.SuccessesPerStep
	maximum := c.MaxConcurrency
	if state.RecoveryTarget > 0 && state.RecoveryTarget < maximum {
		maximum = state.RecoveryTarget
	}
	state.Maximum = maximum
	state.Step = c.UpgradeStep
	if !result.Success {
		state.Successes = 0
		state.LastFailureAt = &now
		state.PausedUntil = now.Add(time.Duration(c.CooldownSeconds) * time.Second)
		return state, current
	}
	if current <= 0 || current >= maximum || result.StartedAt.Before(state.PausedUntil) {
		return state, current
	}
	state.Successes++
	if state.Successes >= c.SuccessesPerStep {
		current = min(current+c.UpgradeStep, maximum)
		state.Concurrency = current
		state.Successes = 0
		state.LastUpgradeAt = &now
		state.LastFailureAt = nil
		state.PausedUntil = now.Add(time.Duration(c.CooldownSeconds) * time.Second)
	}
	return state, current
}

// Request goroutines never wait for storage. Overflow disables promotion until
// the next explicit save, so dropping a failure cannot lead to unsafe upgrades.
func (s *AccountOpsService) ObserveConcurrencyResult(r AccountConcurrencyResult) {
	if s == nil || r.AccountID <= 0 || s.autoBlocked.Load() {
		return
	}
	c, _ := s.autoConfig.Load().(OAuthAutoConfig)
	if !c.UpgradeEnabled {
		return
	}
	select {
	case s.autoResults <- r:
	default:
		s.autoBlocked.Store(true)
		s.failures.Add(1)
	}
}
func (s *AccountOpsService) refreshAutoConfig(ctx context.Context) {
	s.autoConfigMu.Lock()
	defer s.autoConfigMu.Unlock()
	c, err := s.GetOAuthAutoConfig(ctx)
	if err != nil {
		c = DefaultOAuthAutoConfig()
	}
	previous, _ := s.autoConfig.Load().(OAuthAutoConfig)
	if previous.Revision != c.Revision && err == nil {
		s.autoBlocked.Store(false)
	}
	s.autoConfig.Store(c)
}
func (s *AccountOpsService) processConcurrencyResult(r AccountConcurrencyResult) {
	if s.autoBlocked.Load() || s.autoAccounts == nil {
		return
	}
	c, _ := s.autoConfig.Load().(OAuthAutoConfig)
	if !c.UpgradeEnabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !s.autoSeen[r.AccountID] {
		r.Reset = true
	}
	if err := s.autoAccounts.RecordConcurrencyResult(ctx, r, c); err != nil {
		s.autoBlocked.Store(true)
		s.failures.Add(1)
	} else {
		if len(s.autoSeen) >= 10000 {
			s.autoSeen = make(map[int64]bool)
		}
		s.autoSeen[r.AccountID] = true
	}
}

func (s *OpsService) SetAutoConfigObserver(fn func(AccountConcurrencyResult)) {
	s.autoConfigObserver = fn
}
func (s *OpsService) ObserveConcurrencyResult(r AccountConcurrencyResult) {
	if s != nil && s.autoConfigObserver != nil {
		s.autoConfigObserver(r)
	}
}

func (s *AccountOpsService) AutoConfigBlocked() bool { return s.autoBlocked.Load() }

func (s *AccountOpsService) runAutoConfig(ctx context.Context) {
	defer s.wg.Done()
	s.refreshAutoConfig(ctx)
	s.evaluateOAuthDegradation(ctx)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-s.autoResults:
			s.processConcurrencyResult(r)
		case <-ticker.C:
			s.refreshAutoConfig(ctx)
			s.evaluateOAuthDegradation(ctx)
		}
	}
}
