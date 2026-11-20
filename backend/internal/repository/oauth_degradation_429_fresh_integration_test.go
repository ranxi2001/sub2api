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

// The same oracle is run against the production baseline in the dedicated CI.
// Old wrong answers are deliberately present: they cannot authorize this action.
func TestOAuth429RequiresFreshIndependentTestsBeforeAction(t *testing.T) {
	repo, c := degradationPolicyFixture(t)
	off := false
	c.DegradationAction.Upstream429Enabled = nil
	c.DegradationAction.ProbeEnabled = &off
	c.DegradationAction.ThresholdPercent = nil
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(t.Context(), "UPDATE settings SET value=$2 WHERE key=$1", service.SettingKeyOAuthAutoConfig, string(raw))
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		rounds []string
	}{
		{name: "no_quality_plan"},
		{name: "historical_wrong_results", rounds: []string{"incorrect", "incorrect"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := degradationAccountFixture(t, 50, nil, tc.rounds, time.Minute)
			before, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			now := time.Now()
			// The history fixture already records pressure for incorrect rounds.
			if len(tc.rounds) == 0 {
				for range 20 {
					upstreamstats.Default.Record(a.ID, 429, now)
				}
			}
			evidence := upstreamstats.Default.Snapshot([]int64{a.ID}, now.Add(-time.Hour), now)[a.ID]
			require.EqualValues(t, 20, evidence.Total, "exercise the immediate minimum sample boundary")
			require.NoError(t, repo.EvaluateOAuthDegradation(t.Context(), a.ID, now))
			got, err := repo.GetByID(t.Context(), a.ID)
			require.NoError(t, err)
			require.False(t, got.HasOAuthDegradationAction(), "429 pressure and historical wrong answers must not authorize degradation before two fresh independent tests")
			require.Equal(t, 50, got.Concurrency, "HTTP pressure must not reduce account concurrency")
			require.Equal(t, before.Credentials, got.Credentials)
			require.Equal(t, before.Schedulable, got.Schedulable)
			var actions int
			require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT count(*) FROM account_auto_config_events WHERE account_id=$1 AND kind='degradation_applied'", a.ID).Scan(&actions))
			require.Zero(t, actions)
		})
	}
}
