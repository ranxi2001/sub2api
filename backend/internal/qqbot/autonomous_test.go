package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestAutonomousConfigDefaultsAndValidation(t *testing.T) {
	var c Config
	require.NoError(t, json.Unmarshal([]byte(`{"autonomous":{"sample_percent":0,"daily_calls":0}}`), &c))
	require.Equal(t, 60, c.Autonomous.CooldownSeconds)
	require.Zero(t, c.Autonomous.SamplePercent)
	require.Zero(t, c.Autonomous.DailyCalls)
	c = DefaultConfig()
	require.False(t, c.Autonomous.Enabled)
	require.NoError(t, validateAutonomous(c))
	c.Autonomous.Enabled = true
	require.Error(t, validateAutonomous(c))
	c.Autonomous.Groups = []string{"group"}
	require.Error(t, validateAutonomous(c))
	c.AllowedGroups = []string{"group"}
	require.NoError(t, validateAutonomous(c))
	c.Autonomous.SamplePercent = 101
	require.Error(t, validateAutonomous(c))
	c = DefaultConfig()
	c.Autonomous.Names = []string{"@all"}
	require.Error(t, validateAutonomous(c))
}
func TestAutonomousNamesAndLocalFilters(t *testing.T) {
	a := DefaultAutonomousConfig()
	a.Names = append(a.Names, "小爱")
	for _, text := range []string{"CTAV，帮我看看这个问题", "ctav 请问怎么安装", "CTAV你能帮忙吗", "今天CTAV不错", "CTAVX 帮我", "CTAV的功能怎么样", "CTAV", "这个问题想让cTaV看看", "请帮我看看，小爱", "小爱"} {
		require.True(t, containsBotName(a, text), text)
	}
	for _, text := range []string{"", "   ", "帮我看看这个问题", "CTA", "小明"} {
		require.False(t, containsBotName(a, text), text)
	}
	require.False(t, containsBotName(AutonomousConfig{Names: []string{"", " "}}, "普通消息"))
	require.True(t, botNameCall(a, Event{Content: "小爱"}))
	require.True(t, botNameCall(a, Event{Content: "CTAV 看看 https://example.com"}))
	require.False(t, botNameCall(a, Event{Content: "/生图 CTAV"}))
	bot := Event{Content: "CTAV"}
	bot.Author.Bot = true
	require.False(t, botNameCall(a, bot))
	elements := []MessageElement{{MsgElements: []MessageElement{{Content: "这个问题想让小爱看看"}}}}
	require.True(t, botNameCall(a, Event{MsgElements: elements}))
	require.False(t, botNameCall(a, Event{MessageType: 103, MsgElements: elements}))
	require.False(t, botNameCall(a, Event{MsgElements: []MessageElement{{MessageType: 103, Content: "CTAV", MsgElements: elements}}}))
	for _, text := range []string{"哈哈哈哈", "嗯", "/签到", "有人知道 https://example.com 吗", "@张三 部署好了吗", "sk-secret-key", "这是验证码 12345"} {
		_, reason := autonomousText(Event{Content: text})
		require.NotEmpty(t, reason, text)
	}
	text, reason := autonomousText(Event{Content: "今天这个错误怎么处理？"})
	require.Empty(t, reason)
	require.NotEmpty(t, text)
	e := Event{Content: "这个问题怎么处理？"}
	e.MessageReference.MessageID = "another-user"
	_, reason = autonomousText(e)
	require.NotEmpty(t, reason)
	c := DefaultConfig()
	c.Autonomous.SamplePercent = 0
	require.False(t, sampledMessage(c, Event{ID: "a"}))
	c.Autonomous.SamplePercent = 100
	require.True(t, sampledMessage(c, Event{ID: "a"}))
}

