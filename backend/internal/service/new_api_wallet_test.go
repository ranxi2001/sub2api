package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewAPIWalletDisplayCurrency(t *testing.T) {
	for _, tt := range []struct {
		name, settings, unit string
		amount, rate         float64
	}{
		{"legacy", `{}`, "USD", 12.34, 1},
		{"usd", `{"quota_display_type":"USD","usd_exchange_rate":7.3}`, "USD", 12.34, 1},
		{"cny", `{"quota_display_type":"CNY","usd_exchange_rate":7.3}`, "CNY", 90.082, 7.3},
		{"custom", `{"quota_display_type":"CUSTOM","custom_currency_symbol":"EUR","custom_currency_exchange_rate":0.9}`, "EUR", 11.106, .9},
		{"token mode", `{"quota_display_type":"TOKENS","usd_exchange_rate":7.3}`, "USD", 12.34, 1},
		{"unknown", `{"quota_display_type":"OTHER","usd_exchange_rate":7.3}`, "USD", 12.34, 1},
		{"missing rate", `{"quota_display_type":"CNY"}`, "USD", 12.34, 1},
		{"malformed rate", `{"quota_display_type":"CNY","usd_exchange_rate":"invalid"}`, "USD", 12.34, 1},
		{"malformed display", `{"quota_display_type":[],"usd_exchange_rate":7.3}`, "USD", 12.34, 1},
		{"zero rate", `{"quota_display_type":"CNY","usd_exchange_rate":0}`, "USD", 12.34, 1},
		{"negative rate", `{"quota_display_type":"CNY","usd_exchange_rate":-7.3}`, "USD", 12.34, 1},
		{"excessive rate", `{"quota_display_type":"CNY","usd_exchange_rate":1e7}`, "USD", 12.34, 1},
		{"missing symbol", `{"quota_display_type":"CUSTOM","custom_currency_exchange_rate":2}`, "USD", 12.34, 1},
		{"unsafe symbol", `{"quota_display_type":"CUSTOM","custom_currency_symbol":"C\nNY","custom_currency_exchange_rate":2}`, "USD", 12.34, 1},
		{"ambiguous usd", `{"quota_display_type":"CUSTOM","custom_currency_symbol":"USD","custom_currency_exchange_rate":2}`, "USD", 12.34, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var settings newAPIWalletSettings
			require.NoError(t, json.Unmarshal([]byte(tt.settings), &settings))
			amount, unit, rate := settings.displayWallet(12.34)
			require.Equal(t, tt.amount, amount)
			require.Equal(t, tt.unit, unit)
			require.Equal(t, tt.rate, rate)
		})
	}
	for _, invalid := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		settings := newAPIWalletSettings{QuotaDisplayType: "CNY", USDExchangeRate: &invalid}
		amount, unit, rate := settings.displayWallet(12.34)
		require.Equal(t, 12.34, amount)
		require.Equal(t, "USD", unit)
		require.Equal(t, 1.0, rate)
	}
	factor := 7.3
	settings := newAPIWalletSettings{QuotaDisplayType: "CNY", USDExchangeRate: &factor}
	for _, raw := range []float64{0, -12.34, 1e12} {
		amount, unit, _ := settings.displayWallet(raw)
		if raw == 1e12 {
			require.Equal(t, raw, amount)
			require.Equal(t, "USD", unit)
		} else {
			require.InDelta(t, raw*factor, amount, 1e-10)
			require.Equal(t, "CNY", unit)
		}
	}
}

