//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNotificationMentionPreferencePreservesBaselinesAndQueuedAlerts(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	bot := savedBot(t, db)
	c, err := bot.Config(ctx)
	require.NoError(t, err)
	c.PriceNotifications = PriceNotificationConfig{Enabled: true, Groups: []string{"group"}}
	c.HealthNotifications.Enabled = true
	c.HealthNotifications.Groups = []string{"group"}
	c.HealthNotifications.FailureMinutes = 1
	snapshot := priceTestSnapshot("USD 2")
	bot.SetPriceSnapshotProvider(priceSourceFunc(func(context.Context, time.Time) (PriceSnapshot, error) { return snapshot, nil }))
	bot.SetHealthSnapshotProvider(healthSourceFunc(func(_ context.Context, at time.Time) (HealthSnapshot, error) {
		return HealthSnapshot{UpdatedAt: at, Complete: true, Groups: []HealthMetric{{Key: "group:1", Name: "Example", Requests: 100, ErrorRate: .5}}}, nil
	}))
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	at := time.Now()
	require.NoError(t, bot.checkHealthChanges(ctx, at))
	snapshot = priceTestSnapshot("USD 1.5")
	_, err = bot.checkPriceChanges(ctx, at.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, bot.checkHealthChanges(ctx, at.Add(time.Minute)))
	readBaseline := func(table string) string {
		var baseline string
		require.NoError(t, db.QueryRow(`SELECT snapshot::text FROM `+table+` WHERE app_id='app'`).Scan(&baseline))
		return baseline
	}
	priceBaseline := readBaseline("qq_bot_price_snapshots")
	healthBaseline := readBaseline("qq_bot_health_snapshots")
	c.PriceNotifications.MentionAll, c.HealthNotifications.MentionAll = true, true
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	require.Equal(t, priceBaseline, readBaseline("qq_bot_price_snapshots"))
	require.Equal(t, healthBaseline, readBaseline("qq_bot_health_snapshots"))
	var queued int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE status='queued' AND event_type IN ($1,$2)`, priceNotificationEvent, healthNotificationEvent).Scan(&queued))
	require.Equal(t, 2, queued)
	c, err = bot.Config(ctx)
	require.NoError(t, err)
	require.True(t, c.PriceNotifications.MentionAll)
	require.True(t, c.HealthNotifications.MentionAll)
	var sent []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":"7200"}`))
			return
		}
		var body struct {
			Markdown struct {
				Content string `json:"content"`
			} `json:"markdown"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.True(t, strings.HasPrefix(body.Markdown.Content, "<qqbot-at-everyone />\n"))
		require.Equal(t, 1, strings.Count(body.Markdown.Content, "<qqbot-at-everyone />"))
		sent = append(sent, body.Markdown.Content)
		_, _ = w.Write([]byte(`{"id":"notice"}`))
	}))
	defer server.Close()
	bot.qqURL, bot.tokenURL = server.URL, server.URL+"/token"
	rows, err := db.Query(`SELECT id,app_id,event_type,group_id,payload,created_at FROM qq_bot_jobs WHERE status='queued' AND event_type IN ($1,$2)`, priceNotificationEvent, healthNotificationEvent)
	require.NoError(t, err)
	var jobs []Job
	for rows.Next() {
		var j Job
		require.NoError(t, rows.Scan(&j.ID, &j.AppID, &j.EventType, &j.GroupID, &j.Payload, &j.CreatedAt))
		jobs = append(jobs, j)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	for _, j := range jobs {
		bot.deliverPriceNotification(ctx, c, j)
		var status string
		require.NoError(t, db.QueryRow(`SELECT status FROM qq_bot_jobs WHERE id=$1`, j.ID).Scan(&status))
		require.Equal(t, "completed", status)
	}
	require.Len(t, sent, 2)
}

func TestNotificationBatchMentionsOnlyFirstMessagePerGroup(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	bot := savedBot(t, db)
	c, err := bot.Config(ctx)
	require.NoError(t, err)
	c.PriceNotifications = PriceNotificationConfig{Enabled: true, MentionAll: true}
	c.HealthNotifications.Enabled, c.HealthNotifications.MentionAll = true, true
	c.HealthNotifications.FailureMinutes = 1
	price := "USD 2"
	bot.SetPriceSnapshotProvider(priceSourceFunc(func(context.Context, time.Time) (PriceSnapshot, error) {
		snapshot := priceTestSnapshot(price)
		snapshot.States = append(snapshot.States, snapshot.States[0])
		snapshot.States[1].Key, snapshot.States[1].Name = "group:2", "另一个分组"
		return snapshot, nil
	}))
	bot.SetHealthSnapshotProvider(healthSourceFunc(func(_ context.Context, at time.Time) (HealthSnapshot, error) {
		return HealthSnapshot{UpdatedAt: at, Complete: true, Groups: []HealthMetric{
			{Key: "group:1", Name: "Example", Requests: 100, ErrorRate: .5},
			{Key: "group:2", Name: "Another", Requests: 100, ErrorRate: .5},
		}}, nil
	}))
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	at := time.Now()
	require.NoError(t, bot.checkHealthChanges(ctx, at))
	price = "USD 1.5"
	_, err = bot.checkPriceChanges(ctx, at.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, bot.checkHealthChanges(ctx, at.Add(time.Minute)))
	rows, err := db.Query(`SELECT id,app_id,event_type,group_id,payload,created_at FROM qq_bot_jobs WHERE status='queued' AND event_type IN ($1,$2) ORDER BY created_at,id`, priceNotificationEvent, healthNotificationEvent)
	require.NoError(t, err)
	var jobs []Job
	for rows.Next() {
		var j Job
		require.NoError(t, rows.Scan(&j.ID, &j.AppID, &j.EventType, &j.GroupID, &j.Payload, &j.CreatedAt))
		jobs = append(jobs, j)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Len(t, jobs, 8)
	var delivered []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"test-token","expires_in":"7200"}`))
			return
		}
		var body struct {
			Markdown struct {
				Content string `json:"content"`
			} `json:"markdown"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		delivered = append(delivered, body.Markdown.Content)
		_, _ = w.Write([]byte(`{"id":"notice"}`))
	}))
	defer server.Close()
	bot.qqURL, bot.tokenURL = server.URL, server.URL+"/token"
	seen := map[string]int{}
	for index, j := range jobs {
		key := j.EventType + ":" + j.GroupID
		var payload struct {
			Text         string `json:"text"`
			Continuation bool   `json:"continuation"`
		}
		require.NoError(t, json.Unmarshal(j.Payload, &payload))
		require.Equal(t, seen[key] > 0, payload.Continuation)
		if seen[key] == 0 {
			// Jobs created before continuation tracking remain first messages.
			j.Payload, err = json.Marshal(map[string]any{"text": payload.Text, "sandbox": c.Sandbox})
			require.NoError(t, err)
		}
		bot.deliverPriceNotification(ctx, c, j)
		require.Len(t, delivered, index+1)
		want := payload.Text
		if seen[key] == 0 {
			want = "<qqbot-at-everyone />\n" + want
		}
		require.Equal(t, want, delivered[len(delivered)-1])
		seen[key]++
	}
	require.Len(t, delivered, 8)
	for _, count := range seen {
		require.Equal(t, 2, count)
	}
}
