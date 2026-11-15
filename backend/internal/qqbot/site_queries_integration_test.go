//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func siteQueryMutationSnapshot(t *testing.T, db *sql.DB, uid int64) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"qq_bot_rewards", "redeem_codes", "qq_bot_lottery_draws", "qq_bot_lottery_chance_grants", "qq_bot_reward_codes", "qq_bot_daily_usage", "qq_bot_periodic_growth_progress"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count))
		out[table] = fmt.Sprint(count)
	}
	var balance string
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, uid).Scan(&balance))
	out["balance"] = balance
	return out
}

func TestSiteQueryActivityRulesArePublicAndNeverSettleRewards(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	c, err := s.Config(ctx)
	require.NoError(t, err)
	checkin, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	checkin.Enabled = true
	checkin.StreakChances = 2
	checkin.Growth.Tasks = []GrowthTask{{ID: "binding", Name: "绑定奖励", Kind: "binding", Target: 1, Chances: 5, Enabled: true}}
	require.NoError(t, s.SaveCheckinConfig(ctx, checkin))
	lottery := defaultLotteryConfig()
	lottery.Enabled, lottery.GuaranteeEnabled = true, true
	require.NoError(t, s.SaveLotteryConfig(ctx, lottery))
	before := siteQueryMutationSnapshot(t, db, uid)
	for _, member := range []string{"member", "unbound"} {
		for _, activity := range []string{"all", "checkin", "invite", "lottery", "growth"} {
			text, err := s.querySiteInfo(ctx, c, Job{AppID: "app", GroupID: "group", MemberID: member}, siteQuery{Kind: "activity_rules", Activity: activity})
			require.NoError(t, err)
			require.Contains(t, text, "查询时间：")
			require.Contains(t, text, "未查询个人资格或进度")
			require.NotContains(t, text, "查询失败")
			if activity == "growth" || activity == "all" {
				require.Contains(t, text, "绑定奖励")
				require.Contains(t, text, "5 次抽奖机会")
			}
			if activity == "lottery" || activity == "all" {
				require.Contains(t, text, "保底奖池")
			}
		}
	}
	require.Equal(t, before, siteQueryMutationSnapshot(t, db, uid))
	// Policy is checked at query time, after any model decision that used an
	// older configuration. Disabled groups cannot retrieve stale active rules.
	current, err := s.Config(ctx)
	require.NoError(t, err)
	current.GroupPolicies = []GroupPolicy{{GroupID: "group", DisableActivities: true}}
	_, err = s.SaveConfig(ctx, current)
	require.NoError(t, err)
	text, err := s.querySiteInfo(ctx, c, Job{GroupID: "group", MemberID: "member"}, siteQuery{Kind: "activity_rules"})
	require.NoError(t, err)
	require.Contains(t, text, "当前群未开启活动")
	require.NotContains(t, text, "绑定奖励")
	require.Equal(t, before, siteQueryMutationSnapshot(t, db, uid))
}

func TestSiteQueryModelPriceUsesCurrentBindingWithinApp(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	first, second := user(t, db), user(t, db)
	bind(t, db, first, "member")
	bind(t, db, second, "other-member")
	c, err := s.Config(ctx)
	require.NoError(t, err)
	var received int64
	s.SetModelPriceProvider(siteModelPriceFunc(func(_ context.Context, model string, uid int64, at time.Time) ([]string, error) {
		received = uid
		return []string{"公开报价"}, nil
	}))
	for _, tc := range []struct {
		app, member string
		uid         int64
	}{{"app", "member", first}, {"app", "other-member", second}, {"app", "unbound", 0}, {"different-app", "member", 0}} {
		requestConfig := c
		requestConfig.AppID = tc.app
		_, err = s.querySiteInfo(ctx, requestConfig, Job{GroupID: "group", MemberID: tc.member}, siteQuery{Kind: "model_price", Model: "gpt-5"})
		require.NoError(t, err)
		require.Equal(t, tc.uid, received)
	}
}

func TestSiteAccountQueriesReadOnlyAndBoundToCurrentMember(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	first, second := user(t, db), user(t, db)
	bind(t, db, first, "member")
	bind(t, db, second, "other-member")
	_, err := db.Exec(`UPDATE users SET balance=CASE WHEN id=$1 THEN 12.50 ELSE 99.25 END WHERE id IN ($1,$2)`, first, second)
	require.NoError(t, err)
	insightsUsageTable(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	checkin, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	checkin.Growth.Tasks = []GrowthTask{{ID: "binding", Name: "绑定奖励", Kind: "binding", Target: 1, Chances: 5, Enabled: true}}
	require.NoError(t, s.SaveCheckinConfig(ctx, checkin))
	lottery := defaultLotteryConfig()
	lottery.Enabled = true
	require.NoError(t, s.SaveLotteryConfig(ctx, lottery))
	before := siteQueryMutationSnapshot(t, db, first)
	j := Job{AppID: c.AppID, GroupID: "group", MemberID: "member"}
	for _, kind := range []string{"account_balance", "today_usage", "lottery_chances"} {
		result, err := s.querySiteInfoDetailed(ctx, c, j, siteQuery{Kind: kind})
		require.NoError(t, err)
		require.Equal(t, "succeeded", result.Decision.Status, result.Text)
		require.NotContains(t, result.Text, "99.25")
		if kind == "account_balance" {
			require.Contains(t, result.Text, "余额：USD 12.50")
		}
		if kind == "lottery_chances" {
			require.Contains(t, result.Text, "剩余抽奖次数：0 次", "query must not settle the eligible binding reward")
		}
		require.Equal(t, before, siteQueryMutationSnapshot(t, db, first))
	}
	j.MemberID = "other-member"
	result, err := s.querySiteInfoDetailed(ctx, c, j, siteQuery{Kind: "account_balance"})
	require.NoError(t, err)
	require.Contains(t, result.Text, "余额：USD 99.25")
	j.MemberID = "unbound"
	result, err = s.querySiteInfoDetailed(ctx, c, j, siteQuery{Kind: "account_balance"})
	require.NoError(t, err)
	require.Equal(t, "binding_required", result.Decision.ErrorCode)
	j.MemberID, j.AppID = "member", "different-app"
	result, err = s.querySiteInfoDetailed(ctx, c, j, siteQuery{Kind: "account_balance"})
	require.NoError(t, err)
	require.Equal(t, "identity_unavailable", result.Decision.ErrorCode)
	j.AppID = c.AppID
	_, err = db.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, first)
	require.NoError(t, err)
	for _, kind := range []string{"account_balance", "today_usage", "lottery_chances"} {
		result, err = s.querySiteInfoDetailed(ctx, c, j, siteQuery{Kind: kind})
		require.NoError(t, err)
		require.Equal(t, "binding_required", result.Decision.ErrorCode)
		require.NotContains(t, result.Text, "12.50")
	}
	require.Equal(t, before, siteQueryMutationSnapshot(t, db, first))
}
