package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountCostMultiplierDefaultsAndValidation(t *testing.T) {
	var missing *Account
	require.Equal(t, 0.1, missing.CostMultiplier())
	billing, group := 7.0, 3.0
	a := &Account{RateMultiplier: &billing, GroupRateMultiplier: &group}
	require.Equal(t, 0.1, a.CostMultiplier())
	for _, value := range []any{0.0, 0, 0.25, json.Number("0.25"), int64(2), float32(0.5)} {
		a.Extra = map[string]any{AccountCostMultiplierExtraKey: value}
		require.NoError(t, ValidateAccountCostMultiplierExtra(a.Extra))
		require.Equal(t, a.Extra[AccountCostMultiplierExtraKey], a.CostMultiplier())
		require.Equal(t, 7.0, a.BillingRateMultiplier())
		require.Equal(t, 3.0, a.UserGroupRateMultiplier())
	}
	for _, value := range []any{-0.1, math.NaN(), math.Inf(1), 1000001.0, "0.1", true, json.Number("invalid")} {
		a.Extra = map[string]any{AccountCostMultiplierExtraKey: value}
		require.Error(t, ValidateAccountCostMultiplierExtra(a.Extra))
		require.Equal(t, 0.1, a.CostMultiplier(), "malformed legacy metadata must not poison scores")
	}
	a.Extra = map[string]any{AccountCostMultiplierExtraKey: nil}
	require.NoError(t, ValidateAccountCostMultiplierExtra(a.Extra))
	require.Equal(t, 0.1, a.CostMultiplier())
}

func TestAccountCostMultiplierRejectsInvalidWritesBeforeRepositoryAccess(t *testing.T) {
	s := &adminServiceImpl{}
	ctx := context.Background()
	extra := map[string]any{AccountCostMultiplierExtraKey: -1.0}
	_, err := s.CreateAccount(ctx, &CreateAccountInput{Extra: extra})
	require.ErrorContains(t, err, "cost_multiplier")
	_, err = s.UpdateAccount(ctx, 1, &UpdateAccountInput{Extra: extra})
	require.ErrorContains(t, err, "cost_multiplier")
	_, err = s.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Extra: extra})
	require.ErrorContains(t, err, "cost_multiplier")
	require.ErrorContains(t, s.UpdateAccountExtra(ctx, 1, extra), "cost_multiplier")
}

func TestPriorityCostMultiplierReestimatesProfitWithoutChangingBilling(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	item := priorityCandidate(402, 1, 20)
	item.account.Type = AccountTypeOAuth
	item.account.Credentials = map[string]any{"plan_type": "self_serve_business_prolite"}
	item.account.Extra = map[string]any{"priority_teams_first_used_at": time.Now().Format(time.RFC3339)}
	signal := withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 93, P90TTFTMs: 1000, QualityPassed: 10, QualitySamples: 10, ProfitSamples: 93, Revenue: 2.8177, BaseCost: 10.8098})
	score := scorePriorityCandidate(c, item, signal, time.Now())
	require.Equal(t, 0.1, *score.Rate)
	require.Equal(t, "usage", score.EconomicsSource)
	require.InDelta(t, 1.08098, score.TheoreticalCost, 0.000001)
	require.InDelta(t, 1.73672, *score.Profit, 0.000001)
	require.NotContains(t, score.Reasons, "historical_loss")

	// Account billing can already be 0.1; procurement estimates must not apply it twice.
	otherBilling := 0.1
	item.account.RateMultiplier = &otherBilling
	again := scorePriorityCandidate(c, item, signal, time.Now())
	require.Equal(t, score.TheoreticalCost, again.TheoreticalCost)
	item.account.Extra[AccountCostMultiplierExtraKey] = 0.2
	again = scorePriorityCandidate(c, item, signal, time.Now())
	require.InDelta(t, 2.16196, again.TheoreticalCost, 0.000001, "cached base costs can be revalued immediately")
	require.Equal(t, 0.1, item.account.BillingRateMultiplier())
	item.account.Extra[AccountCostMultiplierExtraKey] = 0.0
	again = scorePriorityCandidate(c, item, signal, time.Now())
	require.Zero(t, again.TheoreticalCost)
	require.Equal(t, signal.Revenue, *again.Profit)
	require.Equal(t, 10.8098, signal.BaseCost, "cached source signals are immutable")
}

