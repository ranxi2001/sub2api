package service

import (
	"context"
	"sort"
	"testing"
	"time"
)

func TestAccountOpsGroupAssignmentDoesNotCreateAutomaticGroup(t *testing.T) {
	first := &Account{ID: 1, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"base_url": "https://Example.com/v1/"}}
	second := &Account{ID: 2, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"base_url": "https://example.com"}}

	a := accountOpsGroupAssignment(first, AccountOpsConfig{})
	b := accountOpsGroupAssignment(second, AccountOpsConfig{})

	if a.ID != "account:1" || b.ID != "account:2" || a.ID == b.ID {
		t.Fatalf("unassigned accounts must not share a dynamic group, got %q and %q", a.ID, b.ID)
	}
	if a.Site != "https://example.com" || a.Provider != "sub2api" || a.Mode != "account" {
		t.Fatalf("unexpected group metadata: %+v", a)
	}
}

func TestAccountOpsGroupAssignmentUsesNewAPIUserID(t *testing.T) {
	a := &Account{ID: 1, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"base_url": "https://api.example.com/v1"}, Extra: map[string]any{
		UpstreamBillingProviderExtraKey: "new_api",
		UpstreamBillingSiteExtraKey:     "https://api.example.com",
		UpstreamBillingUserIDExtraKey:   float64(42),
	}}
	b := &Account{ID: 2, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"base_url": "https://api.example.com"}, Extra: map[string]any{
		UpstreamBillingProviderExtraKey: "new_api",
		UpstreamBillingSiteExtraKey:     "https://api.example.com",
		UpstreamBillingUserIDExtraKey:   float64(43),
	}}

	first, second := accountOpsGroupAssignment(a, AccountOpsConfig{}), accountOpsGroupAssignment(b, AccountOpsConfig{})
	if first.ID == second.ID {
		t.Fatalf("different New API users must not share a group: %q", first.ID)
	}
	if first.Provider != "new_api" || first.Site != "https://api.example.com" {
		t.Fatalf("unexpected New API group metadata: %+v", first)
	}
}

type accountOpsGroupBindingReader struct {
	bindings map[int64]*NewAPIAccountBinding
}

func (r accountOpsGroupBindingReader) GetBinding(_ context.Context, id int64) (*NewAPIAccountBinding, error) {
	return r.bindings[id], nil
}

func TestAccountOpsGroupAssignmentReadsExistingNewAPIBinding(t *testing.T) {
	first := &Account{ID: 1, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.example.com/v1"}}
	second := &Account{ID: 2, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.example.com"}}
	reader := accountOpsGroupBindingReader{bindings: map[int64]*NewAPIAccountBinding{
		1: {Profile: NewAPISiteAuthorization{SiteURL: "https://api.example.com", UserID: 42}},
		2: {Profile: NewAPISiteAuthorization{SiteURL: "https://api.example.com", UserID: 42}},
	}}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
	svc.SetNewAPIGroupReader(reader)
	a := svc.legacyAccountOpsGroupAssignment(context.Background(), first, AccountOpsConfig{})
	b := svc.legacyAccountOpsGroupAssignment(context.Background(), second, AccountOpsConfig{})
	if a.Provider != "new_api" || a.ID == "" || a.ID != b.ID {
		t.Fatalf("existing New API binding should group by upstream user: %+v %+v", a, b)
	}
}

func TestAccountOpsGroupAssignmentManualGroupOverridesAutoGroup(t *testing.T) {
	a := &Account{ID: 7, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"base_url": "https://example.com"}}
	c := AccountOpsConfig{Groups: []AccountOpsGroup{{ID: "manual-team1234", Name: "团队余额", AccountIDs: []int64{7}}}}

	got := accountOpsGroupAssignment(a, c)
	if got.ID != "manual-team1234" || got.Name != "团队余额" || got.Mode != "manual" {
		t.Fatalf("manual group did not override auto assignment: %+v", got)
	}
}

type accountOpsGroupAccounts struct {
	AccountRepository
	items map[int64]*Account
}

func (r *accountOpsGroupAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	return r.items[id], nil
}
func (r *accountOpsGroupAccounts) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Account, error) {
	ids := make([]int64, 0, len(r.items))
	for id := range r.items {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	accounts := make([]Account, 0, len(ids))
	for _, id := range ids {
		accounts = append(accounts, *r.items[id])
	}
	return accounts, nil
}

func TestAccountOpsThresholdScanEmitsOneEventForAGroup(t *testing.T) {
	now := time.Now()
	fresh := now.Add(time.Hour)
	makeAccount := func(id int64, balance float64) *Account {
		return &Account{ID: id, Name: "key", Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"base_url": "https://example.com/v1"}, Extra: map[string]any{
			UpstreamBillingProbeExtraKey: &UpstreamBillingProbeSnapshot{Status: "ok", Balance: &UpstreamBalanceSnapshot{Status: "ok", ReceivedAt: &now, FreshUntil: &fresh, Data: map[string]any{"remaining": balance, "unit": "USD"}}},
		}}
	}
	accounts := &accountOpsGroupAccounts{items: map[int64]*Account{1: makeAccount(1, 4), 2: makeAccount(2, 2)}}
	repo := &opsOnceRepoStub{}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, repo, nil)
	svc.SetNotificationDependencies(accounts, nil, false, "UTC")
	cfg := defaultAccountOpsConfig()
	cfg.Groups = []AccountOpsGroup{{ID: "manual-example", Name: "example.com", Provider: "sub2api", Site: "https://example.com", AccountIDs: []int64{1, 2}}}
	cfg.Enabled = true
	cfg.BalanceThresholds = []AccountOpsBalanceRule{{AccountID: 1, Enabled: true, Threshold: 5, Unit: "USD"}, {AccountID: 2, Enabled: true, Threshold: 5, Unit: "USD"}}
	svc.config.Store(cfg)
	svc.scanBalances(context.Background())
	if len(repo.events) != 1 || repo.observations[0] != "active" {
		t.Fatalf("expected one active grouped event, got %d %v", len(repo.events), repo.observations)
	}
	if repo.events[0].GroupID == "" || repo.events[0].AccountID != 1 || repo.events[0].Details == nil || repo.events[0].Details.Balance == nil || *repo.events[0].Details.Balance != 4 {
		t.Fatalf("expected first grouped account snapshot, got %+v", repo.events[0])
	}
}

