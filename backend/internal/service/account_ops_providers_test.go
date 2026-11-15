package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type opsProviderAccounts struct {
	accountOpsGroupAccounts
	providers   []MatureProvider
	providerErr error
	reads       int
}

func (r *opsProviderAccounts) ReadAccountOpsProviders(context.Context, []int64) ([]MatureProvider, error) {
	r.reads++
	return r.providers, r.providerErr
}
func TestAccountOpsProviderImportedKeysShareOneGroup(t *testing.T) {
	makeKey := func(id int64, provider float64, kind string) Account {
		return Account{ID: id, Type: kind, Extra: map[string]any{"provider_binding": map[string]any{"provider_id": provider, "provider_name": "Fixture provider"}}}
	}
	accounts := []Account{makeKey(1, 7, AccountTypeAPIKey), makeKey(2, 7, AccountTypeAPIKey), makeKey(3, 8, AccountTypeAPIKey), makeKey(4, 7, AccountTypeOAuth)}
	cfg := defaultAccountOpsConfig()
	require.True(t, reconcileAccountOpsProviderGroups(&cfg, accounts))
	require.Len(t, cfg.Groups, 2)
	require.Equal(t, []int64{1, 2}, cfg.Groups[0].AccountIDs)
	require.Equal(t, int64(7), cfg.Groups[0].ProviderID)
	require.Equal(t, []int64{3}, cfg.Groups[1].AccountIDs)
	require.False(t, reconcileAccountOpsProviderGroups(&cfg, accounts))
	cfg.Groups = append(cfg.Groups, AccountOpsGroup{ID: "manual-duplicate", ProviderID: 7})
	require.Error(t, validateAccountOpsGroups(cfg.Groups))
}
func TestAccountOpsProviderBalanceIsSharedAndDoesNotFallBackToKeyQuota(t *testing.T) {
	now := time.Now()
	fresh := now.Add(time.Hour)
	balance := 4.0
	makeKey := func(id int64) *Account {
		return &Account{ID: id, Name: "Fixture", Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Extra: map[string]any{UpstreamBillingProbeExtraKey: &UpstreamBillingProbeSnapshot{Balance: &UpstreamBalanceSnapshot{Status: "ok", ReceivedAt: &now, FreshUntil: &fresh, Data: map[string]any{"remaining": 999.0, "unit": "USD"}}}}}
	}
	accounts := &opsProviderAccounts{accountOpsGroupAccounts: accountOpsGroupAccounts{items: map[int64]*Account{1: makeKey(1), 2: makeKey(2)}}, providers: []MatureProvider{{ID: 7, Status: "connected", Metrics: ProviderMetrics{Balance: &balance, Currency: "USD"}, ObservedAt: &now, RefreshIntervalSeconds: 300}}}
	repo := &opsOnceRepoStub{}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, repo, nil)
	svc.SetNotificationDependencies(accounts, nil, false, "UTC")
	cfg := defaultAccountOpsConfig()
	cfg.Enabled = true
	cfg.Recipient = "one@example.test,two@example.test"
	cfg.Groups = []AccountOpsGroup{{ID: "manual-provider-7", ProviderID: 7, Name: "Shared", AccountIDs: []int64{1, 2}}}
	cfg.BalanceThresholds = []AccountOpsBalanceRule{{AccountID: 1, Enabled: true, Threshold: 50, Unit: "USD"}, {AccountID: 2, Enabled: true, Threshold: 50, Unit: "USD"}}
	svc.config.Store(cfg)
	svc.scanBalances(context.Background())
	require.Len(t, repo.events, 1)
	require.Equal(t, "active", repo.observations[0])
	require.Equal(t, 4.0, *repo.events[0].Details.Balance)
	require.Equal(t, 1, accounts.reads)
	svc.Observe(accounts.items[1], 402, nil, []byte(`{"error":{"code":"insufficient_balance"}}`))
	require.Empty(t, svc.queue, "member failures must not generate a duplicate wallet notification")
	for _, state := range []string{"stale", "failed", "missing", "storage_error"} {
		t.Run(state, func(t *testing.T) {
			saved := append([]MatureProvider(nil), accounts.providers...)
			defer func() { accounts.providers = saved; accounts.providerErr = nil }()
			switch state {
			case "stale":
				old := now.Add(-time.Hour)
				accounts.providers[0].ObservedAt = &old
			case "failed":
				accounts.providers[0].Status = "disconnected"
			case "missing":
				accounts.providers = nil
			case "storage_error":
				accounts.providerErr = errors.New("offline")
			}
			item := svc.opsBalanceAccount(context.Background(), accounts.items[1], cfg, now)
			require.Nil(t, item.Balance)
			require.NotEqual(t, "ok", item.BalanceStatus)
		})
	}
	old, _, _, _ := cfg.ThresholdGroupCriteria(cfg.Groups[0].ID, "balance_threshold")
	cfg.Groups[0].AccountIDs = append(cfg.Groups[0].AccountIDs, 3)
	cfg.BalanceThresholds = append(cfg.BalanceThresholds, AccountOpsBalanceRule{AccountID: 3, Enabled: true, Threshold: 50, Unit: "USD"})
	withExtraKey, _, _, _ := cfg.ThresholdGroupCriteria(cfg.Groups[0].ID, "balance_threshold")
	require.Equal(t, old, withExtraKey, "adding a Key must not reopen the same provider wallet episode")

	cfg.Groups[0].ProviderID = 8
	updated, _, _, _ := cfg.ThresholdGroupCriteria(cfg.Groups[0].ID, "balance_threshold")
	require.NotEqual(t, old, updated, "relinking must invalidate queued alerts and recoveries")
}
func TestAccountOpsProviderKeepsNativeCurrency(t *testing.T) {
	now := time.Now()
	amount := -2.0
	a := &Account{ID: 1, Type: AccountTypeAPIKey}
	repo := &opsProviderAccounts{accountOpsGroupAccounts: accountOpsGroupAccounts{items: map[int64]*Account{1: a}}, providers: []MatureProvider{{ID: 7, Status: "connected", ObservedAt: &now, Metrics: ProviderMetrics{Balance: &amount, Currency: "CNY"}}}}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(repo, nil, false, "UTC")
	cfg := defaultAccountOpsConfig()
	cfg.Groups = []AccountOpsGroup{{ID: "manual-provider-7", ProviderID: 7, AccountIDs: []int64{1}}}
	cfg.BalanceThresholds = []AccountOpsBalanceRule{{AccountID: 1, Enabled: true, Threshold: 50, Unit: "USD"}}
	item := svc.opsBalanceAccount(context.Background(), a, cfg, now)
	require.Equal(t, "CNY", item.Unit)
	require.Equal(t, -2.0, *item.Balance)
	event, state := svc.assessThreshold(context.Background(), a, "balance_threshold", cfg, now)
	require.Nil(t, event)
	require.Equal(t, "suppressed", state, "do not compare a CNY wallet with a USD threshold")
}

