package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type JobFilter struct {
	QueryKind   string `form:"query_kind" json:"query_kind"`
	QueryStatus string `form:"query_status" json:"query_status"`
	GroupID     string `form:"group_id" json:"group_id"`
	MemberID    string `form:"member_id" json:"member_id"`
	Kind        string `form:"kind" json:"kind"`
	Status      string `form:"status" json:"status"`
	From        string `form:"from" json:"from"`
	To          string `form:"to" json:"to"`
	Page        int    `form:"page" json:"page"`
	PageSize    int    `form:"page_size" json:"page_size"`
}
type JobPage struct {
	Items    []Job `json:"items"`
	Total    int   `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

func (s *Service) ListJobs(ctx context.Context, f JobFilter) (JobPage, error) {
	out := JobPage{Items: []Job{}, Page: f.Page, PageSize: f.PageSize}
	if f.Page < 1 || f.Page > 100000 || f.PageSize < 1 || f.PageSize > 100 {
		return out, errors.New("分页参数不正确")
	}
	for _, v := range []string{f.GroupID, f.MemberID, f.Kind, f.Status} {
		if len(v) > 128 {
			return out, errors.New("筛选条件过长")
		}
	}
	switch f.QueryKind {
	case "", "model_price", "channel_status", "activity_rules", "account_balance", "today_usage", "lottery_chances":
	default:
		return out, errors.New("查询类型不正确")
	}
	switch f.QueryStatus {
	case "", "succeeded", "not_found", "unavailable", "failed":
	default:
		return out, errors.New("查询状态不正确")
	}
	from, to, err := recordTimeRange(f.From, f.To)
	if err != nil {
		return out, err
	}
	c, err := s.Config(ctx)
	if err != nil {
		return out, err
	}
	where := ` FROM qq_bot_jobs WHERE app_id=$1 AND ($2='' OR group_id=$2) AND ($3='' OR member_id=$3) AND ($4='' OR kind=$4 OR ($4='notifications' AND kind IN ('price_notification','health_notification','ranking_notification','announcement'))) AND ($5='' OR status=$5) AND ($6::timestamptz IS NULL OR created_at >= $6) AND ($7::timestamptz IS NULL OR created_at <= $7) AND ($8='' OR site_query->>'kind'=$8) AND ($9='' OR site_query->>'status'=$9)`
	args := []any{c.AppID, f.GroupID, f.MemberID, f.Kind, f.Status, from, to, f.QueryKind, f.QueryStatus}
	// Count and page share one snapshot even while workers finish jobs.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,app_id,event_type,group_id,member_id,kind,status,error,input_tokens,output_tokens,created_at,finished_at,trigger_reason,decision_reason,image_decision,site_query,chat_execution`+where+` ORDER BY created_at DESC,id DESC LIMIT $10 OFFSET $11`, append(args, f.PageSize, (f.Page-1)*f.PageSize)...)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var j Job
		var chatExecution []byte
		if err = rows.Scan(&j.ID, &j.AppID, &j.EventType, &j.GroupID, &j.MemberID, &j.Kind, &j.Status, &j.Error, &j.InputTokens, &j.OutputTokens, &j.CreatedAt, &j.FinishedAt, &j.TriggerReason, &j.DecisionReason, &j.ImageDecision, &j.SiteQuery, &chatExecution); err != nil {
			return out, err
		}
		j.ChatExecution = json.RawMessage(chatExecution)
		out.Items = append(out.Items, j)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

