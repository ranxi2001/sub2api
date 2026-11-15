//go:build integration

package qqbot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQQBotImageProgressOrderAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"text", []string{"chat", "answer"}},
		{"text-reference", []string{"chat", "answer"}},
		{"command", []string{"progress", "generate", "notice", "image"}},
		{"tool", []string{"chat", "review", "progress", "generate", "notice", "image"}},
		{"read", []string{"progress", "download", "chat", "answer"}},
		{"long-read", []string{"progress", "download", "chat", "answer", "answer", "answer", "answer"}},
		{"edit", []string{"progress", "download", "chat", "review", "edit", "notice", "image"}},
		{"member-reference", []string{"progress", "download", "chat", "answer"}},
		{"read-failure", []string{"progress", "download", "answer"}},
		{"progress-failure", []string{"chat", "review", "progress", "generate", "notice", "image"}},
		{"progress-timeout", []string{"chat", "review", "progress", "generate", "notice", "image"}},
		{"limit", []string{"chat", "review", "answer"}},
		{"disabled-command", []string{"answer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			s := savedBot(t, db)
			ctx := context.Background()
			c, err := s.Config(ctx)
			require.NoError(t, err)
			events := make(chan string, 20)
			sequences := make(chan int, 10)
			picture := testInputPNG(t)
			s.imageDownload = func(context.Context, string) ([]byte, error) {
				events <- "download"
				if tc.name == "read-failure" {
					return nil, errors.New("image unavailable")
				}
				return picture, nil
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":7200}`))
				case "/v1/chat/completions":
					var body map[string]json.RawMessage
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					if replyImageReviewTest(t, w, "chat_completions", body) {
						events <- "review"
						return
					}
					events <- "chat"
					require.NotContains(t, string(body["messages"]), "正在处理图片，请稍候。")
					switch tc.name {
					case "tool", "limit", "progress-failure", "progress-timeout":
						_, _ = w.Write(imageToolTestJSON(t, "chat_completions", `{"prompt":"cat"}`))
					case "edit":
						_, _ = w.Write(editToolTestJSON(t, "chat_completions", `{"prompt":"blue background","image_index":1}`))
					case "long-read":
						_, _ = w.Write(textToolTestJSON(t, "chat_completions", strings.Repeat("图片说明", 2000)))
					default:
						_, _ = w.Write(textToolTestJSON(t, "chat_completions", "正常回答"))
					}
				case "/v1/images/generations", "/v1/images/edits":
					if strings.HasSuffix(r.URL.Path, "edits") {
						events <- "edit"
					} else {
						events <- "generate"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(picture)}}})
				case "/v2/groups/group/files":
					_, _ = w.Write([]byte(`{"file_info":"image"}`))
				case "/v2/groups/group/messages":
					body := readQQTestReply(t, r)
					sequences <- body.Sequence
					require.LessOrEqual(t, body.Sequence, 5)
					switch {
					case strings.HasPrefix(body.text(), "正在"):
						events <- "progress"
						require.Equal(t, 1, body.Sequence)
						if tc.name == "progress-failure" {
							w.WriteHeader(http.StatusBadGateway)
							return
						}
						if tc.name == "progress-timeout" {
							select {
							case <-r.Context().Done():
							case <-time.After(5 * time.Second):
								t.Error("progress request should be cancelled within its timeout")
							}
							return
						}
					case body.MsgType == 7:
						events <- "image"
					case strings.HasPrefix(body.text(), "图片已"):
						events <- "notice"
					default:
						events <- "answer"
						if tc.name == "long-read" && body.Sequence == 5 {
							require.Contains(t, body.text(), "已截断")
						}
					}
					_, _ = fmt.Fprintf(w, `{"id":"reply-%d","ext_info":{"ref_idx":"ref-%d"}}`, body.Sequence, body.Sequence)
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			c.BaseURL, c.ImageEnabled, c.ImageModel = server.URL+"/v1", tc.name != "disabled-command", "image-model"
			_, err = s.SaveConfig(ctx, c)
			require.NoError(t, err)
			s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
			e := sharedTestEvent("request", "group", "member", "请生成一只猫的图片")
			attachment := Attachment{URL: "https://image.example/picture", ContentType: "image/png"}
			switch tc.name {
			case "read", "long-read", "edit", "read-failure":
				e.Attachments = []Attachment{attachment}
			case "command", "disabled-command":
				e.Content = "/生图 cat"
			case "member-reference":
				photo := sharedTestEvent("photo", "group", "photographer", "")
				photo.Attachments = []Attachment{attachment}
				require.NoError(t, s.rememberIncomingImages(ctx, c, photo))
				e.MessageReference.MessageID = "photo"
			case "text-reference":
				require.NoError(t, s.rememberSentMessage(ctx, c, Job{GroupID: "group", CreatedAt: time.Now()}, []byte(`{"id":"text"}`), "普通文字", false, true))
				e.MessageReference.MessageID = "text"
			case "limit":
				_, err = db.Exec(`INSERT INTO qq_bot_daily_usage(app_id,scope,day,kind,count) VALUES('app','all',$1,'image',$2)`, day(time.Now()), c.TotalDailyImageLimit)
				require.NoError(t, err)
			}
			enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
			s.runOne(ctx)
			var got []string
			for len(events) > 0 {
				got = append(got, <-events)
			}
			require.Equal(t, tc.want, got)
			for i := 1; len(sequences) > 0; i++ {
				require.Equal(t, i, <-sequences, "failed progress must not reuse its reply sequence")
			}
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_message_references WHERE content LIKE '正在%'`).Scan(&count))
			require.Zero(t, count, "progress never becomes a quotable answer")
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_group_messages WHERE content LIKE '%正在处理图片%' OR content LIKE '%正在生成图片%'`).Scan(&count))
			require.Zero(t, count, "progress never enters shared model context")
			var status string
			require.NoError(t, db.QueryRow(`SELECT status FROM qq_bot_jobs WHERE message_id='request'`).Scan(&status))
			if tc.name == "read-failure" {
				require.Equal(t, "failed", status)
			} else {
				require.Equal(t, "completed", status)
			}
			enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
			s.runOne(ctx)
			require.Empty(t, events, "duplicate deliveries cannot repeat notices or paid requests")
		})
	}
}
