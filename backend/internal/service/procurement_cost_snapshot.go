package service

import "github.com/Wei-Shaw/sub2api/internal/domain"

// ProcurementCostSnapshot is request-time evidence, independent of the user
// charge and account debit multiplier. A nil CostMultiplier means unknown, not
// zero. A nil snapshot denotes usage recorded before capture was available.
type ProcurementCostSnapshot struct {
	Version        int                `json:"version"`
	AccountType    string             `json:"account_type"`
	Provider       string             `json:"provider"`
	TargetPlatform string             `json:"target_platform"`
	Basis          string             `json:"basis"`
	TokenBasis     string             `json:"token_basis,omitempty"`
	ImagePrices    map[string]float64 `json:"image_prices,omitempty"`
	CostMultiplier *float64           `json:"cost_multiplier,omitempty"`
}

func captureGatewayProcurementCost(log *UsageLog, account *Account) {
	if log == nil || account == nil {
		return
	}
	if !account.IsGateway() {
		snapshot := &ProcurementCostSnapshot{Version: 2, Basis: "account_usage", AccountType: account.Type, TargetPlatform: log.Platform}
		// Older v2 snapshots only recorded the rate. Freeze both operands for new usage.
		snapshot.TokenBasis = TokenProcurementAccount
		if account.Extra[TokenProcurementBasisKey] == TokenProcurementStandard {
			snapshot.TokenBasis = TokenProcurementStandard
		}
		if rate, ok := accountCostMultiplierNumber(account.Extra[AccountCostMultiplierExtraKey]); ok {
			snapshot.CostMultiplier = &rate
		}
		if log.ImageCount > 0 {
			if prices, ok := account.Extra[ImagePurchasePricesKey].(map[string]any); ok {
				snapshot.ImagePrices = make(map[string]float64, len(prices))
				for tier, raw := range prices {
					if price, valid := accountCostMultiplierNumber(raw); valid {
						snapshot.ImagePrices[tier] = price
					}
				}
			}
		}
		log.ProcurementCostSnapshot = snapshot
		return
	}
	provider := account.GetCredential(GatewayUpstreamKindCredentialKey)
	snapshot := &ProcurementCostSnapshot{Version: 1, AccountType: AccountTypeGateway,
		Provider: provider, TargetPlatform: log.Platform, Basis: "total_cost"}
	switch provider {
	case "new_api":
		if factor, ok := NewAPIGatewayCostForRequest(account, log.Platform); ok {
			snapshot.CostMultiplier = &factor
		}
	case "sub2api":
		if !domain.IsConcretePlatform(log.Platform) {
			break
		}
		if !account.CostMultiplierAutoSyncEnabled() {
			if factor, ok := accountCostMultiplierNumber(account.Extra[AccountCostMultiplierExtraKey]); ok {
				snapshot.CostMultiplier = &factor
			}
		} else {
			declaration := decodeUpstreamBillingProbeSnapshot(account.Extra)
			if factor, ok := declaration.CostMultiplierToSync(); ok {
				snapshot.CostMultiplier = &factor
			}
		}
	}
	log.ProcurementCostSnapshot = snapshot
}
