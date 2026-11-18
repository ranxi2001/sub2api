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
	if a == nil {
		return 0
	}
	spec, ok := domain.LookupPlatform(a.Platform)
	if !ok {
		return 0
	}
	if spec.CNProvider || !applyUSDRate {
		return 1
	}
	return 7
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
