//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPriorityAccountCostMultiplierRoundTrip(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	billing, group := 1.0, 5.0
	account := mustCreateAccount(t, tx.Client(), &service.Account{Name: "cost-roundtrip", Platform: service.PlatformOpenAI, RateMultiplier: &billing, GroupRateMultiplier: &group, Extra: map[string]any{"untouched": true}})
	require.NoError(t, repo.Update(ctx, account))
	require.Equal(t, 0.1, account.CostMultiplier())
	for _, cost := range []float64{0.25, 0} {
		require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{service.AccountCostMultiplierExtraKey: cost}))
		loaded, err := repo.GetByID(ctx, account.ID)
		require.NoError(t, err)
		require.Equal(t, cost, loaded.CostMultiplier())
		require.Equal(t, billing, loaded.BillingRateMultiplier())
		require.Equal(t, group, loaded.UserGroupRateMultiplier())
		require.Equal(t, true, loaded.Extra["untouched"])
	}
	require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{service.AccountCostMultiplierExtraKey: 0.1}), "cost edits invalidate cached scheduler accounts")
}

// Multi-model plans save the executed model on each result. Plan.ModelID is
// only the legacy fallback, and a malformed legacy round cannot poison usage.

func TestPrioritySchedulingFinanceScopeAndMissingPerformance(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	user := mustCreateUser(t, client, &service.User{Email: "priority-finance@test.invalid"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "synthetic-priority-finance", Name: "test"})
	account := mustCreateAccount(t, client, &service.Account{Name: "finance", Platform: service.PlatformOpenAI})
	now := time.Now()
	for _, model := range []string{"model-a", "model-b"} {
		_, err := repo.Create(ctx, &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, RequestID: model, Model: model, InputTokens: 10, ActualCost: 2, TotalCost: 4, CreatedAt: now})
		require.NoError(t, err)
	}
	query := service.PrioritySchedulingQuery{AccountIDs: []int64{account.ID}, Model: "model-a", UsageSince: now.Add(-time.Hour), UsageUntil: now.Add(time.Second)}
	got, err := repo.ReadPrioritySchedulingSignals(ctx, query)
	require.NoError(t, err)
	require.Equal(t, 1, got[account.ID].ProfitSamples)
	require.Equal(t, 2.0, got[account.ID].Revenue)
	require.NotNil(t, got[account.ID].Performance)
	require.Nil(t, got[account.ID].Performance.AvgTPS)
	require.Nil(t, got[account.ID].Performance.AvgTTFTMs)
	require.Nil(t, got[account.ID].Performance.DegradationPercent)
	query.Model = "missing-model"
	missing, err := repo.ReadPrioritySchedulingSignals(ctx, query)
	require.NoError(t, err)
	require.Zero(t, missing[account.ID].ProfitSamples)
	require.Equal(t, got[account.ID].Performance, missing[account.ID].Performance)
}
