//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func seedDirectoryJob(t *testing.T, db *sql.DB, app, group, member, nickname, preview string, at time.Time) {
	t.Helper()
	id := randomID()
	_, err := db.Exec(`INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,member_id,message_id,payload,member_nickname,message_preview,created_at)
 VALUES($1,$2,$1,'GROUP_MESSAGE_CREATE',$3,$4,$1,'{}',$5,$6,$7)`, id, app, group, member, nickname, preview, at)
	require.NoError(t, err)
}

func TestMemberDirectorySearchAndPaginationAreScopedBeforeLimiting(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	now := time.Now()
	for i := range 251 {
		seedDirectoryJob(t, db, "app", "busy-group", fmt.Sprintf("noise-%03d", i), "其他群成员", "普通消息", now.Add(time.Duration(i)*time.Millisecond))
	}
	for i := range 121 {
		seedDirectoryJob(t, db, "app", "group", fmt.Sprintf("member-%03d", i), fmt.Sprintf("成员-%03d", i), "目标群近期消息", now.Add(-time.Duration(i+1)*time.Minute))
	}
	seedDirectoryJob(t, db, "other-app", "group", "other-app-member", "错误机器人", "绝不能泄露", now)
	f := MemberDirectoryFilter{AppID: "app", GroupID: "group", Page: 1, PageSize: 50}
	page, err := s.ListMembers(context.Background(), f)
	require.NoError(t, err)
	require.Equal(t, "app", page.AppID)
	require.Equal(t, 121, page.Total)
	require.Len(t, page.Items, 50)
	require.Equal(t, "member-000", page.Items[0].MemberID)
	f.Page = 3
	page, err = s.ListMembers(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, page.Items, 21)
	require.Equal(t, "member-100", page.Items[0].MemberID)
	f.Page = 4
	page, err = s.ListMembers(context.Background(), f)
	require.NoError(t, err)
	require.Empty(t, page.Items)
	require.Equal(t, 121, page.Total)
	f.Page, f.Search = 1, "成员-120"
	page, err = s.ListMembers(context.Background(), f)
	require.NoError(t, err)
	require.Equal(t, 1, page.Total)
	require.Equal(t, "member-120", page.Items[0].MemberID)
	f.GroupID, f.Search = "busy-group", "其他群"
	page, err = s.ListMembers(context.Background(), f)
	require.NoError(t, err)
	require.Equal(t, 251, page.Total, "administrator reads include observed groups outside AllowedGroups")
	f.GroupID, f.AppID = "group", "other-app"
	_, err = s.ListMembers(context.Background(), f)
	require.ErrorContains(t, err, "AppID")
}

