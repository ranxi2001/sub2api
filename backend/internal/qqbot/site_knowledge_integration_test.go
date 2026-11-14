//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSiteKnowledgeScopedSaveConflictAndLegacyClients(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	original, err := s.Config(ctx)
	require.NoError(t, err)
	draft := original.Public()
	draft.SiteKnowledge = SiteKnowledgeConfig{Enabled: true, Content: "官网：https://example.test\n使用教程：请在个人资料中创建 API Key。"}
	draft.ChatModel = "must not change"
	saved, err := s.SaveConfigSection(ctx, "knowledge", draft.SectionVersions["knowledge"], draft)
	require.NoError(t, err)
	require.Equal(t, draft.SiteKnowledge, saved.SiteKnowledge)
	require.Equal(t, original.ChatModel, saved.ChatModel)
	_, err = s.SaveConfigSection(ctx, "knowledge", draft.SectionVersions["knowledge"], draft)
	require.ErrorIs(t, err, ErrConfigConflict)
	_, err = s.SaveConfigSection(ctx, "knowledge", "", draft)
	require.ErrorIs(t, err, ErrConfigConflict)
	// An older page can still save an unrelated module from its original draft.
	legacy := original.Public()
	legacy.ChatModel = "new-chat-model"
	_, err = s.SaveConfigSection(ctx, "chat", legacy.SectionVersions["chat"], legacy)
	require.NoError(t, err)
	current, err := s.Config(ctx)
	require.NoError(t, err)
	require.Equal(t, draft.SiteKnowledge, current.SiteKnowledge)
	// Older full-config clients do not have this field at all. The legacy
	// endpoint preserves the independently managed knowledge even if omitted.
	raw, err := json.Marshal(current.Public())
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	delete(fields, "site_knowledge")
	raw, err = json.Marshal(fields)
	require.NoError(t, err)
	var oldClient Config
	require.NoError(t, json.Unmarshal(raw, &oldClient))
	oldClient.ChatModel = "legacy-new-model"
	_, err = s.SaveBasicConfig(WithConfigVersion(ctx, ConfigVersion(current)), oldClient)
	require.NoError(t, err)
	current, err = s.Config(ctx)
	require.NoError(t, err)
	require.Equal(t, "legacy-new-model", current.ChatModel)
	require.Equal(t, draft.SiteKnowledge, current.SiteKnowledge)
	disabled := current.Public()
	disabled.SiteKnowledge.Enabled = false
	saved, err = s.SaveConfigSection(ctx, "knowledge", disabled.SectionVersions["knowledge"], disabled)
	require.NoError(t, err)
	require.False(t, saved.SiteKnowledge.Enabled)
	require.Equal(t, draft.SiteKnowledge.Content, saved.SiteKnowledge.Content)
	changes, err := s.ConfigHistory(ctx)
	require.NoError(t, err)
	var knowledgeChanges []ConfigChange
	for _, change := range changes {
		if change.Section == "knowledge" {
			knowledgeChanges = append(knowledgeChanges, change)
		}
	}
	require.Len(t, knowledgeChanges, 2)
	require.JSONEq(t, `{"enabled":false,"content":""}`, string(knowledgeChanges[1].Before["site_knowledge"]))
	// History is newest first; the first change is the disabling operation.
	var last SiteKnowledgeConfig
	require.NoError(t, json.Unmarshal(knowledgeChanges[0].After["site_knowledge"], &last))
	require.False(t, last.Enabled)
	require.Equal(t, draft.SiteKnowledge.Content, last.Content)
}

func TestSiteKnowledgeEntriesScopedSaveAndQuestionIsolation(t *testing.T) {
	ctx := context.Background()
	s := savedBot(t, testDB(t))
	c, err := s.Config(ctx)
	require.NoError(t, err)
	setup := knowledgeEntry("setup", "API 接入教程")
	setup.Keywords = []string{"接入", "密钥"}
	setup.URL = "https://example.test/setup"
	setup.UpdatedAt = "forged"
	recharge := knowledgeEntry("recharge", "充值说明")
	draft := c.Public()
	draft.SiteKnowledge = SiteKnowledgeConfig{Enabled: true, Entries: []SiteKnowledgeEntry{setup, recharge}}
	saved, err := s.SaveConfigSection(ctx, "knowledge", draft.SectionVersions["knowledge"], draft)
	require.NoError(t, err)
	require.Len(t, saved.SiteKnowledge.Entries, 2)
	at := saved.SiteKnowledge.Entries[0].UpdatedAt
	_, err = time.Parse(time.RFC3339, at)
	require.NoError(t, err)
	require.NotEqual(t, "forged", at)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	c.HistoryMessages = 0
	messages := s.historyForQuestion(ctx, c, Job{GroupID: "group", MemberID: "member"}, "如何接入 API？")
	require.Contains(t, sharedHistoryText(messages), "API 接入教程")
	require.NotContains(t, sharedHistoryText(messages), "充值说明")
	require.NotContains(t, sharedHistoryText(s.historyForQuestion(ctx, c, Job{GroupID: "group", MemberID: "member"}, "你好")), "API 接入教程")
	same := c.Public()
	same.SiteKnowledge.Entries[0].UpdatedAt = "forged-again"
	unchanged, err := s.SaveConfigSection(ctx, "knowledge", same.SectionVersions["knowledge"], same)
	require.NoError(t, err)
	require.Equal(t, at, unchanged.SiteKnowledge.Entries[0].UpdatedAt)
}

func TestSiteKnowledgeSharedAndMemberHistoryUseCurrentConfig(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	c.SiteKnowledge = SiteKnowledgeConfig{Enabled: true, Content: "SITE_KNOWLEDGE_CURRENT"}
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	enqueueSharedTestEvent(t, s, c, sharedTestEvent("knowledge-chat", "group", "member", "请问怎么接入？"), groupMessageCreate)
	for _, shared := range []bool{false, true} {
		messages := s.loadHistory(ctx, c, Job{GroupID: "group", MemberID: "member"}, shared)
		require.Len(t, messages, 4)
		require.Contains(t, messages[2].Content, "SITE_KNOWLEDGE_CURRENT")
		require.Contains(t, messages[3].Content, "请问怎么接入？")
	}
	var copies int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_group_messages WHERE content LIKE '%SITE_KNOWLEDGE_CURRENT%'`).Scan(&copies))
	require.Zero(t, copies, "knowledge must not be copied into member or shared history")
	version := sectionVersion(c, "knowledge")
	c.SiteKnowledge.Enabled = false
	_, err = s.SaveConfigSection(ctx, "knowledge", version, c)
	require.NoError(t, err)
	c, err = s.Config(ctx)
	require.NoError(t, err)
	for _, shared := range []bool{false, true} {
		messages := s.loadHistory(ctx, c, Job{GroupID: "group", MemberID: "member"}, shared)
		require.Len(t, messages, 3)
		require.NotContains(t, sharedHistoryText(messages), "SITE_KNOWLEDGE_CURRENT")
	}
}
