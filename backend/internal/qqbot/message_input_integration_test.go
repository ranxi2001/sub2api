//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQQBotImagesAndExactReplyContext(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			db := testDB(t)
			s := savedBot(t, db)
			ctx := context.Background()
			c, err := s.Config(ctx)
			require.NoError(t, err)
			modelInputs := make(chan []byte, 10)
			replies := make(chan string, 30)
			var calls, sent, lookups atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
				case "/v2/groups/group/bot_state", "/v2/groups/other-group/bot_state":
					lookups.Add(1)
					_, _ = w.Write([]byte(`{"member_openid":"this-bot"}`))
				case "/v1/chat/completions", "/v1/responses":
					n := calls.Add(1)
					var body map[string]json.RawMessage
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					key := "messages"
					if protocol == "responses" {
						key = "input"
					}
					modelInputs <- body[key]
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": fmt.Sprintf("answer-%d", n)}}}})
				case "/v2/groups/group/messages":
					var body struct {
						qqTestReply
						Reference struct {
							ID string `json:"message_id"`
						} `json:"message_reference"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					require.True(t, strings.HasPrefix(body.Reference.ID, "incoming-"))
					wantSequence := 1
					if (body.MsgID == "image" || body.MsgID == "broken") && body.text() != "正在处理图片，请稍候。" {
						wantSequence = 2
					}
					require.Equal(t, wantSequence, body.Sequence)
					require.NotContains(t, body.text(), "正在思考")
					replies <- body.text()
					n := sent.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("sent-%d", n), "ext_info": map[string]string{"ref_idx": fmt.Sprintf("REF-%d==", n)}})
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
			s.imageDownload = func(_ context.Context, url string) ([]byte, error) {
				if strings.Contains(url, "broken") {
					return nil, errors.New("private URL failed")
				}
				return testInputPNG(t), nil
			}
			enqueue := func(id, content, member, group, eventType, ref string, attachments []Attachment) {
				e := Event{ID: id, GroupID: group, Content: content, Attachments: attachments, MessageScene: MessageScene{Ext: []string{"msg_idx=incoming-" + id, "auth_token=not-for-model"}}}
				e.Author.MemberID = member
				if ref != "" {
					e.MessageScene.Ext = append(e.MessageScene.Ext, "ref_msg_idx="+ref)
				}
				raw, err := json.Marshal(e)
				require.NoError(t, err)
				require.NoError(t, s.Enqueue(ctx, c, Payload{Type: eventType, Data: raw}))
				s.runOne(ctx)
			}
			enqueue("image", " ", "alice", "group", groupAtMessageCreate, "", []Attachment{{URL: "https://image.example/secret-query", ContentType: "image/png"}})
			require.EqualValues(t, 1, calls.Load(), "image-only messages must not return help")
			require.EqualValues(t, 2, sent.Load(), "image input gets a processing notice before the answer")
			firstInput := string(<-modelInputs)
			require.Contains(t, firstInput, "data:image/png;base64,")
			require.NotContains(t, firstInput, "secret-query")
			require.NotContains(t, firstInput, "not-for-model")
			var history string
			require.NoError(t, db.QueryRow(`SELECT string_agg(content,'\n') FROM qq_bot_group_messages WHERE role='user'`).Scan(&history))
			require.NotContains(t, history, "base64")
			require.NotContains(t, history, "secret-query")
			require.Contains(t, history, "历史记录不保存图片")
			require.Len(t, s.history(ctx, c, Job{GroupID: "group", MemberID: "alice"}), 4)
			var originalRef string
			require.NoError(t, db.QueryRow(`SELECT ref_idx FROM qq_bot_message_references WHERE content='answer-1'`).Scan(&originalRef))
			require.NotEmpty(t, originalRef)

			enqueue("ambient", "大家今晚一起吃火锅", "charlie", "group", groupMessageCreate, "", nil)
			require.EqualValues(t, 1, calls.Load(), "ambient chatter never calls a model")
			enqueue("later", "alice 的后续群聊上下文", "alice", "group", groupAtMessageCreate, "", nil)
			laterInput := string(<-modelInputs)
			require.NotContains(t, laterInput, "大家今晚一起吃火锅")
			require.NotContains(t, laterInput, groupSpeaker(c.AppID, "group", "charlie"))
			require.Contains(t, laterInput, "answer-1", "retain the bot's own reply to Alice")
			enqueue("quote", "解释这条回复", "bob", "group", groupMessageCreate, originalRef, nil)
			require.EqualValues(t, 3, calls.Load())
			quotedInput := string(<-modelInputs)
			require.Contains(t, quotedInput, "answer-1")
			require.Contains(t, quotedInput, "解释这条回复")
			require.NotContains(t, quotedInput, "answer-2")
			require.NotContains(t, quotedInput, "alice 的后续群聊上下文")
			require.NotContains(t, quotedInput, "大家今晚一起吃火锅")
			var turns []json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(quotedInput), &turns))
			require.Contains(t, string(turns[len(turns)-1]), "answer-1", "the explicit quote still targets the original message")
			require.NotContains(t, string(turns[len(turns)-1]), "answer-2")
			require.Zero(t, lookups.Load(), "quoting a known bot reply does not require bot_state")

			enqueue("other-group", "解释这条回复", "bob", "other-group", groupMessageCreate, originalRef, nil)
			enqueue("other-author", "解释这条回复", "bob", "group", groupMessageCreate, "not-a-bot-message", nil)
			require.EqualValues(t, 3, calls.Load(), "references must stay scoped to the same app and group")
			var ambientJobs int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE message_id IN ('other-group','other-author')`).Scan(&ambientJobs))
			require.Zero(t, ambientJobs, "ambient quotes must not occupy the durable queue")
			otherApp := c
			otherApp.AppID = "another-app"
			ref, err := s.referencedMessage(ctx, otherApp, "group", originalRef)
			require.NoError(t, err)
			require.Nil(t, ref)

			enqueue("broken", "看图", "bob", "group", groupAtMessageCreate, "", []Attachment{{URL: "https://image.example/broken", ContentType: "image/png"}})
			require.EqualValues(t, 3, calls.Load(), "do not call the model after image download failure")
			var detail, status string
			require.NoError(t, db.QueryRow(`SELECT error,status FROM qq_bot_jobs WHERE message_id='broken'`).Scan(&detail, &status))
			require.Contains(t, detail, "图片读取失败")
			require.Equal(t, "failed", status)
			var last string
			for len(replies) > 0 {
				last = <-replies
			}
			require.Contains(t, last, "图片读取失败")

			_, err = db.Exec(`UPDATE qq_bot_message_references SET created_at=NOW()-INTERVAL '25 hours'`)
			require.NoError(t, err)
			enqueue("expired", "解释这条回复", "bob", "group", groupMessageCreate, originalRef, nil)
			require.EqualValues(t, 3, calls.Load())
			s.maintenance(ctx)
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_message_references`).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestQQBotFailedSendDoesNotCreateReference(t *testing.T) {
	db := testDB(t)
	s := savedBot(t, db)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"test-only","expires_in":7200}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"id":"not-delivered","ext_info":{"ref_idx":"must-not-exist"}}`))
	}))
	defer server.Close()
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	c, err := s.Config(context.Background())
	require.NoError(t, err)
	require.Error(t, s.send(context.Background(), c, Job{GroupID: "group", MessageID: "source", CreatedAt: time.Now()}, "not delivered", nil, 1))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_message_references`).Scan(&count))
	require.Zero(t, count)
}
