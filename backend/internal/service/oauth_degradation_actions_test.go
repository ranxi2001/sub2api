//go:build unit

package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These assertions compile against the production baseline; RED must be a
// missing business behavior, never an unavailable symbol or compiler failure.
func TestOAuthDegradationHoldSurvivesSuccessAndRevision(t *testing.T) {
	for _, revision := range []string{"original", "new-policy"} {
		t.Run(revision, func(t *testing.T) {
			var state AutoConfigConcurrencyState
			require.NoError(t, json.Unmarshal([]byte(`{"revision":"original","concurrency":10,"degradation_hold":true}`), &state))
			cfg := DefaultOAuthAutoConfig()
			cfg.Revision = revision
			cfg.SuccessesPerStep = 1
			next, concurrency := AdvanceConcurrency(state, 10, cfg, AccountConcurrencyResult{Success: true, StartedAt: time.Now()}, time.Now())
			require.Equal(t, 10, concurrency, "degradation hold must prevent successful requests from restoring concurrency")
			raw, err := json.Marshal(next)
			require.NoError(t, err)
			require.Contains(t, string(raw), `"degradation_hold":true`, "hold must survive policy revision and restart")
		})
	}
}
func TestOAuthDegradationRequiresExplicitThreshold(t *testing.T) {
	cfg := DefaultOAuthAutoConfig()
	require.NoError(t, json.Unmarshal([]byte(`{"degradation_action":{"enabled":true,"threshold_percent":null,"target_concurrency":10,"models":["gpt-6-luna"]}}`), &cfg))
	require.Error(t, ValidateOAuthAutoConfig(cfg), "enabling degradation action requires an explicit threshold")
}
