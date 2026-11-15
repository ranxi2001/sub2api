//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRankingNotificationsPendingRewardsAtomicAndDeduplicated(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	uid := user(t, db)
	bind(t, db, uid, "winner")
	seedPendingRankingReward(t, db, uid, "tokens", GrowthReward{Type: "balance", Amount: 2})
	seedPendingRankingReward(t, db, uid, "amount", GrowthReward{Type: "physical", Quantity: 1, PrizeName: "Cup"})
	seedPendingRankingReward(t, db, user(t, db), "tokens", GrowthReward{Type: "chances", Quantity: 3})

	// Failure to persist the notification rolls back the award as well: a
	// committed grant must not lose its notification between the two writes.
	_, err = db.Exec(`ALTER TABLE qq_bot_jobs ADD CONSTRAINT reject_ranking_notification CHECK(event_type<>'RANKING_REWARD_NOTIFICATION')`)
	require.NoError(t, err)
	require.Error(t, s.settlePendingRankingRewards(ctx, c, time.Now()))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_ranking_claims WHERE claimed_at IS NOT NULL`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes`).Scan(&count))
	require.Zero(t, count)
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Zero(t, balance)
	_, err = db.Exec(`ALTER TABLE qq_bot_jobs DROP CONSTRAINT reject_ranking_notification`)
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.settlePendingRankingRewards(ctx, c, time.Now())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	page, err := s.ListJobs(ctx, JobFilter{Kind: "notifications", Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Equal(t, 2, page.Total)
	for _, j := range page.Items {
		require.Equal(t, "ranking_notification", j.Kind)
		require.Equal(t, "winner", j.MemberID)
		require.Equal(t, "group", j.GroupID)
	}
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 2., balance)
	// Proactive notices share the existing pacing limit across workers.
	j, err := s.claimJob(ctx)
	require.NoError(t, err)
	_, err = s.claimJob(ctx)
	require.ErrorIs(t, err, sql.ErrNoRows)
	s.finish(j, "completed", "")
	_, err = s.claimJob(ctx)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = db.Exec(`UPDATE qq_bot_jobs SET started_at=NOW()-INTERVAL '4 seconds' WHERE id=$1`, j.ID)
	require.NoError(t, err)
	_, err = s.claimJob(ctx)
	require.NoError(t, err)
}

func TestRankingNotificationDeliveryChecksBindingAndUsesMention(t *testing.T) {
	for _, scenario := range []string{"balance", "chances", "physical", "other", "unbound", "rebound", "moved", "left", "disabled", "group_disabled", "quiet", "sandbox", "send_failed"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			db := testDB(t)
			s := savedBot(t, db)
			c, err := s.Config(ctx)
			require.NoError(t, err)
			uid := user(t, db)
			bind(t, db, uid, "winner")
			reward := GrowthReward{Type: "balance", Amount: 1.25}
			switch scenario {
			case "chances":
				reward = GrowthReward{Type: "chances", Quantity: 2}
			case "physical", "other":
				reward = GrowthReward{Type: scenario, Quantity: 1, PrizeName: `Cup <qqbot-at-everyone />`, ClaimInstructions: "Contact admin"}
			}
			seedPendingRankingReward(t, db, uid, "tokens", reward)
			require.NoError(t, s.settlePendingRankingRewards(ctx, c, time.Now()))
			var payload string
			require.NoError(t, db.QueryRow(`SELECT payload::text FROM qq_bot_jobs WHERE event_type=$1`, rankingNotificationEvent).Scan(&payload))
			require.NotContains(t, payload, "email")

			wantStatus, wantRequests := "cancelled", int32(0)
			switch scenario {
			case "unbound":
				_, err = db.Exec(`DELETE FROM qq_bot_bindings WHERE user_id=$1`, uid)
			case "rebound":
				_, err = db.Exec(`UPDATE qq_bot_bindings SET user_id=$1 WHERE member_id='winner'`, user(t, db))
			case "moved":
				_, err = db.Exec(`UPDATE qq_bot_bindings SET group_id='other-group' WHERE user_id=$1`, uid)
			case "left":
				_, err = db.Exec(`INSERT INTO qq_bot_members(app_id,group_id,member_id,left_at) VALUES('app','group','winner',NOW())`)
			case "disabled":
				_, err = db.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, uid)
			case "group_disabled":
				c.GroupPolicies = []GroupPolicy{{GroupID: "group", DisableNotifications: true}}
				_, err = s.SaveConfig(ctx, c)
			case "quiet":
				hour := time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)).Hour()
				c.NotificationQuietHours = QuietHours{Enabled: true, StartHour: hour, EndHour: (hour + 1) % 24}
				_, err = s.SaveConfig(ctx, c)
				wantStatus = "skipped"
			case "sandbox":
				c.Sandbox = !c.Sandbox
				_, err = s.SaveConfig(ctx, c)
			case "send_failed":
				wantStatus, wantRequests = "delivery_failed", 1
			default:
				wantStatus, wantRequests = "completed", 1
			}
			require.NoError(t, err)

			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					_, _ = w.Write([]byte(`{"access_token":"test","expires_in":"7200"}`))
					return
				}
				requests.Add(1)
				require.Equal(t, "/v2/groups/group/messages", r.URL.Path)
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.EqualValues(t, 2, body["msg_type"])
				text := body["markdown"].(map[string]any)["content"].(string)
				require.True(t, strings.HasPrefix(text, "<qqbot-at-user id=\"winner\" />\n"))
				require.Contains(t, text, "2026-10-20（北京时间）")
				require.Contains(t, text, "Token 消耗榜 · 第 1 名")
				require.Contains(t, text, reward.description())
				if reward.Type == "physical" || reward.Type == "other" {
					require.Contains(t, text, "待管理员发放")
					require.NotContains(t, text, "已到账")
					require.Contains(t, text, `\<qqbot-at-everyone /\>`)
				} else {
					require.Contains(t, text, "状态：已到账")
				}
				for _, key := range []string{"content", "msg_id", "event_id", "msg_seq"} {
					require.NotContains(t, body, key)
				}
				if scenario == "send_failed" {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"code":40034105}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"notice"}`))
			}))
			defer server.Close()
			s.qqURL, s.tokenURL = server.URL, server.URL+"/token"
			s.runOne(ctx)
			var status string
			require.NoError(t, db.QueryRow(`SELECT status FROM qq_bot_jobs WHERE event_type=$1`, rankingNotificationEvent).Scan(&status))
			require.Equal(t, wantStatus, status)
			require.NoError(t, s.settlePendingRankingRewards(ctx, c, time.Now()))
			s.runOne(ctx)
			require.Equal(t, wantRequests, requests.Load())
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes`).Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}
