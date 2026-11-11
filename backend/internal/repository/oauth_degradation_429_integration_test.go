//go:build integration

package repository

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOAuthDegradation429PostgresWithoutProbeVerdicts(t *testing.T) {
	repo, c := degradationPolicyFixture(t)
	ctx := t.Context()
	off := false
	c.DegradationAction.ProbeEnabled = &off
	c.DegradationAction.ThresholdPercent = nil
	save := func() {
		raw, err := json.Marshal(c)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyOAuthAutoConfig, string(raw))
		require.NoError(t, err)
	}
	save()
	// Authentication failures can raise the error rate but cannot supply 429 evidence.
	for _, limited := range []int{0, 4} {
		a := degradationAccountFixture(t, 20, nil, nil, time.Minute)
		now := time.Now()
		for i := 0; i < 20; i++ {
			status := 401
			if i < limited {
				status = 429
			}
			upstreamstats.Default.Record(a.ID, status, now)
		}
		require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, now))
		got, err := repo.GetByID(ctx, a.ID)
		require.NoError(t, err)
		require.False(t, got.HasOAuthDegradationAction())
		require.Equal(t, 20, got.Concurrency)
	}

	for _, rounds := range [][]string{nil, {"unknown"}, {"correct"}} {
		a := degradationAccountFixture(t, 20, nil, rounds, time.Minute)
		now := time.Now()
		for range 19 {
			upstreamstats.Default.Record(a.ID, 429, now)
		}
		require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, now))
		got, err := repo.GetByID(ctx, a.ID)
		require.NoError(t, err)
		require.False(t, got.HasOAuthDegradationAction())
		upstreamstats.Default.Record(a.ID, 200, now)
		// The main switch still gates a default-on HTTP condition.
		c.DegradationAction.Enabled = false
		save()
		require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, now))
		got, err = repo.GetByID(ctx, a.ID)
		require.NoError(t, err)
		require.False(t, got.HasOAuthDegradationAction())
		c.DegradationAction.Enabled = true
		save()
		require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, now))
		got, err = repo.GetByID(ctx, a.ID)
		require.NoError(t, err)
		require.True(t, got.HasOAuthDegradationAction())
		require.Equal(t, 10, got.Concurrency)
		var state service.OAuthDegradationState
		raw, err := json.Marshal(got.Extra[service.OAuthDegradationActionExtraKey])
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &state))
		require.Equal(t, "upstream_429", state.TriggerReason)
		require.NotNil(t, state.UpstreamError)
		require.Equal(t, int64(19), state.UpstreamError.RateLimited)
		require.Equal(t, int64(20), state.UpstreamError.Total)
		require.Equal(t, 80.0, state.Upstream429ShareThresholdPercent)
		require.Zero(t, state.DegradationPercent)
		require.NoError(t, repo.EvaluateOAuthDegradation(ctx, a.ID, now.Add(2*time.Hour)))
		var n int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_auto_config_events WHERE account_id=$1 AND kind='degradation_applied'", a.ID).Scan(&n))
		require.Equal(t, 1, n, "action must remain one-time after evidence expires")
	}
}
