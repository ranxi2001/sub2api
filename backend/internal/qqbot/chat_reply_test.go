package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func testChatEnvelope(text string) string {
	return (groupHistoryEntry{Speaker: "机器人", ID: "0123456789abcdef", ReplyTo: "fedcba9876543210", Text: text}).message("assistant").Content
}

func TestNormalizeChatReply(t *testing.T) {
	want := "先喝口水，慢慢说。\n包含 \"引号\"、反斜杠 \\ 和 {括号} 🙂"
	wrapped := testChatEnvelope(want)
	for _, text := range []string{wrapped, " \n" + wrapped + "\n", "```json\n" + wrapped + "\n```", "```\n" + wrapped + "\n```", "~~~json\r\n" + wrapped + "\r\n~~~"} {
		require.Equal(t, want, normalizeChatReply(text))
	}
	require.Equal(t, "hello", normalizeChatReply(`{"speaker":"机器人","reply_to":"0123456789abcdef","text":"hello"}`))
	require.Empty(t, normalizeChatReply(testChatEnvelope(" \n")))

	for _, text := range []string{
		" 普通回复 **Markdown**\n保留换行  路径",
		`{"text":"a JSON example"}`,
		`{"action":"reply","text":"autonomous decision"}`,
		`{"speaker":"机器人","text":"no internal identifiers"}`,
		`{"speaker":"机器人","id":"example","text":"example schema"}`,
		strings.Replace(wrapped, "机器人", "成员-0123456789abcdef", 1),
		strings.Replace(wrapped, "fedcba9876543210", "not-a-hash-value!", 1),
		strings.TrimSuffix(wrapped, "}") + `,"extra":true}`,
		`{"speaker":"机器人","id":"0123456789abcdef"}`,
		`{"speaker":"机器人","id":"0123456789abcdef","text":null}`,
		`{"speaker":"机器人","id":"0123456789abcdef","text":123}`,
		strings.TrimSuffix(wrapped, "}"),
		"[" + wrapped + "]",
		wrapped + "\n{}",
		"示例：\n```json\n" + wrapped + "\n```",
		"```json\n" + wrapped + "\n```\n说明",
		"```go\n" + wrapped + "\n```",
	} {
		require.Equal(t, text, normalizeChatReply(text), "non-envelope content must survive unchanged")
	}
}

func TestGroupHistoryModelMessage(t *testing.T) {
	for _, text := range []string{"已发送的回复", testChatEnvelope("已发送的回复")} {
		stored := Message{Role: "assistant", Content: testChatEnvelope(text)}
		got := groupHistoryModelMessage(stored)
		require.Equal(t, Message{Role: "assistant", Content: "已发送的回复"}, got)
		require.Equal(t, testChatEnvelope(text), stored.Content, "stored metadata is not mutated")
	}
	for _, m := range []Message{
		{Role: "user", Content: testChatEnvelope("用户提供的 JSON"), Images: []string{"image"}},
		{Role: "system", Content: testChatEnvelope("指令示例")},
		{Role: "assistant", Content: "普通历史回复"},
		{Role: "assistant", Content: `{"text":"合法 JSON 回答"}`},
	} {
		require.Equal(t, m, groupHistoryModelMessage(m))
	}
}

func TestAutonomousReplyUnwrapsHistoryEnvelope(t *testing.T) {
	for _, tc := range []struct {
		text string
		ok   bool
	}{
		{"可以先看看错误日志。", true},
		{"@大家", false},
		{"<qqbot-at-everyone />", false},
		{"https://example.invalid", false},
		{strings.Repeat("字", 101), false},
		{"", false},
	} {
		raw, err := json.Marshal(map[string]string{"action": "reply", "text": testChatEnvelope(tc.text)})
		require.NoError(t, err)
		got, err := parseAutonomousReply(string(raw), 100)
		if tc.ok {
			require.NoError(t, err)
			require.Equal(t, tc.text, got)
		} else {
			require.Error(t, err, "extracted text must still pass autonomous validation")
		}
	}
	_, err := parseAutonomousReply(testChatEnvelope("缺少自主接话决策"), 100)
	require.Error(t, err, "the required action protocol cannot be bypassed")
}

func TestChatReplyUnwrapsAcrossProtocols(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, stream := range []bool{false, true} {
			for _, want := range []string{"只发送回复正文。\n支持换行。", ""} {
				t.Run(fmt.Sprintf("%s/stream=%t/empty=%t", protocol, stream, want == ""), func(t *testing.T) {
					wrapped := testChatEnvelope(want)
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						var response any = map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": wrapped}}}, "usage": map[string]int{"prompt_tokens": 7, "completion_tokens": 3}}
						if protocol == "responses" {
							response = map[string]any{"status": "completed", "output_text": wrapped, "usage": map[string]int{"input_tokens": 7, "output_tokens": 3}}
						}
						if !stream {
							require.NoError(t, json.NewEncoder(w).Encode(response))
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						writeEvent := func(v any) {
							b, err := json.Marshal(v)
							require.NoError(t, err)
							_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
						}
						// The envelope spans multiple chunks and is unwrapped only after assembly.
						for _, delta := range []string{wrapped[:10], wrapped[10:]} {
							if protocol == "responses" {
								writeEvent(map[string]any{"type": "response.output_text.delta", "delta": delta})
							} else {
								writeEvent(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": delta}}}})
							}
						}
						if protocol == "responses" {
							writeEvent(map[string]any{"type": "response.completed", "response": response})
						} else {
							writeEvent(map[string]any{"usage": map[string]int{"prompt_tokens": 7, "completion_tokens": 3}})
							_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
						}
					}))
					defer server.Close()
					s := New(nil, nil, false, nil)
					c := DefaultConfig()
					c.BaseURL, c.ChatProtocol = server.URL, protocol
					got, in, out, err := s.chatReply(context.Background(), c, []Message{{Role: "user", Content: "你好"}})
					if want == "" {
						require.EqualError(t, err, "模型没有返回文本")
					} else {
						require.NoError(t, err)
					}
					require.Equal(t, want, got)
					require.EqualValues(t, 7, in)
					require.EqualValues(t, 3, out)
					require.EqualValues(t, 1, calls.Load(), "normalization never makes an extra paid call")
				})
			}
		}
	}
}
