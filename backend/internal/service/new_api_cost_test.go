package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"math"
	"testing"
	"time"
)

func TestNewAPICostConversionIsExplicitAndPreservesRawRatio(t *testing.T) {
	for _, tc := range []struct {
		name              string
		scale, rate, want float64
		ok                bool
	}{
		{"foreign", 7, 0.1, 0.7, true}, {"domestic", 1, 0.7, 0.7, true}, {"free", 7, 0, 0, true},
		{"disabled", 0, 0.1, 0, false}, {"arbitrary factor", 2, 0.1, 0, false}, {"overflow", 7, 1000000, 0, false},
		{"negative", 7, -0.1, 0, false}, {"not finite", 7, math.Inf(1), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := &UpstreamBillingProbeSnapshot{Status: "ok", LastAttemptAt: time.Now(), Data: map[string]any{"provider": "new_api", "object": "new_api.group_billing", "billing_scope": "group", "effective_rate_multiplier": tc.rate}}
			got, ok := NewAPICostToSync(snap, tc.scale)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.rate, snap.Data["effective_rate_multiplier"])
			snap.Status = "failed"
			_, ok = NewAPICostToSync(snap, tc.scale)
			require.False(t, ok)
		})
	}
}

func TestNewAPIConfigUsesPlatformCostsAndOptionalUSDConversion(t *testing.T) {
	s, _, h, accounts := newAPIConfigFixture()
	accounts.accounts[2].Platform = PlatformKimi
	h.bodies["GET /api/pricing"] = `{"success":true,"group_ratio":{"foreign":0.1,"domestic":0.7}}`
	h.bodies["GET /api/token/?p=0&page_size=100"] = `{"success":true,"data":{"items":[{"id":3,"key":"abcd**********wxyz","group":"foreign"},{"id":4,"key":"nope**********xxxx","group":"domestic"}],"total":2,"page":1,"page_size":100}}`
	req := NewAPIConfigRequest{UserID: 7, AccessToken: "fixture-secret", AccountIDs: []int64{1, 2}, ApplyUSDRate: map[string]bool{"1": true, "2": true}}
	out, _, bindings, err := s.verifyNewAPIConfig(context.Background(), 1, req)
	require.NoError(t, err)
	require.Len(t, bindings, 2)
	require.Equal(t, 0.1, *out.Accounts[0].Rate)
	require.Equal(t, 0.7, *out.Accounts[0].CostMultiplier)
	require.Equal(t, 0.7, *out.Accounts[1].Rate)
	require.Equal(t, 0.7, *out.Accounts[1].CostMultiplier)
	require.True(t, *bindings[0].ApplyUSDRate)
	require.Equal(t, 7.0, out.Accounts[0].CostMultiplierScale)
	require.True(t, *bindings[1].ApplyUSDRate)
	require.Equal(t, 1.0, out.Accounts[1].CostMultiplierScale)
	require.Empty(t, accounts.accounts[1].Extra, "preview must not write costs")
	for _, bad := range []map[string]bool{{"99": true}, {"01": true}} {
		req.ApplyUSDRate = bad
		_, err = s.SaveNewAPIConfig(context.Background(), 1, req)
		require.ErrorIs(t, err, ErrNewAPIConfiguration)
	}
	req.ApplyUSDRate = nil
	out, _, _, err = s.verifyNewAPIConfig(context.Background(), 1, req)
	require.NoError(t, err)
	require.Equal(t, 0.7, *out.Accounts[0].CostMultiplier, "USD conversion defaults on")
	req.ApplyUSDRate = map[string]bool{"1": false, "2": false}
	out, _, _, err = s.verifyNewAPIConfig(context.Background(), 1, req)
	require.NoError(t, err)
	require.Equal(t, 0.1, *out.Accounts[0].CostMultiplier)
	require.Equal(t, 0.7, *out.Accounts[1].CostMultiplier)
	require.Equal(t, 1.0, out.Accounts[0].CostMultiplierScale)
	accounts.accounts[1].Extra = map[string]any{AccountCostAutoSyncExtraKey: false}
	out, _, _, err = s.verifyNewAPIConfig(context.Background(), 1, req)
	require.NoError(t, err)
	require.Nil(t, out.Accounts[0].CostMultiplier)
}

func TestNewAPICostSyncIgnoresDynamicGroupsAndUnboundSettings(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI, Extra: map[string]any{NewAPIApplyUSDRateExtraKey: true, UpstreamBillingProviderExtraKey: "new_api"}}
	require.Equal(t, 7.0, NewAPICostScale(a))
	require.True(t, NewAPICostSyncEnabled(a))
	for _, token := range []NewAPIToken{{Group: "auto"}, {Group: "vip", CrossGroupRetry: true}, {Group: "vip", RouteGroups: []string{"vip", "other"}}} {
		snap := newAPISnapshot(a, token, &newAPIData{wallet: 1, ratios: map[string]float64{"auto": 0.1, "vip": 0.1}}, 30, time.Now())
		_, ok := NewAPICostToSync(snap, 7)
		require.False(t, ok)
	}
	a.Extra[AccountCostAutoSyncExtraKey] = false
	require.False(t, NewAPICostSyncEnabled(a))
	delete(a.Extra, AccountCostAutoSyncExtraKey)
	delete(a.Extra, UpstreamBillingProviderExtraKey)
	require.False(t, NewAPICostSyncEnabled(a))
}

func TestNewAPICostScaleUsesExistingPlatformCatalog(t *testing.T) {
	for _, platform := range []string{PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax} {
		require.Equal(t, 1.0, NewAPICostScale(&Account{Platform: platform}))
	}
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformGrok, PlatformMistral, PlatformQwen} {
		require.Equal(t, 7.0, NewAPICostScale(&Account{Platform: platform}))
	}
	require.Equal(t, 1.0, NewAPICostScale(&Account{Platform: PlatformOpenAI, Extra: map[string]any{NewAPIApplyUSDRateExtraKey: false}}))
	require.True(t, NewAPIApplyUSDRate(&Account{}))
	require.Zero(t, NewAPICostScale(nil))
	require.Zero(t, NewAPICostScale(&Account{Platform: "unregistered"}))
}
