package service

import (
	"context"
	"strconv"
	"strings"
	"time"
)

type AccountOpsNotificationSettings struct {
	Enabled         *bool   `json:"enabled"`
	Recipient       *string `json:"recipient"`
	EmailName       *string `json:"email_name"`
	BalanceLow      *bool   `json:"balance_low"`
	WeeklyQuota     *bool   `json:"weekly_quota"`
	CooldownMinutes *int    `json:"cooldown_minutes"`
}
type AccountOpsRuleUpdate struct {
	Metric           string   `json:"metric"`
	Enabled          bool     `json:"enabled"`
	EnabledOnly      bool     `json:"enabled_only,omitempty"`
	Threshold        *float64 `json:"threshold"`
	Unit             string   `json:"unit"`
	ThresholdPercent *float64 `json:"threshold_percent"`
	Window           string   `json:"window"`
	NotifyAlert      *bool    `json:"notify_alert"`
	NotifyRecovery   *bool    `json:"notify_recovery"`
}
type AccountOpsRuleGroup struct {
	AccountIDs []int64              `json:"account_ids"`
	Rule       AccountOpsRuleUpdate `json:"rule"`
}

func (s *AccountOpsService) SaveGroups(ctx context.Context, groups []AccountOpsGroup) (AccountOpsConfig, error) {
	normalized := make([]AccountOpsGroup, len(groups))
	for i, group := range groups {
		normalized[i] = group
		if normalized[i].ID == "" || legacyAccountOpsGroupID(normalized[i].ID) {
			normalized[i].ID = manualAccountOpsGroupID(group.ID + ":" + strconv.Itoa(i) + ":" + group.Name)
		}
		normalized[i].Name = strings.TrimSpace(group.Name)
		normalized[i].Provider = strings.TrimSpace(group.Provider)
		normalized[i].Site = canonicalAccountOpsSite(group.Site)
		normalized[i].AccountIDs = append([]int64(nil), group.AccountIDs...)
	}
	if err := validateAccountOpsGroups(normalized); err != nil {
		return AccountOpsConfig{}, accountOpsConfigValidation(err.Error())
	}
	return s.updateConfig(ctx, func(c *AccountOpsConfig) error {
		c.Groups = normalized
		c.GroupsInitialized = true
		if s.accounts != nil {
			accounts, err := s.accounts.ListAllWithFilters(ctx, "", "", "", "", 0, "")
			if err != nil {
				return err
			}
			byID := make(map[int64]*Account, len(accounts))
			for i := range accounts {
				byID[accounts[i].ID] = &accounts[i]
			}
			for i := range c.Groups {
				provider, site := accountOpsGroupSite(c.Groups[i], byID)
				if c.Groups[i].Provider == "" {
					c.Groups[i].Provider = provider
				}
				if c.Groups[i].Site == "" {
					c.Groups[i].Site = site
				}
			}
			members := make(map[int64]bool)
			for _, group := range c.Groups {
				for _, id := range group.AccountIDs {
					members[id] = true
				}
			}
			ungrouped := make([]int64, 0)
			for _, account := range accounts {
				if !members[account.ID] {
					ungrouped = append(ungrouped, account.ID)
				}
			}
			c.UngroupedAccountIDs = ungrouped
			normalizeAccountOpsGroupRules(c, byID)
		}
		return nil
	})
}

type opsConfigResultKey struct{}
type opsConfigResult struct {
	cfg AccountOpsConfig
	set bool
}
type accountOpsConfigLocker interface {
	WithAccountOpsConfigLock(context.Context, func(context.Context) error) error
}

