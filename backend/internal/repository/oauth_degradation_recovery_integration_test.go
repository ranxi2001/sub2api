//go:build integration

package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestOAuthDegradationAdmissionDoesNotRestoreConcurrency(t *testing.T) {
	f := newAdmissionFixture(t)
	_, err := integrationDB.ExecContext(context.Background(), `UPDATE accounts SET extra=jsonb_set(extra,'{oauth_degradation_action}','{"models":["gpt-6-luna"],"concurrency_cap":1}') WHERE id=$1`, f.account.ID)
	require.NoError(t, err)
	for range 5 {
		require.Equal(t, "observed", f.round("passed"))
	}
	require.Equal(t, "admission_passed", f.round("passed"))
	var concurrency int
	require.NoError(t, integrationDB.QueryRowContext(t.Context(), "SELECT concurrency FROM accounts WHERE id=$1", f.account.ID).Scan(&concurrency))
	require.Equal(t, 1, concurrency, "admission must not restore degradation-limited concurrency")
}
func TestOAuthDegradationQualityRecoveryDoesNotRestoreConcurrency(t *testing.T) {
	now := time.Now().UTC()
	a := &service.Account{Type: service.AccountTypeOAuth, Concurrency: 20, Extra: map[string]any{}, Credentials: map[string]any{}}
	state := qualityState{}
	_, err := transitionQualityModels(a, &state, 7, []string{"probe-model"}, now.Add(time.Hour), "failed", true, now)
	require.NoError(t, err)
	require.Equal(t, 5, a.Concurrency)
	a.Extra["oauth_degradation_action"] = map[string]any{"models": []string{"gpt-6-luna"}, "concurrency_cap": 5}
	_, err = transitionQualityModels(a, &state, 7, []string{"probe-model"}, now.Add(time.Hour), "passed", true, now)
	require.NoError(t, err)
	require.Equal(t, 5, a.Concurrency, "quality recovery must not restore degradation-limited concurrency")
}