func TestAccountOpsSaveGroupsMaterializesMembership(t *testing.T) {
	settings := &accountOpsSettingsStub{}
	svc := NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
	cfg, err := svc.SaveGroups(context.Background(), []AccountOpsGroup{
		{ID: "auto:0123456789abcdef0123456789abcdef", Name: "站点余额", AccountIDs: []int64{1, 2}},
		{ID: "manual-team1234", Name: "团队余额", AccountIDs: []int64{3}},
	})
	if err != nil {
		t.Fatalf("save groups: %v", err)
	}
	if len(cfg.Groups) != 2 || len(cfg.Groups[0].AccountIDs) != 2 || len(cfg.Groups[1].AccountIDs) != 1 || cfg.Groups[0].ID == "auto:0123456789abcdef0123456789abcdef" {
		t.Fatalf("groups should use persisted membership: %+v", cfg.Groups)
	}
}

func TestAccountOpsEnsureGroupsAssignsNewSiteAccountAndCopiesRule(t *testing.T) {
	makeAccount := func(id int64) *Account {
		return &Account{ID: id, Name: "key", Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"base_url": "https://example.com/v1"}}
	}
	accounts := &accountOpsGroupAccounts{items: map[int64]*Account{1: makeAccount(1), 2: makeAccount(2)}}
	settings := &accountOpsSettingsStub{}
	svc := NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(accounts, nil, false, "UTC")
	threshold := 5.0
	cfg := defaultAccountOpsConfig()
	cfg.BalanceThresholds = []AccountOpsBalanceRule{{AccountID: 1, Enabled: true, Threshold: threshold, Unit: "USD"}}
	if err := svc.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatalf("save initial config: %v", err)
	}
	first, err := svc.ensureManualAccountOpsGroups(context.Background())
	if err != nil {
		t.Fatalf("bootstrap groups: %v", err)
	}
	if len(first.Groups) != 1 || len(first.Groups[0].AccountIDs) != 2 {
		t.Fatalf("unexpected bootstrap groups: %+v", first.Groups)
	}
	accounts.items[3] = makeAccount(3)
	second, err := svc.ensureManualAccountOpsGroups(context.Background())
	if err != nil {
		t.Fatalf("assign new account: %v", err)
	}
	if got := second.Groups[0].AccountIDs; len(got) != 3 || got[2] != 3 {
		t.Fatalf("new account should join first site group: %+v", second.Groups)
	}
	var copied *AccountOpsBalanceRule
	for i := range second.BalanceThresholds {
		if second.BalanceThresholds[i].AccountID == 3 {
			copied = &second.BalanceThresholds[i]
		}
	}
	if copied == nil || copied.Threshold != threshold || copied.Unit != "USD" {
		t.Fatalf("new account should inherit group rule: %+v", second.BalanceThresholds)
	}
}