func TestMemberDirectoryUsesScopedBindingsSavedSelectionsAndIncidentIdentities(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	c, err := s.Config(context.Background())
	require.NoError(t, err)
	c.GroupPolicies = []GroupPolicy{{GroupID: "group", Label: "开发交流一群"}}
	c.GroupAdmins = []GroupAdmin{{AppID: "app", GroupID: "group", MemberID: "legacy-admin", Nickname: "历史管理员备注"}}
	c.Moderation.Groups = []string{"group"}
	c.Moderation.Exemptions = []ModerationExemption{{GroupID: "group", MemberID: "legacy-white", Nickname: " 老成员备注 "}, {GroupID: "group", MemberID: "unlabeled-old"}}
	_, err = s.SaveConfig(context.Background(), c)
	require.NoError(t, err)
	c, err = s.Config(context.Background())
	require.NoError(t, err)
	require.Equal(t, "老成员备注", c.Moderation.Exemptions[0].Nickname)
	uid := user(t, db)
	_, err = db.Exec(`WITH updated AS (UPDATE users SET email='bound@example.test' WHERE id=$1 RETURNING id)
 INSERT INTO qq_bot_bindings(app_id,group_id,member_id,user_id,nickname) SELECT 'app','group','bound-member',id,'网站绑定昵称' FROM updated`, uid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO qq_bot_moderation_incidents(id,app_id,sandbox,group_id,member_id,policy_hash,reason,mute_seconds)
 VALUES('incident-only','app',false,'group','incident-member','policy','repeat',300),
 ('other-incident','other-app',false,'group','other-app-member','policy','repeat',300)`)
	require.NoError(t, err)
	seedDirectoryJob(t, db, "app", "other-group", "bound-member", "另一个群的名字", "另一群私有预览", time.Now())
	f := MemberDirectoryFilter{AppID: "app", GroupID: "group", Page: 1, PageSize: 100}
	page, err := s.ListMembers(context.Background(), f)
	require.NoError(t, err)
	require.Equal(t, 5, page.Total)
	byID := map[string]DirectoryMember{}
	for _, member := range page.Items {
		byID[member.MemberID] = member
		require.Equal(t, "开发交流一群", member.GroupLabel)
		require.Empty(t, member.LastSeenAt)
	}
	require.Equal(t, "网站绑定昵称", byID["bound-member"].Nickname)
	require.Equal(t, "bound@example.test", byID["bound-member"].Email)
	require.Empty(t, byID["bound-member"].MessagePreview)
	require.Equal(t, "历史管理员备注", byID["legacy-admin"].Nickname)
	require.Equal(t, "老成员备注", byID["legacy-white"].Nickname)
	require.Empty(t, byID["unlabeled-old"].Nickname)
	require.Empty(t, byID["incident-member"].Nickname)
	f.Search = "BOUND@EXAMPLE"
	page, err = s.ListMembers(context.Background(), f)
	require.NoError(t, err)
	require.Equal(t, 1, page.Total)
	f.Search, f.MemberID = "", "legacy-white"
	page, err = s.ListMembers(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	f.GroupID = "other-group"
	page, err = s.ListMembers(context.Background(), f)
	require.NoError(t, err)
	require.Zero(t, page.Total, "a saved selection cannot be resolved in a different group")
	f.MemberID = "bound-member"
	page, err = s.ListMembers(context.Background(), f)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Empty(t, page.Items[0].Email, "a binding from another group does not label this group identity")
	f.GroupID, f.MemberID = "group", "123456789"
	page, err = s.ListMembers(context.Background(), f)
	require.NoError(t, err)
	require.Zero(t, page.Total, "an arbitrary QQ number must never synthesize a candidate")
}

func TestMemberDirectoryPreservesPrivacyWindowAndVerifiesHistoryOwnership(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	now := time.Now()
	seedDirectoryJob(t, db, "app", "group", "expired", "过期昵称", "过期秘密消息", now.Add(-25*time.Hour))
	seedDirectoryJob(t, db, "app", "group", "recent", "最近昵称", "可搜索近期话题", now)
	for _, tc := range []struct {
		member, nickname, text, replyTo, speaker string
	}{
		{"ambient", "环境发言昵称", "只在群聊历史出现的本人消息", "", ""},
		{"quoted", "引用者本人昵称", "不得显示的引用正文", "quoted-id", ""},
		{"command", "命令昵称", "/绑定 SECRET-CODE", "", ""},
		{"wrong-owner", "伪造昵称", "不属于该成员的私有内容", "", "成员-wrong"},
	} {
		speaker := tc.speaker
		if speaker == "" {
			speaker = groupSpeaker("app", "group", tc.member)
		}
		raw, err := json.Marshal(groupHistoryEntry{Speaker: speaker, Nickname: tc.nickname, Text: tc.text, ReplyTo: tc.replyTo})
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO qq_bot_group_messages(app_id,group_id,member_id,message_key,role,content) VALUES('app','group',$1,$1,'user',$2)`, tc.member, string(raw))
		require.NoError(t, err)
	}
	f := MemberDirectoryFilter{AppID: "app", GroupID: "group", Page: 1, PageSize: 100}
	page, err := s.ListMembers(context.Background(), f)
	require.NoError(t, err)
	require.Equal(t, 6, page.Total)
	byID := map[string]DirectoryMember{}
	for _, member := range page.Items {
		byID[member.MemberID] = member
	}
	require.Empty(t, byID["expired"].Nickname)
	require.Empty(t, byID["expired"].MessagePreview)
	require.NotEmpty(t, byID["expired"].LastSeenAt)
	require.Equal(t, "环境发言昵称", byID["ambient"].Nickname)
	require.Equal(t, "只在群聊历史出现的本人消息", byID["ambient"].MessagePreview)
	require.Equal(t, "引用者本人昵称", byID["quoted"].Nickname)
	require.Empty(t, byID["quoted"].MessagePreview)
	require.Empty(t, byID["command"].MessagePreview)
	require.Empty(t, byID["wrong-owner"].Nickname)
	require.Empty(t, byID["wrong-owner"].MessagePreview)
	for _, search := range []string{"过期秘密", "引用正文", "SECRET-CODE", "伪造昵称", "不属于该成员"} {
		f.Search = search
		page, err = s.ListMembers(context.Background(), f)
		require.NoError(t, err)
		require.Zero(t, page.Total, search)
	}
	f.Search = "只在群聊历史"
	page, err = s.ListMembers(context.Background(), f)
	require.NoError(t, err)
	require.Equal(t, 1, page.Total)
	require.Equal(t, "ambient", page.Items[0].MemberID)
}

