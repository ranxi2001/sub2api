package service

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

// APIKeyDefaultContextLimit applies when a model has no published long-context
// pricing threshold. Admission is independent of whether a group bills that tier.
const APIKeyDefaultContextLimit = 200_000

type apiKeyLongContextOutboundModelKey struct{}

// Only final forwarding checks mark this model. An unknown public alias at
// ingress must not lower the published threshold of its known mapped target.
func withAPIKeyLongContextOutboundModel(ctx context.Context, model string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if model = strings.TrimSpace(model); model != "" {
		return context.WithValue(ctx, apiKeyLongContextOutboundModelKey{}, model)
	}
	return ctx
}

// APIKeyLongContextError is a local admission failure, before an upstream request.
type APIKeyLongContextError struct {
	Code            string
	Limit           int
	EstimatedTokens int
	Reason          string
}

func (e *APIKeyLongContextError) Error() string {
	if e.Code == "context_length_exceeded" {
		return fmt.Sprintf("Long context is disabled for this API key; estimated input exceeds the %d-token limit. Shorten or compact the conversation", e.Limit)
	}
	return "Long context is disabled for this API key; send the complete text history without remote context references or attachments: " + e.Reason
}

// CheckAPIKeyLongContext checks a complete HTTP request or Responses WS frame.
// Cached text supplied in the body is counted in full, without a cache discount.
func (s *GatewayService) CheckAPIKeyLongContext(ctx context.Context, key *APIKey, body []byte, models ...string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var billing *BillingService
	if s != nil {
		billing = s.billingService
		if key != nil && !key.AllowLongContext {
			// Channels may lower the effective model's context boundary. Include
			// their target without changing the requested model or request body.
			models = append([]string(nil), models...)
			for _, model := range models {
				mapping, _ := s.ResolveChannelMappingAndRestrict(ctx, key.GroupID, model)
				if mapping.Mapped {
					models = append(models, mapping.MappedModel)
				}
			}
		}
	}
	return checkAPIKeyLongContext(ctx, billing, key, body, models...)
}

func (s *OpenAIGatewayService) CheckAPIKeyLongContext(ctx context.Context, key *APIKey, body []byte, models ...string) error {
	var billing *BillingService
	if s != nil {
		billing = s.billingService
	}
	return checkAPIKeyLongContext(ctx, billing, key, body, models...)
}

func checkAPIKeyLongContext(ctx context.Context, billing *BillingService, key *APIKey, body []byte, models ...string) error {
	if key == nil || key.AllowLongContext {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return &APIKeyLongContextError{Code: "context_unverifiable", Reason: "invalid JSON context"}
	}
	root := gjson.ParseBytes(body)
	// Keep every duplicate/case variant as a candidate: different downstream
	// decoders select different occurrences. A WS envelope is scanned recursively.
	root.ForEach(func(k, v gjson.Result) bool {
		if strings.EqualFold(k.String(), "model") && v.Type == gjson.String {
			models = append(models, v.String())
		}
		return true
	})
	limit := apiKeyLongContextLimit(billing, models)
	if outboundModel, _ := ctx.Value(apiKeyLongContextOutboundModelKey{}).(string); outboundModel != "" {
		// An unpriced account target still has the default ceiling even when
		// another candidate (for example the public alias) has a higher tier.
		limit = min(limit, apiKeyLongContextLimit(billing, []string{outboundModel}))
		root.ForEach(func(k, v gjson.Result) bool {
			if strings.EqualFold(k.String(), "model") && v.Type == gjson.String {
				limit = min(limit, apiKeyLongContextLimit(billing, []string{v.String()}))
			}
			return true
		})
	}
	model := ""
	if len(models) > 0 {
		model = models[0]
	}
	codec, err := openAIInputTokensCodecForModel(model)
	if err != nil {
		return &APIKeyLongContextError{Code: "context_unverifiable", Limit: limit, Reason: "local token counter unavailable"}
	}
	counter := apiKeyContextCounter{ctx: ctx, codec: codec, limit: limit}
	if err := counter.visit(root, 0, false, false, ""); err != nil {
		return err
	}
	return nil
}

