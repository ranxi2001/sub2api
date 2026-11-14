package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAccountOpsFreshBalanceBoundary(t *testing.T) {
	now := time.Now()
	received := now.Add(-time.Minute)
	fresh := now.Add(time.Minute)
	a := &Account{ID: 1, Type: AccountTypeAPIKey, Extra: map[string]any{UpstreamBillingProbeExtraKey: &UpstreamBillingProbeSnapshot{Status: "ok", Balance: &UpstreamBalanceSnapshot{Status: "ok", ReceivedAt: &received, FreshUntil: &fresh, Data: map[string]any{"remaining": 0.0, "unit": "USD"}}}}}
	item := opsBalanceAccount(a, now)
	require.Equal(t, "ok", item.BalanceStatus)
	require.NotNil(t, item.Balance)
	require.Equal(t, 0.0, *item.Balance)
	snapshot, ok := a.Extra[UpstreamBillingProbeExtraKey].(*UpstreamBillingProbeSnapshot)
	require.True(t, ok)
	snapshot.Balance.Status = "failed"
	require.Nil(t, opsBalanceAccount(a, now).Balance)
	snapshot.Balance.Status = "ok"
	snapshot.Balance.FreshUntil = &received
	require.Equal(t, "stale", opsBalanceAccount(a, now).BalanceStatus)
	snapshot.Balance.FreshUntil = &fresh
	snapshot.Balance.Data["unlimited"] = true
	require.Nil(t, opsBalanceAccount(a, now).Balance)
	a.Type = AccountTypeOAuth
	require.Nil(t, opsBalanceAccount(a, now).Balance)
}
func TestAccountOpsOAuthPercentNeedsFreshRealWindow(t *testing.T) {
	now := time.Now()
	a := &Account{ID: 2, Type: AccountTypeOAuth, Platform: PlatformOpenAI, Extra: map[string]any{"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339), "codex_5h_used_percent": 80.0, "codex_5h_reset_at": now.Add(time.Hour).Format(time.RFC3339)}}
	windows := opsExtraUsageWindows(a, now)
	require.Equal(t, "ok", windows[0].Status)
	require.Equal(t, 80.0, *windows[0].UsedPercent)
	a.Extra["codex_usage_updated_at"] = now.Add(-16 * time.Minute).Format(time.RFC3339)
	windows = opsExtraUsageWindows(a, now)
	require.Equal(t, "stale", windows[0].Status)
	require.Nil(t, windows[0].UsedPercent)
	a.Extra["codex_usage_updated_at"] = now.Format(time.RFC3339)
	a.Extra["codex_5h_reset_at"] = now.Add(-time.Minute).Format(time.RFC3339)
	windows = opsExtraUsageWindows(a, now)
	require.Nil(t, windows[0].UsedPercent)
	delete(a.Extra, "codex_5h_used_percent")
	windows = opsExtraUsageWindows(a, now)
	require.Nil(t, windows[0].UsedPercent)
}

type opsAccountsStub struct {
	AccountRepository
	account *Account
	err     error
}

func (s *opsAccountsStub) GetByID(context.Context, int64) (*Account, error) { return s.account, s.err }
func (s *opsAccountsStub) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Account, error) {
	if s.account == nil {
		return []Account{}, s.err
	}
	return []Account{*s.account}, s.err
}
func TestAccountOpsDeliveryRechecksAmountUnitTypeIdentityAndRules(t *testing.T) {
	now := time.Now()
	received := now.Add(-time.Minute)
	fresh := now.Add(time.Hour)
	a := &Account{ID: 1, Name: "key", Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fake-key"}, Extra: map[string]any{UpstreamBillingProbeExtraKey: &UpstreamBillingProbeSnapshot{Status: "ok", Balance: &UpstreamBalanceSnapshot{Status: "ok", ReceivedAt: &received, FreshUntil: &fresh, Data: map[string]any{"remaining": 5.0, "unit": "USD"}}}}}
	snapshot, ok := a.Extra[UpstreamBillingProbeExtraKey].(*UpstreamBillingProbeSnapshot)
	require.True(t, ok)
	accounts := &opsAccountsStub{account: a}
	repo := &accountOpsRepoStub{}
	sender := &accountOpsSenderStub{}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, repo, sender)
	svc.SetNotificationDependencies(accounts, nil, false, "Asia/Shanghai")
	cfg := defaultAccountOpsConfig()
	cfg.Enabled = true
	cfg.Recipient = "ops@example.test"
	cfg.BalanceLow = false
	cfg.WeeklyQuota = false
	cfg.BalanceThresholds = []AccountOpsBalanceRule{{AccountID: 1, Enabled: true, Threshold: 5, Unit: "USD"}}
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	e, state := svc.assessThreshold(context.Background(), a, "balance_threshold", cfg, now)
	require.Equal(t, "active", state)
	svc.deliverNotification(context.Background(), e)
	require.Equal(t, 1, sender.calls)
	for _, change := range []func(){func() {
		snapshot.Balance.Data["remaining"] = 5.1
	}, func() {
		snapshot.Balance.Data["unit"] = "CNY"
	}, func() { a.Type = AccountTypeOAuth }, func() { a.Credentials["api_key"] = "rotated-fake-key" }, func() {
		cfg.BalanceThresholds[0].Enabled = false
		cfg.Enabled = false
		require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	}} {
		a.Type = AccountTypeAPIKey
		a.Credentials["api_key"] = "fake-key"
		b := snapshot.Balance
		b.Data["remaining"] = 5.0
		b.Data["unit"] = "USD"
		change()
		e.Deliveries = nil
		svc.deliverNotification(context.Background(), e)
		require.Equal(t, 1, sender.calls)
	}
}
func TestAccountOpsQuotaThresholdBoundaryAndPercentValidation(t *testing.T) {
	now := time.Now()
	a := &Account{ID: 2, Type: AccountTypeOAuth, Platform: PlatformOpenAI, Extra: map[string]any{"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339), "codex_5h_used_percent": 80.0, "codex_5h_reset_at": now.Add(time.Hour).Format(time.RFC3339), "codex_7d_used_percent": 90.0, "codex_7d_reset_at": now.Add(time.Hour).Format(time.RFC3339)}}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(&opsAccountsStub{account: a}, nil, false, "Asia/Shanghai")
	cfg := defaultAccountOpsConfig()
	cfg.Enabled = true
	cfg.Recipient = "ops@example.test"
	cfg.QuotaThresholds = []AccountOpsQuotaRule{{AccountID: 2, Enabled: true, ThresholdPercent: 90, Window: "any"}}
	e, state := svc.assessThreshold(context.Background(), a, "quota_threshold", cfg, now)
	require.Equal(t, "active", state)
	require.Equal(t, "7d", e.Details.Window)
	cfg.QuotaThresholds[0].Window = "5h"
	_, state = svc.assessThreshold(context.Background(), a, "quota_threshold", cfg, now)
	require.Equal(t, "resolved", state)
	cfg.QuotaThresholds[0].ThresholdPercent = 0
	require.Error(t, svc.SaveConfig(context.Background(), cfg))
	cfg.QuotaThresholds[0].ThresholdPercent = 101
	require.Error(t, svc.SaveConfig(context.Background(), cfg))
	cfg.QuotaThresholds[0].ThresholdPercent = 80
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	a.Type = AccountTypeAPIKey
	require.Error(t, svc.SaveConfig(context.Background(), cfg))
}

func TestAccountOpsQuotaRuleAcceptsActualModelWindow(t *testing.T) {
	cfg := defaultAccountOpsConfig()
	cfg.QuotaThresholds = []AccountOpsQuotaRule{{AccountID: 1, Enabled: true, ThresholdPercent: 80, Window: "gemini-2.5-pro"}}
	require.NoError(t, ValidateAccountOpsConfig(cfg))
}

func TestAccountOpsMalformedPassiveSourceIsUnknown(t *testing.T) {
	now := time.Now()
	reset := now.Add(time.Hour)
	w := opsWindow("5h", "5h", 1e308, 100, &now, &reset, now)
	require.Nil(t, w.UsedPercent)
	require.Equal(t, "unknown", w.Status)
	require.Nil(t, opsTime(1e308))
	require.Nil(t, opsTime(float64(253402300800)))
}

func TestAccountOpsStaleQueuedBalanceIsSuppressedNotRecovered(t *testing.T) {
	now := time.Now()
	received := now.Add(-time.Minute)
	fresh := now.Add(time.Hour)
	b := &UpstreamBalanceSnapshot{Status: "ok", ReceivedAt: &received, FreshUntil: &fresh, Data: map[string]any{"remaining": 1.0, "unit": "USD"}}
	a := &Account{ID: 1, Type: AccountTypeAPIKey, Extra: map[string]any{UpstreamBillingProbeExtraKey: &UpstreamBillingProbeSnapshot{Status: "ok", Balance: b}}}
	repo := &accountOpsRepoStub{}
	sender := &accountOpsSenderStub{}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, repo, sender)
	svc.SetNotificationDependencies(&opsAccountsStub{account: a}, nil, false, "Asia/Shanghai")
	cfg := defaultAccountOpsConfig()
	cfg.Enabled = true
	cfg.Recipient = "ops@example.test"
	cfg.BalanceThresholds = []AccountOpsBalanceRule{{AccountID: 1, Enabled: true, Threshold: 5, Unit: "USD"}}
	require.NoError(t, svc.SaveConfig(context.Background(), cfg))
	e, state := svc.assessThreshold(context.Background(), a, "balance_threshold", cfg, now)
	require.Equal(t, "active", state)
	b.FreshUntil = &received
	svc.deliverNotification(context.Background(), e)
	require.Equal(t, "suppressed", repo.state)
	require.Zero(t, sender.calls)
}

func TestAccountOpsAssessmentSeparatesRecoveryFromUnavailable(t *testing.T) {
	now := time.Now()
	received := now.Add(-time.Minute)
	fresh := now.Add(time.Hour)
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
	cfg := defaultAccountOpsConfig()
	cfg.BalanceThresholds = []AccountOpsBalanceRule{{AccountID: 1, Enabled: true, Threshold: 5, Unit: "USD"}}
	for _, tc := range []struct {
		name, state string
		change      func(*Account, *UpstreamBalanceSnapshot)
	}{
		{"known recovered", "resolved", func(a *Account, b *UpstreamBalanceSnapshot) { b.Data["remaining"] = 6.0 }},
		{"missing snapshot", "suppressed", func(a *Account, b *UpstreamBalanceSnapshot) { a.Extra = nil }},
		{"failed snapshot", "suppressed", func(a *Account, b *UpstreamBalanceSnapshot) { b.Status = "failed" }},
		{"missing amount", "suppressed", func(a *Account, b *UpstreamBalanceSnapshot) { delete(b.Data, "remaining") }},
		{"mismatched unit", "suppressed", func(a *Account, b *UpstreamBalanceSnapshot) { b.Data["unit"] = "CNY" }},
		{"changed account type", "suppressed", func(a *Account, b *UpstreamBalanceSnapshot) { a.Type = AccountTypeOAuth }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &UpstreamBalanceSnapshot{Status: "ok", ReceivedAt: &received, FreshUntil: &fresh, Data: map[string]any{"remaining": 1.0, "unit": "USD"}}
			a := &Account{ID: 1, Type: AccountTypeAPIKey, Extra: map[string]any{UpstreamBillingProbeExtraKey: &UpstreamBillingProbeSnapshot{Status: "ok", Balance: b}}}
			tc.change(a, b)
			_, state := svc.assessThreshold(context.Background(), a, "balance_threshold", cfg, now)
			require.Equal(t, tc.state, state)
		})
	}
}
func TestAccountOpsQuotaRecoveryNeedsKnownWindowOrAuthoritativeReset(t *testing.T) {
	now := time.Now()
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
	cfg := defaultAccountOpsConfig()
	cfg.QuotaThresholds = []AccountOpsQuotaRule{{AccountID: 2, Enabled: true, ThresholdPercent: 80, Window: "5h"}}
	for _, tc := range []struct {
		name, window, state string
		change              func(map[string]any)
	}{
		{"selected known low", "5h", "resolved", func(e map[string]any) {}},
		{"any missing other window", "any", "suppressed", func(e map[string]any) {}},
		{"any all known low", "any", "resolved", func(e map[string]any) {
			e["codex_7d_used_percent"] = 50.0
			e["codex_7d_reset_at"] = now.Add(time.Hour).Format(time.RFC3339)
		}},
		{"selected amount missing", "5h", "suppressed", func(e map[string]any) { delete(e, "codex_5h_used_percent") }},
		{"selected stale sample", "5h", "suppressed", func(e map[string]any) { e["codex_usage_updated_at"] = now.Add(-16 * time.Minute).Format(time.RFC3339) }},
		{"selected expired reset remains unknown", "5h", "suppressed", func(e map[string]any) {
			e["codex_5h_used_percent"] = 100.0
			e["codex_5h_reset_at"] = now.Add(-30 * time.Second).Format(time.RFC3339)
		}},
		{"old observation cannot prove reset", "5h", "suppressed", func(e map[string]any) {
			e["codex_usage_updated_at"] = now.Add(-16 * time.Minute).Format(time.RFC3339)
			e["codex_5h_reset_at"] = now.Add(-30 * time.Second).Format(time.RFC3339)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339), "codex_5h_used_percent": 50.0, "codex_5h_reset_at": now.Add(time.Hour).Format(time.RFC3339)}
			tc.change(extra)
			a := &Account{ID: 2, Type: AccountTypeOAuth, Platform: PlatformOpenAI, Extra: extra}
			cfg.QuotaThresholds[0].Window = tc.window
			_, state := svc.assessThreshold(context.Background(), a, "quota_threshold", cfg, now)
			require.Equal(t, tc.state, state)
		})
	}
}

