//go:build integration

package qqbot

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQQBotInviteActivityCountsJoinsAndRewardLimit(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	c.InviteReward, c.InviteDailyLimit = 1.25, 2
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	inviter := user(t, db)
	bind(t, db, inviter, "inviter")
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600))
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	yesterday := today.Add(-time.Second)
	addMember := func(app, group, member, from string, invited, joined any) {
		t.Helper()
		_, insertErr := db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at) VALUES($1,$2,$3,$4,$5,$6)`, app, group, member, from, invited, joined)
		require.NoError(t, insertErr)
	}
	addMember("app", "group", "joined-today", "inviter", today.Add(-time.Minute), today)
	addMember("app", "group", "not-bound", "inviter", today, today.Add(time.Second))
	addMember("app", "other-group", "not-bound", "inviter", today, today.Add(time.Second))
	addMember("app", "other-group", "joined-yesterday", "inviter", yesterday.Add(-time.Minute), yesterday)
	addMember("app", "group", "no-join", "inviter", today, nil)
	addMember("app", "group", "late-invite", "inviter", today.Add(time.Minute), today)
	addMember("app", "group", "expired-invite", "inviter", today.AddDate(0, 0, -8), today)
	addMember("app", "group", "other-inviter", "someone-else", today, today)
	addMember("other-app", "group", "other-app", "inviter", today, today)
	addMember("app", "group", "inviter", "inviter", today, today)
	for _, member := range []string{"joined-today", "joined-yesterday"} {
		bind(t, db, user(t, db), member)
	}
	job := Job{AppID: "app", GroupID: "group", MemberID: "inviter"}
	info, err := s.inviteInfo(ctx, c, job)
	require.NoError(t, err)
	require.Contains(t, info, "每人奖励：USD 1.25")
	require.Contains(t, info, "今日入群：2 人")
	require.Contains(t, info, "今日已奖励：0/2 人")

	// Yesterday's join can be paid today; queries must use the actual payout day.
	for _, invitee := range []Job{
		{AppID: "app", GroupID: "group", MemberID: "joined-today"},
		{AppID: "app", GroupID: "other-group", MemberID: "joined-yesterday"},
	} {
		result, rewardErr := s.reward(ctx, c, invitee, "invite")
		require.NoError(t, rewardErr)
		require.Contains(t, result, "邀请奖励已发放")
	}
	info, err = s.inviteInfo(ctx, c, job)
	require.NoError(t, err)
	require.Contains(t, info, "今日入群：2 人")
	require.Contains(t, info, "今日已奖励：2/2 人")

	// History, other reward types and other recipients must not consume today's limit.
	_, err = db.Exec(`INSERT INTO qq_bot_rewards(reward_key,app_id,group_id,member_id,user_id,kind,amount,created_at) VALUES
 ('old-invite','app','group','inviter',$1,'invite',1,$2),
 ('checkin','app','group','inviter',$1,'checkin',1,NOW()),
 ('other-recipient','app','group','joined-today',$3,'invite',1,NOW())`, inviter, yesterday, user(t, db))
	require.NoError(t, err)
	bind(t, db, user(t, db), "not-bound")
	result, err := s.reward(ctx, c, Job{AppID: "app", GroupID: "group", MemberID: "not-bound"}, "invite")
	require.NoError(t, err)
	require.Equal(t, "邀请人今日奖励次数已达上限。", result)
	for range 2 {
		info, err = s.inviteInfo(ctx, c, job)
		require.NoError(t, err)
		require.Contains(t, info, "今日已奖励：2/2 人")
	}
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Equal(t, 2.5, balance, "viewing the activity must not settle or duplicate rewards")
}

func TestQQBotInviteActivityUnavailableStates(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	job := Job{MemberID: "member"}
	info, err := s.inviteInfo(ctx, c, job)
	require.NoError(t, err)
	require.Contains(t, info, "通过官方邀请")
	require.Contains(t, info, "请先发送“绑定 邮箱或绑定码”绑定网站账号。")
	uid := user(t, db)
	bind(t, db, uid, "member")
	for _, state := range []string{"inactive", "deleted"} {
		if state == "inactive" {
			_, err = db.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, uid)
		} else {
			_, err = db.Exec(`UPDATE users SET status='active',deleted_at=NOW() WHERE id=$1`, uid)
		}
		require.NoError(t, err)
		info, err = s.inviteInfo(ctx, c, job)
		require.NoError(t, err)
		require.Contains(t, info, "通过官方邀请")
		require.Contains(t, info, "请先发送“绑定 邮箱或绑定码”绑定网站账号。")
	}
	for _, disabled := range []Config{
		{InviteEnabled: true, InviteReward: 0, InviteDailyLimit: 10},
		{InviteEnabled: true, InviteReward: 1, InviteDailyLimit: 0},
		{InviteEnabled: false, InviteReward: 1, InviteDailyLimit: 10},
	} {
		info, err = s.inviteInfo(ctx, disabled, job)
		require.NoError(t, err)
		require.Equal(t, "邀请活动未开启。", info)
	}
}
