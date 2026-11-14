//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQQSiteQueryFollowupUsesFreshBindingAndNoExtraModel(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			ctx := context.Background()
			db := testDB(t)
			s := savedBot(t, db)
			c, err := s.Config(ctx)
			require.NoError(t, err)
			first, second := user(t, db), user(t, db)
			bind(t, db, first, "member")
			calls, sends := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					fmt.Fprint(w, `{"access_token":"test","expires_in":7200}`)
				case "/v1/chat/completions", "/v1/responses":
					calls++
					if calls == 1 {
						_, _ = w.Write(siteQueryTestResponse(t, protocol, `{"kind":"model_price","model":"model-a","activity":"","page":1}`, false))
					} else if protocol == "responses" {
						fmt.Fprint(w, `{"status":"completed","output_text":"这是引用内容的说明。"}`)
					} else {
						fmt.Fprint(w, `{"choices":[{"message":{"content":"这是引用内容的说明。"}}]}`)
					}
				case "/v2/groups/group/messages":
					_ = readQQTestReply(t, r)
					sends++
					fmt.Fprintf(w, `{"id":"sent-%d","ext_info":{"ref_idx":"sent-%d"}}`, sends, sends)
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			c.BaseURL, c.ChatProtocol, c.CooldownSeconds = server.URL+"/v1", protocol, 0
			_, err = s.SaveConfig(ctx, c)
			require.NoError(t, err)
			s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
			var identities []int64
			var models []string
			s.SetModelPriceProvider(siteModelPriceFunc(func(_ context.Context, model string, uid int64, _ time.Time) ([]string, error) {
				identities = append(identities, uid)
				models = append(models, model)
				return []string{"one", "two", "three", "four", "five", "six complete tiers"}, nil
			}))
			for i, text := range []string{"model-a多少钱", "下一页", "换成 model-b"} {
				if i == 1 {
					_, err = db.Exec("UPDATE qq_bot_bindings SET user_id=$1 WHERE app_id='app' AND member_id='member'", second)
					require.NoError(t, err)
				}
				id := fmt.Sprintf("query-%d", i)
				e := sharedTestEvent(id, "group", "member", text)
				enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
				s.runOne(ctx)
				var status, result string
				var tokens int64
				var raw []byte
				require.NoError(t, db.QueryRow("SELECT status,result_text,input_tokens,site_query FROM qq_bot_jobs WHERE message_id=$1", id).Scan(&status, &result, &tokens, &raw))
				require.Equal(t, "completed", status)
				var decision SiteQueryDecision
				require.NoError(t, json.Unmarshal(raw, &decision))
				require.Equal(t, "succeeded", decision.Status)
				if i == 1 {
					require.Contains(t, result, "six complete tiers")
					require.Equal(t, 2, decision.Page)
				}
				if i > 0 {
					require.Zero(t, tokens)
				}
			}
			require.Equal(t, 1, calls)
			require.Equal(t, []int64{first, second, second}, identities)
			require.Equal(t, []string{"model-a", "model-a", "model-b"}, models)
			// An explicit quote takes priority over the implicit most recent query.
			quoted := sharedTestEvent("quoted-followup", "group", "member", "下一页")
			quoted.MessageScene.Ext = []string{fmt.Sprintf("ref_msg_idx=sent-%d", sends)}
			enqueueSharedTestEvent(t, s, c, quoted, groupAtMessageCreate)
			s.runOne(ctx)
			require.Equal(t, 2, calls)
			require.Len(t, identities, 3)
		})
	}
}
