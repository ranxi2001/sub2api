//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAnnouncementsAtomicQueueAndConcurrentDedup(t *testing.T) {
	db := testDB(t)
	bot := savedBot(t, db)
	ctx := context.Background()
	req := testAnnouncementRequest()
	req.Groups = []string{"other-group", "group"}
	_, err := db.Exec(`ALTER TABLE qq_bot_jobs ADD CONSTRAINT reject_test_announcement CHECK (event_type<>'MANUAL_ANNOUNCEMENT' OR group_id<>'other-group')`)
	require.NoError(t, err)
	_, err = bot.SendAnnouncement(ctx, req)
	require.Error(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs`).Scan(&count))
	require.Zero(t, count, "failure for one group must roll back the whole batch")
	_, err = db.Exec(`ALTER TABLE qq_bot_jobs DROP CONSTRAINT reject_test_announcement`)
	require.NoError(t, err)
	var wg sync.WaitGroup
	type outcome struct {
		result AnnouncementResult
		err    error
	}
	outcomes := make(chan outcome, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := bot.SendAnnouncement(ctx, req)
			outcomes <- outcome{result, err}
		}()
	}
	wg.Wait()
	close(outcomes)
	var first AnnouncementResult
	var fresh int
	for out := range outcomes {
		require.NoError(t, out.err)
		if first.BatchID == "" {
			first = out.result
		}
		require.Equal(t, first.Jobs, out.result.Jobs)
		if !out.result.Duplicate {
			fresh++
		}
	}
	require.Equal(t, 1, fresh)
	require.Len(t, first.Jobs, 2)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs`).Scan(&count))
	require.Equal(t, 2, count)
	// Worker completion and restarts must not lose the idempotency tombstone.
	for _, job := range first.Jobs {
		bot.finish(Job{ID: job.ID}, "completed", "")
	}
	restarted := New(db, testEncryptor{}, true, nil)
	again, err := restarted.SendAnnouncement(ctx, req)
	require.NoError(t, err)
	require.True(t, again.Duplicate)
	require.Equal(t, first.Jobs, again.Jobs)
	req.Text = "different announcement"
	_, err = restarted.SendAnnouncement(ctx, req)
	require.ErrorIs(t, err, ErrAnnouncementConflict)
	page, err := bot.ListJobs(ctx, JobFilter{Kind: "notifications", Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Equal(t, 2, page.Total, "announcement must appear in notification records")
}

func TestAnnouncementsValidateSavedIdentityAndGroupPolicy(t *testing.T) {
	db := testDB(t)
	bot := savedBot(t, db)
	ctx := context.Background()
	c, err := bot.Config(ctx)
	require.NoError(t, err)
	c.GroupPolicies = []GroupPolicy{{GroupID: "other-group", DisableNotifications: true}}
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		edit func(*AnnouncementRequest)
		want error
	}{
		{"app changed", func(r *AnnouncementRequest) { r.AppID = "previous-app" }, ErrAnnouncementConflict},
		{"sandbox changed", func(r *AnnouncementRequest) { r.Sandbox = true }, ErrAnnouncementConflict},
		{"disallowed", func(r *AnnouncementRequest) { r.Groups = []string{"unallowed"} }, ErrAnnouncementInvalid},
		{"notifications disabled", func(r *AnnouncementRequest) { r.Groups = []string{"group", "other-group"} }, ErrAnnouncementInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := testAnnouncementRequest()
			tc.edit(&req)
			_, err := bot.SendAnnouncement(ctx, req)
			require.ErrorIs(t, err, tc.want)
		})
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs`).Scan(&count))
	require.Zero(t, count)
}

func TestAnnouncementsWorkerSendsMarkdownAndMentionChoiceDuringQuietHours(t *testing.T) {
	for _, mention := range []bool{false, true} {
		t.Run(fmt.Sprint(mention), func(t *testing.T) {
			db := testDB(t)
			bot := savedBot(t, db)
			ctx := context.Background()
			c, err := bot.Config(ctx)
			require.NoError(t, err)
			hour := time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)).Hour()
			c.NotificationQuietHours = QuietHours{Enabled: true, StartHour: hour, EndHour: (hour + 1) % 24}
			_, err = bot.SaveConfig(ctx, c)
			require.NoError(t, err)
			var sent []map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					_, _ = w.Write([]byte(`{"access_token":"test","expires_in":7200}`))
					return
				}
				require.Equal(t, "/v2/groups/group/messages", r.URL.Path)
				var payload map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				sent = append(sent, payload)
				_, _ = w.Write([]byte(`{"id":"sent"}`))
			}))
			defer upstream.Close()
			bot.qqURL, bot.tokenURL = upstream.URL, upstream.URL+"/token"
			req := testAnnouncementRequest()
			req.MentionAll = mention
			out, err := bot.SendAnnouncement(ctx, req)
			require.NoError(t, err)
			require.Empty(t, sent, "HTTP request only queues work")
			bot.runOne(ctx)
			require.Len(t, sent, 1)
			expected := req.Text
			if mention {
				expected = "<qqbot-at-everyone />\n" + expected
			}
			require.Equal(t, float64(2), sent[0]["msg_type"])
			require.Equal(t, expected, sent[0]["markdown"].(map[string]any)["content"])
			require.NotContains(t, sent[0], "msg_id")
			require.NotContains(t, sent[0], "event_id")
			var status string
			require.NoError(t, db.QueryRow(`SELECT status FROM qq_bot_jobs WHERE id=$1`, out.Jobs[0].ID).Scan(&status))
			require.Equal(t, "completed", status)
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_daily_usage`).Scan(&count))
			require.Zero(t, count, "manual sends must not reserve AI usage")
		})
	}
}

