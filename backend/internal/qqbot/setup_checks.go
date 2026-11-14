package qqbot

import (
	"context"
	"database/sql"
	"net/url"
	"time"
)

type SetupCheck struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Action  string `json:"action"`
}
type SetupReadiness struct {
	Checks  []SetupCheck `json:"checks"`
	Version string       `json:"version"`
	Ready   bool         `json:"ready"`
}

func (s *Service) SetupChecks(ctx context.Context, owner int64) (SetupReadiness, error) {
	c, err := s.Config(ctx)
	if err != nil {
		return SetupReadiness{}, err
	}
	out := SetupReadiness{Checks: []SetupCheck{}, Version: ConfigVersion(c), Ready: true}
	add := func(code, status, message, action string) {
		out.Checks = append(out.Checks, SetupCheck{code, status, message, action})
		if status == "blocked" {
			out.Ready = false
		}
	}
	status := func(ok bool) string {
		if ok {
			return "ready"
		}
		return "blocked"
	}
	add("encryption", status(s.encryptionReady), "固定凭据加密密钥", "/admin/qqbot/config?tab=connection")
	add("credentials", status(c.AppID != "" && c.AppSecret != ""), "QQ 凭据已填写；凭据测试另行验证", "/admin/qqbot/config?tab=connection")
	add("groups", status(len(c.AllowedGroups) > 0), "至少选择一个允许群", "/admin/qqbot/groups")
	if !c.ChatDisabled {
		add("chat", status(c.BaseURL != "" && c.APIKey != "" && c.ChatModel != "" && c.DailyChatLimit > 0 && c.TotalDailyChatLimit > 0), "聊天连接、模型与次数额度", "/admin/qqbot/config?tab=chat")
	}
	if c.ImageEnabled {
		base, key := imageConnection(c)
		add("images", status(base != "" && key != "" && c.ImageModel != "" && c.DailyImageLimit > 0 && c.TotalDailyImageLimit > 0), "图片连接、模型与次数额度", "/admin/qqbot/config?tab=images")
	}
	var last sql.NullTime
	if err = s.db.QueryRowContext(ctx, `SELECT MAX(last_seen_at) FROM qq_bot_groups WHERE app_id=$1`, c.AppID).Scan(&last); err != nil {
		return out, err
	}
	if last.Valid {
		add("events", "warning", "曾收到群事件（"+last.Time.Format(time.RFC3339)+"）；历史不能证明当前沙箱、目标群及平台权限", "/admin/qqbot/groups")
	} else {
		add("events", "pending", "尚未收到群事件，请在目标群 @ 机器人验证回调", "/admin/qqbot/groups")
	}
	if c.Moderation.Enabled || c.Autonomous.Enabled {
		add("permissions", "pending", "自主接话需全量消息；治理还需 QQ 管理员及接口权限，请在平台核对", "/admin/qqbot/config?tab=connection")
	}
	if s.siteAI != nil {
		opts, e := s.siteAI.Options(ctx, owner, c)
		if e != nil {
			add("site", "warning", "站点依赖暂时无法读取", "/admin/settings")
		} else {
			u, e := url.Parse(opts.FrontendURL)
			valid := e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
			if !valid {
				add("frontend", "warning", "公开站点 HTTPS 地址未就绪，邮件确认链接和回调需检查", "/admin/settings")
			}
			if opts.EmailConfigured {
				add("email", "warning", "邮件配置完整；未发送测试邮件，不代表实际投递成功", "/admin/settings")
			} else {
				add("email", "warning", "邮件未配置，邮箱绑定和邮件确认转账不可用；网站绑定码仍保留", "/admin/settings")
			}
		}
	}
	if c.RewardBudgetDaily == nil {
		add("reward_budget", "warning", "未设置余额奖励硬预算", "/admin/qqbot/activities?tab=templates")
	} else if *c.RewardBudgetDaily == 0 {
		add("reward_budget", "warning", "余额奖励预算为 0，暂停新增余额奖励", "/admin/qqbot/activities?tab=templates")
	} else {
		add("reward_budget", "ready", "每日余额奖励硬预算已设置", "/admin/qqbot/activities?tab=templates")
	}
	probes, e := readSetupProbes(ctx, s.db, c)
	if e != nil {
		return out, e
	}
	for _, kind := range requiredSetupProbes(c) {
		if result, ok := probes[kind]; ok {
			add("probe_"+kind, result.Status, result.Message, "/admin/qqbot/records?tab=diagnostics")
		} else {
			add("probe_"+kind, "pending", "尚未验证所选能力："+kind, "/admin/qqbot/records?tab=diagnostics")
			out.Ready = false
		}
	}
	return out, nil
}
