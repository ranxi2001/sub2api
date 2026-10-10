package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

type opsBatchAccounts struct {
	AccountRepository
	accounts map[int64]*Account
	errID    int64
}

func (a opsBatchAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	if id == a.errID {
		return nil, errors.New("private-lookup-error")
	}
	account, ok := a.accounts[id]
	if !ok {
		return nil, ErrAccountNotFound
	}
	return account, nil
}

func opsBatchFixture(t *testing.T) (*AccountOpsService, *accountOpsSettingsStub) {
	t.Helper()
	settings := &accountOpsSettingsStub{raw: `{"enabled":true,"recipient":"ops@example.test","balance_low":true,"weekly_quota":true,"cooldown_minutes":90,"balance_thresholds":[{"account_id":41,"enabled":true,"threshold":5,"unit":"USD","notify_alert":false,"notify_recovery":true},{"account_id":99,"enabled":true,"threshold":3,"unit":"CNY"}],"quota_thresholds":[{"account_id":61,"enabled":false,"threshold_percent":70,"window":"7d"}],"encrypted_webhooks":[{"id":"old","provider":"dingtalk","enabled":true,"url_cipher":"old-url-cipher","secret_cipher":"old-secret-cipher","revision":"old-revision"}]}`}
	svc := NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(opsBatchAccounts{accounts: map[int64]*Account{
		41: {ID: 41, Type: AccountTypeAPIKey}, 99: {ID: 99, Type: AccountTypeAPIKey}, 2048: {ID: 2048, Type: AccountTypeAPIKey},
		61: {ID: 61, Type: AccountTypeOAuth}, 62: {ID: 62, Type: AccountTypeOAuth},
	}}, nil, false, "UTC")
	return svc, settings
}

func TestAccountOpsBatchBalancePreservesPrivateConfigAndOtherRules(t *testing.T) {
	svc, settings := opsBatchFixture(t)
	var before storedOpsConfig
	require.NoError(t, json.Unmarshal([]byte(settings.raw), &before))
	threshold := 0.0
	out, err := svc.SaveRulesBatch(context.Background(), []int64{41, 2048}, AccountOpsRuleUpdate{
		Metric: "balance", Enabled: true, Threshold: &threshold, Unit: "USD", NotifyAlert: opsBool(true), NotifyRecovery: opsBool(false),
	})
	require.NoError(t, err)
	require.Len(t, out.BalanceThresholds, 3)
	for _, index := range []int{0, 2} {
		rule := out.BalanceThresholds[index]
		require.Equal(t, 0.0, rule.Threshold)
		require.Equal(t, "USD", rule.Unit)
		require.True(t, *rule.NotifyAlert)
		require.False(t, *rule.NotifyRecovery)
	}
	require.Equal(t, "ops@example.test", out.Recipient)
	require.Equal(t, 90, out.CooldownMinutes)
	require.True(t, out.Enabled && out.BalanceLow && out.WeeklyQuota)
	var after storedOpsConfig
	require.NoError(t, json.Unmarshal([]byte(settings.raw), &after))
	require.Equal(t, before.StoredWebhooks, after.StoredWebhooks)
	require.Equal(t, before.BalanceThresholds[1].Threshold, after.BalanceThresholds[1].Threshold)
	require.Equal(t, before.QuotaThresholds[0].ThresholdPercent, after.QuotaThresholds[0].ThresholdPercent)
	public, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(public), "cipher")
	require.NotContains(t, string(public), "old-revision")
}

func TestAccountOpsBatchQuotaUsesCommonExplicitRule(t *testing.T) {
	svc, _ := opsBatchFixture(t)
	threshold := 85.0
	out, err := svc.SaveRulesBatch(context.Background(), []int64{61, 62}, AccountOpsRuleUpdate{
		Metric: "quota", Enabled: true, ThresholdPercent: &threshold, Window: "any", NotifyAlert: opsBool(false), NotifyRecovery: opsBool(true),
	})
	require.NoError(t, err)
	require.Len(t, out.QuotaThresholds, 2)
	for _, rule := range out.QuotaThresholds {
		require.Equal(t, 85.0, rule.ThresholdPercent)
		require.Equal(t, "any", rule.Window)
		require.True(t, rule.Enabled)
		require.False(t, *rule.NotifyAlert)
		require.True(t, *rule.NotifyRecovery)
	}
}

