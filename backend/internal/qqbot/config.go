package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

const configKey = "qq_bot_config"

// Accept saved settings and older clients using turns without copying any old
// per-member conversations into the new shared group history. Explicit zero
// still disables history; the new message count takes precedence when present.
func (c *Config) UnmarshalJSON(data []byte) error {
	type configJSON Config
	decoded := configJSON(*c)
	decoded.Moderation = DefaultModerationConfig()
	decoded.Autonomous = DefaultAutonomousConfig()
	decoded.HealthNotifications = DefaultHealthNotificationConfig()
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	// Spam reporting is retired. Ignore the old switch in saved settings and
	// requests so it cannot collect evidence or authorize queued punishments.
	decoded.Moderation.ReportEnabled = false
	var history struct {
		Messages      *int  `json:"history_messages"`
		Turns         *int  `json:"history_turns"`
		InviteEnabled *bool `json:"invite_enabled"`
	}
	if err := json.Unmarshal(data, &history); err != nil {
		return err
	}
	// Existing installations used the amount and limit as their activity switch.
	// Preserve that behavior until an explicit switch has been saved.
	if history.InviteEnabled == nil {
		decoded.InviteEnabled = decoded.InviteReward > 0 && decoded.InviteDailyLimit > 0
	}
	switch {
	case history.Messages != nil:
		decoded.HistoryMessages = *history.Messages
	case history.Turns != nil:
		if *history.Turns < 0 || *history.Turns > 20 {
			return errors.New("历史轮数超出范围")
		}
		decoded.HistoryMessages = *history.Turns * 2
	default:
		decoded.HistoryMessages = 12
	}
	*c = Config(decoded)
	return nil
}

type Encryptor interface {
	Encrypt(string) (string, error)
	Decrypt(string) (string, error)
}
type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Service) loadConfig(ctx context.Context, db queryRower) (Config, error) {
	c := DefaultConfig()
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, configKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal([]byte(raw), &c); err != nil {
		return c, fmt.Errorf("QQ 配置读取失败")
	}
	for _, p := range []*string{&c.AppSecret, &c.APIKey, &c.ImageAPIKey} {
		if *p != "" {
			*p, err = s.encryptor.Decrypt(*p)
			if err != nil {
				return c, fmt.Errorf("QQ 凭据解密失败，请检查固定加密密钥")
			}
		}
	}
	return c, nil
}
func (s *Service) Config(ctx context.Context) (Config, error) { return s.loadConfig(ctx, s.db) }

