//go:build integration

package qqbot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQQChatPreviewIdentityAndNoMutations(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			ctx := context.Background()
			db := testDB(t)
			s := savedBot(t, db)
			uid := user(t, db)
			bind(t, db, uid, "member")
			c, err := s.Config(ctx)
			require.NoError(t, err)
			modelCalls, queryCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				modelCalls++
				_, _ = w.Write(siteQueryTestResponse(t, protocol, `{"kind":"model_price","model":"model-test","activity":"","page":1}`, false))
			}))
			defer server.Close()
			c.BaseURL, c.ChatProtocol, c.ImageEnabled = server.URL, protocol, true
			c.ImageModel = "test-image-model"
			_, err = s.SaveConfig(ctx, c)
			require.NoError(t, err)
			var receivedUser int64
			s.SetModelPriceProvider(siteModelPriceFunc(func(_ context.Context, _ string, id int64, _ time.Time) ([]string, error) {
				queryCalls++
				receivedUser = id
				return []string{fmt.Sprintf("当前报价 uid=%d", id)}, nil
			}))
			before := siteQueryMutationSnapshot(t, db, uid)
			for _, table := range []string{"qq_bot_jobs", "qq_bot_group_messages"} {
				// Include conversation/job writes beyond the activity ledgers.
				var count int
				require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count))
				before[table] = fmt.Sprint(count)
			}
			for _, tc := range []struct {
				member string
				wantID int64
			}{{"member", uid}, {"", 0}} {
				out, err := s.PreviewChat(ctx, ChatPreviewRequest{GroupID: "group", MemberID: tc.member, Question: "model-test多少钱"})
				require.NoError(t, err)
				require.Equal(t, "site_query", out.Action)
				require.Equal(t, tc.wantID, receivedUser)
				require.Contains(t, out.Text, fmt.Sprintf("uid=%d", tc.wantID))
			}
			require.Equal(t, 2, modelCalls)
			require.Equal(t, 2, queryCalls)
			for _, req := range []ChatPreviewRequest{
				{GroupID: "other-group", MemberID: "member", Question: "model-test多少钱"},
				{GroupID: "group", MemberID: "unknown-member", Question: "model-test多少钱"},
				{GroupID: "group", MemberID: "member", Question: "/抽奖 10"},
			} {
				_, err := s.PreviewChat(ctx, req)
				require.Error(t, err)
			}
			require.Equal(t, 2, modelCalls, "invalid identity and slash actions must not call a model")
			after := siteQueryMutationSnapshot(t, db, uid)
			for _, table := range []string{"qq_bot_jobs", "qq_bot_group_messages"} {
				var count int
				require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count))
				after[table] = fmt.Sprint(count)
			}
			require.Equal(t, before, after)
		})
	}
}