func TestAccountOpsBatchRejectsWholeUpdateOnInvalidInput(t *testing.T) {
	threshold := 9.0
	for _, tc := range []struct {
		name   string
		ids    []int64
		change func(*AccountOpsRuleUpdate)
	}{
		{name: "empty selection"},
		{name: "duplicate ID", ids: []int64{41, 41}},
		{name: "zero ID", ids: []int64{41, 0}},
		{name: "negative ID", ids: []int64{41, -2}},
		{name: "missing second account", ids: []int64{41, 42}},
		{name: "mixed account types", ids: []int64{41, 61}},
		{name: "invalid metric", ids: []int64{41}, change: func(v *AccountOpsRuleUpdate) { v.Metric = "bad" }},
		{name: "missing threshold even for existing rule", ids: []int64{41}, change: func(v *AccountOpsRuleUpdate) { v.Threshold = nil }},
		{name: "missing unit even for existing rule", ids: []int64{41}, change: func(v *AccountOpsRuleUpdate) { v.Unit = "" }},
		{name: "missing alert flag", ids: []int64{41}, change: func(v *AccountOpsRuleUpdate) { v.NotifyAlert = nil }},
		{name: "missing recovery flag", ids: []int64{41}, change: func(v *AccountOpsRuleUpdate) { v.NotifyRecovery = nil }},
		{name: "negative threshold", ids: []int64{41}, change: func(v *AccountOpsRuleUpdate) { n := -1.0; v.Threshold = &n }},
		{name: "nonfinite threshold", ids: []int64{41}, change: func(v *AccountOpsRuleUpdate) { n := math.Inf(1); v.Threshold = &n }},
		{name: "quota requires percentage", ids: []int64{61}, change: func(v *AccountOpsRuleUpdate) { v.Metric = "quota"; v.Window = "any" }},
		{name: "quota requires window", ids: []int64{61}, change: func(v *AccountOpsRuleUpdate) { v.Metric = "quota"; v.ThresholdPercent = &threshold }},
		{name: "quota rejects invalid percentage", ids: []int64{61}, change: func(v *AccountOpsRuleUpdate) {
			n := 101.0
			v.Metric = "quota"
			v.ThresholdPercent = &n
			v.Window = "any"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, settings := opsBatchFixture(t)
			before := settings.raw
			configBefore, err := svc.GetConfig(context.Background())
			require.NoError(t, err)
			v := AccountOpsRuleUpdate{Metric: "balance", Enabled: true, Threshold: &threshold, Unit: "USD", NotifyAlert: opsBool(true), NotifyRecovery: opsBool(false)}
			if tc.change != nil {
				tc.change(&v)
			}
			_, err = svc.SaveRulesBatch(context.Background(), tc.ids, v)
			require.ErrorIs(t, err, ErrAccountOpsConfigValidation)
			require.Equal(t, before, settings.raw)
			require.Equal(t, configBefore, svc.currentConfig().public())
		})
	}
}

func TestAccountOpsBatchSelectionLimitAndLookupFailure(t *testing.T) {
	svc, settings := opsBatchFixture(t)
	before := settings.raw
	ids := make([]int64, 1001)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	threshold := 4.0
	v := AccountOpsRuleUpdate{Metric: "balance", Enabled: true, Threshold: &threshold, Unit: "USD", NotifyAlert: opsBool(true), NotifyRecovery: opsBool(true)}
	_, err := svc.SaveRulesBatch(context.Background(), ids, v)
	require.ErrorIs(t, err, ErrAccountOpsConfigValidation)
	require.Equal(t, before, settings.raw)
	svc.accounts = opsBatchAccounts{accounts: map[int64]*Account{41: {ID: 41, Type: AccountTypeAPIKey}}, errID: 2048}
	_, err = svc.SaveRulesBatch(context.Background(), []int64{41, 2048}, v)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAccountOpsConfigValidation)
	require.NotContains(t, err.Error(), "private-lookup-error")
	require.Equal(t, before, settings.raw)
}