func TestAccountOpsProviderRecoveryRechecksCurrentWallet(t *testing.T) {
	now := time.Now()
	balance := 100.0
	a := &Account{ID: 1, Type: AccountTypeAPIKey}
	accounts := &opsProviderAccounts{accountOpsGroupAccounts: accountOpsGroupAccounts{items: map[int64]*Account{1: a}}, providers: []MatureProvider{{ID: 7, Status: "connected", ObservedAt: &now, Metrics: ProviderMetrics{Balance: &balance, Currency: "USD"}}}}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &opsOnceRepoStub{eligible: true}, nil)
	svc.SetNotificationDependencies(accounts, nil, false, "UTC")
	cfg := defaultAccountOpsConfig()
	cfg.Groups = []AccountOpsGroup{{ID: "manual-provider-7", ProviderID: 7, Name: "Shared", AccountIDs: []int64{1}}}
	cfg.BalanceThresholds = []AccountOpsBalanceRule{{AccountID: 1, Enabled: true, Threshold: 50, Unit: "USD"}}
	event := &AccountOpsEvent{AccountID: 1, GroupID: "manual-provider-7", Kind: "balance_threshold", Phase: "recovery"}
	state, err := svc.recheckThreshold(context.Background(), event, cfg)
	require.NoError(t, err)
	require.Equal(t, "active", state)
	accounts.providers = nil
	state, err = svc.recheckThreshold(context.Background(), event, cfg)
	require.NoError(t, err)
	require.Equal(t, "deferred", state, "removed provider must not announce recovery using an old snapshot")
}