func TestMemberDirectoryAppChangeRequiresExplicitWhitelistClear(t *testing.T) {
	for _, oldApp := range []string{"app", ""} {
		t.Run("old_app="+oldApp, func(t *testing.T) {
			db := testDB(t)
			s := New(db, testEncryptor{}, true, nil)
			ctx := context.Background()
			if oldApp != "" {
				s = savedBot(t, db)
			}
			c, err := s.Config(ctx)
			require.NoError(t, err)
			c.AllowedGroups = []string{"group"}
			c.Moderation.Groups = []string{"group"}
			c.Moderation.Exemptions = []ModerationExemption{{GroupID: "group", MemberID: "old-member", Nickname: "旧机器人白名单姓名"}}
			if oldApp == "" {
				// Simulate a pre-existing unassigned configuration. The save path
				// must not silently adopt its identities for the first new AppID.
				raw, err := json.Marshal(c)
				require.NoError(t, err)
				_, err = db.Exec(`INSERT INTO settings(key,value) VALUES($1,$2)`, configKey, string(raw))
				require.NoError(t, err)
			} else {
				_, err = s.SaveConfig(ctx, c)
				require.NoError(t, err)
			}
			c, err = s.Config(ctx)
			require.NoError(t, err)
			var storedBefore, storedAfter string
			require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key=$1`, configKey).Scan(&storedBefore))
			draft := c
			draft.AppID, draft.AppSecret = "new-app", "new-secret"
			_, err = s.SaveConfigSection(ctx, "connection", sectionVersion(c, "connection"), draft)
			require.ErrorContains(t, err, "先清空消息治理白名单")
			require.NoError(t, db.QueryRow(`SELECT value FROM settings WHERE key=$1`, configKey).Scan(&storedAfter))
			require.Equal(t, storedBefore, storedAfter, "a rejected application change must not delete or reinterpret saved entries")
			unchanged, err := s.Config(ctx)
			require.NoError(t, err)
			require.Equal(t, oldApp, unchanged.AppID)
			require.Equal(t, c.Moderation.Exemptions, unchanged.Moderation.Exemptions)
			// Explicitly clearing the existing application's list is a separate
			// reviewable save. The subsequent connection-only save can succeed.
			clearDraft := unchanged
			clearDraft.Moderation.Exemptions = nil
			_, err = s.SaveConfigSection(ctx, "moderation", sectionVersion(unchanged, "moderation"), clearDraft)
			require.NoError(t, err)
			cleared, err := s.Config(ctx)
			require.NoError(t, err)
			draft = cleared
			draft.AppID, draft.AppSecret = "new-app", "new-secret"
			_, err = s.SaveConfigSection(ctx, "connection", sectionVersion(cleared, "connection"), draft)
			require.NoError(t, err)
			current, err := s.Config(ctx)
			require.NoError(t, err)
			require.Equal(t, "new-app", current.AppID)
			require.Empty(t, current.Moderation.Exemptions)
			page, err := s.ListMembers(ctx, MemberDirectoryFilter{AppID: "new-app", GroupID: "group", Page: 1, PageSize: 20})
			require.NoError(t, err)
			require.Empty(t, page.Items)
			page, err = s.ListMembers(ctx, MemberDirectoryFilter{AppID: "new-app", GroupID: "group", MemberID: "old-member", Page: 1, PageSize: 20})
			require.NoError(t, err)
			require.Empty(t, page.Items)
		})
	}
}
