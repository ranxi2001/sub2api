package service

import (
	"math"
	"slices"
)

// CustomerCostDetails is display-only, immutable request-time evidence. Costs
// are Standard components before the customer rate; upstream UsageLog monetary
// fields and the billing command remain authoritative and unchanged.
type CustomerCostDetails struct {
	CacheCostDetails  []CustomerCacheCostDetail `json:"cache_cost_details,omitempty"`
	Version           int                       `json:"version"`
	FreeFast          bool                      `json:"free_fast"`
	InputCost         float64                   `json:"input_cost"`
	OutputCost        float64                   `json:"output_cost"`
	ImageInputCost    float64                   `json:"image_input_cost"`
	ImageOutputCost   float64                   `json:"image_output_cost"`
	CacheCreationCost float64                   `json:"cache_creation_cost"`
	CacheReadCost     float64                   `json:"cache_read_cost"`
	TotalCost         float64                   `json:"total_cost"`
	ActualCost        float64                   `json:"actual_cost"`
	Multiplier        float64                   `json:"multiplier"`
}

// Capture the calculation that actually supplied the customer charge. Never
// reconstruct a Standard quote from Priority totals or current group settings.
// Non-token/additive tool charges have no complete token breakdown here, so
// leave their display evidence unknown instead of allocating those fees to tokens.
func newFreeFastCustomerCostDetails(cost *CostBreakdown, multiplier float64) *CustomerCostDetails {
	if cost == nil || (cost.BillingMode != "" && cost.BillingMode != string(BillingModeToken)) {
		return nil
	}
	sum := cost.InputCost + cost.OutputCost + cost.ImageInputCost + cost.ImageOutputCost + cost.CacheCreationCost + cost.CacheReadCost
	for _, value := range []float64{cost.InputCost, cost.OutputCost, cost.ImageInputCost, cost.ImageOutputCost, cost.CacheCreationCost, cost.CacheReadCost, cost.TotalCost, cost.ActualCost, multiplier} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return nil
		}
	}
	if math.Abs(sum-cost.TotalCost) > 1e-12*math.Max(1, cost.TotalCost) {
		return nil
	}
	// The gateway supplies its resolved personal/group/peak rate and the
	// Standard quote's model surcharge. Reasoning and context are already
	// applied to the component prices. Keep the rate even for zero quotes.
	if math.Abs(sum*multiplier-cost.ActualCost) > 1e-12*math.Max(1, cost.ActualCost) {
		return nil
	}
	version := 1
	if cost.customerCacheCosts != nil {
		version = 2
	}
	return &CustomerCostDetails{Version: version, FreeFast: true,
		CacheCostDetails: slices.Clone(cost.customerCacheCosts),
		InputCost:        cost.InputCost, OutputCost: cost.OutputCost,
		ImageInputCost: cost.ImageInputCost, ImageOutputCost: cost.ImageOutputCost,
		CacheCreationCost: cost.CacheCreationCost, CacheReadCost: cost.CacheReadCost,
		TotalCost: cost.TotalCost, ActualCost: cost.ActualCost, Multiplier: multiplier,
	}
}