func TestAutonomousImageCandidatesKeepContentFilters(t *testing.T) {
	image := Attachment{URL: "https://image.example/reaction.gif", ContentType: "image/gif"}
	for _, tc := range []struct {
		name string
		e    Event
		text string
	}{
		{"picture", Event{Attachments: []Attachment{image}}, ""},
		{"short caption", Event{Content: "哈哈", Attachments: []Attachment{image}}, "哈哈"},
		{"parallel picture", Event{MsgElements: []MessageElement{{Content: "看看", MsgElements: []MessageElement{{Attachments: []Attachment{image}}}}}}, "看看"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, reason := autonomousText(tc.e)
			require.Empty(t, reason)
			require.Equal(t, tc.text, text)
		})
	}
	for _, tc := range []struct {
		name   string
		e      Event
		reason string
	}{
		{"other mention", Event{Content: "@张三", Attachments: []Attachment{image}}, "addressed_elsewhere"},
		{"nested mention", Event{MsgElements: []MessageElement{{Content: "@张三", Attachments: []Attachment{image}}}}, "addressed_elsewhere"},
		{"nested command", Event{MsgElements: []MessageElement{{Content: "签到", Attachments: []Attachment{image}}}}, "addressed_elsewhere"},
		{"nested quote", Event{MsgElements: []MessageElement{{MessageType: 103, Attachments: []Attachment{image}}}}, "addressed_elsewhere"},
		{"link caption", Event{Content: "https://example.com", Attachments: []Attachment{image}}, "filtered_content"},
		{"nested credentials", Event{MsgElements: []MessageElement{{Content: "密码12345", Attachments: []Attachment{image}}}}, "filtered_content"},
		{"long caption", Event{Content: strings.Repeat("字", 1001), Attachments: []Attachment{image}}, "too_long"},
		{"non-image", Event{Content: "这个怎么处理", Attachments: []Attachment{{ContentType: "video/mp4"}}}, "non_text"},
		{"mixed media", Event{Attachments: []Attachment{image, {ContentType: "audio/ogg"}}}, "non_text"},
		{"too many pictures", Event{Attachments: []Attachment{image, image, image, image, image}}, "non_text"},
		{"unavailable sticker", Event{MsgElements: []MessageElement{{MessageType: 3}}}, "non_text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, reason := autonomousText(tc.e)
			require.Equal(t, tc.reason, reason)
		})
	}
}

func TestAutonomousImageRepetitionUsesAttachmentIdentity(t *testing.T) {
	first := Event{Attachments: []Attachment{{URL: "https://image.example/first", ContentType: "image/png"}}}
	second := Event{Attachments: []Attachment{{URL: "https://image.example/second", ContentType: "image/png"}}}
	require.NotEqual(t, autonomousContentHash(first, "哈哈"), autonomousContentHash(second, "哈哈"))
	require.Equal(t, autonomousContentHash(first, " 哈哈 "), autonomousContentHash(first, "哈哈"))
	first.Attachments[0].URL = "//image.example/second"
	require.Equal(t, autonomousContentHash(first, ""), autonomousContentHash(second, ""))
	require.Equal(t, codeHash("hello"), autonomousContentHash(Event{}, " HELLO "))
}

func TestAutonomousActiveBeijingWindow(t *testing.T) {
	a := DefaultAutonomousConfig()
	require.Equal(t, a.StartHour, a.EndHour)
	a.StartHour, a.EndHour = 9, 23
	at := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	require.True(t, autonomousActive(a, at))
	require.False(t, autonomousActive(a, at.Add(-time.Second)))
	require.False(t, autonomousActive(a, time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC)))
	a.StartHour, a.EndHour = 23, 9
	require.True(t, autonomousActive(a, at.Add(-time.Second)))
	require.False(t, autonomousActive(a, at))
	a.StartHour = a.EndHour
	require.True(t, autonomousActive(a, at))
}
func TestAutonomousStrictOutput(t *testing.T) {
	text, err := parseAutonomousReply(`{"action":"reply","text":"可以先看看错误日志。"}`, 100)
	require.NoError(t, err)
	require.Equal(t, "可以先看看错误日志。", text)
	text, err = parseAutonomousReply(`{"action":"skip"}`, 100)
	require.NoError(t, err)
	require.Empty(t, text)
	for _, raw := range []string{`{"action":"reply","text":"@大家"}`, `{"action":"reply","text":"<qqbot-at-everyone />"}`, `{"action":"reply","text":""}`, `{"action":"reply","text":"answer","other":true}`, `{"action":"skip"} {}`, "普通回复", `{"action":"reply","text":"` + strings.Repeat("字", 101) + `"}`} {
		_, err = parseAutonomousReply(raw, 100)
		require.Error(t, err, raw)
	}
}