func TestAnnouncementsRevocationExpiryAndNoReplay(t *testing.T) {
	for _, scenario := range []string{"revoked", "sandbox", "expired", "failed"} {
		t.Run(scenario, func(t *testing.T) {
			db := testDB(t)
			bot := savedBot(t, db)
			ctx := context.Background()
			req := testAnnouncementRequest()
			out, err := bot.SendAnnouncement(ctx, req)
			require.NoError(t, err)
			c, err := bot.Config(ctx)
			require.NoError(t, err)
			want := "cancelled"
			switch scenario {
			case "revoked":
				c.GroupPolicies = []GroupPolicy{{GroupID: "group", DisableNotifications: true}}
			case "sandbox":
				c.Sandbox = true
			case "expired":
				want = "expired"
				_, err = db.Exec(`UPDATE qq_bot_jobs SET created_at=NOW()-INTERVAL '61 minutes' WHERE id=$1`, out.Jobs[0].ID)
				require.NoError(t, err)
			case "failed":
				want = "delivery_failed"
			}
			_, err = bot.SaveConfig(ctx, c)
			require.NoError(t, err)
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					_, _ = w.Write([]byte(`{"access_token":"test","expires_in":7200}`))
					return
				}
				calls++
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"code":40034105,"message":"permission denied"}`))
			}))
			defer upstream.Close()
			bot.qqURL, bot.tokenURL = upstream.URL, upstream.URL+"/token"
			bot.runOne(ctx)
			bot.runOne(ctx)
			var status string
			require.NoError(t, db.QueryRow(`SELECT status FROM qq_bot_jobs WHERE id=$1`, out.Jobs[0].ID).Scan(&status))
			require.Equal(t, want, status)
			if scenario == "failed" {
				require.Equal(t, 1, calls, "failed delivery must never auto replay")
			} else {
				require.Zero(t, calls)
			}
		})
	}
}

func TestAnnouncementsShareAutomaticNotificationPacing(t *testing.T) {
	db := testDB(t)
	bot := savedBot(t, db)
	ctx := context.Background()
	_, err := bot.SendAnnouncement(ctx, testAnnouncementRequest())
	require.NoError(t, err)
	first, err := bot.claimJob(ctx)
	require.NoError(t, err)
	require.Equal(t, "announcement", first.Kind)
	_, err = db.Exec(`INSERT INTO qq_bot_jobs(id,app_id,event_key,event_type,group_id,payload,kind) VALUES('auto','app','auto','PRICE_CHANGE_NOTIFICATION','other-group','{}','price_notification')`)
	require.NoError(t, err)
	_, err = bot.claimJob(ctx)
	require.Error(t, err)
	bot.finish(first, "completed", "")
	_, err = bot.claimJob(ctx)
	require.Error(t, err, "completion must preserve proactive-message pacing")
	_, err = db.Exec(`UPDATE qq_bot_jobs SET started_at=NOW()-INTERVAL '4 seconds' WHERE id=$1`, first.ID)
	require.NoError(t, err)
	second, err := bot.claimJob(ctx)
	require.NoError(t, err)
	require.Equal(t, "price_notification", second.Kind)
}

func TestAnnouncementsRecheckPolicyAfterTokenRefresh(t *testing.T) {
	for _, scenario := range []string{"disabled", "group", "sandbox"} {
		t.Run(scenario, func(t *testing.T) {
			db := testDB(t)
			bot := savedBot(t, db)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					_, _ = w.Write([]byte(`{"access_token":"test","expires_in":7200}`))
					return
				}
				calls++
				_, _ = w.Write([]byte(`{"id":"must-not-send"}`))
			}))
			defer upstream.Close()
			bot.qqURL, bot.tokenURL = upstream.URL, upstream.URL+"/token"
			out, err := bot.SendAnnouncement(ctx, testAnnouncementRequest())
			require.NoError(t, err)
			done := make(chan struct{})
			go func() { defer close(done); bot.runOne(ctx) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("token request did not start")
			}
			c, err := bot.Config(ctx)
			require.NoError(t, err)
			switch scenario {
			case "disabled":
				c.Enabled = false
			case "group":
				c.GroupPolicies = []GroupPolicy{{GroupID: "group", DisableNotifications: true}}
			case "sandbox":
				c.Sandbox = true
			}
			_, err = bot.SaveConfig(ctx, c)
			require.NoError(t, err)
			once.Do(func() { close(release) })
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("worker did not finish")
			}
			require.Zero(t, calls)
			var status string
			require.NoError(t, db.QueryRow(`SELECT status FROM qq_bot_jobs WHERE id=$1`, out.Jobs[0].ID).Scan(&status))
			require.Equal(t, "cancelled", status)
		})
	}
}
