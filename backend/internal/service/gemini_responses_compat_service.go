package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type geminiResponsesOptions struct {
	clientTools apicompat.ResponsesClientToolMapping
}

// ForwardAsResponses uses Gemini's native request builder and token provider,
// including Vertex service-account auth. Anthropic is only an intermediate
// message representation; no Anthropic upstream or credential provider is used.
func (s *GeminiMessagesCompatService) ForwardAsResponses(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	if err := CheckUpstreamModeration(ctx, c, account); err != nil {
		return nil, err
	}
	startTime := time.Now()
	if account == nil || account.Platform != PlatformGemini {
		return nil, s.writeGeminiResponsesError(c, http.StatusBadRequest, "invalid_request_error", "Gemini account required for Responses compatibility")
	}
	switch account.Type {
	case AccountTypeAPIKey, AccountTypeServiceAccount:
	case AccountTypeOAuth:
		return nil, s.writeGeminiResponsesError(c, http.StatusBadRequest, "invalid_request_error", "Gemini OAuth accounts do not support Responses compatibility; use a Vertex service account or API key")
	default:
		return nil, s.writeGeminiResponsesError(c, http.StatusBadRequest, "invalid_request_error", "Unsupported Gemini account type for Responses compatibility")
	}
	normalized, _, err := normalizeOpenAIResponsesLegacyIngressWithConfig(s.cfg, body)
	if err != nil {
		return nil, s.writeGeminiResponsesError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	adapted, mapping, err := adaptResponsesClientToolsForAnthropic(normalized)
	if err != nil {
		return nil, s.writeGeminiResponsesError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	var request apicompat.ResponsesRequest
	if err := json.Unmarshal(adapted, &request); err != nil {
		return nil, s.writeGeminiResponsesError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
	}
	if strings.TrimSpace(request.Model) == "" {
		return nil, s.writeGeminiResponsesError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
	}
	// Gemini compatibility is stateless. Do not silently drop a continuation ID.
	if gjson.GetBytes(adapted, "previous_response_id").String() != "" {
		return nil, s.writeGeminiResponsesError(c, http.StatusBadRequest, "invalid_request_error", "Gemini Responses requires full input history; previous_response_id is not supported")
	}
	claudeRequest, err := apicompat.ResponsesToAnthropicRequest(&request)
	if err != nil {
		return nil, s.writeGeminiResponsesError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	claudeBody, err := json.Marshal(claudeRequest)
	if err != nil {
		return nil, fmt.Errorf("marshal Gemini Responses request: %w", err)
	}
	// Reuse Chat's JSON schema validation and Gemini generationConfig mapping.
	var responseFormat json.RawMessage
	format := gjson.GetBytes(adapted, "text.format")
	if format.Exists() && format.Type != gjson.Null {
		if !format.IsObject() {
			return nil, s.writeGeminiResponsesError(c, http.StatusBadRequest, "invalid_request_error", "text.format must be an object")
		}
		responseFormat = json.RawMessage(format.Raw)
		if format.Get("type").String() == "json_schema" {
			responseFormat, err = sjson.SetRawBytes([]byte(`{"type":"json_schema"}`), "json_schema", responseFormat)
			if err != nil {
				return nil, err
			}
		}
	}
	return s.forwardGeminiOpenAICompat(ctx, c, account, claudeBody, responseFormat, request.Model, request.Stream, false, startTime, &geminiResponsesOptions{clientTools: mapping})
}

func (s *GeminiMessagesCompatService) handleResponsesNonStreamingResponseFromGemini(c *gin.Context, resp *http.Response, model string, isOAuth bool, options *geminiResponsesOptions) (*ClaudeUsage, error) {
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}
	body = unwrapIfNeeded(isOAuth, body)
	var upstream map[string]any
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, s.writeGeminiResponsesError(c, http.StatusBadGateway, "upstream_error", "Failed to parse upstream response")
	}
	if err := validateGeminiResponsesResult(upstream); err != nil {
		return nil, s.writeGeminiResponsesError(c, http.StatusBadGateway, "upstream_error", err.Error())
	}
	response, usage, err := geminiResponseToResponses(upstream, model, body, nil)
	if err != nil {
		return nil, s.writeGeminiResponsesError(c, http.StatusBadGateway, "upstream_error", "Failed to convert upstream response")
	}
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	if err := writeGeminiResponsesJSON(c, response, options); err != nil {
		return nil, err
	}
	return usage, nil
}

func validateGeminiResponsesResult(upstream map[string]any) error {
	if upstream["error"] != nil || extractGeminiFinishReason(upstream) == "" {
		return fmt.Errorf("Gemini response has no valid completion result")
	}
	return nil
}

func writeGeminiResponsesJSON(c *gin.Context, response *apicompat.ResponsesResponse, options *geminiResponsesOptions) error {
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	body, _, err = apicompat.RestoreResponsesClientToolPayload(body, options.clientTools)
	if err != nil {
		return err
	}
	c.Data(http.StatusOK, "application/json", body)
	return nil
}

// Queue heartbeats may already have committed SSE headers before validation or
// upstream rejection. Keep the error on the same Responses wire in that case.
func (s *GeminiMessagesCompatService) writeGeminiResponsesError(c *gin.Context, status int, code, message string) error {
	err := fmt.Errorf("%s", message)
	if c.Writer.Written() && strings.HasPrefix(c.Writer.Header().Get("Content-Type"), "text/event-stream") {
		state := apicompat.NewAnthropicEventToResponsesState()
		writeAnthropicCompatResponsesStreamFailure(c, state, apicompat.NewResponsesClientToolStreamRestorer(apicompat.ResponsesClientToolMapping{}), &anthropicCompatStreamFailure{err: err, errType: code, message: message})
		return err
	}
	c.JSON(status, gin.H{"error": gin.H{"code": code, "type": code, "message": message}})
	return err
}
