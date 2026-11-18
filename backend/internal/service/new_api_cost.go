package service

import "github.com/shopspring/decimal"

const NewAPICostScaleExtraKey = "new_api_cost_multiplier_scale"

// Zero opts out; one keeps the upstream rate, seven converts the owner's
// foreign-model pricing convention. This is not a live currency exchange rate.
func ValidNewAPICostScale(scale float64) bool { return scale == 0 || scale == 1 || scale == 7 }

func NewAPICostScale(a *Account) float64 {
	if a != nil && a.CostMultiplierAutoSyncEnabled() && a.Extra[UpstreamBillingProviderExtraKey] == "new_api" {
		if scale, ok := accountCostMultiplierNumber(a.Extra[NewAPICostScaleExtraKey]); ok && ValidNewAPICostScale(scale) {
			return scale
		}
	}
	return 0
}

func NewAPICostToSync(snap *UpstreamBillingProbeSnapshot, scale float64) (float64, bool) {
	if snap == nil || snap.Status != UpstreamBillingProbeStatusOK || snap.LastAttemptAt.IsZero() ||
		!ValidNewAPICostScale(scale) || scale == 0 || snap.Data["provider"] != "new_api" ||
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
