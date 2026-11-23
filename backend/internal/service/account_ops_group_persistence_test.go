package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

type accountOpsGroupPersistenceSettings struct {
	accountOpsSettingsStub
	writes int
}

func (s *accountOpsGroupPersistenceSettings) Set(ctx context.Context, key, value string) error {
	s.writes++
	return s.accountOpsSettingsStub.Set(ctx, key, value)
}

type accountOpsGroupPersistenceAccounts struct {
	accountOpsGroupAccounts
	reads int
	lists int
}

func (r *accountOpsGroupPersistenceAccounts) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.reads++
	return r.accountOpsGroupAccounts.GetByID(ctx, id)
}

func (r *accountOpsGroupPersistenceAccounts) ListAllWithFilters(ctx context.Context, platform, accountType, status, search string, groupID int64, privacyMode string) ([]Account, error) {
	r.lists++
	return r.accountOpsGroupAccounts.ListAllWithFilters(ctx, platform, accountType, status, search, groupID, privacyMode)
}

func TestAccountOpsEnsureGroupsDoesNotRewritePersistedEmptyUngroupedAccounts(t *testing.T) {
	ctx := context.Background()
	accounts := &accountOpsGroupPersistenceAccounts{accountOpsGroupAccounts: accountOpsGroupAccounts{items: map[int64]*Account{
		1: {ID: 1, Name: "key", Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"base_url": "https://example.com/v1"}},
	}}}
	settings := &accountOpsGroupPersistenceSettings{}
	svc := NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(accounts, nil, false, "UTC")
	cfg := defaultAccountOpsConfig()
	cfg.BalanceThresholds = []AccountOpsBalanceRule{{AccountID: 1, Enabled: true, Threshold: 5, Unit: "USD"}}
	require.NoError(t, svc.SaveConfig(ctx, cfg))
	first, err := svc.ensureManualAccountOpsGroups(ctx)
	require.NoError(t, err)
	require.Len(t, first.Groups, 1)
	require.Equal(t, []int64{1}, first.Groups[0].AccountIDs)

	// Exercise the actual JSON persistence boundary: omitempty removes an empty
	// ungrouped list, so loading the saved config produces a nil slice again.
	var persisted map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(settings.raw), &persisted))
	require.NotContains(t, persisted, "ungrouped_account_ids")
	raw, writes, reads := settings.raw, settings.writes, accounts.reads
	svc = NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(accounts, nil, false, "UTC")
	for range 3 {
		got, err := svc.ensureManualAccountOpsGroups(ctx)
		require.NoError(t, err)
		require.Equal(t, first.Groups, got.Groups)
		require.Equal(t, first.BalanceThresholds, got.BalanceThresholds)
	}
	require.Equal(t, writes, settings.writes, "unchanged groups must not rewrite the persisted JSON")
	require.Equal(t, reads, accounts.reads, "unchanged groups must not revalidate every rule account")
	require.Equal(t, raw, settings.raw)
}

func TestAccountOpsThresholdAccountsReusesGroupAccountRead(t *testing.T) {
	accounts := &accountOpsGroupPersistenceAccounts{accountOpsGroupAccounts: accountOpsGroupAccounts{items: map[int64]*Account{
		1: {ID: 1, Name: "key", Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"base_url": "https://example.com/v1"}},
	}}}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(accounts, nil, false, "UTC")
	items, err := svc.ThresholdAccounts(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NotEmpty(t, items[0].GroupID)
	require.Len(t, svc.currentConfig().Groups, 1)
	require.Equal(t, 2, accounts.lists, "initial group persistence requires a fresh account snapshot")

	_, err = svc.ThresholdAccounts(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, accounts.lists, "unchanged groups reuse the account list loaded for the page")
}

type accountOpsCountingBindingReader struct {
	accountOpsGroupBindingReader
	reads int
}

func (r *accountOpsCountingBindingReader) GetBinding(ctx context.Context, id int64) (*NewAPIAccountBinding, error) {
	r.reads++
	return r.accountOpsGroupBindingReader.GetBinding(ctx, id)
}

func TestAccountOpsReconcileReadsBindingOnlyForLegacyGroups(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "already migrated", true: "legacy account group"}[legacy], func(t *testing.T) {
			reader := &accountOpsCountingBindingReader{accountOpsGroupBindingReader: accountOpsGroupBindingReader{bindings: map[int64]*NewAPIAccountBinding{
				1: {Profile: NewAPISiteAuthorization{SiteURL: "https://example.com", UserID: 42}},
			}}}
			svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
			svc.SetNewAPIGroupReader(reader)
			accounts := []Account{{ID: 1, Name: "key", Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"base_url": "https://example.com/v1"}}}
			cfg := defaultAccountOpsConfig()
			cfg.GroupsInitialized = true
			cfg.Groups = []AccountOpsGroup{{ID: "manual-example1", Name: "shared", Provider: "sub2api", Site: "https://example.com", AccountIDs: []int64{1}}}
			if legacy {
				cfg.Groups[0].ID = legacyManualAccountOpsAccountGroupID(1)
			}
			svc.reconcileAccountOpsGroups(context.Background(), &cfg, accounts)
			require.Len(t, cfg.Groups, 1)
			require.Equal(t, []int64{1}, cfg.Groups[0].AccountIDs)
			if legacy {
				require.Equal(t, 1, reader.reads)
				require.Equal(t, "new_api", cfg.Groups[0].Provider)
				require.NotEqual(t, legacyManualAccountOpsAccountGroupID(1), cfg.Groups[0].ID)
			} else {
				require.Zero(t, reader.reads, "already migrated groups do not need upstream binding discovery")
			}
		})
	}
}

func TestAccountOpsThresholdAccountsAtGroupCapacity(t *testing.T) {
	ctx := context.Background()
	settings := &accountOpsGroupPersistenceSettings{}
	svc := NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
	accounts := &accountOpsGroupPersistenceAccounts{accountOpsGroupAccounts: accountOpsGroupAccounts{items: map[int64]*Account{
		1: {ID: 1, Name: "New account", Type: AccountTypeAPIKey, Platform: PlatformOpenAI},
	}}}
	svc.SetNotificationDependencies(accounts, nil, false, "UTC")
	cfg := defaultAccountOpsConfig()
	cfg.GroupsInitialized = true
	for i := 0; i < maxAccountOpsGroups; i++ {
		cfg.Groups = append(cfg.Groups, AccountOpsGroup{ID: fmt.Sprintf("manual-example-%03d", i), Name: "Saved group"})
	}
	require.NoError(t, svc.SaveConfig(ctx, cfg))
	raw, writes := settings.raw, settings.writes
	items, err := svc.ThresholdAccounts(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "account:1", items[0].GroupID)
	groups, err := svc.ThresholdGroups(ctx)
	require.NoError(t, err)
	require.Len(t, groups, maxAccountOpsGroups)
	require.Equal(t, raw, settings.raw)
	require.Equal(t, writes, settings.writes)
	cfg.Groups = cfg.Groups[:len(cfg.Groups)-1]
	require.NoError(t, svc.SaveConfig(ctx, cfg))
	items, err = svc.ThresholdAccounts(ctx)
	require.NoError(t, err)
	require.Equal(t, legacyManualAccountOpsAccountGroupID(1), items[0].GroupID)
}
