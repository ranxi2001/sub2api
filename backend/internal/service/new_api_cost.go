package service

import (
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/shopspring/decimal"
)

const NewAPIApplyUSDRateExtraKey = "new_api_apply_usd_rate"

func NewAPIApplyUSDRate(a *Account) bool {
	if a == nil {
		return true
	}
	enabled, ok := a.Extra[NewAPIApplyUSDRateExtraKey].(bool)
	return !ok || enabled
}

// The existing platform catalog decides the fixed conversion. This is not
// a live exchange rate and is never supplied by the caller.
func NewAPICostScale(a *Account) float64 { return newAPICostScaleFor(a, NewAPIApplyUSDRate(a)) }

func newAPICostScaleFor(a *Account, applyUSDRate bool) float64 {
	// A gateway can serve x1 and x7 requests concurrently. Neither probe preview
	// nor persistence may turn its raw group ratio into one account-wide cost.
	if a == nil || a.IsGateway() {
		return 0
	}
	return newAPICostScaleForPlatform(a.Platform, applyUSDRate)
}

// NewAPICostScaleForRequest uses the concrete target only for gateway accounts.
// Legacy single-platform accounts retain their existing account-based formula.
func NewAPICostScaleForRequest(a *Account, targetPlatform string) float64 {
	if !a.IsGateway() {
		return NewAPICostScale(a)
	}
	return newAPICostScaleForPlatform(targetPlatform, NewAPIApplyUSDRate(a))
}

func newAPICostScaleForPlatform(platform string, applyUSDRate bool) float64 {
	spec, ok := domain.LookupPlatform(platform)
	if !ok {
		return 0
	}
	if spec.CNProvider || !applyUSDRate {
		return 1
	}
	return 7
}

// NewAPIGatewayCostForRequest converts a successful fixed group ratio without
// mutating the shared account, raw ratio or wallet. No fixed ratio means no
// converted cost. Persistence in usage/profit snapshots is a separate step.
func NewAPIGatewayCostForRequest(a *Account, targetPlatform string) (float64, bool) {
	if !a.IsGateway() || !NewAPICostSyncEnabled(a) {
		return 0, false
	}
	return NewAPICostToSync(decodeUpstreamBillingProbeSnapshot(a.Extra), NewAPICostScaleForRequest(a, targetPlatform))
}

func NewAPICostSyncEnabled(a *Account) bool {
	if a == nil || a.Extra[UpstreamBillingProviderExtraKey] != "new_api" || !a.CostMultiplierAutoSyncEnabled() {
		return false
	}
	return true
}

func NewAPICostToSync(snap *UpstreamBillingProbeSnapshot, scale float64) (float64, bool) {
	if snap == nil || snap.Status != UpstreamBillingProbeStatusOK || snap.LastAttemptAt.IsZero() ||
		(scale != 1 && scale != 7) || snap.Data["provider"] != "new_api" ||
		snap.Data["object"] != "new_api.group_billing" || snap.Data["billing_scope"] != "group" {
		return 0, false
	}
	rate, ok := accountCostMultiplierNumber(snap.Data["effective_rate_multiplier"])
	if !ok {
		return 0, false
	}
	cost, _ := decimal.NewFromFloat(rate).Mul(decimal.NewFromFloat(scale)).Float64()
	if value, ok := accountCostMultiplierNumber(cost); ok {
		return value, true
	}
	return 0, false
}
