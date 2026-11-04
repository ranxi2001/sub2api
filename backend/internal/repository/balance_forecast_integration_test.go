//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/balanceforecast"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBalanceForecastRealBillingAndMigrations(t *testing.T) {
	repo, cmd, usage, _ := durableBillingFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := repo.db.ExecContext(ctx, `UPDATE balance_forecast_coverage SET started_at=$1`, now.Add(-48*time.Hour))
	require.NoError(t, err)
	_, err = repo.db.ExecContext(ctx, `UPDATE users SET created_at=$2,balance_notify_enabled=true WHERE id=$1`, cmd.UserID, now.Add(-48*time.Hour))
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		c, u := cmd, usage
		c.RequestID = fmt.Sprintf("forecast-real-%d", i)
		c.RequestFingerprint = ""
		u.RequestID = c.RequestID
		u.CreatedAt = now.Add(-time.Duration(i+1) * time.Hour)
		require.NoError(t, repo.StageUsageBilling(ctx, &c, &u))
		result, err := repo.ApplyStagedUsageBilling(ctx, service.UsageBillingEventKey{RequestID: c.RequestID, APIKeyID: c.APIKeyID})
		require.NoError(t, err)
		require.True(t, result.Result.Applied)
		require.NoError(t, repo.AcknowledgeUsageBillingCache(ctx, service.UsageBillingEventKey{RequestID: c.RequestID, APIKeyID: c.APIKeyID}))
		_, err = repo.db.ExecContext(ctx, `UPDATE usage_billing_events SET applied_at=$2 WHERE request_id=$1`, c.RequestID, u.CreatedAt)
		require.NoError(t, err)
	}
	store := &balanceforecast.Store{DB: repo.db}
	candidates, err := store.Candidates(ctx, now, 0, 100)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	c := candidates[0]
	require.True(t, c.Reliable())
	require.Equal(t, 3, c.Samples)
	require.Equal(t, 3.0, c.Spent)
	require.Equal(t, 97.0, c.Balance)
	require.Equal(t, 0.125, c.Hourly)
	claim, err := store.Claim(ctx, c, now, 1000, 3)
	require.NoError(t, err)
	require.Positive(t, claim)
	var balance float64
	require.NoError(t, repo.db.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, cmd.UserID).Scan(&balance))
	require.Equal(t, 97.0, balance, "forecast must never change money")
}