func TestAccountOpsValidationErrorPreservesPersistedConfiguration(t *testing.T) {
	settings := &accountOpsSettingsStub{raw: `{"enabled":false,"cooldown_minutes":60}`}
	before := settings.raw
	accounts := &opsAccountsStub{account: &Account{ID: 1, Type: AccountTypeOAuth}}
	svc := NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(accounts, nil, false, "Asia/Shanghai")
	cfg := defaultAccountOpsConfig()
	cfg.BalanceThresholds = []AccountOpsBalanceRule{{AccountID: 1, Enabled: true, Threshold: 5, Unit: "USD"}}
	err := svc.SaveConfig(context.Background(), cfg)
	require.ErrorIs(t, err, ErrAccountOpsConfigValidation)
	require.Equal(t, before, settings.raw)
	accounts.err = errors.New("private database lookup detail")
	err = svc.SaveConfig(context.Background(), cfg)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAccountOpsConfigValidation)
	require.NotContains(t, err.Error(), "private database")
	require.Equal(t, before, settings.raw)
}

func TestAccountOpsBalanceCollectionStateAndHistoricalValue(t *testing.T) {
	now := time.Now()
	received, expired := now.Add(-2*time.Hour), now.Add(-time.Hour)
	a := &Account{ID: 1, Type: AccountTypeAPIKey, Extra: map[string]any{UpstreamBillingProbeExtraKey: &UpstreamBillingProbeSnapshot{Status: "ok", Balance: &UpstreamBalanceSnapshot{Status: "ok", ReceivedAt: &received, FreshUntil: &expired, Data: map[string]any{"remaining": 42.0, "unit": "USD"}}}}}
	item := opsBalanceAccount(a, now)
	require.False(t, item.ProbeEnabled)
	require.Equal(t, "stale", item.BalanceStatus)
	require.Nil(t, item.Balance, "historical values must not trigger alerts or recovery")
	require.NotNil(t, item.LastKnownBalance)
	require.Equal(t, 42.0, *item.LastKnownBalance)
	a.Extra[UpstreamBillingProbeEnabledExtraKey] = true
	require.True(t, opsBalanceAccount(a, now).ProbeEnabled)
	snapshot, ok := a.Extra[UpstreamBillingProbeExtraKey].(*UpstreamBillingProbeSnapshot)
	require.True(t, ok)
	snapshot.Balance.Status = "failed"
	require.Nil(t, opsBalanceAccount(a, now).LastKnownBalance)
	snapshot.Balance.Status = "ok"
	future := now.Add(time.Hour)
	snapshot.Balance.ReceivedAt = &future
	require.Nil(t, opsBalanceAccount(a, now).LastKnownBalance)
	a.Extra = nil
	item = opsBalanceAccount(a, now)
	require.False(t, item.ProbeEnabled)
	require.Equal(t, "unknown", item.BalanceStatus)
	require.Nil(t, item.Balance)
	require.Nil(t, item.LastKnownBalance)
}