func TestAccountOpsEnsureGroupsKeepsExplicitlyUngroupedAccountOut(t *testing.T) {
	makeAccount := func(id int64) *Account {
		return &Account{ID: id, Name: "key", Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"base_url": "https://example.com/v1"}}
	}
	accounts := &accountOpsGroupAccounts{items: map[int64]*Account{1: makeAccount(1), 2: makeAccount(2)}}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(accounts, nil, false, "UTC")
	initial := defaultAccountOpsConfig()
	initial.Groups = []AccountOpsGroup{{ID: "manual-example1", Name: "example.com", Provider: "sub2api", Site: "https://example.com", AccountIDs: []int64{1, 2}}}
	if err := svc.SaveConfig(context.Background(), initial); err != nil {
		t.Fatalf("save initial groups: %v", err)
	}
	if _, err := svc.ensureManualAccountOpsGroups(context.Background()); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	if _, err := svc.SaveGroups(context.Background(), []AccountOpsGroup{{ID: "manual-example1", Name: "example.com", Provider: "sub2api", Site: "https://example.com", AccountIDs: []int64{1}}}); err != nil {
		t.Fatalf("remove account from group: %v", err)
	}
	got, err := svc.ensureManualAccountOpsGroups(context.Background())
	if err != nil {
		t.Fatalf("reconcile after removal: %v", err)
	}
	if len(got.Groups) != 1 || len(got.Groups[0].AccountIDs) != 1 || got.Groups[0].AccountIDs[0] != 1 {
		t.Fatalf("explicitly ungrouped account was re-added: %+v", got.Groups)
	}
	if len(got.UngroupedAccountIDs) != 1 || got.UngroupedAccountIDs[0] != 2 {
		t.Fatalf("expected account 2 to remain explicitly ungrouped: %+v", got.UngroupedAccountIDs)
	}
}

func TestAccountOpsEnsureGroupsRepairsLegacyNewAPISingleAccountGroups(t *testing.T) {
	makeAccount := func(id int64, name string) *Account {
		return &Account{ID: id, Name: name, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{"base_url": "https://www.tken.shop/v1"}}
	}
	accounts := &accountOpsGroupAccounts{items: map[int64]*Account{1: makeAccount(1, "ChatGPT TKEN 0.08"), 2: makeAccount(2, "TKEN Plus 0.12")}}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(accounts, nil, false, "UTC")
	svc.SetNewAPIGroupReader(accountOpsGroupBindingReader{bindings: map[int64]*NewAPIAccountBinding{
		1: {Profile: NewAPISiteAuthorization{SiteURL: "https://www.tken.shop", UserID: 2783}},
		2: {Profile: NewAPISiteAuthorization{SiteURL: "https://www.tken.shop", UserID: 2783}},
	}})
	desired := svc.legacyAccountOpsGroupAssignment(context.Background(), accounts.items[1], AccountOpsConfig{})
	initial := defaultAccountOpsConfig()
	initial.Groups = []AccountOpsGroup{
		{ID: manualAccountOpsGroupID(desired.ID), Name: "https://www.tken.shop", Provider: "sub2api", Site: "https://www.tken.shop", AccountIDs: []int64{1, 2}},
		{ID: legacyManualAccountOpsAccountGroupID(1), Name: "ChatGPT TKEN 0.08", Provider: "new_api", Site: "https://www.tken.shop"},
		{ID: legacyManualAccountOpsAccountGroupID(2), Name: "TKEN Plus 0.12", Provider: "new_api", Site: "https://www.tken.shop"},
	}
	initial.GroupsInitialized = true
	if err := svc.SaveConfig(context.Background(), initial); err != nil {
		t.Fatalf("save initial config: %v", err)
	}

	got, err := svc.ensureManualAccountOpsGroups(context.Background())
	if err != nil {
		t.Fatalf("reconcile legacy groups: %v", err)
	}
	if len(got.Groups) != 1 || got.Groups[0].Provider != "new_api" || got.Groups[0].Site != "https://www.tken.shop" {
		t.Fatalf("expected one repaired New API group, got %+v", got.Groups)
	}
	if len(got.Groups[0].AccountIDs) != 2 || got.Groups[0].AccountIDs[0] != 1 || got.Groups[0].AccountIDs[1] != 2 {
		t.Fatalf("expected both accounts in the shared group, got %+v", got.Groups[0].AccountIDs)
	}
}
