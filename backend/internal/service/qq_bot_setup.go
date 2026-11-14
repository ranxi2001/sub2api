package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/qqbot"
	"html"
	"sort"
	"strings"
)

type qqBotSiteAI struct {
	keys     *APIKeyService
	groups   GroupRepository
	users    UserRepository
	accounts AccountRepository
	settings *SettingService
	email    *EmailService
	baseURL  string
}

func qqBotSiteChatProtocols(platform string) []string {
	if domain.UsesOpenAIGateway(platform) {
		return []string{"chat_completions", "responses"}
	}
	switch platform {
	case PlatformAnthropic, PlatformAntigravity, PlatformComposite:
		return []string{"chat_completions", "responses"}
	case PlatformGemini:
		return []string{"chat_completions"}
	default:
		return nil
	}
}

func qqBotSiteDefaultModelCandidates(platform string) []string {
	// The shared catalog falls back to Claude models for unknown platforms. Only
	// use it for platforms with an explicit catalog or Anthropic itself; other
	// providers get candidates from their configured models and mappings.
	switch platform {
	case PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok, PlatformOpenCodeGo, PlatformComposite:
		return defaultModelsListCandidateIDs(platform)
	default:
		return nil
	}
}

func qqBotSiteImageGroupSupported(g *Group) bool {
	return g != nil && g.IsActive() && g.AllowImageGeneration && (g.Platform == PlatformOpenAI || g.Platform == PlatformGrok || g.Platform == PlatformComposite)
}

func qqBotSiteImageModelCandidates(g Group, accounts []Account) []string {
	models := []string{}
	seen := map[string]bool{}
	isImageModel := func(model string) bool {
		return isOpenAIImageGenerationModel(model) || isGeminiCompatibleImageModel(model)
	}
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model != "" && !strings.ContainsAny(model, "*?") && !seen[model] && g.ModelAllowlist.Allows(model) {
			seen[model] = true
			models = append(models, model)
		}
	}
	for _, model := range g.ModelAllowlist.Models {
		if isImageModel(model) {
			add(model)
		}
	}
	if g.ModelRoutingEnabled {
		for model, accountIDs := range g.ModelRouting {
			if len(accountIDs) > 0 && isImageModel(model) {
				add(model)
			}
		}
	}
	for _, account := range accounts {
		for model, upstream := range account.GetModelMapping() {
			// OpenAI Images validates the public model before account mapping;
			// arbitrary aliases only work on the Grok media route. Composite
			// aliases targeting OpenAI need an explicit Images route instead.
			grokAlias := g.Platform == PlatformGrok || (g.Platform == PlatformComposite && account.Platform == PlatformGrok)
			if isImageModel(model) || (grokAlias && isImageModel(upstream)) {
				add(model)
			}
		}
	}
	if g.Platform == PlatformOpenAI || g.Platform == PlatformComposite {
		for _, model := range openai.DefaultModelIDs() {
			if IsGPTImageGenerationModel(model) {
				add(model)
			}
		}
	}
	if g.Platform == PlatformGrok || g.Platform == PlatformComposite {
		for _, model := range xai.DefaultModelIDs() {
			if isGrokImageGenerationModel(model) {
				add(model)
			}
		}
	}
	sort.Strings(models)
	return models
}

func (p *qqBotSiteAI) Options(ctx context.Context, owner int64, c qqbot.Config) (qqbot.SiteAIOptions, error) {
	out := qqbot.SiteAIOptions{Groups: []qqbot.SiteAIGroup{}, ImageGroups: []qqbot.SiteAIGroup{}, FrontendURL: p.settings.GetFrontendURL(ctx)}
	mail, mailErr := p.email.GetSMTPConfig(ctx)
	out.EmailConfigured = mailErr == nil && mail != nil && mail.Host != "" && mail.From != ""
	user, err := p.users.GetByID(ctx, owner)
	if err != nil {
		return out, err
	}
	groups, err := p.groups.ListActive(ctx)
	if err != nil {
		return out, err
	}
	for _, g := range groups {
		protocols := qqBotSiteChatProtocols(g.Platform)
		if !g.IsActive() || len(protocols) == 0 {
			continue
		}
		if _, e := p.keys.loadBindableAPIKeyGroup(ctx, user, g.ID); e != nil {
			continue
		}
		models := []string{}
		seen := map[string]bool{}
		add := func(m string) {
			m = strings.TrimSpace(m)
			if m != "" && !strings.ContainsAny(m, "*?") && !seen[m] && g.ModelAllowlist.Allows(m) {
				seen[m] = true
				models = append(models, m)
			}
		}
		for _, m := range g.ModelAllowlist.Models {
			add(m)
		}
		if g.ModelRoutingEnabled {
			for m, accountIDs := range g.ModelRouting {
				if len(accountIDs) > 0 {
					add(m)
				}
			}
		}
		accounts, e := p.accounts.ListSchedulableByGroupID(ctx, g.ID)
		if e != nil {
			return out, e
		}
		for _, a := range accounts {
			for m := range a.GetModelMapping() {
				add(m)
			}
		}
		for _, m := range qqBotSiteDefaultModelCandidates(g.Platform) {
			add(m)
		}
		sort.Strings(models)
		out.Groups = append(out.Groups, qqbot.SiteAIGroup{ID: g.ID, Name: g.Name, Platform: g.Platform, Models: models, ChatProtocols: protocols})
		if qqBotSiteImageGroupSupported(&g) {
			out.ImageGroups = append(out.ImageGroups, qqbot.SiteAIGroup{ID: g.ID, Name: g.Name, Platform: g.Platform, Models: qqBotSiteImageModelCandidates(g, accounts), ChatProtocols: []string{}})
		}
	}
	if c.SiteAPIKeyID > 0 {
		if key, e := p.keys.GetByID(ctx, c.SiteAPIKeyID); e == nil && key.UserID == c.SiteOwnerID {
			out.CurrentQuota = key.Quota
			out.CurrentUsed = key.QuotaUsed
		}
	}
	if c.ImageSiteAPIKeyID > 0 {
		if key, e := p.keys.GetByID(ctx, c.ImageSiteAPIKeyID); e == nil && key.UserID == c.ImageSiteOwnerID {
			out.CurrentImageQuota = key.Quota
			out.CurrentImageUsed = key.QuotaUsed
		}
	}
	return out, nil
}
func (p *qqBotSiteAI) Prepare(ctx context.Context, tx *sql.Tx, owner, groupID int64, quota float64, old qqbot.Config) (qqbot.SiteAIConnection, error) {
	return p.prepare(ctx, tx, owner, groupID, quota, old, false)
}

