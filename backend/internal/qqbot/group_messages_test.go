package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUnmentionedGroupMessagesDoNotAccessDataOrCallModels(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	s := New(nil, nil, false, nil)
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	c := DefaultConfig()
	c.Enabled, c.ImageEnabled = true, true
	c.BaseURL = server.URL + "/v1"
	for _, content := range []string{"你好", "签到有什么用", "帮我看一下我的余额", "画图 一只猫", "/不存在", "/新对话", "/查看结果 task", "帮我解释 /签到", "@CTAV 你好"} {
		t.Run(content, func(t *testing.T) {
			e := Event{ID: "ignored", GroupID: "group", Content: content}
			e.Author.MemberID = "member"
			raw, err := json.Marshal(e)
			require.NoError(t, err)
			require.NoError(t, s.Enqueue(context.Background(), c, Payload{Type: groupMessageCreate, Data: raw}))
			j := Job{EventType: groupMessageCreate, GroupID: "group", MessageID: "ignored", CreatedAt: time.Now()}
			require.NoError(t, s.message(context.Background(), c, &j, e))
		})
	}
	// Bot messages must never trigger commands or recursive image generation.
	for _, content := range []string{"/签到", "/生图 一只猫"} {
		e := Event{ID: "bot", GroupID: "group", Content: content}
		e.Author.Bot = true
		raw, err := json.Marshal(e)
		require.NoError(t, err)
		require.NoError(t, s.Enqueue(context.Background(), c, Payload{Type: groupMessageCreate, Data: raw}))
	}
	require.Zero(t, calls.Load())
	require.True(t, shouldQueueGroupMessage(groupMessageCreate, Event{Content: "/成长任务"}))
}

