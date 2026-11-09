//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOAuthDegradationStaleEditPreservesAction(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	a := mustCreateAccount(t, tx.Client(), &service.Account{Name: "degradation-edit-fixture", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Concurrency: 20, Credentials: map[string]any{}})
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	stale, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	// The editor loaded before another worker committed the degradation action.
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET concurrency=10,extra=jsonb_set(COALESCE(extra,'{}'::jsonb),'{oauth_degradation_action}','{"concurrency_cap":10,"models":["gpt-6-luna"]}') WHERE id=$1`, a.ID)
	require.NoError(t, err)
	stale.Name = "changed by administrator"
	stale.Extra = map[string]any{"manual": "keep"}
	require.NoError(t, repo.Update(ctx, stale))
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Contains(t, got.Extra, "oauth_degradation_action", "stale editor must not erase a committed degradation action")
	require.Equal(t, 10, got.Concurrency, "stale account snapshot must not restore concurrency")
	require.Equal(t, "keep", got.Extra["manual"])
	// Incoming marker injection cannot replace server-owned scope.
	require.NoError(t, repo.UpdateExtra(ctx, a.ID, map[string]any{"oauth_degradation_action": map[string]any{"models": []string{"gpt-6.1-sol"}, "concurrency_cap": 100}}))
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	data, err := json.Marshal(got.Extra["oauth_degradation_action"])
	require.NoError(t, err)
	require.Contains(t, string(data), "gpt-6-luna")
	require.NotContains(t, string(data), "gpt-6.1-sol")
}

func TestOAuthDegradationBulkAndRepeatedStaleEditRetainLowerManualLimit(t *testing.T) {
	ctx := t.Context()
	tx := testEntTx(t)
	a := mustCreateAccount(t, tx.Client(), &service.Account{Name: "degradation-manual-fixture", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Concurrency: 20})
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	stale, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET concurrency=3,extra=jsonb_set(COALESCE(extra,'{}'::jsonb),'{oauth_degradation_action}','{"concurrency_cap":10,"models":["gpt-6-luna"]}') WHERE id=$1`, a.ID)
	require.NoError(t, err)
	require.NoError(t, repo.Update(ctx, stale))
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, 3, got.Concurrency, "stale save retains later manual reduction")
	for _, proposed := range []int{0, -1, 20} {
		_, err = repo.BulkUpdate(ctx, []int64{a.ID}, service.AccountBulkUpdate{Concurrency: &proposed, Extra: map[string]any{"oauth_degradation_action": nil, "manual": "bulk"}})
		require.NoError(t, err)
		got, err = repo.GetByID(ctx, a.ID)
		require.NoError(t, err)
		require.Equal(t, 3, got.Concurrency)
		require.Contains(t, got.Extra, "oauth_degradation_action")
		require.Equal(t, "bulk", got.Extra["manual"])
	}
}
