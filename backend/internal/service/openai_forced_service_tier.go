package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/tidwall/gjson"
	"strings"
)

// openAIModelSupportsUltrafast reports whether OpenAI offers the Ultrafast
// service tier for the model. It is the single
// source for both the group-forced tier gate and the Ultrafast billing
// multiplier, so a model can never be forced onto Ultrafast yet billed below it.
func openAIModelSupportsUltrafast(model string) bool {
	normalized := normalizeKnownOpenAICodexModel(model)
	return isOpenAIGPT6AstraModel(normalized) || normalized == "gpt-6.1-sol" || normalized == "gpt-6.2-sol" || normalized == "gpt-6.2-astra"
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
	if group.ForceOpenAIUltrafast && openAIModelSupportsUltrafast(model) {
		// Resolve the published Pro entitlement first, then account-specific grants.
		if base := openai.GPT62ModelID(model); base != "" && account.IsOpenAIOAuthLike() {
			if gpt62PlanIncludesUltrafast(account, base) {
				return OpenAIFastTierUltrafast
			}
			metadata, ok := account.GetUpstreamModelMetadata(base)
			if ok && gjson.GetBytes(metadata.CodexToolCapabilities["service_tiers"], `#(id=="ultrafast")`).Exists() {
				return OpenAIFastTierUltrafast
			}
		} else {
			return OpenAIFastTierUltrafast
		}
	}
	if group.ForceOpenAIFast {
		return OpenAIFastTierPriority
	}
	return ""
}

// On ChatGPT subscriptions, Pro 200 includes GPT-6.2 Sol Ultrafast and
// Pro 500 includes both GPT-6.2 models. Workspace grants come from discovery.
func gpt62PlanIncludesUltrafast(account *Account, base string) bool {
	if !account.IsOpenAIOAuthLike() || (base != "gpt-6.2-sol" && base != "gpt-6.2-astra") {
		return false
	}
	// Use the same subscription SKU aliases as the account plan picker.
	plan := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '_':
			return -1
		}
		return r
	}, strings.ToLower(strings.TrimSpace(account.GetCredential("plan_type"))))
	switch plan {
	case "pro", "chatgptpro":
		return base == "gpt-6.2-sol"
	case "promax":
		return true
	default:
		return false
	}
}
