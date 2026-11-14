package qqbot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCustomCommandValidation(t *testing.T) {
	valid := CustomCommand{Command: "/a", Reply: "第一行\n第二行", Enabled: true}
	for _, tt := range []struct {
		name      string
		commands  []CustomCommand
		wantError string
	}{
		{name: "empty"},
		{name: "multiline", commands: []CustomCommand{valid}},
		{name: "unicode and punctuation", commands: []CustomCommand{{Command: "/使用教程_1-帮助", Reply: "教程"}}},
		{name: "case sensitive", commands: []CustomCommand{valid, {Command: "/A", Reply: "大写"}}},
		{name: "reply length boundary", commands: []CustomCommand{{Command: "/a", Reply: strings.Repeat("字", 2000)}}},
		{name: "name length boundary", commands: []CustomCommand{{Command: "/" + strings.Repeat("字", 32), Reply: "好"}}},
		{name: "duplicate including disabled", commands: []CustomCommand{valid, {Command: "/a", Reply: "重复"}}, wantError: "重复"},
		{name: "built in", commands: []CustomCommand{{Command: "/签到", Reply: "覆盖"}}, wantError: "内置指令"},
		{name: "built in alias", commands: []CustomCommand{{Command: "/画图", Reply: "覆盖"}}, wantError: "内置指令"},
		{name: "help alias", commands: []CustomCommand{{Command: "/help", Reply: "覆盖"}}, wantError: "内置指令"},
		{name: "unnormalized", commands: []CustomCommand{{Command: "a", Reply: "好"}}, wantError: "名称须为"},
		{name: "arguments", commands: []CustomCommand{{Command: "/a 参数", Reply: "好"}}, wantError: "名称须为"},
		{name: "mention markup", commands: []CustomCommand{{Command: "/<@bot>", Reply: "好"}}, wantError: "名称须为"},
		{name: "long name", commands: []CustomCommand{{Command: "/" + strings.Repeat("字", 33), Reply: "好"}}, wantError: "名称须为"},
		{name: "blank reply", commands: []CustomCommand{{Command: "/a", Reply: " \n\t"}}, wantError: "回复内容"},
		{name: "long reply", commands: []CustomCommand{{Command: "/a", Reply: strings.Repeat("字", 2001)}}, wantError: "回复内容"},
		{name: "too many", commands: make([]CustomCommand, 21), wantError: "最多 20 条"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := DefaultConfig()
			c.CustomCommands = tt.commands
			err := validateConfig(c)
			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantError)
			}
		})
	}
	input := []CustomCommand{{Command: " a ", Reply: "\n第一行\n第二行\n", Enabled: true}}
	normalized := normalizeCustomCommands(input)
	require.Equal(t, []CustomCommand{{Command: "/a", Reply: "第一行\n第二行", Enabled: true}}, normalized)
	require.Equal(t, " a ", input[0].Command, "normalization must not mutate the caller's slice")
	require.NotNil(t, normalizeCustomCommands(nil))
}

