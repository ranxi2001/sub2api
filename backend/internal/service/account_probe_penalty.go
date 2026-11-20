package service

import (
	"context"
	"fmt"
	"math"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const AccountProbePenaltyExtraKey = "probe_penalty"

func (s *GatewayService) ProbePenaltySettlementEnabled() bool {
	return s != nil && (s.cfg == nil || s.cfg.RunMode != "simple") && s.DurableUsageBillingEnabled()
}

// Probe penalties are an explicit customer charge, never procurement cost.
// The nested object is replaced atomically by all account editing APIs.
func ValidateAccountProbePenaltyExtra(extra map[string]any) error {
	raw := extra[AccountProbePenaltyExtraKey]
	if raw == nil {
		return nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return infraerrors.BadRequest("INVALID_PROBE_PENALTY", "probe_penalty must contain enabled and minimum_charge")
	}
	enabled, boolean := value["enabled"].(bool)
	minimum, number := accountCostMultiplierNumber(value["minimum_charge"])
	if !boolean || !number || minimum > 1000 || (enabled && minimum <= 0) || QuantizeUsageBillingAmount(minimum) != minimum {
		return infraerrors.BadRequest("INVALID_PROBE_PENALTY", "probe_penalty requires a boolean enabled and a finite minimum_charge between 0 and 1000 with at most eight decimal places; enabled requires a positive charge")
	}
	return nil
}

func (a *Account) ProbePenaltyMinimum() float64 {
	if a == nil || a.Platform != PlatformAnthropic {
		return 0
	}
	value, ok := a.Extra[AccountProbePenaltyExtraKey].(map[string]any)
	if !ok || value["enabled"] != true || ValidateAccountProbePenaltyExtra(a.Extra) != nil {
		return 0
	}
	minimum, _ := accountCostMultiplierNumber(value["minimum_charge"])
	return minimum
}

// A request-time receipt, also persisted in customer_cost_details. Output tokens
// in the protocol become billing equivalents; UsageLog keeps observed tokens.
type ProbePenaltyReceipt struct {
	MinimumCharge        float64 `json:"minimum_charge"`
	Adjustment           float64 `json:"adjustment"`
	ActualCost           float64 `json:"actual_cost"`
	OriginalOutputTokens int     `json:"original_output_tokens"`
	BillingOutputTokens  int     `json:"billing_output_tokens"`
	OutputTokenPrice     float64 `json:"output_token_price"`
}

func calculateProbePenalty(cost *CostBreakdown, minimum, outputPrice float64, outputTokens int) (*ProbePenaltyReceipt, error) {
	if cost == nil || minimum <= 0 || QuantizeUsageBillingAmount(cost.ActualCost) >= minimum {
		return nil, nil
	}
	if cost.BillingMode != "" && cost.BillingMode != string(BillingModeToken) {
		return nil, fmt.Errorf("probe penalty requires token billing")
	}
	if outputTokens < 0 || outputPrice <= 0 || math.IsNaN(outputPrice) || math.IsInf(outputPrice, 0) || cost.ActualCost < 0 || math.IsNaN(cost.ActualCost) || math.IsInf(cost.ActualCost, 0) {
		return nil, fmt.Errorf("probe penalty has no finite positive output token price")
	}
	adjustment := QuantizeUsageBillingAmount(minimum - QuantizeUsageBillingAmount(cost.ActualCost))
	extra := math.Ceil((minimum - cost.ActualCost) / outputPrice)
	if extra > 1e9 || float64(outputTokens)+extra > 1e9 {
		return nil, fmt.Errorf("probe penalty exceeds billing token limit")
	}
	return &ProbePenaltyReceipt{MinimumCharge: minimum, Adjustment: adjustment, ActualCost: minimum,
		OriginalOutputTokens: outputTokens, BillingOutputTokens: outputTokens + int(extra), OutputTokenPrice: outputPrice}, nil
}

// The handler must publish the canonical committed receipt, including replay.
type probePenaltyResultKey struct{}

func WithProbePenaltyResult(ctx context.Context, result *ForwardResult) context.Context {
	return context.WithValue(ctx, probePenaltyResultKey{}, result)
}
