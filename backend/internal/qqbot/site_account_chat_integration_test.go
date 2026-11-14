//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQQSiteAccountChatReturnsBackendResultDirectly(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			ctx := context.Background()
			db := testDB(t)
			s := savedBot(t, db)
			uid := user(t, db)
			bind(t, db, uid, "member")
			_, err := db.Exec(`UPDATE users SET balance=12.50 WHERE id=$1`, uid)
			require.NoError(t, err)
			c, err := s.Config(ctx)
			require.NoError(t, err)
			chats := 0
			var replies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					fmt.Fprint(w, `{"access_token":"test-only","expires_in":7200}`)
				case "/v1/chat/completions", "/v1/responses":
					chats++
					var body map[string]json.RawMessage
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					key := "messages"
					if protocol == "responses" {
						key = "input"
					}
					require.NotContains(t, string(body[key]), "12.50", "personal query results never return to the model")
					_, _ = w.Write(siteQueryTestResponse(t, protocol, `{"kind":"account_balance","model":"","activity":"","page":1}`, false))
				case "/v2/groups/group/messages":
					reply := readQQTestReply(t, r)
					replies = append(replies, reply.text())
					fmt.Fprint(w, `{"id":"personal-query-reply","ext_info":{"ref_idx":"personal-query-ref"}}`)
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			c.BaseURL, c.ChatProtocol, c.CooldownSeconds = server.URL+"/v1", protocol, 0
			_, err = s.SaveConfig(ctx, c)
			require.NoError(t, err)
			s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
			before := siteQueryMutationSnapshot(t, db, uid)
			e := sharedTestEvent("personal-query-current", "group", "member", "麻烦核实一下我在本站钱包中的可用资金")
			enqueueSharedTestEvent(t, s, c, e, groupAtMessageCreate)
			s.runOne(ctx)
			var status, reason string
			require.NoError(t, db.QueryRow(`SELECT status,decision_reason FROM qq_bot_jobs WHERE message_id='personal-query-current'`).Scan(&status, &reason))
			require.Equal(t, "completed", status)
			require.Equal(t, "site_query:account_balance", reason)
			require.Equal(t, 1, chats, "a query result is sent without a second chat request")
			require.Contains(t, strings.Join(replies, "\n"), "余额：USD 12.50")
			var shareable bool
			require.NoError(t, db.QueryRow(`SELECT shareable FROM qq_bot_message_references WHERE message_id='personal-query-reply'`).Scan(&shareable))
			require.False(t, shareable)
			delete(before, "qq_bot_daily_usage")
			after := siteQueryMutationSnapshot(t, db, uid)
			delete(after, "qq_bot_daily_usage") // the single model call consumes chat quota
			require.Equal(t, before, after)
		})
	}
}
