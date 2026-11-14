//go:build integration

package qqbot

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type priceSourceFunc func(context.Context, time.Time) (PriceSnapshot, error)

func (f priceSourceFunc) PriceSnapshot(ctx context.Context, at time.Time) (PriceSnapshot, error) {
	return f(ctx, at)
}

func TestPriceNotificationsDurableDedupAndAtomicQueue(t *testing.T) {
	db := testDB(t)
	bot := savedBot(t, db)
	ctx := context.Background()
	c, err := bot.Config(ctx)
	require.NoError(t, err)
	c.PriceNotifications = PriceNotificationConfig{Enabled: true, Scheduled: true, Groups: []string{"group", "other-group"}}
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	snapshot := priceTestSnapshot("USD 2")
	source := priceSourceFunc(func(context.Context, time.Time) (PriceSnapshot, error) { return snapshot, nil })
	bot.SetPriceSnapshotProvider(source)
	at := time.Now()
	_, err = bot.checkPriceChanges(ctx, at)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE event_type=$1`, priceNotificationEvent).Scan(&count))
	require.Zero(t, count)
	snapshot = priceTestSnapshot("USD 1.5")
	// Failure for the second recipient rolls back the first recipient and baseline.
	_, err = db.Exec(`ALTER TABLE qq_bot_jobs ADD CONSTRAINT reject_test_notice CHECK (event_type <> 'PRICE_CHANGE_NOTIFICATION' OR group_id <> 'other-group')`)
	require.NoError(t, err)
	_, err = bot.checkPriceChanges(ctx, at.Add(16*time.Second))
	require.Error(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE event_type=$1`, priceNotificationEvent).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`ALTER TABLE qq_bot_jobs DROP CONSTRAINT reject_test_notice`)
	require.NoError(t, err)
	var wg sync.WaitGroup
	var failures atomic.Int32
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := bot.checkPriceChanges(ctx, at.Add(16*time.Second)); err != nil {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Zero(t, failures.Load())
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE event_type=$1`, priceNotificationEvent).Scan(&count))
	require.Equal(t, 2, count)
	restarted := New(db, testEncryptor{}, true, nil)
	restarted.SetPriceSnapshotProvider(source)
	_, err = restarted.checkPriceChanges(ctx, at.Add(40*time.Second))
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE event_type=$1`, priceNotificationEvent).Scan(&count))
	require.Equal(t, 2, count)
	first, err := restarted.claimJob(ctx)
	require.NoError(t, err)
	require.Equal(t, "price_notification", first.Kind)
	_, err = restarted.claimJob(ctx)
	require.Error(t, err, "concurrent proactive sends must be blocked")
	restarted.finish(first, "completed", "")
	_, err = restarted.claimJob(ctx)
	require.Error(t, err, "rate limit survives completion")
	_, err = db.Exec(`UPDATE qq_bot_jobs SET started_at=NOW()-INTERVAL '4 seconds' WHERE id=$1`, first.ID)
	require.NoError(t, err)
	second, err := restarted.claimJob(ctx)
	require.NoError(t, err)
	require.NotEqual(t, first.GroupID, second.GroupID)
}

