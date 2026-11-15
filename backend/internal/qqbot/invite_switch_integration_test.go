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

func TestInviteSwitchBlocksSettlementAndPreservesRulesOnReenable(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	insightsUsageTable(t, db)
	stale, err := s.Config(ctx)
	require.NoError(t, err)
	inviter, invitee := user(t, db), user(t, db)
	bind(t, db, inviter, "inviter")
	bind(t, db, invitee, "invitee")
	invite, err := s.InviteConfig(ctx)
	require.NoError(t, err)
	require.True(t, invite.Enabled)
	invite.Enabled = false
	require.NoError(t, s.SaveInviteConfig(ctx, invite))
	disabled, err := s.Config(ctx)
	require.NoError(t, err)
	require.False(t, disabled.InviteEnabled)
	require.Equal(t, stale.InviteReward, disabled.InviteReward)
	require.Equal(t, stale.InviteDailyLimit, disabled.InviteDailyLimit)

	// A queued event may hold an enabled snapshot when the administrator disables awards.
	now := time.Now().Truncate(time.Second)
	e := Event{GroupID: "group", MemberID: "invitee", ApplySource: "invited", InvitedBy: "inviter", ApplyAt: now.Add(-time.Minute).Format(time.RFC3339)}
	j := Job{AppID: stale.AppID, GroupID: e.GroupID, MemberID: e.MemberID, EventType: "GROUP_JOIN_REQUEST"}
	require.NoError(t, s.memberEvent(ctx, stale, j, e))
	j.EventType, e.Timestamp = "GROUP_MEMBER_ADD", json.RawMessage(strconv.FormatInt(now.Unix(), 10))
	require.NoError(t, s.memberEvent(ctx, stale, j, e))
	text, err := s.reward(ctx, stale, j, "invite")
	require.NoError(t, err)
	require.Contains(t, text, "未开启")
	for _, member := range []string{"inviter", "invitee"} {
		require.NoError(t, s.SettleInvites(ctx, member))
		_, err = s.myRewards(ctx, disabled, Job{AppID: disabled.AppID, GroupID: "group", MemberID: member})
		require.NoError(t, err)
	}
	text, err = s.inviteInfo(ctx, disabled, Job{MemberID: "inviter"})
	require.NoError(t, err)
	require.Contains(t, text, "邀请活动未开启")
	text, err = s.querySiteInfo(ctx, stale, Job{GroupID: "group", MemberID: "inviter"}, siteQuery{Kind: "activity_rules", Activity: "invite"})
	require.NoError(t, err)
	require.Contains(t, text, "邀请活动未开启")
	preview, err := s.PreviewActivities(ctx, inviter)
	require.NoError(t, err)
	require.False(t, preview.InviteEligible)
	page, err := s.ListActivityRecords(ctx, "invite", "", 1, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "disabled", page.Items[0].Status)
	var count int
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards WHERE kind='invite'`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Zero(t, balance)

	// Reopening reuses the amount and limit and can settle relationships recorded while closed.
	invite.Enabled = true
	require.NoError(t, s.SaveInviteConfig(ctx, invite))
	got, err := s.InviteConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, invite, got)
	preview, err = s.PreviewActivities(ctx, inviter)
	require.NoError(t, err)
	require.True(t, preview.InviteEligible)
	for range 2 {
		require.NoError(t, s.SettleInvites(ctx, "inviter"))
	}
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Equal(t, stale.InviteReward, balance)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards WHERE kind='invite'`).Scan(&count))
	require.Equal(t, 1, count)
	page, err = s.ListActivityRecords(ctx, "invite", "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, "rewarded", page.Items[0].Status)
}

func TestInviteSwitchSurvivesBasicSavesAndRejectsStaleActivityDrafts(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	basic, err := s.Config(ctx)
	require.NoError(t, err)
	first, err := s.InviteConfig(ctx)
	require.NoError(t, err)
	version := ConfigVersion(first)
	disabled := first
	disabled.Enabled = false
	require.NoError(t, s.SaveInviteConfig(WithConfigVersion(ctx, version), disabled))
	got, err := s.InviteConfig(ctx)
	require.NoError(t, err)
	require.NotEqual(t, version, ConfigVersion(got), "switch changes must invalidate old activity drafts")
	require.ErrorIs(t, s.SaveInviteConfig(WithConfigVersion(ctx, version), first), ErrConfigConflict)
	// An older basic form must not restore its enabled switch or overwrite reward rules.
	basic.ChatModel = "saved-basic-after-invite-disable"
	basic.InviteReward = 99
	basic.InviteDailyLimit = 99
	_, err = s.SaveBasicConfig(ctx, basic)
	require.NoError(t, err)
	got, err = s.InviteConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, disabled, got)
	current, err := s.Config(ctx)
	require.NoError(t, err)
	require.Equal(t, basic.ChatModel, current.ChatModel)
	require.False(t, current.Public().InviteEnabled)
}

func TestInviteSwitchLegacyActivityRequestsPreserveCurrentValue(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	for _, initial := range []bool{false, true} {
		for _, switchField := range []string{"", `,"enabled":null`} {
			current, err := s.InviteConfig(ctx)
			require.NoError(t, err)
			current.Enabled = initial
			require.NoError(t, s.SaveInviteConfig(ctx, current))
			var legacy InviteConfig
			require.NoError(t, json.Unmarshal([]byte(`{"invite_reward":2.5,"invite_daily_limit":7`+switchField+`}`), &legacy))
			require.NoError(t, s.SaveInviteConfig(ctx, legacy))
			got, err := s.InviteConfig(ctx)
			require.NoError(t, err)
			require.Equal(t, initial, got.Enabled)
			require.Equal(t, 2.5, got.InviteReward)
			require.Equal(t, 7, got.InviteDailyLimit)
		}
	}
	var explicit InviteConfig
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":false,"invite_reward":2.5,"invite_daily_limit":7}`), &explicit))
	require.NoError(t, s.SaveInviteConfig(ctx, explicit))
	got, err := s.InviteConfig(ctx)
	require.NoError(t, err)
	require.False(t, got.Enabled)
}

func TestInviteSwitchLoadsLegacyPersistedConfiguration(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	for _, tc := range []struct {
		amount float64
		limit  int
		want   bool
	}{{1.25, 10, true}, {0, 10, false}, {1.25, 0, false}} {
		_, err := db.Exec(`UPDATE settings SET value=((value::jsonb-'invite_enabled') || jsonb_build_object('invite_reward',$2::numeric,'invite_daily_limit',$3::integer))::text WHERE key=$1`, configKey, tc.amount, tc.limit)
		require.NoError(t, err)
		got, err := s.InviteConfig(ctx)
		require.NoError(t, err)
		require.Equal(t, tc.want, got.Enabled)
		got.Enabled = false
		require.NoError(t, s.SaveInviteConfig(ctx, got))
		c, err := s.Config(ctx)
		require.NoError(t, err)
		require.False(t, c.InviteEnabled, "explicit false must remain off despite positive legacy amounts")
	}
}
