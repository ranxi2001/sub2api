package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Personal query identity comes exclusively from the signed event's job. These
// queries never invoke reward settlement or return data to the chat model.
func (s *Service) querySiteAccount(ctx context.Context, c Config, j Job, q siteQuery, at time.Time) SiteQueryResult {
	if s.db == nil {
		return siteQueryOutcome("账户查询暂时不可用。", "unavailable", "storage_unavailable")
	}
	if j.AppID == "" || j.AppID != c.AppID || j.MemberID == "" {
		return siteQueryOutcome("无法确认当前提问者身份，请在群内重新发起查询。", "unavailable", "identity_unavailable")
	}
	// The model request may have taken time: recheck the current bot and group
	// before reading personal data instead of trusting the old turn settings.
	current, err := s.Config(ctx)
	if err != nil {
		return siteQueryOutcome("账户查询失败，请稍后重试。", "failed", "config_read_failed")
	}
	if current.AppID != j.AppID || !current.Enabled || !current.allows(j.GroupID) {
		return siteQueryOutcome("当前群未启用机器人，账户查询暂时不可用。", "unavailable", "group_disabled")
	}
	var text string
	switch q.Kind {
	case "account_balance":
		var balance float64
		err = s.db.QueryRowContext(ctx, `SELECT u.balance FROM qq_bot_bindings b JOIN users u ON u.id=b.user_id WHERE b.app_id=$1 AND b.member_id=$2 AND u.deleted_at IS NULL AND u.status='active'`, j.AppID, j.MemberID).Scan(&balance)
		if errors.Is(err, sql.ErrNoRows) {
			return siteQueryOutcome(bindFirst, "unavailable", "binding_required")
		}
		text = fmt.Sprintf("💳 **我的余额**\n余额：USD %.2f", balance)
	case "today_usage":
		text, err = s.todayUsage(ctx, current, j, at)
	case "lottery_chances":
		if current.groupPolicy(j.GroupID).DisableActivities {
			return siteQueryOutcome("当前群未开启活动。", "unavailable", "activities_disabled")
		}
		text, err = s.lotteryInfo(ctx, current, j, false)
	}
	if err != nil {
		return siteQueryOutcome("账户查询失败，请稍后重试。", "failed", "account_query_failed")
	}
	if text == bindFirst {
		return siteQueryOutcome(text, "unavailable", "binding_required")
	}
	if q.Kind == "lottery_chances" && text == "抽奖活动未开启。" {
		return siteQueryOutcome(text, "unavailable", "lottery_disabled")
	}
	return siteQueryOutcome(text, "succeeded", "")
}