func TestPriceNotificationsDeliveryAndRevocation(t *testing.T) {
	db := testDB(t)
	bot := savedBot(t, db)
	ctx := context.Background()
	c, err := bot.Config(ctx)
	require.NoError(t, err)
	c.PriceNotifications = PriceNotificationConfig{Enabled: true, Scheduled: true, Groups: []string{"group"}}
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	snapshot := priceTestSnapshot("USD 2")
	bot.SetPriceSnapshotProvider(priceSourceFunc(func(context.Context, time.Time) (PriceSnapshot, error) { return snapshot, nil }))
	at := time.Now()
	_, err = bot.checkPriceChanges(ctx, at)
	require.NoError(t, err)
	snapshot = priceTestSnapshot("USD 1.5")
	_, err = bot.checkPriceChanges(ctx, at.Add(16*time.Second))
	require.NoError(t, err)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = w.Write([]byte(`{"access_token":"test","expires_in":"7200"}`))
			return
		}
		requests++
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"code":40034105}`))
	}))
	defer server.Close()
	bot.qqURL = server.URL
	bot.tokenURL = server.URL + "/token"
	bot.runOne(ctx)
	var status, detail string
	require.NoError(t, db.QueryRow(`SELECT status,error FROM qq_bot_jobs WHERE kind='price_notification'`).Scan(&status, &detail))
	require.Equal(t, "delivery_failed", status)
	require.Contains(t, detail, "40034105")
	bot.runOne(ctx)
	require.Equal(t, 1, requests)
	snapshot = priceTestSnapshot("USD 1")
	_, err = bot.checkPriceChanges(ctx, at.Add(32*time.Second))
	require.NoError(t, err)
	c.PriceNotifications.Enabled = false
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	var pending int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE status='queued' AND kind='price_notification'`).Scan(&pending))
	require.Zero(t, pending)
	bot.runOne(ctx)
	require.Equal(t, 1, requests)
	c.PriceNotifications.Enabled = true
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	_, err = bot.checkPriceChanges(ctx, at.Add(48*time.Second))
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE status='queued' AND kind='price_notification'`).Scan(&pending))
	require.Zero(t, pending)
}

func TestPriceNotificationsScheduledOnlyAndSourceFailure(t *testing.T) {
	db := testDB(t)
	bot := savedBot(t, db)
	ctx := context.Background()
	c, err := bot.Config(ctx)
	require.NoError(t, err)
	c.PriceNotifications = PriceNotificationConfig{Enabled: true, Scheduled: true, Groups: []string{"group"}}
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	snapshot := priceTestSnapshot("USD 2")
	var sourceErr error
	bot.SetPriceSnapshotProvider(priceSourceFunc(func(context.Context, time.Time) (PriceSnapshot, error) { return snapshot, sourceErr }))
	at := time.Now()
	snapshot.NextCheck = at.Add(time.Second)
	due, err := bot.checkPriceChanges(ctx, at)
	require.NoError(t, err)
	require.Equal(t, snapshot.NextCheck, due)
	snapshot = priceTestSnapshot("USD 2")
	snapshot.States[0].Fields[1].Value = "0.5×"
	sourceErr = errors.New("database unavailable")
	_, err = bot.checkPriceChanges(ctx, at.Add(time.Second))
	require.Error(t, err)
	sourceErr = nil
	_, err = bot.checkPriceChanges(ctx, at.Add(time.Second))
	require.NoError(t, err)
	var payload string
	require.NoError(t, db.QueryRow(`SELECT payload::text FROM qq_bot_jobs WHERE kind='price_notification'`).Scan(&payload))
	require.Contains(t, payload, "定时切换")
	c.AllowedGroups = []string{"other-group"}
	c.PriceNotifications.Groups = []string{"other-group"}
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE status='cancelled'`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestPriceNotificationsEnableCapturesBaselineBeforeFirstScan(t *testing.T) {
	db := testDB(t)
	bot := savedBot(t, db)
	ctx := context.Background()
	snapshot := priceTestSnapshot("USD 2")
	bot.SetPriceSnapshotProvider(priceSourceFunc(func(context.Context, time.Time) (PriceSnapshot, error) { return snapshot, nil }))
	c, err := bot.Config(ctx)
	require.NoError(t, err)
	c.PriceNotifications = PriceNotificationConfig{Enabled: true, Scheduled: false, Groups: []string{"group", "other-group"}}
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	var baseline string
	require.NoError(t, db.QueryRow(`SELECT snapshot::text FROM qq_bot_price_snapshots WHERE app_id='app'`).Scan(&baseline))
	require.Contains(t, baseline, "USD 2")
	snapshot = priceTestSnapshot("USD 1.5")
	// Reordering the same recipient set must not reset the baseline or lose an edit.
	c.PriceNotifications.Groups = []string{"other-group", "group", "group"}
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	_, err = bot.checkPriceChanges(ctx, time.Now().Add(16*time.Second))
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE kind='price_notification'`).Scan(&count))
	require.Equal(t, 2, count)
}
