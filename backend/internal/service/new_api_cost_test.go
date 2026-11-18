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

func TestNewAPIConfigPreviewsAndSavesIndependentCostScales(t *testing.T) {
	s, _, h, accounts := newAPIConfigFixture()
	h.bodies["GET /api/pricing"] = `{"success":true,"group_ratio":{"foreign":0.1,"domestic":0.7}}`
	h.bodies["GET /api/token/?p=0&page_size=100"] = `{"success":true,"data":{"items":[{"id":3,"key":"abcd**********wxyz","group":"foreign"},{"id":4,"key":"nope**********xxxx","group":"domestic"}],"total":2,"page":1,"page_size":100}}`
	req := NewAPIConfigRequest{UserID: 7, AccessToken: "fixture-secret", AccountIDs: []int64{1, 2}, CostMultiplierScales: map[string]float64{"1": 7, "2": 1}}
	out, _, bindings, err := s.verifyNewAPIConfig(context.Background(), 1, req)
	require.NoError(t, err)
	require.Len(t, bindings, 2)
	require.Equal(t, 0.1, *out.Accounts[0].Rate)
	require.Equal(t, 0.7, *out.Accounts[0].CostMultiplier)
	require.Equal(t, 0.7, *out.Accounts[1].Rate)
	require.Equal(t, 0.7, *out.Accounts[1].CostMultiplier)
	require.Equal(t, 7.0, *bindings[0].CostMultiplierScale)
	require.Equal(t, 1.0, *bindings[1].CostMultiplierScale)
	require.Empty(t, accounts.accounts[1].Extra, "preview must not write costs")
	for _, bad := range []map[string]float64{{"1": 2}, {"99": 7}, {"01": 7}, {"1": math.NaN()}} {
		req.CostMultiplierScales = bad
		_, err = s.SaveNewAPIConfig(context.Background(), 1, req)
		require.ErrorIs(t, err, ErrNewAPIConfiguration)
	}
	req.CostMultiplierScales = nil
	out, _, _, err = s.verifyNewAPIConfig(context.Background(), 1, req)
	require.NoError(t, err)
	require.Nil(t, out.Accounts[0].CostMultiplier)
}

func TestNewAPICostSyncIgnoresDynamicGroupsAndUnboundSettings(t *testing.T) {
	a := &Account{Extra: map[string]any{NewAPICostScaleExtraKey: 7.0, UpstreamBillingProviderExtraKey: "new_api"}}
	require.Equal(t, 7.0, NewAPICostScale(a))
	for _, token := range []NewAPIToken{{Group: "auto"}, {Group: "vip", CrossGroupRetry: true}, {Group: "vip", RouteGroups: []string{"vip", "other"}}} {
		snap := newAPISnapshot(a, token, &newAPIData{wallet: 1, ratios: map[string]float64{"auto": 0.1, "vip": 0.1}}, 30, time.Now())
		_, ok := NewAPICostToSync(snap, 7)
		require.False(t, ok)
	}
	a.Extra[AccountCostAutoSyncExtraKey] = false
	require.Zero(t, NewAPICostScale(a))
	delete(a.Extra, AccountCostAutoSyncExtraKey)
	delete(a.Extra, UpstreamBillingProviderExtraKey)
	require.Zero(t, NewAPICostScale(a))
}
