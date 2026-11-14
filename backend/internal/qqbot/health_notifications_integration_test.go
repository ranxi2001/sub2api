//go:build integration

package qqbot

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type healthSourceFunc func(context.Context, time.Time) (HealthSnapshot, error)

func (f healthSourceFunc) HealthSnapshot(ctx context.Context, at time.Time) (HealthSnapshot, error) {
	return f(ctx, at)
}

func TestHealthNotificationsAtomicQueueRestartAndRecovery(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	bot := savedBot(t, db)
	c, err := bot.Config(ctx)
	require.NoError(t, err)
	c.HealthNotifications.Enabled = true
	c.HealthNotifications.FailureMinutes = 1
	c.HealthNotifications.RecoveryMinutes = 1
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	ttft := int64(1000)
	rate := .5
	source := healthSourceFunc(func(_ context.Context, at time.Time) (HealthSnapshot, error) {
		return HealthSnapshot{UpdatedAt: at, Complete: true, Groups: []HealthMetric{{Key: "1:openai", Name: "Example", Requests: 100, ErrorRate: rate, TTFTMs: &ttft}}}, nil
	})
	bot.SetHealthSnapshotProvider(source)
	at := time.Now()
	require.NoError(t, bot.checkHealthChanges(ctx, at))
	_, err = db.Exec(`ALTER TABLE qq_bot_jobs ADD CONSTRAINT reject_health CHECK(event_type<>'HEALTH_CHANGE_NOTIFICATION' OR group_id<>'other-group')`)
	require.NoError(t, err)
	require.Error(t, bot.checkHealthChanges(ctx, at.Add(time.Minute)))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE event_type=$1`, healthNotificationEvent).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`ALTER TABLE qq_bot_jobs DROP CONSTRAINT reject_health`)
	require.NoError(t, err)
	var wg sync.WaitGroup
	var failures atomic.Int32
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if bot.checkHealthChanges(ctx, at.Add(time.Minute)) != nil {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Zero(t, failures.Load())
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE event_type=$1`, healthNotificationEvent).Scan(&count))
	require.Equal(t, 2, count)
	restarted := New(db, testEncryptor{}, true, nil)
	restarted.SetHealthSnapshotProvider(source)
	require.NoError(t, restarted.checkHealthChanges(ctx, at.Add(2*time.Minute)))
	rate = 0
	require.NoError(t, restarted.checkHealthChanges(ctx, at.Add(3*time.Minute)))
	require.NoError(t, restarted.checkHealthChanges(ctx, at.Add(4*time.Minute)))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE event_type=$1`, healthNotificationEvent).Scan(&count))
	require.Equal(t, 4, count)
	j, err := restarted.claimJob(ctx)
	require.NoError(t, err)
	require.Equal(t, healthNotificationEvent, j.EventType)
	_, err = restarted.claimJob(ctx)
	require.Error(t, err, "proactive notifications share the serial rate limit")
	c.HealthNotifications.Enabled = false
	_, err = restarted.SaveConfig(ctx, c)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_jobs WHERE status='queued' AND event_type=$1`, healthNotificationEvent).Scan(&count))
	require.Zero(t, count)
	before := j.Status
	require.NotEmpty(t, before)
	restarted.deliverPriceNotification(ctx, c, j)
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM qq_bot_jobs WHERE id=$1`, j.ID).Scan(&status))
	require.Equal(t, "cancelled", status)
}
