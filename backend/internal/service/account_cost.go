package service

import (
	"encoding/json"
	"math"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const AccountCostMultiplierExtraKey = "cost_multiplier"
const AccountCostAutoSyncExtraKey = "cost_multiplier_auto_sync"

// TokenProcurementBasisKey selects the verified price book for token usage.
const TokenProcurementBasisKey = "token_procurement_basis"
const TokenProcurementStandard = "standard_charge"
const TokenProcurementAccount = "account_stats"
const ImagePurchasePricesKey = "image_purchase_prices"
const DefaultAccountCostMultiplier = 0.1

// CostMultiplier is the saved estimate used for profitability.
// Successful upstream rate probes update this value without changing billing.
// Keeping it in extra preserves existing account import/export and caches.
func (a *Account) CostMultiplier() float64 {
	if a != nil {
		if value, ok := accountCostMultiplierNumber(a.Extra[AccountCostMultiplierExtraKey]); ok {
			return value
		}
	}
	return DefaultAccountCostMultiplier
}

// CostMultiplierAutoSyncEnabled defaults to true for existing accounts.
func (a *Account) CostMultiplierAutoSyncEnabled() bool {
	if a == nil {
		return true
	}
	enabled, present := a.Extra[AccountCostAutoSyncExtraKey].(bool)
	return !present || enabled
}

func accountCostMultiplierNumber(raw any) (float64, bool) {
	var value float64
	switch v := raw.(type) {
	case float64:
		value = v
	case float32:
		value = float64(v)
	case int:
		value = float64(v)
	case int64:
		value = float64(v)
	case json.Number:
		var err error
		value, err = v.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return value, !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1000000
}

// Null removes an override and restores the default; zero is an explicit cost.
func ValidateAccountCostMultiplierExtra(extra map[string]any) error {
	if raw := extra[NewAPIApplyUSDRateExtraKey]; raw != nil {
		if _, ok := raw.(bool); !ok {
			return ErrNewAPIConfiguration
		}
	}

	if raw := extra[AccountCostAutoSyncExtraKey]; raw != nil {
		if _, ok := raw.(bool); !ok {
			return infraerrors.BadRequest("INVALID_COST_MULTIPLIER_AUTO_SYNC", "cost_multiplier_auto_sync must be a boolean")
		}
	}
	if raw := extra[TokenProcurementBasisKey]; raw != nil {
		basis, ok := raw.(string)
		if !ok || (basis != TokenProcurementAccount && basis != TokenProcurementStandard) {
			return infraerrors.BadRequest("INVALID_TOKEN_PROCUREMENT_BASIS", "token_procurement_basis must be account_stats or standard_charge")
		}
	}
	if raw := extra[ImagePurchasePricesKey]; raw != nil {
		prices, ok := raw.(map[string]any)
		if !ok {
			return infraerrors.BadRequest("INVALID_IMAGE_PURCHASE_PRICES", "image purchase prices must be a tier-to-price object")
		}
		for tier, price := range prices {
			_, valid := accountCostMultiplierNumber(price)
			if !valid || (tier != "1K" && tier != "2K" && tier != "4K") {
				return infraerrors.BadRequest("INVALID_IMAGE_PURCHASE_PRICES", "expected a finite nonnegative price for 1K, 2K or 4K")
			}
		}
	}
	raw, exists := extra[AccountCostMultiplierExtraKey]
	if !exists || raw == nil {
		return nil
	}
	value, ok := accountCostMultiplierNumber(raw)
	if !ok {
		return infraerrors.BadRequest("INVALID_COST_MULTIPLIER", "cost_multiplier must be a finite number between 0 and 1000000")
	}
	extra[AccountCostMultiplierExtraKey] = value
	return nil
}

// CostMultiplierToSync returns the successful probe's effective token cost.
// A failed probe may carry old data; it must never rewrite the saved cost.
func (s *UpstreamBillingProbeSnapshot) CostMultiplierToSync() (float64, bool) {
	if s == nil || s.Status != UpstreamBillingProbeStatusOK || s.LastAttemptAt.IsZero() || s.Data["provider"] == "new_api" || s.Data["billing_mode"] == "multi_group" {
		return 0, false
	}
	value, ok := upstreamBillingRateAt(s.Data, s.LastAttemptAt)
	if !ok {
		return 0, false
	}
	return accountCostMultiplierNumber(value)
}
