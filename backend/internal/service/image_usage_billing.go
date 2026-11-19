package service

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/gin-gonic/gin"
)

func openAIUsageBillingModels(result *OpenAIForwardResult, fields ChannelUsageFields) (requested, primary string, candidates []string) {
	requested = fields.OriginalModel
	if fields.APIKeyMappedModel != "" {
		requested = fields.APIKeyMappedModel
	}
	channelModel := fields.ChannelMappedModel
	if !fields.ChannelMappingApplied && channelModel == fields.OriginalModel {
		// An unchanged channel field is an audit alias, not a price override.
		channelModel = requested
	}
	resultModel := result.Model
	if !fields.ChannelMappingApplied && result.Model == fields.OriginalModel {
		resultModel = requested
	}
	primary = forwardResultBillingModel(resultModel, result.UpstreamModel)
	if result.BillingModel != "" {
		primary = strings.TrimSpace(result.BillingModel)
	}
	if fields.BillingModelSource == BillingModelSourceChannelMapped && fields.ChannelMappedModel != "" &&
		((fields.APIKeyMappedModel != "" && fields.ChannelMappingApplied) || fields.ChannelMappedModel != fields.OriginalModel) {
		primary = fields.ChannelMappedModel
	}
	if fields.BillingModelSource == BillingModelSourceRequested && fields.OriginalModel != "" {
		primary = requested
	}
	return requested, primary, usageBillingModelCandidates(primary, result.BillingModel, channelModel, requested, result.UpstreamModel, resultModel)
}

type imageBillingModelKey struct{}

// WithImageBillingModel captures the pricing model selection before forwarding.
func WithImageBillingModel(ctx context.Context, fields ChannelUsageFields) context.Context {
	return context.WithValue(ctx, imageBillingModelKey{}, fields)
}

// ImageMinimumCharge is a final customer charge per completed image. It is
// deliberately not a token price or a multiplier and survives personal discounts.
func imageMinimumCharge(group *Group, models ...string) float64 {
	for _, model := range models {
		if p := matchGroupModelPricing(group, model); p != nil && p.BillingMode == BillingModeToken && p.ImageMinimumCharge != nil {
			return *p.ImageMinimumCharge
		}
	}
	return 0
}

func validateImageMinimumCharge(platform string, p ChannelModelPricing) error {
	if p.ImageMinimumCharge == nil {
		return nil
	}
	value := *p.ImageMinimumCharge
	if platform != PlatformOpenAI || p.BillingMode != BillingModeToken || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1000 {
		return fmt.Errorf("image_minimum_charge requires OpenAI token pricing and a finite final per-image charge between 0 and 1000")
	}
	if QuantizeUsageBillingAmount(value) != value {
		return fmt.Errorf("image_minimum_charge supports at most eight decimal places")
	}
	return nil
}

// applyImageMinimumCharge preserves every observed token and its base cost.
// The adjustment is customer-only; account cost statistics must not inherit it.
func applyImageMinimumCharge(cost *CostBreakdown, count, retries int, perImage float64, standard *CustomerCostDetails) *CustomerCostDetails {
	if cost == nil || cost.BillingMode != string(BillingModeToken) || count <= 0 || perImage <= 0 {
		return nil
	}
	minimum := QuantizeUsageBillingAmount(perImage * float64(count) * float64(1+max(retries, 0)))
	before := QuantizeUsageBillingAmount(cost.ActualCost)
	cost.ActualCost = math.Max(before, minimum)
	details := CustomerCostDetails{
		Version: 3, InputCost: cost.InputCost, OutputCost: cost.OutputCost,
		ImageInputCost: cost.ImageInputCost, ImageOutputCost: cost.ImageOutputCost,
		CacheCreationCost: cost.CacheCreationCost, CacheReadCost: cost.CacheReadCost,
		TotalCost: cost.TotalCost, ActualCost: cost.ActualCost,
	}
	if standard != nil {
		details = *standard
	}
	details.Version = 3
	details.ActualCost = cost.ActualCost
	details.ImageMinimumCharge = minimum
	details.ImageMinimumChargeAdjustment = QuantizeUsageBillingAmount(cost.ActualCost - before)
	return &details
}

// ImageBillingReceipt is filled only after the durable billing transaction
// succeeds. Equivalent usage counts billing quanta, never upstream model tokens.
type ImageBillingReceipt struct {
	Settled                 bool    `json:"-"`
	ActualCost              float64 `json:"actual_cost"`
	BillingMode             string  `json:"billing_mode"`
	ImageCount              int     `json:"image_count"`
	MinimumCharge           float64 `json:"minimum_charge,omitempty"`
	MinimumChargeAdjustment float64 `json:"minimum_charge_adjustment,omitempty"`
}

// A token represents one 1e-8 ledger quantum. At $0.01 / million tokens this
// exactly represents the database's eight-decimal charge, with no second debit.
const ImageEquivalentOutputPricePerMillion = 0.01

func (r ImageBillingReceipt) EquivalentTokens() (int64, error) {
	if !r.Settled || math.IsNaN(r.ActualCost) || math.IsInf(r.ActualCost, 0) || r.ActualCost < 0 {
		return 0, fmt.Errorf("settled image billing receipt unavailable")
	}
	units := math.Round(QuantizeUsageBillingAmount(r.ActualCost) * 1e8)
	if units > 9007199254740991 {
		return 0, fmt.Errorf("image billing usage exceeds JSON safe integer range")
	}
	return int64(units), nil
}

// Explicit image cards and settlement receipts require observed completion;
// an empty upstream data array cannot silently become the requested n images.
func resolveImageCountForBillingMode(c *gin.Context, fields ChannelUsageFields, model, upstreamModel string, reported, requested int) int {
	key := getAPIKeyFromContext(c)
	if c != nil && c.GetHeader("X-Image-Usage") == "billing" {
		return max(reported, 0)
	}
	if key != nil {
		requestedModel, billingModel, _ := openAIUsageBillingModels(&OpenAIForwardResult{Model: model, UpstreamModel: upstreamModel}, fields)
		if p := matchGroupModelPricing(key.Group, billingModel); p != nil && p.BillingMode == BillingModeImage {
			return max(reported, 0)
		}
		for _, model := range []string{requestedModel, billingModel} {
			if p := matchGroupModelPricing(key.Group, model); p != nil && p.BillingMode == BillingModeToken && p.ImageMinimumCharge != nil {
				return max(reported, 0)
			}
		}
	}
	return resolveBillableImageCount(reported, requested, imageCountBillingEnabled(key))
}

type imageBillingReceiptKey struct{}

// WithImageBillingReceipt requests a receipt from synchronous durable recording.
// It is an observation sink only and cannot override pricing or an actual debit.
func WithImageBillingReceipt(ctx context.Context, receipt *ImageBillingReceipt) context.Context {
	return context.WithValue(ctx, imageBillingReceiptKey{}, receipt)
}
