//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQQBotMyInvitesCountsStatesAndDeduplicatesAcrossGroups(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	c.InviteDailyLimit = 20
	inviter := user(t, db)
	bind(t, db, inviter, "inviter")
	bind(t, db, user(t, db), "other-inviter")
	for _, member := range []string{"ready", "paid", "paid-elsewhere", "paused"} {
		bind(t, db, user(t, db), member)
	}
	unavailable := user(t, db)
	bind(t, db, unavailable, "unavailable")
	_, err = db.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, unavailable)
	require.NoError(t, err)
	now := time.Now().Truncate(time.Second)
	invited, joined := now.Add(-2*time.Minute), now.Add(-time.Minute)
	add := func(group, member, from string, invitation, join, left any) {
		t.Helper()
		_, insertErr := db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at,left_at)
 VALUES('app',$1,$2,$3,$4,$5,$6)`, group, member, from, invitation, join, left)
		require.NoError(t, insertErr)
	}
	add("group", "pending", "inviter", invited, nil, nil)
	add("other-group", "pending", "inviter", invited, nil, nil)
	add("group", "unbound", "inviter", invited, joined, nil)
	add("other-group", "unbound", "inviter", invited, joined, now)
	add("group", "ready", "inviter", invited, joined, nil)
	add("blocked-group", "ready", "inviter", invited, joined, nil)
	add("other-group", "ready", "inviter", invited, joined, now)
	add("group", "paid", "inviter", invited, joined, nil)
	add("other-group", "paid", "inviter", invited, joined, nil)
	add("group", "left", "inviter", invited, joined, now)
	add("group", "invalid", "inviter", joined.Add(-8*24*time.Hour), joined, nil)
	add("group", "unavailable", "inviter", invited, joined, nil)
	add("group", "inviter", "inviter", invited, joined, nil)
	add("blocked-group", "paused", "inviter", invited, joined, nil)
	add("group", "paid-elsewhere", "inviter", invited, joined, nil)
	add("other-group", "paid-elsewhere", "other-inviter", invited, joined, nil)
	// Ordinary joins, substring matches, another inviter and another application
	// must not become this member's invitations.
	add("group", "ordinary", "", nil, joined, nil)
	add("group", "other-recipient", "other-inviter", invited, joined, nil)
	add("group", "substring", "prefix-inviter-suffix", invited, joined, nil)
	_, err = db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at)
 VALUES('other-app','group','other-app-member','inviter',$1,$2)`, invited, joined)
	require.NoError(t, err)
	for _, member := range []string{"paid", "paid-elsewhere"} {
		result, payErr := s.reward(ctx, c, Job{GroupID: "other-group", MemberID: member}, "invite")
		require.NoError(t, payErr)
		require.Contains(t, result, "邀请奖励已发放")
	}
	// A historical reward must remain rewarded even after leaving the paid group.
	_, err = db.Exec(`UPDATE qq_bot_members SET left_at=$1 WHERE member_id='paid'`, now)
	require.NoError(t, err)
	for range 2 {
		info, readErr := s.myInvites(ctx, c, Job{MemberID: "inviter"})
		require.NoError(t, readErr)
		require.Equal(t, "👥 **我的邀请**\n待进群：1 人\n待绑定：1 人\n已奖励：1 人\n已退群：1 人", info)
	}
	var rewards, history int
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards`).Scan(&rewards))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM redeem_codes`).Scan(&history))
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Equal(t, 2, rewards)
	require.Equal(t, 2, history)
	require.Equal(t, 1.0, balance, "querying invitations must not settle the ready invitation")
}

func TestQQBotMyInvitesTracksRequestThroughJoinAndLeave(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	now := time.Now().Truncate(time.Second)
	e := Event{GroupID: "group", MemberID: "invitee", ApplySource: "invited", InvitedBy: "inviter", ApplyAt: now.Add(-time.Minute).Format(time.RFC3339)}
	j := Job{AppID: c.AppID, GroupID: e.GroupID, MemberID: e.MemberID, EventType: "GROUP_JOIN_REQUEST"}
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	read := func() string {
		t.Helper()
		info, readErr := s.myInvites(ctx, c, Job{MemberID: "inviter"})
		require.NoError(t, readErr)
		return info
	}
	require.Equal(t, "👥 **我的邀请**\n待进群：1 人\n待绑定：0 人\n已奖励：0 人\n已退群：0 人", read())

	e.Timestamp = json.RawMessage(strconv.FormatInt(now.Unix(), 10))
	j.EventType = "GROUP_MEMBER_ADD"
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	require.Equal(t, "👥 **我的邀请**\n待进群：0 人\n待绑定：1 人\n已奖励：0 人\n已退群：0 人", read())

	e.Timestamp = json.RawMessage(strconv.FormatInt(now.Add(time.Second).Unix(), 10))
	j.EventType = "GROUP_MEMBER_REMOVE"
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	require.Equal(t, "👥 **我的邀请**\n待进群：0 人\n待绑定：0 人\n已奖励：0 人\n已退群：1 人", read())
}

