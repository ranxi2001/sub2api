//go:build integration

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
	"time"

	"github.com/stretchr/testify/require"
)

func TestQQSiteChatMenuAndDirectQueryDelivery(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		for _, scenario := range []string{"menu", "query", "long-query-with-image", "quoted-how-to"} {
			t.Run(protocol+"/"+scenario, func(t *testing.T) {
				ctx := context.Background()
				db := testDB(t)
				s := savedBot(t, db)
				c, err := s.Config(ctx)
				require.NoError(t, err)
				var chats, queries atomic.Int32
				var replies []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/token":
						fmt.Fprint(w, `{"access_token":"local-test","expires_in":7200}`)
					case "/v1/chat/completions", "/v1/responses":
						chats.Add(1)
						var body map[string]json.RawMessage
						require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
						key := "messages"
						if protocol == "responses" {
							key = "input"
						}
						require.Contains(t, string(body[key]), "真实指令清单 JSON")
						require.Contains(t, string(body[key]), "价格 模型名")
						if scenario == "quoted-how-to" {
							if protocol == "responses" {
								fmt.Fprint(w, `{"status":"completed","output_text":"这里解释示例的用法。"}`)
							} else {
								fmt.Fprint(w, `{"choices":[{"message":{"content":"这里解释示例的用法。"}}]}`)
							}
							return
						}
						_, _ = w.Write(siteQueryTestResponse(t, protocol, `{"kind":"channel_status","model":"","activity":"","page":1}`, false))
					case "/v2/groups/group/messages":
						reply := readQQTestReply(t, r)
						require.LessOrEqual(t, reply.Sequence, 5)
						replies = append(replies, reply.text())
						_, _ = fmt.Fprintf(w, `{"id":"sent-%d","ext_info":{"ref_idx":"ref-%d"}}`, len(replies), len(replies))
					default:
						t.Errorf("unexpected endpoint %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				c.BaseURL, c.ChatProtocol, c.CooldownSeconds = server.URL+"/v1", protocol, 0
				_, err = s.SaveConfig(ctx, c)
				require.NoError(t, err)
				s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
				s.SetChannelStatusProvider(channelStatusProviderFunc(func(context.Context) (*ChannelStatusReport, error) {
					queries.Add(1)
					report := channelStatusFixture()
					if scenario == "long-query-with-image" {
						for i := 0; i < 80; i++ {
							report.Groups = append(report.Groups, ChannelStatusGroup{Name: strings.Repeat("测试分组", 20), Metrics: report.Overall})
						}
					}
					return report, nil
				}))
				e := sharedTestEvent("site-current", "group", "member", "现在渠道稳不稳？")
				if scenario == "menu" {
					e.Content = "菜单"
				}
				if scenario == "long-query-with-image" {
					e.Attachments = []Attachment{{ContentType: "image/png", URL: "https://example.test/input.png"}}
					s.imageDownload = func(context.Context, string) ([]byte, error) { return testInputPNG(t), nil }
				}
				if scenario == "quoted-how-to" {
					e.Content = "怎么用"
					previous := Job{ID: "previous", GroupID: "group", MessageID: "previous", CreatedAt: time.Now()}
					require.NoError(t, s.rememberSentMessage(ctx, c, previous, []byte(`{"id":"quoted","ext_info":{"ref_idx":"quoted"}}`), "这是接入代码示例。", false, true))
					e.MessageScene.Ext = []string{"ref_msg_idx=quoted"}
				}
				enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
				s.runOne(ctx)
				var status, reason string
				var in int64
				require.NoError(t, db.QueryRow(`SELECT status,decision_reason,input_tokens FROM qq_bot_jobs WHERE message_id='site-current'`).Scan(&status, &reason, &in))
				require.Equal(t, "completed", status)
				text := strings.Join(replies, "\n")
				switch scenario {
				case "menu":
					require.Zero(t, chats.Load())
					require.Zero(t, queries.Load())
					require.Contains(t, text, "指令帮助")
					require.Contains(t, text, "价格 模型名")
				case "quoted-how-to":
					require.EqualValues(t, 1, chats.Load())
					require.Zero(t, queries.Load())
					require.Contains(t, text, "这里解释示例")
				default:
					require.EqualValues(t, 1, chats.Load())
					require.EqualValues(t, 1, queries.Load())
					require.EqualValues(t, 11, in)
					require.Equal(t, "site_query:channel_status", reason)
					require.Contains(t, text, "查询时间：")
					require.NotContains(t, text, "已截断")
					require.NotContains(t, text, "内容过长")
					var shareable bool
					require.NoError(t, db.QueryRow(`SELECT BOOL_OR(shareable) FROM qq_bot_message_references WHERE source_job_id=(SELECT id FROM qq_bot_jobs WHERE message_id='site-current')`).Scan(&shareable))
					require.False(t, shareable)
				}
			})
		}
	}
}