func apiKeyLongContextLimit(billing *BillingService, models []string) int {
	limit := 0
	for _, model := range models {
		if billing == nil || strings.TrimSpace(model) == "" {
			continue
		}
		pricing, err := billing.GetModelPricing(model)
		if err != nil || pricing == nil || pricing.LongContextInputThreshold <= 0 {
			continue
		}
		candidate := pricing.LongContextInputThreshold
		if pricing.LongContextThresholdInclusive {
			candidate--
		}
		if candidate > 0 && (limit == 0 || candidate < limit) {
			limit = candidate
		}
	}
	if limit == 0 {
		return APIKeyDefaultContextLimit
	}
	return limit
}

// apiKeyContextCounter deliberately counts all decoded input strings and JSON
// structure, including tool schemas. This is a local conservative estimate, not
// a claim that different providers share an exact tokenizer. Duplicate fields
// remain visible, unlike an encoding/json map.
type apiKeyContextCounter struct {
	ctx   context.Context
	codec tokenizer.Codec
	limit int
	total int
}

func (c *apiKeyContextCounter) visit(value gjson.Result, depth int, literal, inputItem bool, parentField string) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if depth > 64 {
		return c.unverifiable("context nesting exceeds the local counter limit")
	}
	if err := c.add(1); err != nil {
		return err
	}
	if inputItem && !literal && value.IsObject() && apiKeyStoredContextItemReference(value) {
		return c.unverifiable("stored input item reference")
	}
	if value.IsObject() || value.IsArray() {
		var visitErr error
		value.ForEach(func(k, v gjson.Result) bool {
			if value.IsObject() {
				field := strings.ToLower(k.String())
				if !literal && isAPIKeyOpaqueContextField(field) && hasAPIKeyContextValue(v) {
					visitErr = c.unverifiable("unmeasurable context field " + k.String())
					return false
				}
				if !literal && field == "type" && isAPIKeyOpaqueContextType(v.String()) {
					visitErr = c.unverifiable("unmeasurable context type " + v.String())
					return false
				}
				if !literal && field == "prompt" && v.IsObject() && apiKeyJSONHasContextField(v, "id") {
					visitErr = c.unverifiable("stored prompt reference")
					return false
				}
				if visitErr = c.count(k.String()); visitErr != nil {
					return false
				}
			}
			// Schemas, metadata and tool arguments/results are literal data.
			// Their field names do not load context, but still count in full.
			metadata := depth == 0 && value.IsObject() && strings.EqualFold(k.String(), "metadata")
			nestedLiteral := literal || metadata || (value.IsObject() && (strings.EqualFold(k.String(), "parameters") || strings.EqualFold(k.String(), "input_schema") || apiKeyToolDataField(value, parentField, k.String())))
			nestedItem := (value.IsArray() && inputItem) || (value.IsObject() && (strings.EqualFold(k.String(), "input") || strings.EqualFold(k.String(), "messages")))
			visitErr = c.visit(v, depth+1, nestedLiteral, nestedItem, k.String())
			return visitErr == nil
		})
		return visitErr
	}
	if value.Type == gjson.Null {
		return nil
	}
	return c.count(value.String())
}

func apiKeyToolDataField(value gjson.Result, parentField, field string) bool {
	// Anthropic tool input is arbitrary JSON. Tool-result content remains a
	// protocol block array so real images/documents there are still rejected.
	if field == "input" {
		switch value.Get("type").String() {
		case "tool_use", "server_tool_use", "custom_tool_call":
			return true
		}
	}
	// Gemini uses structured JSON for both function arguments and results.
	return (parentField == "functionCall" || parentField == "function_call") && field == "args" ||
		(parentField == "functionResponse" || parentField == "function_response") && field == "response"
}

func (c *apiKeyContextCounter) count(text string) error {
	// Bounded pieces avoid quadratic work on adversarial megabyte-long single
	// strings. Splitting may slightly overestimate, which keeps admission strict.
	const chunkBytes = 8192
	for len(text) > 0 {
		if err := c.ctx.Err(); err != nil {
			return err
		}
		end := min(len(text), chunkBytes)
		for end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		n, err := c.codec.Count(text[:end])
		if err != nil {
			return c.unverifiable("input text is not countable")
		}
		if err := c.add(n); err != nil {
			return err
		}
		text = text[end:]
	}
	return nil
}

