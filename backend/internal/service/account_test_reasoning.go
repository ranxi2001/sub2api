package service

import (
	"context"
	"strings"
)

// AccountTestReasoning describes only the effort values accepted by the model.
// An empty list means the test should leave reasoning to the upstream default.
type AccountTestReasoning struct {
	SupportedReasoningLevels []string `json:"supported_reasoning_levels"`
	DefaultReasoningLevel    string   `json:"default_reasoning_level"`
}

func (s *AccountTestService) GetAccountTestReasoning(ctx context.Context, account *Account, modelID string) AccountTestReasoning {
	target := strings.TrimSpace(modelID)
	if !account.IsOpenAIPassthroughEnabled() {
		target = account.GetMappedModel(target)
	}
	// Use the same account-keyed cache as the model picker. Read the manifest
	// before its public-list projection, which intentionally removes capabilities.
	if account.IsOpenAI() && s != nil && s.openaiGatewayService != nil {
		var response *OpenAIModelsResponse
		var err error
		if account.IsOpenAIOAuthLike() {
			version := CodexCanonicalClientVersion()
			if s.openaiGatewayService.settingService != nil {
				version = s.openaiGatewayService.settingService.GetOpenAICodexClientVersion(ctx)
			}
			response, err = s.openaiGatewayService.FetchCodexModelsManifest(ctx, account, version, "")
		} else {
			response, err = s.openaiGatewayService.FetchOpenAIModelsList(ctx, account)
		}
		if err == nil && response != nil {
			_, metadata, parseErr := extractUpstreamModelCatalog(response.Body, false)
			if parseErr == nil {
				if result, ok := accountTestReasoningFromMetadata(metadata[target]); ok {
					return result
				}
			}
		}
	}
	if metadata, ok := account.GetUpstreamModelMetadata(target); ok {
		if result, known := accountTestReasoningFromMetadata(metadata); known {
			return result
		}
	}
	descriptor := newConfiguredCodexModelDescriptor(target)
	result := AccountTestReasoning{SupportedReasoningLevels: []string{}}
	// The generic descriptor's synthetic "none" is not evidence that an unknown
	// model accepts reasoning.effort. Leave unsupported/unknown models unset.
	for _, level := range descriptor.SupportedReasoningLevels {
		if level.Effort != "none" {
			result.SupportedReasoningLevels = append(result.SupportedReasoningLevels, level.Effort)
		}
	}
	if len(result.SupportedReasoningLevels) > 0 && descriptor.DefaultReasoningLevel != nil {
		result.DefaultReasoningLevel = *descriptor.DefaultReasoningLevel
	}
	return result
}

func accountTestReasoningFromMetadata(metadata UpstreamModelMetadata) (AccountTestReasoning, bool) {
	result := AccountTestReasoning{SupportedReasoningLevels: []string{}}
	if metadata.Reasoning != nil && !*metadata.Reasoning {
		return result, true
	}
	levels := normalizeReasoningLevels(metadata.SupportedReasoningLevels)
	if len(levels) == 0 {
		return result, false
	}
	result.SupportedReasoningLevels = levels
	result.DefaultReasoningLevel = levels[0]
	for _, level := range levels {
		if level == normalizeReasoningLevel(metadata.DefaultReasoningLevel) {
			result.DefaultReasoningLevel = level
			break
		}
	}
	return result, true
}
