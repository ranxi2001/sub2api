package balanceforecast

import (
	"context"
	"fmt"
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

func TestForecastRecoveryGapMatchesBoundedSweepCycles(t *testing.T) {
	for _, tc := range []struct {
		users        int
		gapMinutes   int
		visitMinutes int
	}{
		{1999, 10, 5},
		{2000, 15, 10}, // An exact full sweep needs its own EOF/reset tick.
		{2001, 15, 10},
		{4000, 20, 15},
		{4001, 20, 15},
	} {
		t.Run(fmt.Sprint(tc.users), func(t *testing.T) {
			s, now := fixture(t)
			ctx := context.Background()
			migration, err := os.ReadFile("../../migrations/332_balance_forecast_delivery.sql")
			require.NoError(t, err)
			_, err = s.DB.ExecContext(ctx, string(migration))
			require.NoError(t, err)
			_, err = s.DB.ExecContext(ctx, `INSERT INTO users(id,balance,created_at) SELECT id,30,$2 FROM generate_series(2,$1::bigint) id`, tc.users, now.Add(-48*time.Hour))
			require.NoError(t, err)
			// None of these four rows belongs to the scanner's eligible page.
			_, err = s.DB.ExecContext(ctx, `INSERT INTO users(id,balance,created_at,status,deleted_at,balance_notify_enabled) VALUES
			 (100001,30,$1,'inactive',NULL,true),
			 (100002,30,$1,'active',$1,true),
			 (100003,30,$1,'active',NULL,false),
			 (100004,0,$1,'active',NULL,true)`, now)
			require.NoError(t, err)
			gap, err := s.RecoveryObservationGap(ctx, 2000, 5*time.Minute)
			require.NoError(t, err)
			require.Equal(t, time.Duration(tc.gapMinutes)*time.Minute, gap)
			_, err = s.RecoveryObservationGap(ctx, 0, 5*time.Minute)
			require.Error(t, err)

			c := Candidate{ID: 1, ObservationHours: 24, Hourly: 1, Hours: 1, Covered: true, Samples: 3, First: now.Add(-3 * time.Hour), Last: now.Add(-time.Hour)}
			id, err := s.ClaimWithRecoveryGap(ctx, c, now, 12, 1, gap)
			require.NoError(t, err)
			require.Positive(t, id)
			// A capped episode must recover with the normal scanner cadence,
			// including the >10-minute revisit interval on large installations.
			c.Hours = 15
			healthyStart := now.Add(2 * time.Hour)
			visit := time.Duration(tc.visitMinutes) * time.Minute
			for elapsed := time.Duration(0); elapsed <= time.Hour; elapsed += visit {
				_, err = s.ClaimWithRecoveryGap(ctx, c, healthyStart.Add(elapsed), 12, 1, gap)
				require.NoError(t, err)
			}
			var active bool
			require.NoError(t, s.DB.QueryRowContext(ctx, `SELECT active FROM balance_forecast_episodes WHERE user_id=1`).Scan(&active))
			require.False(t, active, "normal bounded rotation must close the recovered episode")
			c.Hours = 1
			id, err = s.ClaimWithRecoveryGap(ctx, c, healthyStart.Add(time.Hour+time.Minute), 12, 1, gap)
			require.NoError(t, err)
			require.Positive(t, id, "a later warning has a fresh allowance after recovery")

			// A real observation gap still resets recovery rather than treating
			// elapsed wall time without observations as continuous health.
			c.Hours = 15
			secondStart := healthyStart.Add(3 * time.Hour)
			_, err = s.ClaimWithRecoveryGap(ctx, c, secondStart, 12, 1, gap)
			require.NoError(t, err)
			resumed := secondStart.Add(gap + time.Minute)
			_, err = s.ClaimWithRecoveryGap(ctx, c, resumed, 12, 1, gap)
			require.NoError(t, err)
			var healthySince time.Time
			require.NoError(t, s.DB.QueryRowContext(ctx, `SELECT healthy_since,active FROM balance_forecast_episodes WHERE user_id=1`).Scan(&healthySince, &active))
			require.True(t, healthySince.Equal(resumed))
			require.True(t, active, "a disconnected observation must not close the episode")
		})
	}
}