func TestAutonomousOneSwitchDefaultsPreserveOverrides(t *testing.T) {
	old := DefaultAutonomousConfig()
	next := old
	next.Enabled = true
	got := normalizeAutonomous(old, next)
	require.True(t, got.InheritGroups)
	require.Equal(t, 90, got.FollowupSeconds)
	next.Groups = []string{"only-this-group"}
	next.DailyCalls = 0
	next.StartHour = 9
	got = normalizeAutonomous(old, next)
	require.False(t, got.InheritGroups)
	require.Equal(t, next.Groups, got.Groups)
	require.Zero(t, got.DailyCalls)
	require.Equal(t, 9, got.StartHour)
	legacy := AutonomousConfig{Enabled: true, Groups: []string{"group"}, Names: []string{"custom"}, SamplePercent: 15, CooldownSeconds: 120, MinMessages: 6, MemberCooldownSeconds: 600, HourlyReplies: 3, DailyReplies: 20, DailyCalls: 40, StartHour: 9, EndHour: 23, FollowupSeconds: 60, MaxChars: 100}
	got = normalizeAutonomous(old, legacy)
	require.Equal(t, 2, got.MinMessages)
	require.Equal(t, legacy.Names, got.Names)
	require.Equal(t, legacy.Groups, got.Groups)
	require.Equal(t, legacy, normalizeAutonomous(legacy, legacy), "loading/saving an enabled profile must not silently raise budgets")
	legacy.DailyCalls = 0
	require.Zero(t, normalizeAutonomous(old, legacy).DailyCalls)
}
func TestAutonomousFollowupAdmission(t *testing.T) {
	c := DefaultConfig()
	now := time.Now()
	st := autonomousState{LastReply: sql.NullTime{Time: now, Valid: true}, ReplyKind: "chat", ReplyMember: "alice"}
	require.True(t, canAutonomousFollowup(c, st, "alice", now, false))
	require.False(t, canAutonomousFollowup(c, st, "bob", now, false))
	require.False(t, canAutonomousFollowup(c, st, "alice", now.Add(2*time.Minute), false))
	st.Followups = 3
	require.False(t, canAutonomousFollowup(c, st, "alice", now, false))
	st.ReplyTrigger = "followup_claimed"
	require.True(t, canAutonomousFollowup(c, st, "alice", now, true))
	require.False(t, canAutonomousFollowup(c, st, "alice", now, false))
	c.Autonomous.FollowupSeconds = 0
	require.False(t, canAutonomousFollowup(c, st, "alice", now, true))
	for _, text := range []string{"真的？", "离谱", "没看懂", "不太对吧", "是这样啊"} {
		require.True(t, followupText(text), text)
	}
	for _, text := range []string{"好的", "谢谢！", "嗯", "哈哈哈", "OK", "？？", "😂😂"} {
		require.False(t, followupText(text), text)
	}
}

func TestAutonomousNaturalToneUsesExistingChatAndRespectsSwitch(t *testing.T) {
	c := DefaultConfig()
	c.Enabled = true
	c.AllowedGroups = []string{"group"}
	c.HistoryMessages = 0
	c.SystemPrompt = "站长原有人设"
	c.Autonomous.InheritGroups = true
	s := &Service{}
	messages := s.history(context.Background(), c, Job{GroupID: "group", MemberID: "alice"})
	require.NotContains(t, messages[1].Content, naturalConversationInstructions)
	c.Autonomous.Enabled = true
	messages = s.history(context.Background(), c, Job{GroupID: "group", MemberID: "alice"})
	require.Equal(t, c.SystemPrompt, messages[0].Content)
	require.Contains(t, messages[1].Content, naturalConversationInstructions)
	c.ChatDisabled = true
	messages = s.history(context.Background(), c, Job{GroupID: "group", MemberID: "alice"})
	require.NotContains(t, messages[1].Content, naturalConversationInstructions)
}