func (c *apiKeyContextCounter) add(tokens int) error {
	c.total += tokens
	if c.total > c.limit {
		return &APIKeyLongContextError{Code: "context_length_exceeded", Limit: c.limit, EstimatedTokens: c.total}
	}
	return nil
}

func (c *apiKeyContextCounter) unverifiable(reason string) error {
	return &APIKeyLongContextError{Code: "context_unverifiable", Limit: c.limit, EstimatedTokens: c.total, Reason: reason}
}

func hasAPIKeyContextValue(value gjson.Result) bool {
	if value.Type == gjson.Null || value.Type == gjson.False {
		return false
	}
	if value.Type == gjson.String {
		return strings.TrimSpace(value.String()) != ""
	}
	return true
}

func apiKeyJSONHasContextField(value gjson.Result, field string) bool {
	found := false
	value.ForEach(func(k, v gjson.Result) bool {
		found = strings.EqualFold(k.String(), field) && hasAPIKeyContextValue(v)
		return !found
	})
	return found
}

func apiKeyStoredContextItemReference(value gjson.Result) bool {
	found := false
	value.ForEach(func(k, v gjson.Result) bool {
		if !strings.EqualFold(k.String(), "id") || v.Type != gjson.String {
			return true
		}
		id := strings.ToLower(strings.TrimSpace(v.String()))
		for _, prefix := range []string{"msg_", "rs_", "fc_", "compaction_", "resp_"} {
			if strings.HasPrefix(id, prefix) {
				found = true
				return false
			}
		}
		return true
	})
	if !found {
		return false
	}
	// Responses history can include complete output objects with server-issued
	// IDs. Those IDs do not replace the visible message/tool text. Keep rejecting
	// reference-only objects and opaque reasoning/compaction state.
	kind := strings.ToLower(strings.TrimSpace(value.Get("type").String()))
	switch kind {
	case "message", "", "reasoning":
		return !apiKeyJSONHasVisibleContextField(value, "content")
	case "function_call":
		return !apiKeyJSONHasVisibleContextField(value, "arguments")
	case "function_call_output":
		return !apiKeyJSONHasVisibleContextField(value, "output")
	case "custom_tool_call":
		return !apiKeyJSONHasVisibleContextField(value, "input")
	case "custom_tool_call_output":
		return !apiKeyJSONHasVisibleContextField(value, "output")
	default:
		return true
	}
}

func apiKeyJSONHasVisibleContextField(value gjson.Result, field string) bool {
	found := false
	value.ForEach(func(k, v gjson.Result) bool {
		// An explicitly supplied empty string/array is still visible content;
		// null or an absent field leaves the server-side item as a reference.
		found = strings.EqualFold(k.String(), field) && (v.Type == gjson.String || v.IsArray() || v.IsObject())
		return !found
	})
	return found
}

func isAPIKeyOpaqueContextField(field string) bool {
	switch field {
	case "previous_response_id", "previousresponseid", "conversation", "conversation_id", "conversationid",
		"cachedcontent", "cached_content", "file_id", "fileid", "file_uri", "fileuri", "file_url", "fileurl",
		"filedata", "file_data", "inline_data", "inlinedata", "image_url", "imageurl", "audio_url", "audiourl",
		"video_url", "videourl", "input_audio", "encrypted_content", "encryptedcontent", "vector_store_ids", "vectorstoreids",
		"prompt_id", "promptid", "mcp_servers", "mcpservers", "googlesearch", "google_search", "googlesearchretrieval",
		"google_search_retrieval", "urlcontext", "url_context", "retrieval", "codeexecution", "code_execution":
		return true
	default:
		return false
	}
}

func isAPIKeyOpaqueContextType(kind string) bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if strings.HasPrefix(kind, "session.") || strings.HasPrefix(kind, "web_search_") || strings.HasPrefix(kind, "web_fetch_") {
		return true
	}
	switch kind {
	case "item_reference", "input_file", "input_image", "input_audio", "input_video", "image", "document", "audio", "file", "video", "video_url",
		"file_search", "web_search", "web_search_preview", "web_fetch", "code_interpreter", "mcp", "compaction", "response.append", "redacted_thinking":
		return true
	default:
		return false
	}
}
