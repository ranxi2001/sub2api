//go:build integration

package qqbot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQQBotChatImageToolExecutionAndLimits(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, scenario := range []string{"image", "text", "disabled", "limit", "disable-during-chat", "image-failure", "invalid-tool", "delivery-failure", "empty-promise"} {
			t.Run(protocol+"/"+scenario, func(t *testing.T) {
				db := testDB(t)
				s := savedBot(t, db)
				ctx := context.Background()
				c, err := s.Config(ctx)
				require.NoError(t, err)
				var chats, images, deliveries atomic.Int32
				var picture bytes.Buffer
				require.NoError(t, png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 16, 16))))
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/token":
						_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":7200}`))
					case "/v1/chat/completions", "/v1/responses":
						chats.Add(1)
						var request map[string]any
						require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
						var reviewBody map[string]json.RawMessage
						encoded, _ := json.Marshal(request)
						require.NoError(t, json.Unmarshal(encoded, &reviewBody))
						if replyImageReviewTest(t, w, protocol, reviewBody) {
							chats.Add(-1)
							return
						}
						input, marshalErr := json.Marshal(request)
						require.NoError(t, marshalErr)
						require.Contains(t, string(input), "蓝紫色霓虹的猫咪海报")
						require.Contains(t, string(input), "就这个吧")
						if scenario == "disabled" {
							require.Equal(t, "auto", request["tool_choice"])
							tools, _ := json.Marshal(request["tools"])
							require.Contains(t, string(tools), siteQueryToolName)
							require.NotContains(t, string(tools), chatImageToolName)
						} else {
							require.Equal(t, "required", request["tool_choice"])
						}
						if scenario == "text" {
							_, _ = w.Write(textToolTestJSON(t, protocol, "还需要确定尺寸吗？"))
							return
						}
						if scenario == "empty-promise" {
							if protocol == "responses" {
								_, _ = w.Write([]byte(`{"status":"completed","output_text":"我给你安排一张。","usage":{"input_tokens":11,"output_tokens":7}}`))
							} else {
								_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"我给你安排一张。"}}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`))
							}
							return
						}
						if scenario == "disabled" {
							// The same words can be a text turn; only the model decides.
							if protocol == "responses" {
								_, _ = w.Write([]byte(`{"status":"completed","output_text":"还需要确定尺寸吗？","usage":{"input_tokens":11,"output_tokens":7}}`))
							} else {
								_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"还需要确定尺寸吗？"}}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`))
							}
							return
						}
						if scenario == "disable-during-chat" {
							changed := c
							changed.ImageEnabled = false
							_, saveErr := s.SaveConfig(ctx, changed)
							require.NoError(t, saveErr)
						}
						args := `{"prompt":"蓝紫色霓虹的猫咪海报，简洁构图"}`
						if scenario == "invalid-tool" {
							args = `{"prompt":"cat","url":"https://example.invalid"}`
						}
						_, _ = w.Write(imageToolTestJSON(t, protocol, args))
					case "/images/v1/images/generations":
						images.Add(1)
						require.Equal(t, "Bearer separate-image-key", r.Header.Get("Authorization"))
						var request map[string]any
						require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
						require.Equal(t, "image-model", request["model"])
						require.Equal(t, "蓝紫色霓虹的猫咪海报，简洁构图", request["prompt"])
						require.EqualValues(t, 1, request["n"])
						if scenario == "image-failure" {
							w.WriteHeader(http.StatusBadGateway)
							return
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(picture.Bytes())}}})
					case "/v2/groups/group/files":
						_, _ = w.Write([]byte(`{"file_info":"uploaded"}`))
					case "/v2/groups/group/messages":
						deliveries.Add(1)
						body := readQQTestReply(t, r)
						if body.MsgType == 7 {
							require.Equal(t, "uploaded", body.Media.FileInfo)
							// Result bytes must already be durable before QQ delivery.
							var storedBytes int
							require.NoError(t, db.QueryRow(`SELECT octet_length(result_image) FROM qq_bot_jobs WHERE message_id='current'`).Scan(&storedBytes))
							require.Positive(t, storedBytes)
							if scenario == "delivery-failure" {
								w.WriteHeader(http.StatusBadGateway)
								return
							}
						} else {
							require.NotContains(t, body.text(), "我给你安排一张")
							require.NotContains(t, body.text(), chatImageToolName)
							require.NotContains(t, body.text(), "separate-image-key")
						}
						_, _ = fmt.Fprintf(w, `{"id":"sent-%d","ext_info":{"ref_idx":"sent-index-%d"}}`, deliveries.Load(), deliveries.Load())
					default:
						t.Errorf("unexpected endpoint: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				c.BaseURL, c.ChatProtocol = server.URL+"/v1", protocol
				c.ImageEnabled, c.ImageModel = scenario != "disabled", "image-model"
				c.ImageBaseURL, c.ImageAPIKey = server.URL+"/images/v1", "separate-image-key"
				c.CooldownSeconds = 5 // The tool must not hit its own chat cooldown.
				_, err = s.SaveConfig(ctx, c)
				require.NoError(t, err)
				s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
				previous := Job{ID: "pending-plan", GroupID: "group", MessageID: "previous", CreatedAt: time.Now().Add(-time.Minute)}
				require.NoError(t, s.rememberSentMessage(ctx, c, previous, []byte(`{"id":"plan","ext_info":{"ref_idx":"plan-index"}}`), "可以做一张蓝紫色霓虹的猫咪海报。", false, true))
				_, err = db.Exec(`INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,member_id,message_id,payload,status) VALUES('pending-plan','app','pending-plan','GROUP_AT_MESSAGE_CREATE','group','member','plan-request','{}','completed')`)
				require.NoError(t, err)
				_, err = db.Exec(`INSERT INTO qq_bot_pending_image_plans(app_id,group_id,member_id,job_id,source_message_id,action,prompt,request_quote,expires_at) VALUES('app','group','member','pending-plan','plan-request','generate_image','蓝紫色霓虹的猫咪海报','帮我设计猫咪海报',NOW()+INTERVAL '10 minutes')`)
				require.NoError(t, err)
				if scenario == "limit" {
					_, err = db.Exec(`INSERT INTO qq_bot_daily_usage(app_id,scope,day,kind,count) VALUES('app','member:member',$1,'image',$2)`, day(time.Now()), c.DailyImageLimit)
					require.NoError(t, err)
				}
				e := sharedTestEvent("current", "group", "member", "就这个吧")
				e.MessageScene.Ext = []string{"ref_msg_idx=plan-index"}
				enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
				s.runOne(ctx)
				var kind, status, text string
				var in, out int64
				require.NoError(t, db.QueryRow(`SELECT kind,status,result_text,input_tokens,output_tokens FROM qq_bot_jobs WHERE message_id='current'`).Scan(&kind, &status, &text, &in, &out))
				wantIn, wantOut := int64(11), int64(7)
				if scenario == "empty-promise" {
					wantIn, wantOut = 33, 21
				} else if scenario != "text" && scenario != "disabled" && scenario != "invalid-tool" {
					wantIn, wantOut = 22, 14
				}
				require.Equal(t, wantIn, in)
				require.Equal(t, wantOut, out)
				var chatCount, imageCount int
				require.NoError(t, db.QueryRow(`SELECT COALESCE(SUM(count) FILTER(WHERE kind='chat'),0),COALESCE(SUM(count) FILTER(WHERE kind='image'),0) FROM qq_bot_daily_usage WHERE scope='all'`).Scan(&chatCount, &imageCount))
				require.Equal(t, 1, chatCount)
				wantImages, wantStatus := 0, "completed"
				switch scenario {
				case "image", "image-failure", "delivery-failure":
					wantImages = 1
					require.Equal(t, "image", kind)
					if scenario == "image-failure" {
						wantStatus = "failed"
					}
					if scenario == "delivery-failure" {
						wantStatus = "delivery_failed"
					}
				case "invalid-tool", "empty-promise":
					wantStatus = "failed"
				case "limit":
					require.Contains(t, text, "今日使用次数已达上限")
				case "disable-during-chat":
					require.Contains(t, text, "功能未启用")
				default:
					require.Equal(t, "还需要确定尺寸吗？", text)
				}
				require.Equal(t, wantStatus, status)
				require.EqualValues(t, wantImages, images.Load())
				require.Equal(t, wantImages, imageCount)
				if scenario == "image" {
					require.EqualValues(t, 3, deliveries.Load())
					ref, refErr := s.referencedMessage(ctx, c, "group", "sent-index-3")
					require.NoError(t, refErr)
					require.NotNil(t, ref)
					require.True(t, ref.Image)
					require.True(t, ref.Shareable)
					require.Equal(t, "图片已生成。", ref.Content)
				}
				// Re-delivered QQ events cannot repeat either paid upstream call,
				// including after a model failure or failed image delivery.
				enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
				s.runOne(ctx)
				wantChats := 1
				if scenario == "empty-promise" {
					wantChats = 3
				}
				require.EqualValues(t, wantChats, chats.Load())
				require.EqualValues(t, wantImages, images.Load())
			})
		}
	}
}
