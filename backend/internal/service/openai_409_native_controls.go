package service

import (
	"context"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// projectOpenAI409ImageControls applies the native builder's explicit image
// policy to a verified readable source BEFORE freezing its representation.
// It never accepts the builder's final body as a new trusted snapshot. The
// ordinary builder still checks permission/model validity and applies these
// idempotent transforms; any other late task or controls mutation is refused.
func (s *OpenAIGatewayService) projectOpenAI409ImageControls(ctx context.Context, c *gin.Context, account *Account, body []byte) ([]byte, error) {
	isCodexCLI := openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator")) ||
		(s.cfg != nil && s.cfg.Gateway.ForceCodexCLI)
	strip := isCodexCLI && account.CodexImageGenerationExplicitToolPolicy() == codexImageGenerationExplicitToolPolicyStrip
	apiKey := getAPIKeyFromContext(c)
	allowed := GroupAllowsImageGenerationLatest(c.Request.Context(), apiKeyGroup(apiKey))
	bridge := isCodexCLI && !isOpenAIResponsesLiteHeader(c.GetHeader(responsesLiteHeader)) &&
		allowed && !strip && s.isCodexImageGenerationBridgeEnabled(ctx, account, apiKey)
	requestedModel := gjson.GetBytes(body, "model").String()
	_, upstreamModel := resolveOpenAIForwardMappedModels(account, requestedModel, false)
	normalize := allowed && (bridge || isOpenAIImageGenerationModel(requestedModel) ||
		openAIRequestBodyImageGenerationToolNeedsNormalization(body) || isOpenAIImageGenerationModel(upstreamModel))
	spark := isCodexSparkModel(upstreamModel) && openAIRequestBodyHasImageGenerationDeclaration(body)
	if !strip && !normalize && !spark {
		return body, nil
	}
	var decoded map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &decoded); err != nil {
		return nil, err
	}
	// Model routing is independent of input/controls. Evaluate the same target
	// model as Forward, then retain the caller's model for normal routing and
	// billing; projecting a tool cannot silently select a different account.
	originalModel, hadModel := decoded["model"]
	decoded["model"] = upstreamModel
	if strip {
		stripOpenAIImageGenerationTools(decoded)
	}
	if normalize {
		if bridge {
			ensureOpenAIResponsesImageGenerationTool(decoded)
			ensureOpenAIResponsesImageGenerationToolChoiceAuto(decoded)
		}
		normalizeOpenAIResponsesImageGenerationTools(decoded)
		normalizeOpenAIResponsesImageOnlyModel(decoded)
		if bridge {
			applyCodexImageGenerationBridgeInstructions(decoded)
		}
	}
	if spark {
		stripCodexSparkImageGenerationTools(decoded)
	}
	if hadModel {
		decoded["model"] = originalModel
	} else {
		delete(decoded, "model")
	}
	return json.Marshal(decoded)
}