func TestAccountOpsBatchRejectsChangedAccountCurrency(t *testing.T) {
	svc, settings := opsBatchFixture(t)
	accounts, ok := svc.accounts.(opsBatchAccounts)
	require.True(t, ok)
	accounts.accounts[2048].Extra = map[string]any{UpstreamBillingProbeExtraKey: &UpstreamBillingProbeSnapshot{
		Status: "ok", Balance: &UpstreamBalanceSnapshot{Status: "failed", Data: map[string]any{"unit": "CNY"}},
	}}
	before := settings.raw
	threshold := 5.0
	_, err := svc.SaveRulesBatch(context.Background(), []int64{41, 2048}, AccountOpsRuleUpdate{
		Metric: "balance", Enabled: true, Threshold: &threshold, Unit: "USD", NotifyAlert: opsBool(true), NotifyRecovery: opsBool(true),
	})
	require.ErrorIs(t, err, ErrAccountOpsConfigValidation)
	require.Equal(t, before, settings.raw)
}

func TestAccountOpsGroupedBatchEnabledOnlyPreservesMixedUnits(t *testing.T) {
	svc, _ := opsBatchFixture(t)
	groups := []AccountOpsRuleGroup{
		{AccountIDs: []int64{41, 99}, Rule: AccountOpsRuleUpdate{Metric: "balance", Enabled: false, EnabledOnly: true}},
	}

	out, err := svc.SaveRuleGroupsBatch(context.Background(), groups)
	require.NoError(t, err)
	require.Len(t, out.BalanceThresholds, 2)
	byID := make(map[int64]AccountOpsBalanceRule, len(out.BalanceThresholds))
	for _, rule := range out.BalanceThresholds {
		byID[rule.AccountID] = rule
	}
	require.False(t, byID[41].Enabled)
	require.False(t, byID[99].Enabled)
	require.Equal(t, 5.0, byID[41].Threshold)
	require.Equal(t, "USD", byID[41].Unit)
	require.Equal(t, 3.0, byID[99].Threshold)
	require.Equal(t, "CNY", byID[99].Unit)
}

func opsMixedBatchGroups() []AccountOpsRuleGroup {
	amount, percent := 8.0, 90.0
	return []AccountOpsRuleGroup{
		{AccountIDs: []int64{41, 2048}, Rule: AccountOpsRuleUpdate{Metric: "balance", Enabled: true, Threshold: &amount, Unit: "USD", NotifyAlert: opsBool(true), NotifyRecovery: opsBool(false)}},
		{AccountIDs: []int64{61, 62}, Rule: AccountOpsRuleUpdate{Metric: "quota", Enabled: true, ThresholdPercent: &percent, Window: "any", NotifyAlert: opsBool(false), NotifyRecovery: opsBool(true)}},
	}
}

func TestAccountOpsGroupedBatchPreservesOtherSettingsAndAppliesIndependentFlags(t *testing.T) {
	svc, settings := opsBatchFixture(t)
	var before storedOpsConfig
	require.NoError(t, json.Unmarshal([]byte(settings.raw), &before))
	out, err := svc.SaveRuleGroupsBatch(context.Background(), opsMixedBatchGroups())
	require.NoError(t, err)
	require.Len(t, out.BalanceThresholds, 3)
	require.Len(t, out.QuotaThresholds, 2)
	for _, rule := range out.BalanceThresholds {
		if rule.AccountID == 99 {
			require.Equal(t, 3.0, rule.Threshold)
			require.Equal(t, "CNY", rule.Unit)
			continue
		}
		require.Equal(t, 8.0, rule.Threshold)
		require.True(t, *rule.NotifyAlert)
		require.False(t, *rule.NotifyRecovery)
	}
	for _, rule := range out.QuotaThresholds {
		require.Equal(t, 90.0, rule.ThresholdPercent)
		require.Equal(t, "any", rule.Window)
		require.False(t, *rule.NotifyAlert)
		require.True(t, *rule.NotifyRecovery)
	}
	var after storedOpsConfig
	require.NoError(t, json.Unmarshal([]byte(settings.raw), &after))
	require.Equal(t, before.StoredWebhooks, after.StoredWebhooks)
	require.Equal(t, before.Recipient, after.Recipient)
	require.Equal(t, before.CooldownMinutes, after.CooldownMinutes)
}