func (p *qqBotSiteAI) PrepareImage(ctx context.Context, tx *sql.Tx, owner, groupID int64, quota float64, old qqbot.Config) (qqbot.SiteAIConnection, error) {
	return p.prepare(ctx, tx, owner, groupID, quota, old, true)
}

func (p *qqBotSiteAI) prepare(ctx context.Context, tx *sql.Tx, owner, groupID int64, quota float64, old qqbot.Config, image bool) (qqbot.SiteAIConnection, error) {
	var out qqbot.SiteAIConnection
	if groupID <= 0 {
		return out, errors.New("请选择本站分组")
	}
	user, err := p.users.GetByID(ctx, owner)
	if err != nil {
		return out, err
	}
	group, err := p.keys.loadBindableAPIKeyGroup(ctx, user, groupID)
	if err != nil {
		return out, err
	}
	if image {
		if !qqBotSiteImageGroupSupported(group) {
			return out, errors.New("本站图片 AI 需要启用且允许生图的 OpenAI、Grok 或合成分组")
		}
	} else {
		protocols := qqBotSiteChatProtocols(group.Platform)
		if !group.IsActive() || len(protocols) == 0 {
			return out, errors.New("本站 AI 需要启用且支持聊天接口的分组")
		}
		protocol := old.ChatProtocol
		if protocol == "" {
			protocol = "chat_completions"
		}
		supported := false
		for _, candidate := range protocols {
			if protocol == candidate {
				supported = true
				break
			}
		}
		if !supported {
			return out, fmt.Errorf("%s 分组不支持聊天接口 %s，请选择支持的接口模式", group.Platform, protocol)
		}
	}
	out.BaseURL = p.baseURL
	out.GroupID = groupID
	out.OwnerID = owner
	connection, previousGroup, previousKeyID, previousOwner, previousKey := old.AIConnection, old.SiteGroupID, old.SiteAPIKeyID, old.SiteOwnerID, old.APIKey
	keyName := "QQ bot " + old.AppID
	if image {
		connection, previousGroup, previousKeyID, previousOwner, previousKey = old.ImageAIConnection, old.ImageSiteGroupID, old.ImageSiteAPIKeyID, old.ImageSiteOwnerID, old.ImageAPIKey
		keyName = "QQ bot image " + old.AppID
	}
	if connection == "site" && previousGroup == groupID && previousKeyID > 0 {
		key, e := p.keys.GetByID(ctx, previousKeyID)
		if e != nil {
			return out, errors.New("机器人专用 Key 不存在，请重新配置本站 AI")
		}
		if key.UserID != previousOwner || key.Key != APIKeyStoredValue(previousKey) || key.GroupID == nil || *key.GroupID != groupID || previousKey == "" {
			return out, errors.New("机器人专用 Key 身份不匹配")
		}
		if key.Status != StatusActive || key.IsExpired() || key.IsQuotaExhausted() {
			return out, errors.New("机器人专用 Key 已停用或额度耗尽，请在 API Key 管理恢复或调整额度")
		}
		if quota != key.Quota {
			return out, errors.New("已有专用 Key 的总额度请在 API Key 管理调整，不会通过重新保存重置用量")
		}
		out.OwnerID = key.UserID
		out.KeyID = key.ID
		out.Key = previousKey
		return out, nil
	}
	// The dedicated key and encrypted bot configuration commit in the same SQL transaction.
	// A validation failure or process crash cannot leave an active orphan key.
	if err := p.keys.checkAPIKeyCreateLimits(ctx, owner); err != nil {
		return out, err
	}
	if p.keys.cfg != nil && p.keys.cfg.APIKeyCreate.MaxActivePerUser > 0 {
		// A setup may create chat and image keys in the same transaction. The
		// repository's separate connection cannot see the first uncommitted key.
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys WHERE user_id=$1 AND deleted_at IS NULL`, owner).Scan(&count); err != nil {
			return out, fmt.Errorf("count api keys in setup: %w", err)
		}
		if count >= int64(p.keys.cfg.APIKeyCreate.MaxActivePerUser) {
			return out, ErrAPIKeyCountExceeded
		}
	}
	raw, err := p.keys.GenerateKey()
	if err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name,group_id,status,quota) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`,
		owner, APIKeyStoredValue(raw), html.EscapeString(keyName), groupID, StatusActive, quota).Scan(&out.KeyID); err != nil {
		return out, err
	}
	out.Key = raw
	return out, nil
}
func (p *qqBotSiteAI) Revoke(ctx context.Context, keyID, owner int64) error {
	return p.keys.Delete(ctx, keyID, owner)
}
func qqBotInternalBaseURL(port int) string { return fmt.Sprintf("http://127.0.0.1:%d/v1", port) }
