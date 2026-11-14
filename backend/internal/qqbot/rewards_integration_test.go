//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQQBotMyRewardsSummarizesHistoryByCategory(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	uid, otherUser := user(t, db), user(t, db)
	bind(t, db, uid, "member")
	job := Job{MemberID: "member"}
	info, err := s.myRewards(ctx, c, job)
	require.NoError(t, err)
	require.Equal(t, "🎁 **累计奖励**\n合计：USD 0.00\n\n抽奖：USD 0.00\n邀请：USD 0.00\n签到：USD 0.00\n成长：USD 0.00\n连签：USD 0.00\n排行：USD 0.00\n\n活动优惠券：累计 0 张，当前可用 0 张\n发送「我的优惠券」查看网站券包及使用规则。", info)
	_, err = db.Exec(`INSERT INTO qq_bot_rewards(reward_key,app_id,group_id,member_id,user_id,kind,amount,created_at) VALUES
 ('lottery-1','app','group','member',$1,'lottery',2.50,NOW()-INTERVAL '60 days'),
 ('lottery-2','app','other-group','member',$1,'lottery',0.75,NOW()),
 ('lottery-zero','app','group','member',$1,'lottery',0,NOW()),
 ('invite','app','group','member',$1,'invite',2.50,NOW()),
 ('checkin-1','app','group','member',$1,'checkin',0.10,NOW()-INTERVAL '1 day'),
 ('checkin-2','app','other-group','member',$1,'checkin',0.20,NOW()),
 ('growth','app','group','member',$1,'growth',1.25,NOW()),
 ('streak','app','group','member',$1,'streak',0.50,NOW()),
 ('ranking','app','group','member',$1,'ranking',0.75,NOW()),
 ('other-user','app','group','someone-else',$2,'lottery',99,NOW())`, uid, otherUser)
	require.NoError(t, err)
	info, err = s.myRewards(ctx, c, job)
	require.NoError(t, err)
	require.Equal(t, "🎁 **累计奖励**\n合计：USD 8.55\n\n抽奖：USD 3.25\n邀请：USD 2.50\n签到：USD 0.30\n成长：USD 1.25\n连签：USD 0.50\n排行：USD 0.75\n\n活动优惠券：累计 0 张，当前可用 0 张\n发送「我的优惠券」查看网站券包及使用规则。", info)

	_, err = db.Exec(`INSERT INTO qq_bot_rewards(reward_key,app_id,group_id,member_id,user_id,kind,amount) VALUES('historical','app','group','member',$1,'redemption',2)`, uid)
	require.NoError(t, err)
	info, err = s.myRewards(ctx, c, job)
	require.NoError(t, err)
	require.Contains(t, info, "历史奖励：USD 2.00")
	require.Contains(t, info, "合计：USD 10.55")

	info, err = s.myRewards(ctx, c, Job{MemberID: "unbound"})
	require.NoError(t, err)
	require.Equal(t, "请先发送“绑定 邮箱或绑定码”绑定网站账号。", info)
	_, err = db.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, uid)
	require.NoError(t, err)
	info, err = s.myRewards(ctx, c, job)
	require.NoError(t, err)
	require.Equal(t, "请先发送“绑定 邮箱或绑定码”绑定网站账号。", info)
}

func TestQQBotMyRewardsCommandSettlesInvitesWithoutDuplicatePayouts(t *testing.T) {
	db := testDB(t)
	insightsUsageTable(t, db)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	inviter := user(t, db)
	bind(t, db, inviter, "inviter")
	for _, member := range []string{"first-invitee", "second-invitee"} {
		bind(t, db, user(t, db), member)
	}
	var reply string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
		case "/v2/groups/group/messages":
			body := readQQTestReply(t, r)
			reply = body.text()
			_, _ = w.Write([]byte(`{"id":"sent"}`))
		default:
			t.Errorf("reward command called unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	query := func(id, member string) {
		t.Helper()
		e := Event{ID: id, GroupID: "group", Content: "/我的奖励"}
		e.Author.MemberID = member
		raw, marshalErr := json.Marshal(e)
		require.NoError(t, marshalErr)
		require.NoError(t, s.Enqueue(ctx, c, Payload{ID: id, Type: groupMessageCreate, Data: raw}))
		s.runOne(ctx)
		require.Contains(t, reply, "<qqbot-at-user id=\""+member+"\" />\n🎁 **累计奖励**")
		require.Contains(t, reply, "🎁 **累计奖励**")
		for _, category := range []string{"抽奖", "签到", "成长", "连签", "排行"} {
			require.Contains(t, reply, category+"：USD 0.00")
		}
	}
	addInvite := func(group, member string) {
		t.Helper()
		_, insertErr := db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,inviter_id,invited_at,joined_at) VALUES('app',$1,$2,'inviter',NOW()-INTERVAL '1 minute',NOW())`, group, member)
		require.NoError(t, insertErr)
	}
	addInvite("group", "first-invitee")
	query("invitee-query", "first-invitee")
	require.Contains(t, reply, "邀请：USD 0.00")
	require.Contains(t, reply, "合计：USD 0.00")
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Equal(t, c.InviteReward, balance)

	// The inviter can retry eligible invitations from another allowed group too.
	addInvite("other-group", "second-invitee")
	for _, id := range []string{"inviter-query", "repeat-query"} {
		query(id, "inviter")
		require.Contains(t, reply, "邀请：USD 2.00")
		require.Contains(t, reply, "合计：USD 2.00")
	}
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, inviter).Scan(&balance))
	require.Equal(t, 2*c.InviteReward, balance)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards WHERE kind='invite' AND user_id=$1`, inviter).Scan(&count))
	require.Equal(t, 2, count)
}
