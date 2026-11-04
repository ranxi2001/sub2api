package balanceforecast

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestForecastClaimsSurviveConcurrencyRestartCapAndRecovery(t *testing.T) {
	s, now := fixture(t)
	migration, err := os.ReadFile("../../migrations/332_balance_forecast_delivery.sql")
	require.NoError(t, err)
	_, err = s.DB.Exec(string(migration))
	require.NoError(t, err)
	c := Candidate{ID: 1, Balance: 1, Spent: 24, ObservationHours: 24, Hourly: 1, Hours: 1, Covered: true, Samples: 3, First: now.Add(-3 * time.Hour), Last: now.Add(-time.Hour)}
	ctx := context.Background()
	var wg sync.WaitGroup
	ids := make(chan int64, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); id, err := s.Claim(ctx, c, now, 12, 3); ids <- id; errs <- err }()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	claims := 0
	for id := range ids {
		if id > 0 {
			claims++
		}
	}
	require.Equal(t, 1, claims, "concurrent processes reserve only one round per hour")
	// A process crash leaves 'sending'; restart must count it, not replay it.
	restarted := &Store{DB: s.DB}
	for i := 1; i <= 2; i++ {
		id, err := restarted.Claim(ctx, c, now.Add(time.Duration(i)*time.Hour), 12, 3)
		require.NoError(t, err)
		require.Positive(t, id)
		require.NoError(t, restarted.Finish(ctx, id, "sent", now))
	}
	id, err := restarted.Claim(ctx, c, now.Add(3*time.Hour), 12, 3)
	require.NoError(t, err)
	require.Zero(t, id)
	// Threshold jitter must not reset the episode or create fresh allowance.
	c.Hours = 15
	_, err = restarted.Claim(ctx, c, now.Add(4*time.Hour), 12, 3)
	require.NoError(t, err)
	c.Hours = 13
	_, err = restarted.Claim(ctx, c, now.Add(5*time.Hour), 12, 3)
	require.NoError(t, err)
	c.Hours = 15
	_, err = restarted.Claim(ctx, c, now.Add(6*time.Hour), 12, 3)
	require.NoError(t, err)
	c.Hours = 1
	id, err = restarted.Claim(ctx, c, now.Add(6*time.Hour+time.Minute), 12, 3)
	require.NoError(t, err)
	require.Zero(t, id)
	c.Hours = 15
	_, err = restarted.Claim(ctx, c, now.Add(7*time.Hour), 12, 3)
	require.NoError(t, err)
	for minute := 5; minute <= 60; minute += 5 {
		_, err = restarted.Claim(ctx, c, now.Add(7*time.Hour+time.Duration(minute)*time.Minute), 12, 3)
		require.NoError(t, err)
	}
	c.Hours = 1
	id, err = restarted.Claim(ctx, c, now.Add(9*time.Hour), 12, 3)
	require.NoError(t, err)
	require.Positive(t, id)
	var generation int
	require.NoError(t, s.DB.QueryRow(`SELECT generation FROM balance_forecast_episodes WHERE user_id=1`).Scan(&generation))
	require.Equal(t, 2, generation)
}

func TestForecastOnlyPreSMTPSuppressionReleasesAllowance(t *testing.T) {
	s, now := fixture(t)
	migration, err := os.ReadFile("../../migrations/332_balance_forecast_delivery.sql")
	require.NoError(t, err)
	_, err = s.DB.Exec(string(migration))
	require.NoError(t, err)
	c := Candidate{ID: 1, ObservationHours: 24, Hourly: 1, Hours: 1, Covered: true, Samples: 3, First: now.Add(-3 * time.Hour), Last: now.Add(-time.Hour)}
	ctx := context.Background()
	id, err := s.Claim(ctx, c, now, 12, 1)
	require.NoError(t, err)
	require.Positive(t, id)
	require.NoError(t, s.Finish(ctx, id, "skipped", now))
	next, err := s.Claim(ctx, c, now.Add(time.Hour), 12, 1)
	require.NoError(t, err)
	require.Greater(t, next, id)
	require.NoError(t, s.Finish(ctx, next, "uncertain", now))
	id, err = s.Claim(ctx, c, now.Add(2*time.Hour), 12, 1)
	require.NoError(t, err)
	require.Zero(t, id)
	require.Error(t, s.Finish(ctx, next, "pending", now))
}
