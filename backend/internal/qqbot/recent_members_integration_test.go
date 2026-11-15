//go:build integration

package qqbot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQQBotRecentMembersIdentificationAndExpiry(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	ctx := context.Background()
	c, err := s.Config(ctx)
	require.NoError(t, err)
	c.HistoryMessages = 0
	_, err = s.SaveBasicConfig(ctx, c)
	require.NoError(t, err)
	e := sharedTestEvent("identify", "group", "alice", "我是小王，确认管理员身份 4826")
	e.Author.Username = ""
	enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
	var j Job
	require.NoError(t, db.QueryRow(`SELECT id FROM qq_bot_jobs WHERE message_id='identify'`).Scan(&j.ID))
	s.finish(j, "completed", "")
	var payload string
	require.NoError(t, db.QueryRow(`SELECT payload::text FROM qq_bot_jobs WHERE id=$1`, j.ID).Scan(&payload))
	require.Equal(t, "{}", payload)
	runtime, err := s.Runtime(ctx, c)
	require.NoError(t, err)
	require.Len(t, runtime.RecentMembers, 1)
	member := runtime.RecentMembers[0]
	require.Empty(t, member.Nickname)
	require.Equal(t, e.Content, member.MessagePreview)
	require.Equal(t, "alice", member.MemberID)
	require.False(t, member.LastSeenAt.IsZero())

	// Newest message wins, with a bound-account identity when QQ omits a name.
	_, err = db.Exec(`INSERT INTO users(id,email) VALUES(42,'alice@example.test');
 INSERT INTO qq_bot_bindings(app_id,member_id,group_id,user_id,nickname) VALUES('app','alice','group',42,'网站绑定小王')`)
	require.NoError(t, err)
	e.ID, e.Content = "latest", "确认管理员身份 9182"
	enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
	command := sharedTestEvent("command", "group", "bob", "/福利 兑换 SECRET-REWARD")
	enqueueSharedTestEvent(t, s, c, command, groupAtMessageCreate)
	_, err = db.Exec(`INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,member_id,payload,member_nickname,message_preview)
 VALUES('other-app','other-app','other-app','GROUP_AT_MESSAGE_CREATE','group','outsider','{}','wrong app','secret'),
 ('other-group','app','other-group','GROUP_AT_MESSAGE_CREATE','not-allowed','outsider','{}','wrong group','secret'),
 ('notice','app','notice','HEALTH_CHANGE_NOTIFICATION','group','notice-member','{}','notification','secret')`)
	require.NoError(t, err)
	members, err := s.recentMembers(ctx, c)
	require.NoError(t, err)
	require.Len(t, members, 2)
	for _, got := range members {
		if got.MemberID == "alice" {
			require.Equal(t, "网站绑定小王", got.Nickname)
			require.Equal(t, "alice@example.test", got.Email)
			require.Equal(t, e.Content, got.MessagePreview)
		} else {
			require.Equal(t, "bob", got.MemberID)
			require.Empty(t, got.MessagePreview)
		}
	}
	_, err = db.Exec(`UPDATE qq_bot_jobs SET created_at=NOW()-INTERVAL '25 hours'`)
	require.NoError(t, err)
	members, err = s.recentMembers(ctx, c)
	require.NoError(t, err)
	require.Empty(t, members)
	s.maintenance(ctx)
	var retained int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE member_nickname<>'' OR message_preview<>''`).Scan(&retained))
	require.Zero(t, retained)
}
