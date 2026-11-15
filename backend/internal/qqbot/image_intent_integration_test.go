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

func TestQQBotImageIntentGuardBeforeQuotaAndProgress(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, scenario := range []string{"dean", "greeting", "capability", "appreciation", "ordinary-text", "explicit", "continuation-without-plan", "quoted-evidence", "malformed", "upstream-failure", "quota", "command"} {
			t.Run(protocol+"/"+scenario, func(t *testing.T) {
				db := testDB(t)
				s := savedBot(t, db)
				ctx := context.Background()
				c, err := s.Config(ctx)
				require.NoError(t, err)
				current := "帮我画一只猫"
				switch scenario {
				case "dean":
					current = "现在你还会骂我吗？我真的是Dean啊"
				case "greeting", "ordinary-text":
					current = "下午好🌹"
				case "capability":
					current = "你能画图吗？"
				case "appreciation":
					current = "这个不错"
				case "continuation-without-plan":
					current = "就按刚才方案来一张"
				case "quoted-evidence":
					current = "这是什么意思"
				case "command":
					current = "/生图 一只猫"
				}
				var chats, reviews, images, progress atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/token":
						fmt.Fprint(w, `{"access_token":"test","expires_in":7200}`)
					case "/v1/chat/completions", "/v1/responses":
						var body map[string]json.RawMessage
						require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
						if strings.Contains(string(body["tools"]), imageReviewToolName) {
							reviews.Add(1)
							encoded, _ := json.Marshal(body)
							require.NotContains(t, string(encoded), "PERSONA_PRIVATE")
							key := "messages"
							if protocol == "responses" {
								key = "input"
							}
							var messages []struct {
								Content string `json:"content"`
							}
							require.NoError(t, json.Unmarshal(body[key], &messages))
							require.Len(t, messages, 2)
							var data map[string]any
							require.NoError(t, json.Unmarshal([]byte(messages[1].Content), &data))
							require.Equal(t, current, data["current_message"])
							require.Nil(t, data["pending_plan"])
							var reserved int
							require.NoError(t, db.QueryRow(`SELECT COALESCE(SUM(count),0) FROM qq_bot_daily_usage WHERE scope='all' AND kind='image'`).Scan(&reserved))
							if scenario != "quota" {
								require.Zero(t, reserved)
							}
							require.Zero(t, progress.Load())
							review := imageIntentReview{Disposition: "execute", Basis: "explicit", Evidence: current}
							switch scenario {
							case "dean", "greeting", "capability", "appreciation":
								review = imageIntentReview{Disposition: "text", Basis: "none", Text: "这次正常文字回答。"}
							case "continuation-without-plan":
								review.Basis = "continuation"
							case "quoted-evidence":
								review.Evidence = "帮我画一只猫"
							case "malformed":
								fmt.Fprint(w, `{"status":"completed","output_text":"可以执行"}`)
								return
							case "upstream-failure":
								w.WriteHeader(http.StatusBadGateway)
								return
							}
							_, _ = w.Write(imageReviewTestJSON(t, protocol, review))
							return
						}
						chats.Add(1)
						if scenario == "ordinary-text" {
							_, _ = w.Write(textToolTestJSON(t, protocol, "下午好！"))
							return
						}
						_, _ = w.Write(imageToolTestJSON(t, protocol, `{"prompt":"cat"}`))
					case "/v1/images/generations":
						images.Add(1)
						require.EqualValues(t, 1, progress.Load())
						var raw string
						require.NoError(t, db.QueryRow(`SELECT image_decision->>'execution' FROM qq_bot_jobs WHERE message_id='current'`).Scan(&raw))
						require.Equal(t, "started", raw)
						json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(testInputPNG(t))}}})
					case "/v2/groups/group/files":
						fmt.Fprint(w, `{"file_info":"image"}`)
					case "/v2/groups/group/messages":
						reply := readQQTestReply(t, r)
						if strings.Contains(reply.text(), "正在生成图片") {
							progress.Add(1)
						}
						fmt.Fprintf(w, `{"id":"sent-%d","ext_info":{"ref_idx":"ref-%d"}}`, reply.Sequence, reply.Sequence)
					default:
						t.Errorf("unexpected path: %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				c.BaseURL, c.ChatProtocol, c.ImageEnabled, c.ImageModel, c.SystemPrompt = server.URL+"/v1", protocol, true, "image-model", "PERSONA_PRIVATE"
				c.CooldownSeconds = 0
				_, err = s.SaveConfig(ctx, c)
				require.NoError(t, err)
				s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
				if scenario == "quota" {
					_, err = db.Exec(`INSERT INTO qq_bot_daily_usage(app_id,scope,day,kind,count) VALUES('app','member:member',$1,'image',$2)`, day(time.Now()), c.DailyImageLimit)
					require.NoError(t, err)
				}
				e := sharedTestEvent("current", "group", "member", current)
				if scenario == "quoted-evidence" {
					e.MsgElements = []MessageElement{{MessageType: 103, Content: "帮我画一只猫"}}
				}
				enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
				s.runOne(ctx)
				var kind, status string
				var raw []byte
				var in, out int64
				require.NoError(t, db.QueryRow(`SELECT kind,status,image_decision,input_tokens,output_tokens FROM qq_bot_jobs WHERE message_id='current'`).Scan(&kind, &status, &raw, &in, &out))
				var receipt imageDecision
				require.NoError(t, json.Unmarshal(raw, &receipt))
				wantImage := scenario == "explicit" || scenario == "command"
				require.Equal(t, wantImage, images.Load() == 1)
				require.Equal(t, images.Load(), progress.Load())
				wantReviews := int32(1)
				if scenario == "ordinary-text" || scenario == "command" {
					wantReviews = 0
				}
				require.Equal(t, wantReviews, reviews.Load())
				if wantImage {
					require.Equal(t, "image", kind)
					require.Equal(t, "succeeded", receipt.Execution)
				} else {
					require.Equal(t, "chat", kind)
				}
				if scenario == "malformed" || scenario == "upstream-failure" {
					require.Equal(t, "failed", status)
					require.Equal(t, "error", receipt.Review)
				} else {
					require.Equal(t, "completed", status)
				}
				switch scenario {
				case "dean", "greeting", "capability", "appreciation":
					require.Equal(t, "text", receipt.Review)
				case "continuation-without-plan", "quoted-evidence":
					require.Equal(t, "clarify", receipt.Review)
				case "quota":
					require.Equal(t, "quota_blocked", receipt.Execution)
				}
				if scenario != "command" && scenario != "ordinary-text" && scenario != "malformed" && scenario != "upstream-failure" {
					require.EqualValues(t, 22, in)
					require.EqualValues(t, 14, out)
				}
				require.NotContains(t, string(raw), current)
				require.NotContains(t, string(raw), "PERSONA_PRIVATE")
				require.NotContains(t, string(raw), "prompt")
				var used int
				require.NoError(t, db.QueryRow(`SELECT COALESCE(SUM(count),0) FROM qq_bot_daily_usage WHERE scope='all' AND kind='image'`).Scan(&used))
				require.EqualValues(t, images.Load(), used)
				page, pageErr := s.ListJobs(ctx, JobFilter{Page: 1, PageSize: 20})
				require.NoError(t, pageErr)
				require.Len(t, page.Items, 1)
				require.JSONEq(t, string(raw), string(page.Items[0].ImageDecision))
				before := chats.Load() + reviews.Load() + images.Load()
				enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
				s.runOne(ctx)
				require.Equal(t, before, chats.Load()+reviews.Load()+images.Load(), "duplicate event cannot replay review or generation")
			})
		}
	}
}