// Authorize writes against the current settings, then keep them stable until
// commit. Always take this lock before any binding, reward or usage lock.
func (s *Service) lockConfig(ctx context.Context, tx *sql.Tx) (Config, error) {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared(71427101)`); err != nil {
		return Config{}, err
	}
	return s.loadConfig(ctx, tx)
}

func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("地址必须是完整的 HTTP(S) URL，不能包含密码、查询参数或片段")
	}
	return nil
}
func validateConfig(c Config) error {
	if err := validateOptionalCoupon(c.InviteCouponTemplateID, c.InviteCouponQuantity); err != nil {
		return err
	}
	if c.RewardBudgetDaily != nil && (math.IsNaN(*c.RewardBudgetDaily) || math.IsInf(*c.RewardBudgetDaily, 0) || *c.RewardBudgetDaily < 0 || *c.RewardBudgetDaily > 1000000 || math.Abs(*c.RewardBudgetDaily*1e8-math.Round(*c.RewardBudgetDaily*1e8)) > .001) {
		return errors.New("每日奖励预算须为 0～1000000 USD，最多 8 位小数；0 暂停余额奖励，留空不限制")
	}
	if c.AIConnection != "" && c.AIConnection != "external" && c.AIConnection != "site" {
		return errors.New("AI 连接类型不正确")
	}
	if c.ImageAIConnection != "" && c.ImageAIConnection != "reuse_chat" && c.ImageAIConnection != "external" && c.ImageAIConnection != "site" {
		return errors.New("图片 AI 连接类型不正确")
	}
	if c.ImageAIConnection != "site" && (c.ImageSiteAPIKeyID != 0 || c.ImageSiteGroupID != 0 || c.ImageSiteOwnerID != 0) {
		return errors.New("本站图片 AI 归属信息必须通过本站图片 AI 连接入口管理")
	}
	q := c.NotificationQuietHours
	if q.StartHour < 0 || q.StartHour > 23 || q.EndHour < 0 || q.EndHour > 23 || (q.Enabled && q.StartHour == q.EndHour) {
		return errors.New("通知静默开始与结束小时须在 0～23 之间且不能相同")
	}
	if c.Enabled && c.ImageEnabled {
		base, key := imageConnection(c)
		if base == "" || key == "" {
			return errors.New("启用生图须配置可用的生图地址和密钥（或复用聊天连接）")
		}
	}
	if err := validateGroupPolicies(c); err != nil {
		return err
	}
	if math.IsNaN(c.RewardAlertDaily) || math.IsInf(c.RewardAlertDaily, 0) || c.RewardAlertDaily < 0 || c.RewardAlertDaily > 1000000 {
		return errors.New("每日奖励预警线须在 0 到 1000000 USD 之间")
	}
	if err := validateModeration(c); err != nil {
		return err
	}
	if err := validateHealthNotifications(c); err != nil {
		return err
	}
	if err := validateAutonomous(c); err != nil {
		return err
	}
	if err := validatePriceNotifications(c); err != nil {
		return err
	}
	if err := validateCustomCommands(c.CustomCommands); err != nil {
		return err
	}
	if err := validateGroupAdmins(c); err != nil {
		return err
	}
	if err := validateSiteKnowledge(c.SiteKnowledge); err != nil {
		return err
	}
	if c.ChatProtocol != "responses" && c.ChatProtocol != "chat_completions" {
		return errors.New("聊天接口模式不正确")
	}
	for _, u := range []string{c.BaseURL, c.ImageBaseURL} {
		if u != "" {
			if err := validateURL(u); err != nil {
				return err
			}
		}
	}
	for _, n := range []int{c.DailyChatLimit, c.DailyImageLimit, c.TotalDailyChatLimit, c.TotalDailyImageLimit, c.InviteDailyLimit} {
		if n < 0 || n > 100000 {
			return errors.New("每日次数必须在 0 到 100000 之间；0 表示禁止")
		}
	}
	if c.MaxOutputTokens < 64 || c.MaxOutputTokens > 8192 || c.HistoryMessages < 0 || c.HistoryMessages > 100 || c.CooldownSeconds < 0 || c.CooldownSeconds > 3600 {
		return errors.New("输出长度、历史消息条数（0～100）或冷却时间超出范围")
	}
	for _, n := range []float64{c.CheckinReward, c.InviteReward} {
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1000 || math.Abs(n*1e8-math.Round(n*1e8)) > 0.001 {
			return errors.New("奖励需为 0 到 1000 之间的数值，最多 8 位小数；0 表示关闭")
		}
	}
	if err := validateRechargeRequirement(c.inviteRechargeRequirement()); err != nil {
		return err
	}
	if math.IsNaN(c.TransferFeePercent) || math.IsInf(c.TransferFeePercent, 0) || c.TransferFeePercent < 0 || c.TransferFeePercent > 100 {
		return errors.New("手续费比例须在 0 到 100 之间")
	}
	if math.IsNaN(c.TransferMinAmount) || math.IsInf(c.TransferMinAmount, 0) || c.TransferMinAmount < 0 || c.TransferMinAmount > 1000000 {
		return errors.New("最小转账金额须在 0 到 1000000 之间")
	}
	if math.IsNaN(c.TransferMaxAmount) || math.IsInf(c.TransferMaxAmount, 0) || c.TransferMaxAmount < 0 {
		return errors.New("最大转账金额不能为负数")
	}
	if c.TransferMaxAmount > 0 && c.TransferMaxAmount < c.TransferMinAmount {
		return errors.New("最大转账金额不能小于最小转账金额")
	}
	if len(c.SystemPrompt) > 8000 || len(c.ChatModel) > 200 || len(c.ImageModel) > 200 || len(c.AllowedGroups) > 100 {
		return errors.New("配置内容过长")
	}
	for _, g := range c.AllowedGroups {
		if len(g) < 1 || len(g) > 128 || strings.ContainsAny(g, " \r\n\t") {
			return errors.New("群 OpenID 格式不正确")
		}
	}
	if c.ImageSize != "1024x1024" && c.ImageSize != "1536x1024" && c.ImageSize != "1024x1536" && c.ImageSize != "auto" {
		return errors.New("图片尺寸不正确")
	}
	if !validImageOutputFormat(c.ImageOutputFormat) {
		return errors.New("图片输出格式须为默认、PNG、JPEG、WebP 或 GIF")
	}
	if c.Enabled && (c.AppID == "" || c.AppSecret == "" || len(c.AllowedGroups) == 0) {
		return errors.New("启用前请填写 QQ 凭据和允许使用的群")
	}
	if c.Enabled && !c.ChatDisabled && (c.BaseURL == "" || c.APIKey == "" || c.ChatModel == "") {
		return errors.New("启用 AI 聊天前请填写中转地址、API Key 和聊天模型；也可先关闭 AI 聊天")
	}
	if c.Enabled && c.ImageEnabled && c.ImageModel == "" {
		return errors.New("请填写生图模型")
	}
	if c.ImageBaseURL != "" && c.ImageAPIKey == "" {
		return errors.New("使用独立生图地址时，请填写独立生图 API Key")
	}
	return nil
}

func (s *Service) SaveConfig(ctx context.Context, c Config) (Config, error) {
	return s.saveConfig(ctx, func(Config) Config { return c })
}

// SaveBasicConfig preserves activity settings under the same lock as invitation edits.
func (s *Service) SaveBasicConfig(ctx context.Context, c Config) (Config, error) {
	return s.saveConfigChecked(ctx, func(old Config) (Config, error) {
		if err := checkConfigVersion(ctx, old); err != nil {
			return old, err
		}
		// Knowledge has its own independently versioned module. Legacy clients
		// saving the full basic configuration must not erase or overwrite it.
		c.SiteKnowledge = old.SiteKnowledge
		c.RewardBudgetDaily = old.RewardBudgetDaily
		c.AIConnection, c.SiteAPIKeyID, c.SiteGroupID, c.SiteOwnerID = old.AIConnection, old.SiteAPIKeyID, old.SiteGroupID, old.SiteOwnerID
		preserveImageConnectionMetadata(&c, old)
		c.InviteEnabled = old.InviteEnabled
		c.InviteReward = old.InviteReward
		c.InviteCouponTemplateID = old.InviteCouponTemplateID
		c.InviteCouponQuantity = old.InviteCouponQuantity
		c.InviteDailyLimit = old.InviteDailyLimit
		c.InviteMinRechargeTotal = old.InviteMinRechargeTotal
		c.InviteMinRecharge30d = old.InviteMinRecharge30d
		c.InviteRechargeWindowDays = old.InviteRechargeWindowDays
		c.CheckinReward = old.CheckinReward
		c.TransferEnabled = old.TransferEnabled
		c.TransferFeePercent = old.TransferFeePercent
		c.TransferMinAmount = old.TransferMinAmount
		c.TransferMaxAmount = old.TransferMaxAmount
		return c, nil
	}, "")
}

func (s *Service) saveConfig(ctx context.Context, update func(Config) Config) (Config, error) {
	return s.saveConfigChecked(ctx, func(c Config) (Config, error) { return update(c), nil }, "")
}

func (s *Service) saveConfigChecked(ctx context.Context, update func(Config) (Config, error), section string) (Config, error) {
	return s.saveConfigTransaction(ctx, func(_ *sql.Tx, c Config) (Config, error) { return update(c) }, section, nil)
}
func (s *Service) saveConfigTransaction(ctx context.Context, update func(*sql.Tx, Config) (Config, error), section string, beforeCommit func(*sql.Tx, Config) error) (Config, error) {
	var c Config
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(71427101)`); err != nil {
		return c, err
	}
	old, err := s.loadConfig(ctx, tx)
	if err != nil {
		return c, err
	}
	c, err = update(tx, old)
	if err != nil {
		return c, err
	}
	if imageConnectionMetadataOmitted(c) {
		preserveImageConnectionMetadata(&c, old)
	}
	if !managedSetupAllowed(ctx) && (old.AIConnection != c.AIConnection || old.SiteAPIKeyID != c.SiteAPIKeyID || old.SiteGroupID != c.SiteGroupID || old.SiteOwnerID != c.SiteOwnerID || (old.AIConnection == "site" && (old.ChatProtocol != c.ChatProtocol || old.BaseURL != c.BaseURL || (c.APIKey != "" && c.APIKey != old.APIKey) || c.ClearAPIKey))) {
		return c, errors.New("本站 AI 凭据和接口由快速配置管理，请通过本站 AI 连接入口修改")
	}
	if !managedSetupAllowed(ctx) && (old.ImageAIConnection != c.ImageAIConnection || old.ImageSiteAPIKeyID != c.ImageSiteAPIKeyID || old.ImageSiteGroupID != c.ImageSiteGroupID || old.ImageSiteOwnerID != c.ImageSiteOwnerID || managedImageCredentialsChanged(old, c)) {
		return c, errors.New("本站图片 AI 凭据由快速配置管理，请通过本站图片 AI 连接入口修改")
	}
	if c.ImageAIConnection == "reuse_chat" {
		c.ImageBaseURL, c.ImageAPIKey = "", ""
		c.ClearImageAPIKey = true
	}
	if ConfigVersion(old.AllowedGroups) != ConfigVersion(c.AllowedGroups) {
		reconcileRemovedGroups(&c)
	}
	c.SectionVersions = nil
	c.SetupWarning = ""
	c.Moderation.ReportEnabled = false
	c.CustomCommands = normalizeCustomCommands(c.CustomCommands)
	c.SiteKnowledge = normalizeSiteKnowledge(old.SiteKnowledge, c.SiteKnowledge)
	c.Autonomous = normalizeAutonomous(old.Autonomous, c.Autonomous)
	c.AppID = strings.TrimSpace(c.AppID)
	c.GroupAdmins = normalizeGroupAdmins(c.GroupAdmins)
	c.Moderation.Exemptions = normalizeModerationExemptions(c.Moderation.Exemptions)
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.ImageBaseURL = strings.TrimRight(strings.TrimSpace(c.ImageBaseURL), "/")
	// Exemptions predate per-entry AppID fields. Never reinterpret an existing
	// member OpenID under a different application, including an unassigned legacy
	// config. The operator must explicitly clear and then reselect identities.
	if old.AppID != c.AppID && len(c.Moderation.Exemptions) > 0 {
		return c, errors.New("更换机器人 AppID 前请先清空消息治理白名单，保存后再更换机器人并重新选择成员")
	}
	if old.AppID != "" && old.AppID != c.AppID {
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM qq_bot_bindings WHERE app_id=$1`, old.AppID).Scan(&n); err != nil {
			return c, err
		}
		if n > 0 {
			return c, errors.New("已有账号绑定，不能直接更换机器人 AppID")
		}
		if c.AppSecret == "" && !c.ClearAppSecret {
			return c, errors.New("更换 AppID 时请同时填写新的 AppSecret")
		}
	}
	if old.BaseURL != "" && old.BaseURL != c.BaseURL && c.APIKey == "" && !c.ClearAPIKey {
		return c, errors.New("修改中转地址时请重新填写 API Key")
	}
	if old.ImageBaseURL != "" && old.ImageBaseURL != c.ImageBaseURL && c.ImageAPIKey == "" && !c.ClearImageAPIKey {
		return c, errors.New("修改生图地址时请重新填写或清除生图 API Key")
	}
	merge := func(dst *string, prev string, clear bool) {
		if clear {
			*dst = ""
		} else if *dst == "" {
			*dst = prev
		}
	}
	merge(&c.AppSecret, old.AppSecret, c.ClearAppSecret)
	merge(&c.APIKey, old.APIKey, c.ClearAPIKey)
	merge(&c.ImageAPIKey, old.ImageAPIKey, c.ClearImageAPIKey)
	c.ClearAppSecret = false
	c.ClearAPIKey = false
	c.ClearImageAPIKey = false
	if err = validateConfig(c); err != nil {
		return c, err
	}
	if err = resetSpamReports(ctx, tx, old, c); err != nil {
		return c, err
	}
	if (old.Moderation.Enabled || c.Moderation.Enabled) && moderationPolicy(old) != moderationPolicy(c) {
		// Retain replay tombstones, but never reuse old counting windows after a
		// disable/enable cycle or policy change. Successful mutes are not lifted.
		if _, err = tx.ExecContext(ctx, `UPDATE qq_bot_moderation_events SET policy_hash='' WHERE app_id=$1`, old.AppID); err != nil {
			return c, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE qq_bot_moderation_members m SET cooldown_until=LEAST(cooldown_until,NOW()),active_incident_id='' WHERE app_id=$1 AND NOT EXISTS(SELECT 1 FROM qq_bot_moderation_operations o WHERE o.incident_id=m.active_incident_id AND o.kind='mute' AND o.status IN ('started','succeeded','unconfirmed'))`, old.AppID); err != nil {
			return c, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE qq_bot_moderation_operations o SET status='cancelled',error='配置或白名单已变更，取消处罚',finished_at=NOW() FROM qq_bot_moderation_incidents i WHERE i.id=o.incident_id AND i.app_id=$1 AND o.status='queued'`, old.AppID); err != nil {
			return c, err
		}
	}
	if err = resetAutonomous(ctx, tx, old, c); err != nil {
		return c, err
	}
	if err = s.resetHealthNotifications(ctx, tx, old, c); err != nil {
		return c, err
	}
	if err = s.resetPriceNotifications(ctx, tx, old, c); err != nil {
		return c, err
	}
	if c.AppID != "" && (c.HistoryMessages == 0 || !c.Enabled) {
		if _, err = tx.ExecContext(ctx, `DELETE FROM qq_bot_group_messages WHERE app_id=$1`, c.AppID); err != nil {
			return c, err
		}
	}
	if section != "" {
		history := []ConfigChange{}
		if _, err = readActivityConfig(ctx, tx, configHistoryKey, &history); err != nil {
			return c, err
		}
		changedSections := []string{section}
		if section == "groups" {
			for _, dependent := range []string{"autonomous", "moderation", "notifications", "group-admins"} {
				if sectionVersion(old, dependent) != sectionVersion(c, dependent) {
					changedSections = append(changedSections, dependent)
				}
			}
		}
		for _, changed := range changedSections {
			history = append([]ConfigChange{{At: time.Now(), Section: changed, Before: publicSection(old, changed), After: publicSection(c, changed)}}, history...)
		}
		if len(history) > 30 {
			history = history[:30]
		}
		rawHistory, marshalErr := json.Marshal(history)
		if marshalErr != nil {
			return c, marshalErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,$2,NOW()) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=NOW()`, configHistoryKey, string(rawHistory)); err != nil {
			return c, err
		}
	}
	stored := c
	for _, p := range []*string{&stored.AppSecret, &stored.APIKey, &stored.ImageAPIKey} {
		if *p != "" {
			if !s.encryptionReady {
				return c, errors.New("请先配置固定 TOTP_ENCRYPTION_KEY，再保存机器人密钥")
			}
			*p, err = s.encryptor.Encrypt(*p)
			if err != nil {
				return c, errors.New("凭据加密失败")
			}
		}
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return c, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,$2,NOW()) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=NOW()`, configKey, string(raw))
	if err != nil {
		return c, err
	}
	if beforeCommit != nil {
		if err = beforeCommit(tx, c); err != nil {
			return c, err
		}
	}
	if err = tx.Commit(); err != nil {
		return c, err
	}
	s.wakePriceMonitor()
	return c.Public(), nil
}