type Overview struct {
	Day            string            `json:"day"`
	LastEvent      *time.Time        `json:"last_event"`
	Queued         int               `json:"queued"`
	Running        int               `json:"running"`
	Failed         int               `json:"failed"`
	ChatUsed       int               `json:"chat_used"`
	ImageUsed      int               `json:"image_used"`
	ChatRemaining  int               `json:"chat_remaining"`
	ImageRemaining int               `json:"image_remaining"`
	RewardAmount   float64           `json:"reward_amount"`
	PendingRewards int               `json:"pending_rewards"`
	RewardAlert    bool              `json:"reward_alert"`
	Checks         []DiagnosticCheck `json:"checks"`
}
type DiagnosticCheck struct {
	Code    string `json:"code"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func configChecks(c Config, group string) []DiagnosticCheck {
	checks := []DiagnosticCheck{
		{"enabled", c.Enabled, "机器人总开关"},
		{"credentials", c.AppID != "" && c.AppSecret != "", "QQ AppID / AppSecret 已配置（不代表平台权限已验证）"},
		{"groups", len(c.AllowedGroups) > 0, "至少允许一个群"},
		{"chat", !c.ChatDisabled, "AI 聊天开关"},
		{"model", c.BaseURL != "" && c.APIKey != "" && c.ChatModel != "", "聊天模型连接信息完整"},
		{"quota", c.TotalDailyChatLimit > 0 && c.DailyChatLimit > 0, "聊天总额度和成员额度大于零"},
	}
	if group != "" {
		checks = append(checks, DiagnosticCheck{"allowed", c.allows(group), "目标群在允许名单中"}, DiagnosticCheck{"group_chat", c.groupLimit(group, "chat") > 0, "目标群已允许聊天并有配置额度"})
	}
	return checks
}
func (s *Service) Overview(ctx context.Context) (Overview, error) {
	c, err := s.Config(ctx)
	out := Overview{Day: day(time.Now())}
	if err != nil {
		return out, err
	}
	out.Checks = configChecks(c, "")
	err = s.db.QueryRowContext(ctx, `SELECT MAX(last_seen_at) FROM qq_bot_groups WHERE app_id=$1`, c.AppID).Scan(&out.LastEvent)
	if err != nil {
		return out, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE status='queued'),COUNT(*) FILTER(WHERE status='running'),COUNT(*) FILTER(WHERE status IN ('failed','delivery_failed','interrupted','expired') AND (created_at AT TIME ZONE 'Asia/Shanghai')::date=$2::date) FROM qq_bot_jobs WHERE app_id=$1`, c.AppID, out.Day).Scan(&out.Queued, &out.Running, &out.Failed)
	if err != nil {
		return out, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(count) FILTER(WHERE kind='chat'),0),COALESCE(SUM(count) FILTER(WHERE kind='image'),0) FROM qq_bot_daily_usage WHERE app_id=$1 AND scope='all' AND day=$2::date`, c.AppID, out.Day).Scan(&out.ChatUsed, &out.ImageUsed)
	if err != nil {
		return out, err
	}
	out.ChatRemaining = max(0, c.TotalDailyChatLimit-out.ChatUsed)
	out.ImageRemaining = max(0, c.TotalDailyImageLimit-out.ImageUsed)
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount),0) FROM qq_bot_rewards WHERE app_id=$1 AND (created_at AT TIME ZONE 'Asia/Shanghai')::date=$2::date`, c.AppID, out.Day).Scan(&out.RewardAmount)
	if err != nil {
		return out, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM qq_bot_reward_codes WHERE app_id=$1 AND status='pending'`, c.AppID).Scan(&out.PendingRewards)
	out.RewardAlert = c.RewardAlertDaily > 0 && out.RewardAmount >= c.RewardAlertDaily
	return out, err
}

type Diagnosis struct {
	Checks []DiagnosticCheck `json:"checks"`
	Jobs   []Job             `json:"jobs"`
}

func (s *Service) Diagnose(ctx context.Context, group, member string) (Diagnosis, error) {
	out := Diagnosis{}
	if len(group) > 128 || len(member) > 128 || strings.TrimSpace(group) == "" {
		return out, errors.New("请选择目标群")
	}
	c, err := s.Config(ctx)
	if err != nil {
		return out, err
	}
	out.Checks = configChecks(c, group)
	var usage, global, personal int
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(count) FILTER(WHERE scope=$2),0),COALESCE(SUM(count) FILTER(WHERE scope='all'),0),COALESCE(SUM(count) FILTER(WHERE scope=$3),0) FROM qq_bot_daily_usage WHERE app_id=$1 AND day=$4::date AND kind='chat'`, c.AppID, "group:"+group, "member:"+member, day(time.Now())).Scan(&usage, &global, &personal)
	if err != nil {
		return out, err
	}
	out.Checks = append(out.Checks, DiagnosticCheck{"remaining", usage < c.groupLimit(group, "chat") && global < c.TotalDailyChatLimit, "今日群额度和全局额度尚未用尽"})
	if member != "" {
		out.Checks = append(out.Checks, DiagnosticCheck{"member_quota", personal < c.DailyChatLimit, "该成员今日聊天额度尚未用尽"})
	}
	if member != "" {
		var recent bool
		err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM qq_bot_jobs WHERE app_id=$1 AND member_id=$2 AND kind IN ('chat','image') AND started_at>NOW()-($3*INTERVAL '1 second'))`, c.AppID, member, c.CooldownSeconds).Scan(&recent)
		if err != nil {
			return out, err
		}
		out.Checks = append(out.Checks, DiagnosticCheck{"cooldown", !recent, "该成员当前未处于聊天/生图冷却时间"})
	}
	out.Checks = append(out.Checks,
		DiagnosticCheck{"autonomous", c.autonomousGroup(group), "目标群已开启自主接话（关闭仍可通过提及机器人聊天）"},
		DiagnosticCheck{"autonomous_hours", autonomousActive(c.Autonomous, time.Now()), "当前在自主接话的北京时间活动时段内"},
		DiagnosticCheck{"notification_hours", !c.NotificationQuietHours.active(time.Now()), "当前不在主动通知静默时段内"})
	page, err := s.ListJobs(ctx, JobFilter{GroupID: group, MemberID: member, Page: 1, PageSize: 10})
	out.Jobs = page.Items
	return out, err
}

type CommandInfo struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Builtin     bool   `json:"builtin"`
	Enabled     bool   `json:"enabled"`
	Reply       string `json:"reply"`
}

func (s *Service) Commands(ctx context.Context) ([]CommandInfo, error) {
	c, err := s.Config(ctx)
	out := []CommandInfo{}
	if err != nil {
		return out, err
	}
	for _, item := range helpSystemItems() {
		out = append(out, CommandInfo{Command: item.command, Description: item.description, Usage: item.usage, Builtin: true, Enabled: true})
	}
	for _, item := range c.CustomCommands {
		if isRetiredCommand(strings.TrimPrefix(item.Command, "/")) {
			continue
		}
		out = append(out, CommandInfo{Command: strings.TrimPrefix(item.Command, "/"), Description: "自定义固定回复", Enabled: item.Enabled, Reply: item.Reply})
	}
	return out, nil
}

func recordTimeRange(start, end string) (from, to any, err error) {
	var first, last time.Time
	if start != "" {
		first, err = time.Parse(time.RFC3339, start)
		if err != nil {
			return nil, nil, errors.New("开始时间格式不正确")
		}
		from = first
	}
	if end != "" {
		last, err = time.Parse(time.RFC3339, end)
		if err != nil {
			return nil, nil, errors.New("结束时间格式不正确")
		}
		to = last
	}
	if start != "" && end != "" && first.After(last) {
		return nil, nil, errors.New("开始时间不能晚于结束时间")
	}
	return from, to, nil
}
