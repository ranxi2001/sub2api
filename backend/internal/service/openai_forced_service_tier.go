package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// openAIModelSupportsUltrafast reports whether OpenAI offers the Ultrafast
// service tier for the model: GPT-6 Astra and GPT-6.1 Sol only. It is the single
// source for both the group-forced tier gate and the 6x Ultrafast billing
// multiplier, so a model can never be forced onto Ultrafast yet billed below it.
func openAIModelSupportsUltrafast(model string) bool {
	normalized := normalizeKnownOpenAICodexModel(model)
	return isOpenAIGPT6AstraModel(normalized) || normalized == "gpt-6.1-sol"
}

// openAIGroupForcedServiceTier returns the service_tier that the trusted request
// Group forces onto an OpenAI-account request for the given upstream model, or
// "" when the group forces nothing and the client's value is kept.
//
// The two group flags are independent and resolve as Ultrafast > Fast > client:
//   - ForceOpenAIUltrafast applies only to models that support Ultrafast
//     (openAIModelSupportsUltrafast) and yields "ultrafast".
//   - Otherwise ForceOpenAIFast yields "priority", for every model.
//
// So a group with only ForceOpenAIUltrafast leaves unsupported models to the
// client, and a group with both flags falls back to Fast for them.
func openAIGroupForcedServiceTier(ctx context.Context, account *Account, model string) string {
	if ctx == nil || account == nil || account.Platform != PlatformOpenAI {
		return ""
	}
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if !IsGroupContextValid(group) || !groupSupportsOpenAIFast(group.Platform) {
		return ""
	}
	switch {
	case group.ForceOpenAIUltrafast && openAIModelSupportsUltrafast(model):
		return OpenAIFastTierUltrafast
	case group.ForceOpenAIFast:
		return OpenAIFastTierPriority
	default:
		return ""
	}
}
