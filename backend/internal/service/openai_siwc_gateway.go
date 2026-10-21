package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func normalizeSIWCBody(body []byte) ([]byte, error) {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return nil, infraerrors.BadRequest("OPENAI_SIWC_INVALID_REQUEST", "Invalid Responses request")
	}
	// This route has no server-side conversation storage or hosted tool_search.
	for _, key := range []string{"background", "conversation", "previous_response_id", "max_output_tokens", "max_completion_tokens", "max_tool_calls", "metadata", "moderation", "multi_agent", "prompt", "prompt_cache_retention", "prompt_cache_options", "safety_identifier", "temperature", "top_logprobs", "top_p", "truncation", "user", "client_metadata"} {
		delete(payload, key)
	}
	payload["store"] = false
	payload["stream"] = true
	if text, ok := payload["input"].(string); ok {
		payload["input"] = []any{map[string]any{"type": "message", "role": "user", "content": text}}
	}
	if payload["input"] == nil {
		payload["input"] = []any{}
	}
	if items, ok := payload["input"].([]any); ok {
		kept := make([]any, 0, len(items))
		for _, raw := range items {
			if item, ok := raw.(map[string]any); ok {
				if item["type"] == "configuration_update" {
					if update, ok := item["reasoning"].(map[string]any); ok {
						if effort, ok := update["effort"].(string); ok && effort != "" {
							reasoning, _ := payload["reasoning"].(map[string]any)
							if reasoning == nil {
								reasoning = map[string]any{}
							}
							reasoning["effort"] = effort
							payload["reasoning"] = reasoning
						}
					}
					continue
				}
				if item["role"] == "system" {
					item["role"] = "developer"
				}
			}
			kept = append(kept, raw)
		}
		payload["input"] = kept
	}
	if tools, ok := payload["tools"].([]any); ok {
		var local []any
		grouped := make([]any, 0, len(tools))
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("invalid SIWC tool declaration")
			}
			switch tool["type"] {
			case "function", "custom":
				local = append(local, tool)
			case "namespace":
				children, _ := tool["tools"].([]any)
				for _, child := range children {
					entry, ok := child.(map[string]any)
					if !ok || (entry["type"] != "function" && entry["type"] != "custom") {
						return nil, infraerrors.BadRequest("OPENAI_SIWC_UNSUPPORTED_TOOL", "SIWC namespaces support function/custom tools")
					}
				}
				grouped = append(grouped, tool)
			case "web_search", "web_search_preview":
				grouped = append(grouped, tool)
			default:
				return nil, infraerrors.BadRequest("OPENAI_SIWC_UNSUPPORTED_TOOL", "SIWC does not support this hosted tool; select Codex or use function/custom tools")
			}
		}
		if len(local) > 0 {
			merged := false
			for _, raw := range grouped {
				if namespace, ok := raw.(map[string]any); ok && namespace["type"] == "namespace" && namespace["name"] == "functions" {
					children, _ := namespace["tools"].([]any)
					namespace["tools"] = append(children, local...)
					merged = true
					break
				}
			}
			if !merged {
				grouped = append(grouped, map[string]any{"type": "namespace", "name": "functions", "tools": local})
			}
		}
		payload["tools"] = grouped
	}
	return json.Marshal(payload)
}

func (s *OpenAIGatewayService) buildSIWCRequest(ctx context.Context, c *gin.Context, body []byte, token string) (*http.Request, error) {
	if suffix := openAIResponsesRequestPathSuffix(c); suffix != "" {
		return nil, infraerrors.BadRequest("OPENAI_SIWC_UNSUPPORTED_ENDPOINT", "SIWC supports /v1/responses; compact and other endpoints are unavailable")
	}
	if token == "" {
		return nil, infraerrors.BadRequest("OPENAI_SIWC_AUTH_REQUIRED", "Authorize SIWC for this account first")
	}
	normalized, err := normalizeSIWCBody(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(normalized))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if gjson.GetBytes(body, "stream").Bool() {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("Connection", "Keep-Alive")
	req.Header.Set("User-Agent", fmt.Sprintf("Sub2API (%s; %s)", runtime.GOOS, runtime.GOARCH))
	if sid := strings.TrimSpace(c.GetHeader("Session-Id")); sid != "" {
		req.Header.Set("session_id", sid)
		req.Header.Set("X-Client-Request-Id", sid)
	}
	return req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI)), nil
}

func (s *OpenAIGatewayService) forwardSIWC(ctx context.Context, c *gin.Context, account *Account, body []byte, start time.Time) (*OpenAIForwardResult, error) {
	if suffix := openAIResponsesRequestPathSuffix(c); suffix != "" {
		return nil, infraerrors.BadRequest("OPENAI_SIWC_UNSUPPORTED_ENDPOINT", "SIWC supports /v1/responses only")
	}
	reqModel := gjson.GetBytes(body, "model").String()
	mapped := normalizeOpenAIModelForUpstream(account, resolveOpenAIForwardModel(account, reqModel, ""))
	upstream, err := sjson.SetBytes(body, "model", mapped)
	if err != nil {
		return nil, err
	}
	if cacheKey := gjson.GetBytes(upstream, "prompt_cache_key").String(); cacheKey != "" {
		upstream, err = sjson.SetBytes(upstream, "prompt_cache_key", isolateOpenAIUpstreamSessionID(getAPIKeyIDFromContext(c), account, cacheKey))
		if err != nil {
			return nil, err
		}
	}
	// Validate before acquiring tokens. Preserve the downstream stream preference;
	// the shared parser converts the upstream SSE terminal response to JSON.
	if _, err = normalizeSIWCBody(upstream); err != nil {
		return nil, err
	}
	SetActualOpenAIUpstreamEndpoint(c, openAIResponsesUpstreamEndpoint)
	return s.forwardOpenAIPassthrough(withOpenAITransportMode(ctx, "siwc"), c, account, body, upstream, reqModel, false, extractOpenAIReasoningEffortFromBody(body, reqModel), gjson.GetBytes(body, "stream").Bool(), start)
}