func TestNewAPIWalletCurrencyPreviewAndPeriodicSnapshot(t *testing.T) {
	s, repo, h, accounts := newAPIConfigFixture()
	h.bodies["GET /api/status"] = `{"success":true,"data":{"quota_per_unit":500000,"quota_display_type":"CNY","usd_exchange_rate":7.3}}`
	h.bodies["GET /api/user/self"] = `{"success":true,"data":{"id":7,"quota":6170000}}`
	a := accounts.accounts[1]
	a.Extra = map[string]any{UpstreamBillingProviderExtraKey: "new_api"}
	req := NewAPIConfigRequest{UserID: 7, AccessToken: "fixture-secret", AccountIDs: []int64{1}}
	preview, err := s.PreviewNewAPIConfig(context.Background(), 1, req)
	require.NoError(t, err)
	require.Equal(t, 90.082, preview.Wallet.Amount)
	require.Equal(t, "CNY", preview.Wallet.Unit)
	saved, err := s.SaveNewAPIConfig(context.Background(), 1, req)
	require.NoError(t, err)
	require.Equal(t, preview.Wallet, saved.Wallet)

	// Procurement uses its fixed 7/1 factor, not the display rate of 7.3.
	for _, apply := range []bool{true, false} {
		a.Extra[NewAPIApplyUSDRateExtraKey] = apply
		snap, err := s.probeNewAPIAccount(context.Background(), a, repo.bindings[1], 30)
		require.NoError(t, err)
		require.Equal(t, "ok", snap.Balance.Status)
		require.Equal(t, 90.082, snap.Balance.Data["remaining"])
		require.Equal(t, 90.082, snap.Balance.Data["wallet_balance"])
		require.Equal(t, "CNY", snap.Balance.Data["unit"])
		require.Equal(t, 12.34, snap.Balance.Data["raw_wallet_balance"])
		require.Equal(t, "USD", snap.Balance.Data["raw_unit"])
		require.Equal(t, 7.3, snap.Balance.Data["display_exchange_rate"])
		require.Equal(t, .5, snap.Data["effective_rate_multiplier"])
		cost, ok := NewAPICostToSync(snap, newAPICostScaleFor(a, apply))
		require.True(t, ok)
		expected := .5
		if apply {
			expected = 3.5
		}
		require.Equal(t, expected, cost)
	}
	delete(h.bodies, "GET /api/pricing")
	snap, err := s.probeNewAPIAccount(context.Background(), a, repo.bindings[1], 30)
	require.NoError(t, err)
	require.Equal(t, "unsupported", snap.Status)
	require.Equal(t, "ok", snap.Balance.Status)
	require.Equal(t, 90.082, snap.Balance.Data["remaining"])
	h.bodies["GET /api/status"] = `{"success":true,"data":{"quota_per_unit":500000,"quota_display_type":"CNY","usd_exchange_rate":"invalid"}}`
	snap, err = s.probeNewAPIAccount(context.Background(), a, repo.bindings[1], 30)
	require.NoError(t, err)
	require.Equal(t, "ok", snap.Balance.Status)
	require.Equal(t, "USD", snap.Balance.Data["unit"])
	require.Equal(t, 12.34, snap.Balance.Data["remaining"])
}

func TestNewAPIWalletCurrencyPreservesThresholdUnits(t *testing.T) {
	now := time.Now()
	a := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	snap := newAPISnapshot(a, NewAPIToken{ID: 1, Group: "vip"}, &newAPIData{wallet: 90.082, walletUnit: "CNY", rawWallet: 12.34, walletRate: 7.3}, 30, now.Add(-time.Minute))
	a.Extra = map[string]any{UpstreamBillingProbeExtraKey: snap}
	item := opsBalanceAccount(a, now)
	require.Equal(t, "CNY", item.Unit)
	require.Equal(t, 90.082, *item.Balance)
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(opsBatchAccounts{accounts: map[int64]*Account{1: a}}, nil, false, "UTC")
	cfg := AccountOpsConfig{}
	threshold := 15.0
	require.NoError(t, svc.applyRule(context.Background(), &cfg, 1, AccountOpsRuleUpdate{Metric: "balance", Enabled: true, Threshold: &threshold, Unit: "USD"}, true))
	require.Equal(t, "USD", cfg.BalanceThresholds[0].Unit)
	require.Equal(t, threshold, cfg.BalanceThresholds[0].Threshold)
	require.Error(t, svc.applyRule(context.Background(), &cfg, 1, AccountOpsRuleUpdate{Metric: "balance", Enabled: true, Threshold: &threshold, Unit: "EUR"}, true))
	for _, tt := range []struct {
		unit      string
		threshold float64
		state     string
		amount    float64
	}{{"USD", 15, "active", 12.34}, {"USD", 10, "resolved", 12.34}, {"CNY", 100, "active", 90.082}, {"CNY", 80, "resolved", 90.082}, {"EUR", 100, "suppressed", 0}} {
		cfg := AccountOpsConfig{BalanceThresholds: []AccountOpsBalanceRule{{AccountID: 1, Enabled: true, Threshold: tt.threshold, Unit: tt.unit}}}
		event, state := svc.assessThreshold(context.Background(), a, "balance_threshold", cfg, now)
		require.Equal(t, tt.state, state)
		if event != nil {
			require.Equal(t, tt.unit, event.Details.Unit)
			require.Equal(t, tt.amount, *event.Details.Balance)
		}
	}
	snap.Balance.FreshUntil = probeTimePtr(now.Add(-time.Second))
	require.Nil(t, opsBalanceAccount(a, now).balanceInUnit("USD"))
	snap.Balance.FreshUntil = probeTimePtr(now.Add(time.Minute))
	snap.Balance.Status = "failed"
	require.Nil(t, opsBalanceAccount(a, now).balanceInUnit("USD"))
	snap.Balance.Status = "ok"
	delete(snap.Balance.Data, "source")
	require.Nil(t, opsBalanceAccount(a, now).balanceInUnit("USD"))
	item.ProviderID = 123
	require.Nil(t, item.balanceInUnit("USD"))
}