func TestCustomCommandRepliesAndMessageFiltering(t *testing.T) {
	var replies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
		case "/v2/groups/group/bot_state":
			_, _ = w.Write([]byte(`{"member_openid":"this-bot"}`))
		case "/v2/groups/group/messages":
			body := readQQTestReply(t, r)
			replies = append(replies, body.text())
			_, _ = w.Write([]byte(`{"id":"sent"}`))
		default:
			t.Errorf("custom commands must not call models: %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	// A nil database proves fixed replies need neither binding nor AI usage checks.
	s := New(nil, nil, false, nil)
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	c := DefaultConfig()
	c.CustomCommands = []CustomCommand{
		{Command: "/a", Reply: "第一行\n第二行", Enabled: true},
		{Command: "/停用", Reply: "不会出现", Enabled: false},
		{Command: "/长回复", Reply: strings.Repeat("字", 2000), Enabled: true},
		{Command: "/代码", Reply: "```html\n" + strings.Repeat("x", 1600) + "<br />\n```\n正文<br>下一行", Enabled: true},
	}
	for _, tt := range []struct {
		name, eventType, content string
		mentions                 []Mention
		wantReply                string
	}{
		{name: "unmentioned", eventType: groupMessageCreate, content: " /a \n", wantReply: "<qqbot-at-user id=\"member\" />\n第一行\n第二行"},
		{name: "mention event", eventType: groupAtMessageCreate, content: "<@this-bot> /a", wantReply: "第一行\n第二行"},
		{name: "full stream mention", eventType: groupMessageCreate, content: "/a", mentions: []Mention{{ID: "this-bot"}}, wantReply: "<qqbot-at-user id=\"member\" />\n第一行\n第二行"},
		{name: "prefix mismatch", eventType: groupMessageCreate, content: "/abc"},
		{name: "arguments mismatch", eventType: groupMessageCreate, content: "/a 参数"},
		{name: "case mismatch", eventType: groupMessageCreate, content: "/A"},
		{name: "plain command", eventType: groupMessageCreate, content: "a", wantReply: "<qqbot-at-user id=\"member\" />\n第一行\n第二行"},
		{name: "plain mention", eventType: groupAtMessageCreate, content: "<@this-bot> a", wantReply: "第一行\n第二行"},
		{name: "plain full stream mention", eventType: groupMessageCreate, content: "a", mentions: []Mention{{ID: "this-bot"}}, wantReply: "<qqbot-at-user id=\"member\" />\n第一行\n第二行"},
		{name: "plain other mention", eventType: groupMessageCreate, content: "<@other> a"},
		{name: "disabled", eventType: groupMessageCreate, content: "/停用"},
		{name: "other mention", eventType: groupMessageCreate, content: "/a", mentions: []Mention{{ID: "another-bot"}}},
		{name: "other inline mention", eventType: groupMessageCreate, content: "<@other> /a"},
		{name: "disabled mentioned", eventType: groupAtMessageCreate, content: "/停用", wantReply: "未知指令，请发送 帮助。"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := Event{Content: tt.content, GroupID: "group", Mentions: tt.mentions}
			e.Author.MemberID = "member"
			j := Job{EventType: tt.eventType, GroupID: "group", MemberID: "member", MessageID: tt.name, CreatedAt: time.Now()}
			before := len(replies)
			require.NoError(t, s.message(context.Background(), c, &j, e))
			if tt.wantReply == "" {
				require.Len(t, replies, before)
			} else {
				require.Len(t, replies, before+1)
				require.Equal(t, tt.wantReply, replies[before])
			}
			if tt.eventType == groupMessageCreate && len(groupMentionIDs(e)) == 0 {
				require.Equal(t, tt.wantReply != "", shouldQueueGroupMessage(tt.eventType, e, c.CustomCommands...))
			}
		})
	}
	for _, eventType := range []string{groupMessageCreate, groupAtMessageCreate} {
		e := Event{Content: "/a"}
		e.Author.Bot = true
		require.False(t, shouldQueueGroupMessage(eventType, e, c.CustomCommands...))
	}
	before := len(replies)
	j := Job{EventType: groupAtMessageCreate, GroupID: "group", MessageID: "long", CreatedAt: time.Now()}
	require.NoError(t, s.message(context.Background(), c, &j, Event{Content: "/长回复"}))
	require.Equal(t, c.CustomCommands[2].Reply, strings.Join(replies[before:], ""))
	require.Len(t, replies[before:], 2)

	// HTML examples must survive even when their opening code fence appears
	// in a previous QQ message. Only the final prose break is normalized.
	before = len(replies)
	j.MessageID = "code"
	require.NoError(t, s.message(context.Background(), c, &j, Event{Content: "/代码"}))
	require.Equal(t, strings.ReplaceAll(c.CustomCommands[3].Reply, "正文<br>下一行", "正文\n下一行"), strings.Join(replies[before:], ""))

	j.MessageID = "help"
	before = len(replies)
	require.NoError(t, s.message(context.Background(), c, &j, Event{Content: "/帮助"}))
	help := strings.Join(replies[before:], "")
	require.Contains(t, help, "- **a**：自定义回复")
	require.Contains(t, help, "- **长回复**：自定义回复")
	require.NotContains(t, help, "**停用**")
}
