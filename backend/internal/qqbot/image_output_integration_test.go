//go:build integration

package qqbot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQQBotGIFWorkerPersistsAndReusesAnimation(t *testing.T) {
	for _, scenario := range []string{"command", "chat", "responses", "edit", "delivery-failure", "static-result", "quota", "review-denied"} {
		t.Run(scenario, func(t *testing.T) {
			db := testDB(t)
			s := savedBot(t, db)
			ctx := context.Background()
			c, err := s.Config(ctx)
			require.NoError(t, err)
			data := testAnimatedGIF(t, 3, 8, 8)
			var generations, uploads, replies atomic.Int32
			protocol := "chat_completions"
			if scenario == "responses" {
				protocol = "responses"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					_, _ = w.Write([]byte(`{"access_token":"qq-token","expires_in":7200}`))
				case "/v1/chat/completions", "/v1/responses":
					var body map[string]json.RawMessage
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					if strings.Contains(string(body["tools"]), imageReviewToolName) {
						input := body["messages"]
						if protocol == "responses" {
							input = body["input"]
						}
						require.Contains(t, string(input), "输出格式：gif")
						if scenario == "review-denied" {
							_, _ = w.Write(imageReviewTestJSON(t, protocol, imageIntentReview{Disposition: "text", Basis: "none", Text: "这里只讨论 GIF。"}))
							return
						}
						require.True(t, replyImageReviewTest(t, w, protocol, body))
						return
					}
					if scenario == "edit" {
						_, _ = w.Write(editToolTestJSON(t, protocol, `{"prompt":"让猫挥手循环动画","image_index":1,"output_format":"gif"}`))
					} else {
						_, _ = w.Write(imageToolTestJSON(t, protocol, `{"prompt":"挥手猫循环动画","output_format":"gif"}`))
					}
				case "/v1/images/generations", "/v1/images/edits":
					generations.Add(1)
					if scenario == "edit" {
						require.Equal(t, "/v1/images/edits", r.URL.Path)
						require.NoError(t, r.ParseMultipartForm(1<<20))
						defer r.MultipartForm.RemoveAll()
						require.Equal(t, "gif", r.FormValue("output_format"))
					} else {
						var request map[string]any
						require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
						require.Equal(t, "gif", request["output_format"])
						require.NotContains(t, request["prompt"], "--gif")
					}
					result := data
					if scenario == "static-result" {
						result = testInputPNG(t)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(result)}}})
				case "/v2/groups/group/files":
					n := uploads.Add(1)
					var request map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					require.Equal(t, "ctav.gif", request["file_name"])
					require.Equal(t, base64.StdEncoding.EncodeToString(data), request["file_data"])
					var stored []byte
					var mime string
					require.NoError(t, db.QueryRow(`SELECT result_image,image_type FROM qq_bot_jobs WHERE message_id='gif-request'`).Scan(&stored, &mime))
					require.Equal(t, data, stored, "persist full animation before uploading")
					require.Equal(t, "image/gif", mime)
					if scenario == "delivery-failure" && n == 1 {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					_, _ = w.Write([]byte(`{"file_info":"gif"}`))
				case "/v2/groups/group/messages":
					reply := readQQTestReply(t, r)
					if scenario == "static-result" && !strings.Contains(reply.text(), "正在生成") {
						require.Contains(t, reply.text(), "没有返回 GIF")
					}
					_, _ = fmt.Fprintf(w, `{"id":"sent-%d"}`, replies.Add(1))
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			c.BaseURL, c.ChatProtocol, c.ImageModel, c.ImageEnabled = server.URL+"/v1", protocol, "gpt-image-2.6", true
			c.CooldownSeconds = 0
			_, err = s.SaveConfig(ctx, c)
			require.NoError(t, err)
			s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
			e := sharedTestEvent("gif-request", "group", "member", "帮我生成挥手猫的 GIF 动图")
			if scenario == "command" || scenario == "delivery-failure" || scenario == "static-result" || scenario == "quota" {
				e.Content = "生图 --gif 挥手猫循环动画"
			}
			if scenario == "edit" {
				e.Attachments = []Attachment{{URL: "https://image.example/source", ContentType: "image/png"}}
				s.imageDownload = func(context.Context, string) ([]byte, error) { return testInputPNG(t), nil }
			}
			if scenario == "quota" {
				_, err = db.Exec(`INSERT INTO qq_bot_daily_usage(app_id,scope,day,kind,count) VALUES('app','member:member',$1,'image',$2)`, day(time.Now()), c.DailyImageLimit)
				require.NoError(t, err)
			}
			enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
			s.runOne(ctx)
			var status string
			require.NoError(t, db.QueryRow(`SELECT status FROM qq_bot_jobs WHERE message_id='gif-request'`).Scan(&status))
			switch scenario {
			case "static-result":
				require.Equal(t, "failed", status)
				require.Zero(t, uploads.Load())
			case "delivery-failure":
				require.Equal(t, "delivery_failed", status)
				job, err := scanJob(db.QueryRow(`SELECT ` + jobColumns + ` FROM qq_bot_jobs WHERE message_id='gif-request'`))
				require.NoError(t, err)
				require.NoError(t, s.message(ctx, c, &job, e))
				require.EqualValues(t, 2, uploads.Load())
				disabled := c
				disabled.ImageEnabled = false
				require.ErrorIs(t, s.message(ctx, disabled, &job, e), ErrDisabled)
				disabled = c
				disabled.GroupPolicies = []GroupPolicy{{GroupID: "group", DisableImages: true}}
				require.ErrorIs(t, s.message(ctx, disabled, &job, e), ErrDisabled)
				require.EqualValues(t, 2, uploads.Load())
			case "quota", "review-denied":
				require.Equal(t, "completed", status)
				require.Zero(t, generations.Load())
				require.Zero(t, uploads.Load())
			default:
				require.Equal(t, "completed", status)
				require.EqualValues(t, 1, uploads.Load())
			}
			if scenario != "quota" && scenario != "review-denied" {
				require.EqualValues(t, 1, generations.Load())
			}
			before := generations.Load()
			enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
			s.runOne(ctx)
			require.Equal(t, before, generations.Load(), "duplicate events never regenerate")
		})
	}
}
