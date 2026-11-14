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

func TestQQActivityRecordsFollowOfficialQQInvitationEvents(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	inviter, invitee := user(t, db), user(t, db)
	bind(t, db, inviter, "inviter")
	_, err = db.Exec(`UPDATE users SET email=CASE WHEN id=$1 THEN 'inviter@example.com' ELSE 'invitee@example.com' END`, inviter)
	require.NoError(t, err)
	now := time.Now().Truncate(time.Second)
	e := Event{GroupID: "group", MemberID: "invitee", ApplySource: "invited", InvitedBy: "inviter", ApplyAt: now.Add(-2 * time.Minute).Format(time.RFC3339)}
	j := Job{AppID: "app", GroupID: "group", MemberID: "invitee", EventType: "GROUP_JOIN_REQUEST"}
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	read := func() ActivityRecord {
		t.Helper()
		page, readErr := s.ListActivityRecords(ctx, "invite", "", 1, 20)
		require.NoError(t, readErr)
		require.Equal(t, 1, page.Total)
		require.Len(t, page.Items, 1)
		return page.Items[0]
	}
	r := read()
	require.Equal(t, "pending_join", r.Status)
	require.True(t, r.Inviter.Bound)
	require.False(t, r.Participant.Bound)
	require.Equal(t, "inviter@example.com", r.Inviter.Email)
	require.Nil(t, r.JoinedAt)

	j.EventType, e.Timestamp = "GROUP_MEMBER_ADD", json.RawMessage(strconv.FormatInt(now.Add(-time.Minute).Unix(), 10))
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	require.Equal(t, "invitee_unbound", read().Status)
	bind(t, db, invitee, "invitee")
	require.Equal(t, "pending_reward", read().Status)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards`).Scan(&count))
	require.Zero(t, count, "reading records must not pay pending rewards")
	_, err = s.reward(ctx, c, j, "invite")
	require.NoError(t, err)
	r = read()
	require.Equal(t, "rewarded", r.Status)
	require.Equal(t, 1.0, r.Amount)
	require.NotNil(t, r.RewardedAt)
	require.Equal(t, inviter, r.RewardUserID)

	// Rebinding must not attribute a historical credit to the new account.
	_, err = db.Exec(`DELETE FROM qq_bot_bindings WHERE member_id IN ('inviter','invitee')`)
	require.NoError(t, err)
	replacement := user(t, db)
	bind(t, db, replacement, "inviter")
	r = read()
	require.Equal(t, "rewarded", r.Status)
	require.Equal(t, replacement, r.Inviter.UserID)
	require.False(t, r.Participant.Bound)
	require.Equal(t, inviter, r.RewardUserID)
	require.Equal(t, "inviter@example.com", r.RewardEmail)

	j.EventType, e.Timestamp = "GROUP_MEMBER_REMOVE", json.RawMessage(strconv.FormatInt(now.Unix(), 10))
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	r = read()
	require.Equal(t, "rewarded", r.Status)
	require.NotNil(t, r.LeftAt, "membership is separate from historical payout status")

	// Ordinary joins do not invent an inviter. Neither website referrals nor
	// another QQ application's events may appear in this application's list.
	e.MemberID, j.MemberID, j.EventType = "ordinary-member", "ordinary-member", "GROUP_MEMBER_ADD"
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	e.MemberID, j.MemberID, j.EventType, e.ApplySource = "not-invited", "not-invited", "GROUP_JOIN_REQUEST", "search"
	require.NoError(t, s.memberEvent(ctx, c, j, e))
	_, err = db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id) VALUES('other-app','group','member','inviter')`)
	require.NoError(t, err)
	read()
	page, err := s.ListActivityRecords(ctx, "invite", "INVITER", 1, 1)
	require.NoError(t, err)
	require.Equal(t, 1, page.Total)
	page, err = s.ListActivityRecords(ctx, "invite", "%", 1, 20)
	require.NoError(t, err)
	require.Empty(t, page.Items, "search wildcards are literal")
}