func TestAccountOpsGroupedBatchRejectsInvalidGroupWithoutPublishingEarlierChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*[]AccountOpsRuleGroup)
	}{
		{"no groups", func(g *[]AccountOpsRuleGroup) { *g = nil }},
		{"too many groups", func(g *[]AccountOpsRuleGroup) { *g = append(*g, (*g)[1]) }},
		{"empty subgroup", func(g *[]AccountOpsRuleGroup) { (*g)[1].AccountIDs = nil }},
		{"duplicate across groups", func(g *[]AccountOpsRuleGroup) { (*g)[1].AccountIDs = []int64{41} }},
		{"missing account in second group", func(g *[]AccountOpsRuleGroup) { (*g)[1].AccountIDs = []int64{61, 999} }},
		{"wrong type in second group", func(g *[]AccountOpsRuleGroup) { (*g)[0].AccountIDs = []int64{41}; (*g)[1].AccountIDs = []int64{2048} }},
		{"missing second-group flags", func(g *[]AccountOpsRuleGroup) { (*g)[1].Rule.NotifyRecovery = nil }},
		{"missing second-group percentage", func(g *[]AccountOpsRuleGroup) { (*g)[1].Rule.ThresholdPercent = nil }},
		{"missing second-group window", func(g *[]AccountOpsRuleGroup) { (*g)[1].Rule.Window = "" }},
		{"invalid second-group percentage", func(g *[]AccountOpsRuleGroup) { n := 101.0; (*g)[1].Rule.ThresholdPercent = &n }},
		{"invalid second-group metric", func(g *[]AccountOpsRuleGroup) { (*g)[1].Rule.Metric = "unknown" }},
		{"negative second-group ID", func(g *[]AccountOpsRuleGroup) { (*g)[1].AccountIDs = []int64{-1} }},
		{"balance group currency mismatch", func(g *[]AccountOpsRuleGroup) { (*g)[0], (*g)[1] = (*g)[1], (*g)[0]; (*g)[1].Rule.Unit = "CNY" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, settings := opsBatchFixture(t)
			before := settings.raw
			cached, err := svc.GetConfig(context.Background())
			require.NoError(t, err)
			groups := opsMixedBatchGroups()
			tc.change(&groups)
			_, err = svc.SaveRuleGroupsBatch(context.Background(), groups)
			require.ErrorIs(t, err, ErrAccountOpsConfigValidation)
			require.Equal(t, before, settings.raw)
			require.Equal(t, cached, svc.currentConfig().public())
		})
	}
}

func TestAccountOpsGroupedBatchTotalLimitAndLookupFailure(t *testing.T) {
	svc, settings := opsBatchFixture(t)
	before := settings.raw
	groups := opsMixedBatchGroups()
	groups[0].AccountIDs = make([]int64, 500)
	groups[1].AccountIDs = make([]int64, 501)
	accounts := make(map[int64]*Account, 1001)
	for i := 1; i <= 1001; i++ {
		kind := AccountTypeAPIKey
		if i <= 500 {
			groups[0].AccountIDs[i-1] = int64(i)
		} else {
			groups[1].AccountIDs[i-501] = int64(i)
			kind = AccountTypeOAuth
		}
		accounts[int64(i)] = &Account{ID: int64(i), Type: kind}
	}
	_, err := svc.SaveRuleGroupsBatch(context.Background(), groups)
	require.ErrorIs(t, err, ErrAccountOpsConfigValidation)
	require.Contains(t, err.Error(), "1000")
	require.Equal(t, before, settings.raw)
	svc.accounts = opsBatchAccounts{accounts: map[int64]*Account{41: {ID: 41, Type: AccountTypeAPIKey}, 2048: {ID: 2048, Type: AccountTypeAPIKey}, 61: {ID: 61, Type: AccountTypeOAuth}}, errID: 62}
	_, err = svc.SaveRuleGroupsBatch(context.Background(), opsMixedBatchGroups())
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAccountOpsConfigValidation)
	require.Equal(t, before, settings.raw)
	// The combined cap is inclusive: two valid groups totaling 1000 can save.
	settings = &accountOpsSettingsStub{}
	svc = NewAccountOpsService(settings, &accountOpsRepoStub{}, nil)
	svc.SetNotificationDependencies(opsBatchAccounts{accounts: accounts}, nil, false, "UTC")
	groups[1].AccountIDs = groups[1].AccountIDs[:500]
	out, err := svc.SaveRuleGroupsBatch(context.Background(), groups)
	require.NoError(t, err)
	require.Len(t, out.BalanceThresholds, 500)
	require.Len(t, out.QuotaThresholds, 500)
}
