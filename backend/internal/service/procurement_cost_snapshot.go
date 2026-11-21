package service

import "github.com/Wei-Shaw/sub2api/internal/domain"

// ProcurementCostSnapshot is request-time evidence, independent of the user
// charge and account debit multiplier. A nil CostMultiplier means unknown, not
// zero. A nil snapshot denotes history/legacy accounts, not a current estimate.
type ProcurementCostSnapshot struct {
	Version        int      `json:"version"`
	AccountType    string   `json:"account_type"`
	Provider       string   `json:"provider"`
	TargetPlatform string   `json:"target_platform"`
	Basis          string   `json:"basis"`
	CostMultiplier *float64 `json:"cost_multiplier,omitempty"`
}

func captureGatewayProcurementCost(log *UsageLog, account *Account) {
	if log == nil || !account.IsGateway() {
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
