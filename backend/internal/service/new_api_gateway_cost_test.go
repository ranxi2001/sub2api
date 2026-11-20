//go:build unit

package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestNewAPIGatewayCostsFollowRequestTargetWithoutSharedWrites(t *testing.T) {
	account := &Account{ID: 959, Type: AccountTypeGateway, Platform: PlatformOpenAI, Extra: map[string]any{
		UpstreamBillingProviderExtraKey: "new_api", AccountCostMultiplierExtraKey: 99.0,
	}}
	snapshot := newAPISnapshot(account, NewAPIToken{ID: 1, Group: "fixed"}, &newAPIData{wallet: 12, ratios: map[string]float64{"fixed": 0.1}}, 30, time.Now())
	account.Extra[UpstreamBillingProbeExtraKey] = snapshot
	before, err := json.Marshal(account)
	require.NoError(t, err)
	require.NotContains(t, snapshot.Data, "cost_multiplier")
	require.NotContains(t, snapshot.Data, "cost_multiplier_scale")
	require.Zero(t, NewAPICostScale(account), "probe/authorization must not save one platform's cost on the gateway")
	for _, spec := range domain.Platforms() {
		wantScale := 7.0
		if spec.CNProvider {
			wantScale = 1
		}
		// The same account serves both cost scales. Parallel reads are covered by
		// the remote -race command without writing per-platform account copies.
		t.Run(spec.ID, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, wantScale, NewAPICostScaleForRequest(account, spec.ID))
			cost, ok := NewAPIGatewayCostForRequest(account, spec.ID)
			require.True(t, ok)
			require.InDelta(t, 0.1*wantScale, cost, 1e-12)
			after, err := json.Marshal(account)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after), "wallet, raw ratio and shared cost settings must not change")
		})
	}
}

func TestNewAPIGatewayCostsRejectUnresolvedAndUnsupportedRates(t *testing.T) {
	account := &Account{Type: AccountTypeGateway, Extra: map[string]any{UpstreamBillingProviderExtraKey: "new_api"}}
	for _, token := range []NewAPIToken{{Group: "auto"}, {Group: "fixed", CrossGroupRetry: true}, {Group: "fixed", RouteGroups: []string{"fixed", "other"}}, {Group: "missing"}} {
		account.Extra[UpstreamBillingProbeExtraKey] = newAPISnapshot(account, token, &newAPIData{ratios: map[string]float64{"auto": 0.1, "fixed": 0.1}}, 30, time.Now())
		_, ok := NewAPIGatewayCostForRequest(account, PlatformKimi)
		require.False(t, ok, token.Group)
	}
	snapshot := newAPISnapshot(account, NewAPIToken{Group: "fixed"}, &newAPIData{ratios: map[string]float64{"fixed": 0.1}}, 30, time.Now())
	account.Extra[UpstreamBillingProbeExtraKey] = snapshot
	for _, invalid := range []string{"", PlatformComposite, "unknown"} {
		require.Zero(t, NewAPICostScaleForRequest(account, invalid))
		_, ok := NewAPIGatewayCostForRequest(account, invalid)
		require.False(t, ok)
	}
	account.Extra[NewAPIApplyUSDRateExtraKey] = false
	cost, ok := NewAPIGatewayCostForRequest(account, PlatformOpenAI)
	require.True(t, ok)
	require.Equal(t, 0.1, cost)
	require.Zero(t, NewAPICostScaleForRequest(account, PlatformComposite), "disabling x7 must not accept composite as a concrete target")
	account.Extra[AccountCostAutoSyncExtraKey] = false
	_, ok = NewAPIGatewayCostForRequest(account, PlatformOpenAI)
	require.False(t, ok)
	delete(account.Extra, AccountCostAutoSyncExtraKey)
	snapshot.Status = UpstreamBillingProbeStatusFailed
	_, ok = NewAPIGatewayCostForRequest(account, PlatformOpenAI)
	require.False(t, ok, "failed probe with retained data must not create converted cost")
	snapshot.Status = UpstreamBillingProbeStatusOK
	account.Extra[UpstreamBillingProviderExtraKey] = "sub2api"
	_, ok = NewAPIGatewayCostForRequest(account, PlatformOpenAI)
	require.False(t, ok, "Sub2API declaration costs keep their own rules")
}

func TestNewAPICostRequestTargetPreservesLegacyFormula(t *testing.T) {
	for _, kind := range []string{AccountTypeAPIKey, AccountTypeUpstream} {
		for _, spec := range domain.Platforms() {
			account := &Account{Platform: spec.ID, Type: kind}
			for _, target := range []string{PlatformOpenAI, PlatformKimi, PlatformComposite, ""} {
				require.Equal(t, NewAPICostScale(account), NewAPICostScaleForRequest(account, target), kind+"/"+spec.ID)
			}
			_, ok := NewAPIGatewayCostForRequest(account, PlatformKimi)
			require.False(t, ok)
		}
	}
	require.Zero(t, NewAPICostScaleForRequest(nil, PlatformOpenAI))
}