func (s *AccountOpsService) updateConfig(ctx context.Context, mutate func(*AccountOpsConfig) error) (AccountOpsConfig, error) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	var out AccountOpsConfig
	result := &opsConfigResult{}
	ctx = context.WithValue(ctx, opsConfigResultKey{}, result)
	update := func(lockedCtx context.Context) error {
		ctx = lockedCtx
		c, err := s.loadConfig(ctx)
		if err != nil {
			return err
		}
		if err = mutate(&c); err != nil {
			return err
		}
		if err = s.saveConfigWithRuleScope(ctx, c, false); err != nil {
			return err
		}
		return nil
	}
	var err error
	if lock, ok := s.repo.(accountOpsConfigLocker); ok {
		err = lock.WithAccountOpsConfigLock(ctx, update)
	} else {
		err = update(ctx)
	}
	if err == nil && result.set {
		s.config.Store(result.cfg)
		out = result.cfg.public()
	}
	return out, err
}
func (s *AccountOpsService) SaveNotificationSettings(ctx context.Context, v AccountOpsNotificationSettings) (AccountOpsConfig, error) {
	return s.updateConfig(ctx, func(c *AccountOpsConfig) error {
		if v.Enabled != nil {
			c.Enabled = *v.Enabled
		}
		if v.Recipient != nil {
			c.Recipient = *v.Recipient
		}
		if v.EmailName != nil {
			c.EmailName = *v.EmailName
		}
		if v.BalanceLow != nil {
			c.BalanceLow = *v.BalanceLow
		}
		if v.WeeklyQuota != nil {
			c.WeeklyQuota = *v.WeeklyQuota
		}
		if v.CooldownMinutes != nil {
			c.CooldownMinutes = *v.CooldownMinutes
		}
		return nil
	})
}
func (s *AccountOpsService) SaveWebhook(ctx context.Context, id string, v AccountOpsWebhook) (AccountOpsConfig, error) {
	if !opsID.MatchString(id) {
		return AccountOpsConfig{}, accountOpsConfigValidation("invalid robot ID")
	}
	return s.updateConfig(ctx, func(c *AccountOpsConfig) error {
		v.ID = id
		for i, w := range c.Webhooks {
			if w.ID == id {
				c.Webhooks[i] = v
				return nil
			}
		}
		c.Webhooks = append(c.Webhooks, v)
		return nil
	})
}
func (s *AccountOpsService) DeleteWebhook(ctx context.Context, id string) (AccountOpsConfig, error) {
	if !opsID.MatchString(id) {
		return AccountOpsConfig{}, accountOpsConfigValidation("invalid robot ID")
	}
	return s.updateConfig(ctx, func(c *AccountOpsConfig) error {
		for i, w := range c.Webhooks {
			if w.ID == id {
				c.Webhooks = append(c.Webhooks[:i], c.Webhooks[i+1:]...)
				break
			}
		}
		return nil
	})
}
func (s *AccountOpsService) SaveRule(ctx context.Context, id int64, v AccountOpsRuleUpdate) (AccountOpsConfig, error) {
	if id <= 0 || (v.Metric != "balance" && v.Metric != "quota") {
		return AccountOpsConfig{}, accountOpsConfigValidation("invalid threshold rule")
	}
	return s.updateConfig(ctx, func(c *AccountOpsConfig) error {
		return s.applyRule(ctx, c, id, v, false)
	})
}

// SaveRulesBatch preserves the single-group API for existing callers.
func (s *AccountOpsService) SaveRulesBatch(ctx context.Context, ids []int64, v AccountOpsRuleUpdate) (AccountOpsConfig, error) {
	return s.SaveRuleGroupsBatch(ctx, []AccountOpsRuleGroup{{AccountIDs: ids, Rule: v}})
}

