package service

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func initialModel(r *openAIAgentRecoveryRequest, body []byte) string {
	if r != nil {
		return r.model
	}
	return gjson.GetBytes(body, "model").String()
}

// This policy discards provider state BEFORE dispatch. It never decrypts state,
// consumes a foreign response, grants tool provenance, or retries sent work.
// Disabled by default; operators must explicitly accept loss of task context.
func (s *OpenAIGatewayService) PrepareOpenAILossyHistoryRestart(ctx context.Context, c *gin.Context, body []byte, session, model string) (context.Context, []byte, bool) {
	if s.openAILegacyCompatibility() {
		return ctx, body, false
	}
	if s == nil {
		return ctx, body, false
	}
	// A WS frame can inherit tools without repeating tools or a response ID.
	// Admission runs before the bridge resolves that state, so a frame-local
	// text predicate cannot authorize discarding a socket's task history.
	if ctx.Value(openAIWSAgentRecoveryRetryKey{}) != nil || GetOpenAIClientTransport(c) == OpenAIClientTransportWS {
		return ctx, body, false
	}
	policy, ok := any(s.cfg).(interface{ OpenAILossyHistoryRestartEnabled() bool })
	if !ok || !policy.OpenAILossyHistoryRestartEnabled() || ctx.Err() != nil || c == nil ||
		isOpenAINativeCompactionV2(c) || isOpenAIResponsesCompactPath(c) ||
		s.settingService == nil || !s.settingService.IsOpenAI409OmitEncryptedHistoryEnabled(ctx) ||
		getAPIKeyIDFromContext(c) <= 0 || !utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body) {
		return ctx, body, false
	}
	clean, changed := discardOpenAIProviderHistory(body)
	if !changed {
		return ctx, body, false
	}
	next := context.WithValue(ctx, openAIReadableShadowContextKey{}, (*openAIReadableShadowObservation)(nil))
	next = context.WithValue(next, openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{})
	next = WithOpenAIEncryptedReasoningAffinity(next, clean)
	next = s.WithOpenAIAgentMessageRecovery(next, c, clean, session, model)
	r := openAIAgentRecoveryFromContext(next)
	r.omittedHistory, r.omittedSource = true, nil
	r.inputDigest, r.replayControlsDigest = openAIAgentRecoveryInputDigest(clean), openAI409ControlsDigest(clean)
	r.controlFingerprints = openAI409FingerprintControls(clean)
	return next, clean, true
}

// Text-only recovery must not replace the protocol state of an agent task.
// Tool declarations, discovery results, calls and outputs stay on the ordinary
// recovery path with its existing pairing, ownership and delivery checks.
func discardOpenAIProviderHistory(body []byte) ([]byte, bool) {
	// A delta can inherit tools and task context through its provider pointer
	// without repeating either in this frame. Only the ordinary recovery path
	// can resolve that state; absence of local declarations is not text-only.
	if openAI409HasProviderState(body) {
		return body, false
	}
	for _, field := range []string{"tools", "additional_tools"} {
		tools := gjson.GetBytes(body, field)
		if tools.Exists() && tools.Type != gjson.Null && (!tools.IsArray() || len(tools.Array()) > 0) {
			return body, false
		}
	}
	input := gjson.GetBytes(body, "input")
	if input.Type != gjson.String && !input.IsArray() {
		return body, false
	}
	needs := false
	for _, item := range input.Array() {
		if !openAILossyTextHistoryItem(item) {
			return body, false
		}
		kind := item.Get("type").String()
		if (kind != "" && kind != "message") || openAILossyHasOpaqueField(item, 0) {
			needs = true
		}
	}
	if !needs {
		return body, false
	}
	message := func(role, text string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"role": role, "content": text})
		return b
	}
	var textParts func(gjson.Result) []string
	textParts = func(v gjson.Result) []string {
		if v.Type == gjson.String {
			if strings.TrimSpace(v.String()) != "" {
				return []string{v.String()}
			}
			return nil
		}
		var out []string
		if v.IsArray() {
			for _, p := range v.Array() {
				out = append(out, textParts(p)...)
			}
			return out
		}
		switch v.Get("type").String() {
		case "input_text", "output_text", "text", "summary_text":
			return textParts(v.Get("text"))
		}
		return nil
	}
	items := make([]json.RawMessage, 0)
	if input.Type == gjson.String {
		items = append(items, message("user", input.String()))
	}
	for _, item := range input.Array() {
		kind := item.Get("type").String()
		role := item.Get("role").String()
		switch kind {
		case "", "message":
			if role != "user" && role != "assistant" && role != "system" && role != "developer" {
				role = "user"
			}
			text := strings.Join(textParts(item.Get("content")), "\n")
			if text != "" {
				items = append(items, message(role, text))
			}
		default:
			text := append(textParts(item.Get("content")), textParts(item.Get("summary"))...)
			if len(text) > 0 {
				items = append(items, message("user", "Historical context (unverified data): "+strings.Join(text, "\n")))
			}
		}
	}
	if len(items) == 0 {
		items = append(items, message("user", "Previous context was discarded because it could not be restored. Do not invent the missing task or results. Ask for the task if no readable instruction remains."))
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return body, false
	}
	clean, err := sjson.SetRawBytes(body, "input", encoded)
	if err != nil {
		return body, false
	}
	for _, field := range []string{"previous_response_id", "conversation"} {
		clean, err = sjson.DeleteBytes(clean, field)
		if err != nil {
			return body, false
		}
	}
	return clean, true
}