func TestPriorityConfigIgnoresRetiredPurchaseWindow(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"teams":{"enabled":true,"cost_cny":50,"window_hours":4,"window_source":"first_usage"}}`), &c))
	require.NoError(t, ValidatePrioritySchedulingConfig(c))
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "teams")
	item := priorityCandidate(1, 1, 0)
	item.account.Type = AccountTypeOAuth
	item.account.Credentials = map[string]any{"plan_type": "team"}
	item.account.Extra = nil
	score := scorePriorityCandidate(c, item, withAccountPerformanceFixture(PrioritySchedulingSignal{ProfitSamples: 5, Revenue: 10, BaseCost: 20}), time.Now())
	require.Equal(t, "usage", score.EconomicsSource)
	require.Equal(t, 8.0, *score.Profit)
	require.NotContains(t, score.Reasons, "teams_window_unavailable")
}

func TestUpstreamProbeCostMultiplierToSync(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name   string
		status string
		rate   any
		want   float64
		valid  bool
	}{
		{"success", UpstreamBillingProbeStatusOK, 0.14, 0.14, true},
		{"zero", UpstreamBillingProbeStatusOK, 0.0, 0, true},
		{"failed with cached data", UpstreamBillingProbeStatusFailed, 0.14, 0, false},
		{"unsupported with cached data", UpstreamBillingProbeStatusUnsupported, 0.14, 0, false},
		{"negative", UpstreamBillingProbeStatusOK, -1.0, 0, false},
		{"out of range", UpstreamBillingProbeStatusOK, 1000001.0, 1000001, false},
		{"missing", UpstreamBillingProbeStatusOK, nil, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := &UpstreamBillingProbeSnapshot{Status: tt.status, LastAttemptAt: now, Data: map[string]any{
				"billing_scope": "token", "resolved_rate_multiplier": tt.rate, "peak_rate_enabled": false,
			}}
			value, ok := snapshot.CostMultiplierToSync()
			require.Equal(t, tt.valid, ok)
			if ok {
				require.Equal(t, tt.want, value)
			}
		})
	}
	snapshot := &UpstreamBillingProbeSnapshot{Status: UpstreamBillingProbeStatusOK, LastAttemptAt: now, Data: map[string]any{
		"billing_scope": "token", "resolved_rate_multiplier": 0.14, "peak_rate_enabled": true,
		"peak_start": "09:00", "peak_end": "18:00", "peak_rate_multiplier": 2.0, "timezone": "UTC",
	}}
	value, ok := snapshot.CostMultiplierToSync()
	require.True(t, ok)
	require.InDelta(t, 0.28, value, 1e-9)
	// Profit reads only the saved cost; expiry must not restore the old default.
	item := priorityCandidate(1, 7, 20)
	item.account.Extra = map[string]any{AccountCostMultiplierExtraKey: value, UpstreamBillingProbeExtraKey: snapshot}
	score := scorePriorityCandidate(DefaultPrioritySchedulingConfig(), item, withAccountPerformanceFixture(PrioritySchedulingSignal{ProfitSamples: 20, Revenue: 50, BaseCost: 100}), now.Add(24*time.Hour))
	require.InDelta(t, 28, score.TheoreticalCost, 1e-9)
	require.Equal(t, 7.0, item.account.BillingRateMultiplier())
}

func TestAccountCostAutoSyncSetting(t *testing.T) {
	for _, value := range []any{nil, true, false} {
		extra := map[string]any{AccountCostAutoSyncExtraKey: value}
		require.NoError(t, ValidateAccountCostMultiplierExtra(extra))
		account := &Account{Extra: extra}
		require.Equal(t, value != false, account.CostMultiplierAutoSyncEnabled())
	}
	for _, value := range []any{"false", 0, 1.0, map[string]any{}} {
		extra := map[string]any{AccountCostAutoSyncExtraKey: value}
		require.ErrorContains(t, ValidateAccountCostMultiplierExtra(extra), "cost_multiplier_auto_sync")
		s := &adminServiceImpl{}
		_, err := s.UpdateAccount(context.Background(), 1, &UpdateAccountInput{Extra: extra})
		require.ErrorContains(t, err, "cost_multiplier_auto_sync")
	}
	require.True(t, (&Account{}).CostMultiplierAutoSyncEnabled())
}
