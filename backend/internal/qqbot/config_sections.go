package qqbot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrConfigConflict = errors.New("此模块已被其他页面更新，请重新加载后再保存；当前草稿未丢弃")

// A module owns exactly these keys. Never merge arbitrary client-supplied fields.
var configSections = map[string][]string{
	"connection":   {"enabled", "app_id", "app_secret", "sandbox", "clear_app_secret"},
	"groups":       {"allowed_groups", "group_policies"},
	"chat":         {"ai_connection", "site_api_key_id", "site_group_id", "site_owner_id", "chat_disabled", "base_url", "api_key", "chat_model", "chat_protocol", "system_prompt", "max_output_tokens", "history_messages", "clear_api_key"},
	"knowledge":    {"site_knowledge"},
	"images":       {"image_ai_connection", "image_site_api_key_id", "image_site_group_id", "image_site_owner_id", "image_enabled", "image_base_url", "image_api_key", "image_model", "image_size", "image_output_format", "clear_image_api_key"},
	"group-admins": {"group_admins"}, "moderation": {"moderation"}, "autonomous": {"autonomous"},
	"commands": {"custom_commands"}, "notifications": {"price_notifications", "health_notifications", "notification_quiet_hours"},
	"limits": {"daily_chat_limit", "daily_image_limit", "total_daily_chat_limit", "total_daily_image_limit", "cooldown_seconds", "reward_alert_daily", "reward_budget_daily"},
}