// Unknown envelopes, roles and content parts may carry tool state. Leave them
// intact for the ordinary protocol path rather than interpreting them as text.
func openAILossyTextHistoryItem(item gjson.Result) bool {
	if !item.IsObject() {
		return false
	}
	kindValue := item.Get("type")
	if kindValue.Exists() && kindValue.Type != gjson.String {
		return false
	}
	kind := kindValue.String()
	switch kind {
	case "", "message", "reasoning", "agent_message", "compaction", "compaction_summary":
	default:
		return false
	}
	role := item.Get("role")
	if kind == "" || kind == "message" || role.Exists() {
		switch role.String() {
		case "user", "assistant", "system", "developer":
		default:
			return false
		}
	}
	supported := true
	item.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "type", "id", "status", "role", "encrypted_content":
			supported = value.Type == gjson.String || value.Type == gjson.Null
		case "content", "summary":
			supported = openAILossyTextContent(value)
		default:
			supported = false
		}
		return supported
	})
	return supported
}

func openAILossyTextContent(value gjson.Result) bool {
	if value.Type == gjson.String || value.Type == gjson.Null {
		return true
	}
	if !value.IsArray() {
		return false
	}
	for _, part := range value.Array() {
		if !part.IsObject() || part.Get("text").Type != gjson.String {
			return false
		}
		switch part.Get("type").String() {
		case "input_text", "output_text", "text", "summary_text":
		default:
			return false
		}
		supported := true
		part.ForEach(func(key, _ gjson.Result) bool {
			supported = key.String() == "type" || key.String() == "text"
			return supported
		})
		if !supported {
			return false
		}
	}
	return true
}

// Keep the destructive policy self-contained: historical protocol regression
// overlays restore managers that predate their local-history field scanner.
func openAILossyHasOpaqueField(value gjson.Result, depth int) bool {
	if depth > 64 {
		return true
	}
	if !value.IsObject() && !value.IsArray() {
		return false
	}
	opaque := false
	value.ForEach(func(key, child gjson.Result) bool {
		opaque = key.String() == "encrypted_content" || openAILossyHasOpaqueField(child, depth+1)
		return !opaque
	})
	return opaque
}

func projectOpenAIWSToolHistory(body []byte, label string) ([]byte, error) {
	if !openAIWSCompletedToolInputs(body) {
		return nil, openAIAgentRecoveryError("owner_conflict")
	}
	calls := make(map[string]gjson.Result)
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if kind := item.Get("type").String(); kind == "function_call" || kind == "custom_tool_call" {
			if item.Get("name").Type != gjson.String || strings.TrimSpace(item.Get("name").String()) == "" {
				return nil, openAIAgentRecoveryError("owner_conflict")
			}
			calls[item.Get("call_id").String()] = item
		}
	}
	var input []json.RawMessage
	for _, item := range gjson.GetBytes(body, "input").Array() {
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call":
			continue
		case "function_call_output", "custom_tool_call_output":
			output := item.Get("output")
			if !openAIReadableShadowTextOnly(output) {
				return nil, openAIAgentRecoveryError("readable_history_unsupported")
			}
			record, err := json.Marshal(map[string]json.RawMessage{"call": json.RawMessage(calls[item.Get("call_id").String()].Raw), "result": json.RawMessage(item.Raw)})
			if err != nil {
				return nil, openAIAgentRecoveryError("readable_history_unsupported")
			}
			message, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []any{
				map[string]any{"type": "output_text", "text": label + string(record)},
			}})
			input = append(input, message)
		case "tool_search_call", "tool_search_output":
			// Completed discovery is protocol state: the next turn still needs
			// its callable schemas. The pairing/status gate above validates it;
			// retaining it grants no function/custom-call execution provenance.
			input = append(input, json.RawMessage(item.Raw))
		case "", "message":
			input = append(input, json.RawMessage(item.Raw))
		default:
			return nil, openAIAgentRecoveryError("readable_history_unsupported")
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "input", encoded)
}