func TestQQBotPendingImagePlanOwnershipExpiryAndSingleUse(t *testing.T) {
	for _, scenario := range []string{"confirm", "other-member", "other-group", "other-app", "expired", "already-completed", "cancel", "review-failure", "delivery-failure", "matching-quote", "wrong-quote", "expires-during-review", "ordinary-cancel", "disabled-cancel"} {
		t.Run(scenario, func(t *testing.T) {
			db := testDB(t)
			s := savedBot(t, db)
			ctx := context.Background()
			c, err := s.Config(ctx)
			require.NoError(t, err)
			const original = "先帮我设计一张猫咪海报，等我确认再生成"
			const confirmation = "就按刚才的方案来一张"
			plan := &pendingImagePlan{Action: chatImageToolName, Prompt: "蓝紫色霓虹猫咪海报", RequestQuote: "设计一张猫咪海报"}
			var phase, images, reviews atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					fmt.Fprint(w, `{"access_token":"test","expires_in":7200}`)
				case "/v1/chat/completions":
					var body map[string]json.RawMessage
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					if strings.Contains(string(body["tools"]), imageReviewToolName) {
						reviews.Add(1)
						var messages []struct {
							Content string `json:"content"`
						}
						require.NoError(t, json.Unmarshal(body["messages"], &messages))
						var data struct {
							Pending *pendingImagePlan `json:"pending_plan"`
						}
						require.NoError(t, json.Unmarshal([]byte(messages[1].Content), &data))
						if phase.Load() == 0 {
							require.Nil(t, data.Pending)
							_, _ = w.Write(imageReviewTestJSON(t, "chat_completions", imageIntentReview{Disposition: "plan", Basis: "explicit", Evidence: "设计一张猫咪海报"}))
							return
						}
						if phase.Load() == 1 && (scenario == "confirm" || scenario == "cancel" || scenario == "review-failure" || scenario == "matching-quote" || scenario == "wrong-quote" || scenario == "expires-during-review") {
							require.NotNil(t, data.Pending)
						} else {
							require.Nil(t, data.Pending)
						}
						if scenario == "expires-during-review" {
							_, updateErr := db.Exec(`UPDATE qq_bot_pending_image_plans SET expires_at=NOW()-INTERVAL '1 second'`)
							require.NoError(t, updateErr)
						}
						if scenario == "review-failure" {
							w.WriteHeader(http.StatusBadGateway)
							return
						}
						if scenario == "cancel" {
							_, _ = w.Write(imageReviewTestJSON(t, "chat_completions", imageIntentReview{Disposition: "text", Basis: "none", Text: "好的，不生成。"}))
							return
						}
						_, _ = w.Write(imageReviewTestJSON(t, "chat_completions", imageIntentReview{Disposition: "execute", Basis: "continuation", Evidence: confirmation}))
						return
					}
					if phase.Load() == 0 {
						args, _ := json.Marshal(map[string]any{"text": "建议蓝紫色霓虹猫咪海报，确认后再生成。", "image_plan": plan})
						_, _ = w.Write([]byte(strings.ReplaceAll(string(imageToolTestJSON(t, "chat_completions", string(args))), `"name":"generate_image"`, `"name":"reply_text"`)))
						return
					}
					if scenario == "ordinary-cancel" {
						_, _ = w.Write(textToolTestJSON(t, "chat_completions", "好的，不生成。"))
						return
					}
					if scenario == "disabled-cancel" {
						require.Contains(t, string(body["tools"]), siteQueryToolName)
						require.NotContains(t, string(body["tools"]), chatImageToolName)
						fmt.Fprint(w, `{"choices":[{"message":{"content":"好的，不生成。"}}]}`)
						return
					}
					_, _ = w.Write(imageToolTestJSON(t, "chat_completions", `{"prompt":"蓝紫色霓虹猫咪海报"}`))
				case "/v1/images/generations":
					images.Add(1)
					json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(testInputPNG(t))}}})
				case "/v2/groups/group/files":
					fmt.Fprint(w, `{"file_info":"image"}`)
				default:
					if strings.HasSuffix(r.URL.Path, "/messages") {
						reply := readQQTestReply(t, r)
						if scenario == "delivery-failure" && phase.Load() == 0 {
							w.WriteHeader(http.StatusBadGateway)
							return
						}
						fmt.Fprintf(w, `{"id":"sent-%d-%d"}`, phase.Load(), reply.Sequence)
						return
					}
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			c.BaseURL, c.ImageEnabled, c.ImageModel, c.CooldownSeconds = server.URL+"/v1", true, "image-model", 0
			c.AllowedGroups = []string{"group", "other-group"}
			_, err = s.SaveConfig(ctx, c)
			require.NoError(t, err)
			s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
			e := sharedTestEvent("plan-request", "group", "member", original)
			enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
			s.runOne(ctx)
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_pending_image_plans`).Scan(&count))
			require.Equal(t, 1, count)
			loaded, err := s.pendingImagePlan(ctx, c, Job{GroupID: "group", MemberID: "member"})
			require.NoError(t, err)
			if scenario == "delivery-failure" {
				require.Nil(t, loaded)
			} else {
				require.NotNil(t, loaded)
				require.Equal(t, plan.Prompt, loaded.Prompt)
			}
			next := sharedTestEvent("confirmation", "group", "member", confirmation)
			switch scenario {
			case "other-member":
				next.Author.MemberID = "another"
			case "other-group":
				next.GroupID = "other-group"
			case "other-app":
				c.AppID = "other-app"
				_, err = s.SaveConfig(ctx, c)
				require.NoError(t, err)
			case "expired":
				_, err = db.Exec(`UPDATE qq_bot_pending_image_plans SET expires_at=NOW()-INTERVAL '1 second'`)
				require.NoError(t, err)
			case "already-completed":
				require.NoError(t, s.clearPendingImagePlan(ctx, c, Job{GroupID: "group", MemberID: "member"}))
			case "matching-quote":
				next.MessageReference.MessageID = "sent-0-1"
			case "wrong-quote":
				next.MessageReference.MessageID = "unrelated-quote"
			case "cancel", "ordinary-cancel":
				next.Content = "取消，不要图片"
			case "disabled-cancel":
				next.Content = "取消，不要图片"
				c.ImageEnabled = false
				_, err = s.SaveConfig(ctx, c)
				require.NoError(t, err)
			}
			phase.Store(1)
			enqueueSharedTestEvent(t, s, c, next, groupAtMessageCreate)
			s.runOne(ctx)
			require.Equal(t, scenario == "confirm" || scenario == "matching-quote", images.Load() == 1)
			var receipt []byte
			require.NoError(t, db.QueryRow(`SELECT image_decision FROM qq_bot_jobs WHERE message_id='confirmation'`).Scan(&receipt))
			var d imageDecision
			require.NoError(t, json.Unmarshal(receipt, &d))
			if scenario == "confirm" {
				require.Equal(t, "pending_plan_confirmed", d.Reason)
				require.NotEmpty(t, d.PlanRef)
				p, err := s.pendingImagePlan(ctx, c, Job{GroupID: "group", MemberID: "member"})
				require.NoError(t, err)
				require.Nil(t, p)
				// A fresh message cannot replay a completed plan, even if a model says yes.
				phase.Store(2)
				next.ID = "confirmation-again"
				enqueueSharedTestEvent(t, s, c, next, groupAtMessageCreate)
				s.runOne(ctx)
				require.EqualValues(t, 1, images.Load())
			}
			if scenario == "cancel" || scenario == "ordinary-cancel" || scenario == "disabled-cancel" || scenario == "review-failure" {
				p, err := s.pendingImagePlan(ctx, c, Job{GroupID: "group", MemberID: "member"})
				require.NoError(t, err)
				require.Nil(t, p)
			}
			wantReviews := int32(2)
			if scenario == "ordinary-cancel" || scenario == "disabled-cancel" {
				wantReviews = 1
			}
			require.GreaterOrEqual(t, reviews.Load(), wantReviews)
			// Expired plaintext plans are removed rather than retained with job receipts.
			_, err = db.Exec(`UPDATE qq_bot_pending_image_plans SET expires_at=NOW()-INTERVAL '1 second'`)
			require.NoError(t, err)
			s.maintenance(ctx)
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_pending_image_plans`).Scan(&count))
			require.Zero(t, count)
		})
	}
}