func sectionValues(c Config, section string) map[string]json.RawMessage {
	raw, _ := json.Marshal(c)
	var all map[string]json.RawMessage
	_ = json.Unmarshal(raw, &all)
	out := map[string]json.RawMessage{}
	for _, key := range configSections[section] {
		if value, ok := all[key]; ok {
			out[key] = value
		}
	}
	return out
}
func sectionVersion(c Config, section string) string {
	// The application identity is a dependency for every module.
	raw, _ := json.Marshal([]any{c.AppID, sectionValues(c, section)})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func configVersions(c Config) map[string]string {
	c.SectionVersions = nil
	out := map[string]string{}
	for section := range configSections {
		out[section] = sectionVersion(c, section)
	}
	return out
}
func mergeConfigSection(old, draft Config, section string) (Config, error) {
	if _, ok := configSections[section]; !ok {
		return old, errors.New("未知配置模块")
	}
	raw, err := json.Marshal(old)
	if err != nil {
		return old, err
	}
	var values map[string]json.RawMessage
	if err = json.Unmarshal(raw, &values); err != nil {
		return old, err
	}
	for _, key := range configSections[section] {
		delete(values, key)
	}
	for key, value := range sectionValues(draft, section) {
		values[key] = value
	}
	raw, err = json.Marshal(values)
	if err != nil {
		return old, err
	}
	var out Config
	err = json.Unmarshal(raw, &out)
	return out, err
}
func (s *Service) SaveConfigSection(ctx context.Context, section, version string, draft Config, preserveMissingBudget ...bool) (Config, error) {
	return s.saveConfigChecked(ctx, func(old Config) (Config, error) {
		if _, ok := configSections[section]; !ok {
			return old, errors.New("未知配置模块")
		}
		if version == "" || version != sectionVersion(old, section) {
			return old, ErrConfigConflict
		}
		if section == "limits" && len(preserveMissingBudget) > 0 && preserveMissingBudget[0] {
			draft.RewardBudgetDaily = old.RewardBudgetDaily
		}
		// Older module clients do not know the managed connection metadata.
		if section == "chat" && draft.AIConnection == "" && draft.SiteAPIKeyID == 0 && draft.SiteOwnerID == 0 && draft.SiteGroupID == 0 {
			draft.AIConnection, draft.SiteAPIKeyID, draft.SiteOwnerID, draft.SiteGroupID = old.AIConnection, old.SiteAPIKeyID, old.SiteOwnerID, old.SiteGroupID
		}
		if section == "images" && imageConnectionMetadataOmitted(draft) {
			preserveImageConnectionMetadata(&draft, old)
		}
		return mergeConfigSection(old, draft, section)
	}, section)
}

type ConfigChange struct {
	At      time.Time                  `json:"at"`
	Section string                     `json:"section"`
	Before  map[string]json.RawMessage `json:"before"`
	After   map[string]json.RawMessage `json:"after"`
}

const configHistoryKey = "qq_bot_config_history"

func publicSection(c Config, section string) map[string]json.RawMessage {
	out := sectionValues(c.Public(), section)
	for _, key := range []string{"app_secret", "api_key", "image_api_key", "clear_app_secret", "clear_api_key", "clear_image_api_key", "site_api_key_id", "image_site_api_key_id"} {
		delete(out, key)
	}
	return out
}
func (s *Service) ConfigHistory(ctx context.Context) ([]ConfigChange, error) {
	out := []ConfigChange{}
	_, err := readActivityConfig(ctx, s.db, configHistoryKey, &out)
	return out, err
}

// Group policy is an additional restriction. Nil quotas inherit the global cap;
// zero prohibits calls. Missing policies preserve existing installations.
type GroupPolicy struct {
	GroupID              string `json:"group_id"`
	Label                string `json:"label"`
	DisableAutonomous    bool   `json:"disable_autonomous"`
	DisableModeration    bool   `json:"disable_moderation"`
	DisableChat          bool   `json:"disable_chat"`
	DisableImages        bool   `json:"disable_images"`
	DisableActivities    bool   `json:"disable_activities"`
	DisableNotifications bool   `json:"disable_notifications"`
	ChatDailyLimit       *int   `json:"chat_daily_limit"`
	ImageDailyLimit      *int   `json:"image_daily_limit"`
}

func (c Config) groupPolicy(group string) GroupPolicy {
	for _, p := range c.GroupPolicies {
		if p.GroupID == group {
			return p
		}
	}
	return GroupPolicy{GroupID: group}
}
func (c Config) groupLimit(group, kind string) int {
	p := c.groupPolicy(group)
	if kind == "image" {
		if p.DisableImages {
			return 0
		}
		if p.ImageDailyLimit != nil {
			return *p.ImageDailyLimit
		}
		return c.TotalDailyImageLimit
	}
	if c.ChatDisabled || p.DisableChat {
		return 0
	}
	if p.ChatDailyLimit != nil {
		return *p.ChatDailyLimit
	}
	return c.TotalDailyChatLimit
}
func validateGroupPolicies(c Config) error {
	seen := map[string]bool{}
	if len(c.GroupPolicies) > 100 {
		return errors.New("群策略最多 100 条")
	}
	for _, p := range c.GroupPolicies {
		if !c.allows(p.GroupID) || seen[p.GroupID] || len([]rune(p.Label)) > 60 {
			return errors.New("群策略须对应唯一的允许群，备注最多 60 字")
		}
		seen[p.GroupID] = true
		for _, n := range []*int{p.ChatDailyLimit, p.ImageDailyLimit} {
			if n != nil && (*n < 0 || *n > 100000) {
				return fmt.Errorf("群每日额度必须在 0 到 100000 之间")
			}
		}
	}
	return nil
}

// Quiet periods suppress notifications instead of replaying stale alerts later.
type QuietHours struct {
	Enabled   bool `json:"enabled"`
	StartHour int  `json:"start_hour"`
	EndHour   int  `json:"end_hour"`
}

func (q QuietHours) active(at time.Time) bool {
	if !q.Enabled {
		return false
	}
	hour := at.In(time.FixedZone("Asia/Shanghai", 8*3600)).Hour()
	if q.StartHour < q.EndHour {
		return hour >= q.StartHour && hour < q.EndHour
	}
	return hour >= q.StartHour || hour < q.EndHour
}