// SaveRuleGroupsBatch applies all groups and invalidates their outbox records in
// one transaction. A failed group cannot save changes from an earlier group.
func (s *AccountOpsService) SaveRuleGroupsBatch(ctx context.Context, groups []AccountOpsRuleGroup) (AccountOpsConfig, error) {
	if len(groups) == 0 || len(groups) > 2 {
		return AccountOpsConfig{}, accountOpsConfigValidation("select between 1 and 2 rule groups")
	}
	total := 0
	for _, group := range groups {
		total += len(group.AccountIDs)
		if len(group.AccountIDs) == 0 || total > 1000 {
			return AccountOpsConfig{}, accountOpsConfigValidation("select between 1 and 1000 accounts across non-empty groups")
		}
	}
	seen := make(map[int64]bool, total)
	for _, group := range groups {
		for _, id := range group.AccountIDs {
			if id <= 0 || seen[id] {
				return AccountOpsConfig{}, accountOpsConfigValidation("account IDs must be positive and unique across groups")
			}
			seen[id] = true
		}
		v := group.Rule
		if v.EnabledOnly {
			if v.Threshold != nil || v.Unit != "" || v.ThresholdPercent != nil || v.Window != "" || v.NotifyAlert != nil || v.NotifyRecovery != nil {
				return AccountOpsConfig{}, accountOpsConfigValidation("enabled-only batch rules cannot include threshold fields")
			}
			switch v.Metric {
			case "balance", "quota":
			default:
				return AccountOpsConfig{}, accountOpsConfigValidation("invalid threshold rule")
			}
			continue
		}
		if v.NotifyAlert == nil || v.NotifyRecovery == nil {
			return AccountOpsConfig{}, accountOpsConfigValidation("batch notification flags are required")
		}
		switch v.Metric {
		case "balance":
			if v.Threshold == nil || v.Unit == "" {
				return AccountOpsConfig{}, accountOpsConfigValidation("batch threshold and unit are required")
			}
		case "quota":
			if v.ThresholdPercent == nil || v.Window == "" {
				return AccountOpsConfig{}, accountOpsConfigValidation("batch threshold_percent and window are required")
			}
		default:
			return AccountOpsConfig{}, accountOpsConfigValidation("invalid threshold rule")
		}
	}
	return s.updateConfig(ctx, func(c *AccountOpsConfig) error {
		for _, group := range groups {
			for _, id := range group.AccountIDs {
				if err := s.applyRule(ctx, c, id, group.Rule, true); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *AccountOpsService) applyRule(ctx context.Context, c *AccountOpsConfig, id int64, v AccountOpsRuleUpdate, requireCurrentUnit bool) error {
	if v.EnabledOnly {
		switch v.Metric {
		case "balance":
			for i := range c.BalanceThresholds {
				if c.BalanceThresholds[i].AccountID == id {
					if _, err := s.balanceRuleAccount(ctx, id); err != nil {
						return err
					}
					c.BalanceThresholds[i].Enabled = v.Enabled
					return nil
				}
			}
			return accountOpsConfigValidation("balance rule is required for enabled-only updates")
		case "quota":
			for i := range c.QuotaThresholds {
				if c.QuotaThresholds[i].AccountID == id {
					if err := s.validateQuotaRules(ctx, []AccountOpsQuotaRule{c.QuotaThresholds[i]}); err != nil {
						return err
					}
					c.QuotaThresholds[i].Enabled = v.Enabled
					return nil
				}
			}
			return accountOpsConfigValidation("quota rule is required for enabled-only updates")
		default:
			return accountOpsConfigValidation("invalid threshold rule")
		}
	}
	if v.Metric == "balance" {
		r := AccountOpsBalanceRule{AccountID: id, Enabled: v.Enabled, NotifyAlert: v.NotifyAlert, NotifyRecovery: v.NotifyRecovery}
		index := -1
		for i, old := range c.BalanceThresholds {
			if old.AccountID == id {
				r = old
				index = i
				break
			}
		}
		if v.Threshold != nil {
			r.Threshold = *v.Threshold
		} else if index < 0 {
			return accountOpsConfigValidation("threshold is required")
		}
		if v.Unit != "" {
			r.Unit = v.Unit
		} else if index < 0 {
			return accountOpsConfigValidation("unit is required")
		}
		r.Enabled = v.Enabled
		if v.NotifyAlert != nil {
			r.NotifyAlert = v.NotifyAlert
		}
		if v.NotifyRecovery != nil {
			r.NotifyRecovery = v.NotifyRecovery
		}
		account, err := s.balanceRuleAccount(ctx, id)
		if err != nil {
			return err
		}
		if requireCurrentUnit && opsBalanceAccount(account, time.Now()).Unit != r.Unit {
			return accountOpsConfigValidation("batch balance rules require the same current account unit")
		}
		if index < 0 {
			c.BalanceThresholds = append(c.BalanceThresholds, r)
		} else {
			c.BalanceThresholds[index] = r
		}
	} else {
		r := AccountOpsQuotaRule{AccountID: id, Enabled: v.Enabled, NotifyAlert: v.NotifyAlert, NotifyRecovery: v.NotifyRecovery}
		index := -1
		for i, old := range c.QuotaThresholds {
			if old.AccountID == id {
				r = old
				index = i
				break
			}
		}
		if v.ThresholdPercent != nil {
			r.ThresholdPercent = *v.ThresholdPercent
		} else if index < 0 {
			return accountOpsConfigValidation("threshold_percent is required")
		}
		if v.Window != "" {
			r.Window = v.Window
		}
		r.Enabled = v.Enabled
		if v.NotifyAlert != nil {
			r.NotifyAlert = v.NotifyAlert
		}
		if v.NotifyRecovery != nil {
			r.NotifyRecovery = v.NotifyRecovery
		}
		if err := s.validateQuotaRules(ctx, []AccountOpsQuotaRule{r}); err != nil {
			return err
		}
		if index < 0 {
			c.QuotaThresholds = append(c.QuotaThresholds, r)
		} else {
			c.QuotaThresholds[index] = r
		}
	}
	return nil
}
func (s *AccountOpsService) DeleteRule(ctx context.Context, id int64, metric string) (AccountOpsConfig, error) {
	if id <= 0 || (metric != "balance" && metric != "quota") {
		return AccountOpsConfig{}, accountOpsConfigValidation("invalid threshold rule")
	}
	return s.updateConfig(ctx, func(c *AccountOpsConfig) error {
		if metric == "balance" {
			for i, r := range c.BalanceThresholds {
				if r.AccountID == id {
					c.BalanceThresholds = append(c.BalanceThresholds[:i], c.BalanceThresholds[i+1:]...)
					break
				}
			}
		} else {
			for i, r := range c.QuotaThresholds {
				if r.AccountID == id {
					c.QuotaThresholds = append(c.QuotaThresholds[:i], c.QuotaThresholds[i+1:]...)
					break
				}
			}
		}
		return nil
	})
}