func TestFullGroupCommandsWithoutMention(t *testing.T) {
	var replies atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
		case "/v2/groups/group/messages":
			replies.Add(1)
			_, _ = w.Write([]byte(`{"id":"sent"}`))
		default:
			t.Errorf("unmentioned command called unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	s := New(nil, nil, false, nil)
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	c := DefaultConfig()
	commands := []string{"/帮助", "/绑定", "/生图", "/抽奖 错误", "/我的余额 多余参数", "/余额 多余参数", "帮助", "绑定", "生图", "抽奖 错误", "我的余额 多余参数", "余额 多余参数"}
	for i, content := range commands {
		require.True(t, shouldQueueGroupMessage(groupMessageCreate, Event{Content: content}))
		j := Job{EventType: groupMessageCreate, GroupID: "group", MessageID: strconv.Itoa(i), CreatedAt: time.Now()}
		require.NoError(t, s.message(context.Background(), c, &j, Event{GroupID: "group", Content: content}))
	}
	require.EqualValues(t, len(commands), replies.Load())
}

func TestBalanceAliasAndBatchLotteryRouteAsCommands(t *testing.T) {
	for _, content := range []string{"余额", "/余额", "我的余额", "/我的余额", "抽奖 10", "/抽奖 10", "  抽奖   10  ", "抽奖\u300010"} {
		t.Run(content, func(t *testing.T) {
			e := Event{Content: content}
			require.True(t, unmentionedGroupCommand(e))
			require.True(t, shouldQueueGroupMessage(groupMessageCreate, e))
			require.True(t, shouldQueueGroupMessage(groupAtMessageCreate, e))
			require.True(t, privateCommandText(content))
			e.Content = "<@this-bot> " + content
			require.True(t, isBuiltinOrCustomCommand(e, nil))
			e.Author.Bot = true
			require.False(t, shouldQueueGroupMessage(groupMessageCreate, e))
		})
	}
}

func TestMentionWithoutContentRepliesWithHelp(t *testing.T) {
	var replies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
		case "/v2/groups/group/bot_state":
			_, _ = w.Write([]byte(`{"member_openid":"this-bot"}`))
		case "/v2/groups/group/messages":
			reply := readQQTestReply(t, r)
			replies = append(replies, reply.text())
			_, _ = w.Write([]byte(`{"id":"sent"}`))
		default:
			t.Errorf("empty mention called unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	// Help must work without bindings, model usage reservations or a database.
	s := New(nil, nil, false, nil)
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	c := DefaultConfig()
	j := Job{EventType: groupAtMessageCreate, GroupID: "group", MessageID: "help", CreatedAt: time.Now()}
	require.NoError(t, s.message(context.Background(), c, &j, Event{Content: "/帮助"}))
	require.Len(t, replies, 1)
	help := append([]string(nil), replies...)
	for _, content := range []string{"<@this-bot>", "  <@!this-bot> \t\n\u3000", "", " \t\n\u3000"} {
		for _, eventType := range []string{groupAtMessageCreate, groupMessageCreate} {
			t.Run(eventType+"/"+content, func(t *testing.T) {
				e := Event{GroupID: "group", Content: content, Mentions: []Mention{{ID: "this-bot"}}}
				j := Job{EventType: eventType, GroupID: "group", MessageID: "empty-mention", CreatedAt: time.Now()}
				before := len(replies)
				require.NoError(t, s.message(context.Background(), c, &j, e))
				require.Len(t, replies, before+len(help))
				require.Equal(t, help, replies[before:])
			})
		}
	}
}

func TestFullGroupMessagesRequireThisBotsIdentity(t *testing.T) {
	var lookups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
		case "/v2/groups/group/bot_state", "/v2/groups/other-group/bot_state":
			require.Equal(t, http.MethodGet, r.Method)
			require.Equal(t, "QQBot test-only", r.Header.Get("Authorization"))
			lookups.Add(1)
			id := "this-bot"
			if r.URL.Path == "/v2/groups/other-group/bot_state" {
				id = "other-group-bot"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"member_openid": id})
		default:
			t.Errorf("message for another member called unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	s := New(nil, nil, false, nil)
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	c := DefaultConfig()
	for _, content := range []string{"<@another-bot> 你好", "<@another-user> /签到"} {
		e := Event{GroupID: "group", Content: content}
		j := Job{EventType: groupMessageCreate, GroupID: "group", MessageID: "ignored", CreatedAt: time.Now()}
		require.NoError(t, s.message(context.Background(), c, &j, e))
	}
	for _, content := range []string{"你好", "/签到", "/抽奖", "/我的邀请", "/生图 一只猫", "/画图 一只猫"} {
		e := Event{GroupID: "group", Content: content, Mentions: []Mention{{MemberID: "another-bot"}}}
		j := Job{EventType: groupMessageCreate, GroupID: "group", MessageID: "ignored", CreatedAt: time.Now()}
		require.NoError(t, s.message(context.Background(), c, &j, e))
	}
	for _, e := range []Event{
		{GroupID: "group", Content: "<@!this-bot> 你好"},
		{GroupID: "group", Content: "你好", Mentions: []Mention{{ID: "this-bot"}}},
		{GroupID: "group", Content: "/生图 一只猫", Mentions: []Mention{{MemberID: "this-bot"}}},
	} {
		addressed, err := s.groupMessageMentionsBot(context.Background(), c, e)
		require.NoError(t, err)
		require.True(t, addressed)
	}
	require.EqualValues(t, 1, lookups.Load(), "group identity must be cached")
	addressed, err := s.groupMessageMentionsBot(context.Background(), c, Event{GroupID: "other-group", Mentions: []Mention{{ID: "this-bot"}}})
	require.NoError(t, err)
	require.False(t, addressed, "a different group's bot OpenID must not match")
	require.EqualValues(t, 2, lookups.Load())
	changed := c
	changed.AppSecret = "updated-secret"
	_, err = s.groupMessageMentionsBot(context.Background(), changed, Event{GroupID: "group", Mentions: []Mention{{ID: "this-bot"}}})
	require.NoError(t, err)
	require.EqualValues(t, 3, lookups.Load(), "rotating credentials must invalidate identity cache")
}

func TestGroupIdentityFailureDoesNotCallModels(t *testing.T) {
	var lookups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
		case "/v2/groups/group/bot_state":
			lookups.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":11253}`))
		default:
			t.Errorf("failed identity lookup called unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	s := New(nil, nil, false, nil)
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	c := DefaultConfig()
	for range 2 {
		e := Event{GroupID: "group", Content: "/生图 一只猫", Mentions: []Mention{{ID: "some-bot"}}}
		j := Job{EventType: groupMessageCreate, GroupID: "group", MessageID: "ignored", CreatedAt: time.Now()}
		require.ErrorContains(t, s.message(context.Background(), c, &j, e), "bot_state")
	}
	require.EqualValues(t, 1, lookups.Load(), "permission errors must not repeatedly request bot state")
}