func TestQQActivityRecordsDistinguishOtherGroupRewardsAndBindingConditions(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	inviter, invitee := user(t, db), user(t, db)
	bind(t, db, inviter, "inviter")
	bind(t, db, invitee, "invitee")
	_, err := db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at) VALUES
 ('app','group','invitee','inviter',NOW()-INTERVAL '2 minutes',NOW()-INTERVAL '1 minute'),
 ('app','other-group','invitee','other-inviter',NOW()-INTERVAL '2 minutes',NOW()-INTERVAL '1 minute'),
 ('app','group','unbound','other-inviter',NOW()-INTERVAL '2 minutes',NOW()-INTERVAL '1 minute')`)
	require.NoError(t, err)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	_, err = s.reward(ctx, c, Job{GroupID: "group", MemberID: "invitee"}, "invite")
	require.NoError(t, err)
	page, err := s.ListActivityRecords(ctx, "invite", "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, 3, page.Total)
	for _, r := range page.Items {
		switch {
		case r.Participant.MemberID == "unbound":
			require.Equal(t, "both_unbound", r.Status)
		case r.GroupID == "other-group":
			require.Equal(t, "already_rewarded", r.Status)
			require.Zero(t, r.Amount)
			require.Nil(t, r.RewardedAt)
		default:
			require.Equal(t, "rewarded", r.Status)
		}
	}
}

func TestQQActivityRecordsKeepZeroAwardsHistoryAndPaginate(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	uid := user(t, db)
	bind(t, db, uid, "member")
	_, err := db.Exec(`UPDATE users SET email='history@example.com' WHERE id=$1`, uid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO qq_bot_rewards(reward_key,app_id,group_id,member_id,user_id,kind,amount) VALUES
 ('c1','app','group','member',$1,'checkin',0),('c2','app','group','member',$1,'checkin',0.12345678),
 ('c3','other-app','group','member',$1,'checkin',5),('i1','app','group','member',$1,'invite',1)`, uid)
	require.NoError(t, err)
	page, err := s.ListActivityRecords(ctx, "checkin", "HISTORY@", 1, 1)
	require.NoError(t, err)
	require.Equal(t, 2, page.Total)
	require.Len(t, page.Items, 1)
	require.Equal(t, 0.12345678, page.Items[0].Amount)
	firstID := page.Items[0].ID
	page, err = s.ListActivityRecords(ctx, "checkin", "history@", 2, 1)
	require.NoError(t, err)
	require.Zero(t, page.Items[0].Amount)
	require.NotEqual(t, firstID, page.Items[0].ID)
	_, err = db.Exec(`DELETE FROM qq_bot_bindings WHERE user_id=$1`, uid)
	require.NoError(t, err)
	bind(t, db, user(t, db), "member")
	page, err = s.ListActivityRecords(ctx, "checkin", "history@", 1, 20)
	require.NoError(t, err)
	require.False(t, page.Items[0].Participant.Bound)
	require.Equal(t, uid, page.Items[0].Participant.UserID)
	_, err = db.Exec(`INSERT INTO qq_bot_lottery_draws(request_key,app_id,member_id,group_id,user_id,prize_name,amount,config_snapshot,recharge_total,earned,remaining,result_text) VALUES
 ('draw1','app','member','group',$1,'Thanks',0,'{}',10,1,0,'Thanks'),
 ('draw2','other-app','member','group',$1,'Hidden',5,'{}',10,1,0,'Hidden')`, uid)
	require.NoError(t, err)
	page, err = s.ListActivityRecords(ctx, "lottery", "history@", 1, 20)
	require.NoError(t, err)
	require.Equal(t, 1, page.Total)
	require.Len(t, page.Items, 1)
	require.Equal(t, "no_prize", page.Items[0].Status)
	require.Equal(t, "Thanks", page.Items[0].PrizeName)
	require.NotNil(t, page.Items[0].Remaining)
	require.Zero(t, *page.Items[0].Remaining)
	page, err = s.ListActivityRecords(ctx, "checkin", "", 99, 20)
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.Equal(t, 2, page.Total)
}
