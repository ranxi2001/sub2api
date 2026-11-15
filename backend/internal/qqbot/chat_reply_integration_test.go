//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQQBotChatEnvelopeDeliveryAndHistory(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			db := testDB(t)
			s := savedBot(t, db)
			ctx := context.Background()
			c, err := s.Config(ctx)
			require.NoError(t, err)
			var calls, deliveries atomic.Int32
			const answer = "只把这段正文发给群聊。"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					_, _ = w.Write([]byte(`{"access_token":"qq-test-only","expires_in":7200}`))
				case "/v1/chat/completions", "/v1/responses":
					calls.Add(1)
					var request struct {
						Messages []Message `json:"messages"`
						Input    []Message `json:"input"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					messages := request.Messages
					if protocol == "responses" {
						messages = request.Input
					}
					require.NotContains(t, sharedHistoryText(messages), "触发生图了")
					require.NotContains(t, sharedHistoryText(messages), "锦")
					require.NotContains(t, sharedHistoryText(messages), "legacy mixed reply")
					var assistantTexts []string
					for _, m := range messages {
						if m.Role == "assistant" {
							assistantTexts = append(assistantTexts, m.Content)
						}
					}
					require.Equal(t, []string{"之前的正文"}, assistantTexts)
					var current groupHistoryEntry
					require.NoError(t, json.Unmarshal([]byte(messages[len(messages)-1].Content), &current))
					require.Equal(t, "请继续回复", current.Text)
					require.Equal(t, groupSpeaker(c.AppID, "group", "member"), current.Speaker)
					wrapped := testChatEnvelope(answer)
					if protocol == "responses" {
						require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output_text": wrapped}))
					} else {
						require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": wrapped}}}}))
					}
				case "/v2/groups/group/messages":
					deliveries.Add(1)
					reply := readQQTestReply(t, r)
					require.Equal(t, answer, reply.text())
					_, _ = w.Write([]byte(`{"id":"new-answer","ext_info":{"ref_idx":"new-answer-index"}}`))
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			c.BaseURL, c.ChatProtocol = server.URL+"/v1", protocol
			_, err = s.SaveConfig(ctx, c)
			require.NoError(t, err)
			s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
			// Simulate a wrapped reply saved by an older version. The next request
			// must see its body rather than copying the leaked envelope again.
			previous := Job{GroupID: "group", MemberID: "member", MessageID: "previous-question", CreatedAt: time.Now().Add(-time.Minute)}
			require.NoError(t, s.rememberSentMessage(ctx, c, previous, []byte(`{"id":"old-answer"}`), testChatEnvelope("之前的正文"), false, true))
			other := sharedTestEvent("another-question", "group", "another-member", "触发生图了")
			other.Author.Username = "锦"
			enqueueSharedTestEvent(t, s, c, other, groupMessageCreate)
			require.NoError(t, s.rememberSentMessage(ctx, c, Job{GroupID: "group", MemberID: "another-member", MessageID: other.ID}, []byte(`{"id":"another-answer"}`), "锦，你那句触发生图了是什么意思？", false, true))
			_, err = db.Exec(`INSERT INTO qq_bot_group_messages(app_id,group_id,message_key,role,content) VALUES($1,'group','legacy-mixed','assistant',$2)`, c.AppID, testChatEnvelope("legacy mixed reply"))
			require.NoError(t, err)
			enqueueSharedTestEvent(t, s, c, sharedTestEvent("current-question", "group", "member", "请继续回复"), groupAtMessageCreate)
			s.runOne(ctx)
			var status, result string
			require.NoError(t, db.QueryRow(`SELECT status,result_text FROM qq_bot_jobs WHERE message_id='current-question'`).Scan(&status, &result))
			require.Equal(t, "completed", status)
			require.Equal(t, answer, result)
			require.EqualValues(t, 1, calls.Load())
			require.EqualValues(t, 1, deliveries.Load())
			ref, err := s.referencedMessage(ctx, c, "group", "new-answer-index")
			require.NoError(t, err)
			require.NotNil(t, ref)
			require.Equal(t, answer, ref.Content)
			var stored string
			require.NoError(t, db.QueryRow(`SELECT content FROM qq_bot_group_messages WHERE message_key='assistant:new-answer'`).Scan(&stored))
			var entry groupHistoryEntry
			require.NoError(t, json.Unmarshal([]byte(stored), &entry))
			require.Equal(t, answer, entry.Text)
			require.Equal(t, groupHistoryID(c.AppID, "group", "current-question"), entry.ReplyTo)
		})
	}
}