func TestQQBotMyInvitesOmitsOtherStatesAndKeepsHistoricalRewards(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at)
 VALUES('app','group','invitee','inviter',NOW()-INTERVAL '2 minutes',NOW()-INTERVAL '1 minute')`)
	require.NoError(t, err)
	read := func(config Config) string {
		t.Helper()
		info, readErr := s.myInvites(ctx, config, Job{MemberID: "inviter"})
		require.NoError(t, readErr)
		return info
	}
	zero := "👥 **我的邀请**\n待进群：0 人\n待绑定：0 人\n已奖励：0 人\n已退群：0 人"
	unbound := "👥 **我的邀请**\n待进群：0 人\n待绑定：1 人\n已奖励：0 人\n已退群：0 人"
	paid := "👥 **我的邀请**\n待进群：0 人\n待绑定：0 人\n已奖励：1 人\n已退群：0 人"
	require.Equal(t, unbound, read(c), "both users can be unbound")
	invitee := user(t, db)
	bind(t, db, invitee, "invitee")
	require.Equal(t, unbound, read(c), "the inviter can still be unbound")
	inviter := user(t, db)
	bind(t, db, inviter, "inviter")
	require.Equal(t, zero, read(c), "a bound invitation awaiting settlement must not count as unbound")
	for _, disabled := range []struct {
		name   string
		change func(*Config)
	}{
		{"bot disabled", func(c *Config) { c.Enabled = false }},
		{"zero reward", func(c *Config) { c.InviteReward = 0 }},
		{"zero limit", func(c *Config) { c.InviteDailyLimit = 0 }},
		{"group disabled", func(c *Config) { c.AllowedGroups = []string{"other-group"} }},
	} {
		t.Run(disabled.name, func(t *testing.T) {
			changed := c
			disabled.change(&changed)
			require.Equal(t, zero, read(changed), "paused invitations must not count as unbound")
		})
	}
	for _, update := range []string{
		`UPDATE users SET status='disabled' WHERE id=$1`,
		`UPDATE users SET status='active',deleted_at=NOW() WHERE id=$1`,
	} {
		_, err = db.Exec(update, invitee)
		require.NoError(t, err)
		require.Equal(t, zero, read(c), "unavailable bound accounts must not count as unbound")
	}
	_, err = db.Exec(`UPDATE users SET deleted_at=NULL WHERE id=$1`, invitee)
	require.NoError(t, err)
	c.InviteDailyLimit = 1
	result, err := s.reward(ctx, c, Job{GroupID: "group", MemberID: "invitee"}, "invite")
	require.NoError(t, err)
	require.Contains(t, result, "邀请奖励已发放")
	bind(t, db, user(t, db), "second-invitee")
	_, err = db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at)
 VALUES('app','group','second-invitee','inviter',NOW()-INTERVAL '2 minutes',NOW()-INTERVAL '1 minute')`)
	require.NoError(t, err)
	info := read(c)
	require.Equal(t, paid, info, "an invitation blocked by the daily limit must not count as unbound")
	_, err = db.Exec(`DELETE FROM qq_bot_bindings WHERE member_id IN ('inviter','invitee')`)
	require.NoError(t, err)
	bind(t, db, user(t, db), "inviter")
	info = read(c)
	require.Equal(t, paid, info, "rebinding preserves historical rewards and does not count a pending settlement as unbound")
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestQQBotMyInvitesHasNoPaginationLimitAndDoesNotGuessRelationships(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	zero := "👥 **我的邀请**\n待进群：0 人\n待绑定：0 人\n已奖励：0 人\n已退群：0 人"
	info, err := s.myInvites(ctx, c, Job{MemberID: "inviter"})
	require.NoError(t, err)
	require.Equal(t, zero, info)
	_, err = db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at)
 SELECT 'app',g,'invitee-'||n,'inviter',NOW()-INTERVAL '2 minutes',NOW()-INTERVAL '1 minute'
 FROM generate_series(1,150) n CROSS JOIN (VALUES ('group'),('other-group')) groups(g)`)
	require.NoError(t, err)
	info, err = s.myInvites(ctx, c, Job{MemberID: "inviter"})
	require.NoError(t, err)
	require.Equal(t, "👥 **我的邀请**\n待进群：0 人\n待绑定：150 人\n已奖励：0 人\n已退群：0 人", info)
	// A member with no attributed invitations must never inherit another QQ's
	// results, even if it sees the same group or passes a search-like identifier.
	for _, member := range []string{"new-member", "inviter' OR true --", ""} {
		info, err = s.myInvites(ctx, c, Job{MemberID: member})
		require.NoError(t, err)
		require.Equal(t, zero, info)
	}
}
