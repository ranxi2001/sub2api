//go:build integration

package qqbot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQQNaturalAccountQueriesBypassModelAndKeepPrivate(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	insightsUsageTable(t, db)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	_, err := db.Exec(`UPDATE users SET balance=12.50 WHERE id=$1`, uid)
	require.NoError(t, err)
	checkin, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	checkin.Growth.Tasks = []GrowthTask{{ID: "binding", Name: "绑定奖励", Kind: "binding", Target: 1, Chances: 5, Enabled: true}}
	require.NoError(t, s.SaveCheckinConfig(ctx, checkin))
	lottery := defaultLotteryConfig()
	lottery.Enabled = true
	require.NoError(t, s.SaveLotteryConfig(ctx, lottery))
	c, err := s.Config(ctx)
	require.NoError(t, err)
	c.ChatDisabled, c.ImageEnabled = true, false
	c.DailyChatLimit, c.TotalDailyChatLimit = 0, 0
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	var replies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			fmt.Fprint(w, `{"access_token":"test-only","expires_in":7200}`)
		case "/v2/groups/group/bot_state":
			fmt.Fprint(w, `{"member_openid":"this-bot"}`)
		case "/v2/groups/group/messages":
			replies = append(replies, readQQTestReply(t, r).text())
			fmt.Fprintf(w, `{"id":"direct-query-%d","ext_info":{"ref_idx":"direct-ref-%d"}}`, len(replies), len(replies))
		default:
			t.Errorf("direct query must not call model: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
	before := siteQueryMutationSnapshot(t, db, uid)
	for _, eventType := range []string{groupAtMessageCreate, groupMessageCreate} {
		for i, test := range []struct{ question, want string }{
			{"帮我看一下我的余额", "余额：USD 12.50"},
			{"看看我今天的用量", "今日用量"},
			{"我还有多少次抽奖", "剩余抽奖次数：0 次"},
		} {
			id := fmt.Sprintf("%s-direct-%d", eventType, i)
			e := sharedTestEvent(id, "group", "member", "<@this-bot> "+test.question)
			enqueueSharedTestEvent(t, s, c, e, eventType)
			s.runOne(ctx)
			var status, preview string
			require.NoError(t, db.QueryRow(`SELECT status,message_preview FROM qq_bot_jobs WHERE message_id=$1`, id).Scan(&status, &preview))
			require.Equal(t, "completed", status)
			require.Empty(t, preview)
			require.Contains(t, replies[len(replies)-1], test.want)
			require.Equal(t, before, siteQueryMutationSnapshot(t, db, uid), "query must not settle eligible binding rewards or reserve chat quota")
		}
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_group_messages`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_message_references WHERE shareable`).Scan(&count))
	require.Zero(t, count)
	require.Len(t, replies, 6)
	require.NotContains(t, strings.Join(replies, "\n"), "5 次")
}
