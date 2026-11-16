package service

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// This representation is used only by the optional, connection-local history.
// The live request is unchanged. Completed delivery, response/principal scope,
// per-tool proof and explicit unsent admission remain required at restoration.
func openAIWSReadableHistoryItems(body []byte) ([]json.RawMessage, string) {
	if len(body) > openAIWSLocalHistoryLimit {
		return nil, "input_limit"
	}
	if !utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body) {
		return nil, "invalid_json"
	}
	hasReasoning := false
	for _, item := range gjson.GetBytes(body, "input").Array() {
		hasReasoning = hasReasoning || item.Get("type").String() == "reasoning"
	}
	if hasReasoning {
		var err error
		body, err = prepareOpenAI409ReadableReplay(body)
		if err != nil || len(body) > openAIWSLocalHistoryLimit {
			return nil, "reasoning_normalization"
		}
	}
	input := gjson.GetBytes(body, "input")
	var items []json.RawMessage
	if input.Type == gjson.String {
		raw, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": input.String()})
		items = []json.RawMessage{raw}
	} else if input.IsArray() {
		for _, item := range input.Array() {
			items = append(items, json.RawMessage(item.Raw))
		}
	} else {
		return nil, "input_shape"
	}
	size := 0
	for i, raw := range items {
		item := gjson.ParseBytes(raw)
		kind := item.Get("type").String()
		// Existing Responses compatibility treats named outputs without call_id as
		// external inputs (for example a delegated task), not tool executions. Keep
		// their exact JSON as user-supplied data; never mint a completed-tool proof.
		if kind == "function_call_output" && !item.Get("call_id").Exists() &&
			item.Get("name").Type == gjson.String && strings.TrimSpace(item.Get("name").String()) != "" {
			if openAIWSHasOpaqueField(item, 0) || !openAIReadableShadowTextOnly(item.Get("output")) || !openAI409ReadableText(item.Get("output")) {
				return nil, "named_input_unreadable"
			}
			if item.Get("status").Exists() && item.Get("status").String() != "completed" {
				return nil, "incomplete_named_input"
			}
			var err error
			raw, err = json.Marshal(map[string]any{"type": "message", "role": "user", "content": "External named input supplied by the client (JSON, not evidence of tool execution): " + string(raw)})
			if err != nil {
				return nil, "serialization"
			}
		} else if kind == "agent_message" {
			if !openAI409ReadableText(item.Get("content")) && !openAI409ReadableText(item.Get("summary")) {
				return nil, "agent_envelope_unreadable"
			}
			// Reuse the existing lossy-envelope projection with the same caller's
			// readable text as its intent gate. Only the projected original agent item
			// is retained; the temporary guard message never enters replay history.
			intent := item.Get("content")
			if !openAI409ReadableText(intent) {
				intent = item.Get("summary")
			}
			guard, err := json.Marshal(map[string]json.RawMessage{"type": json.RawMessage(`"message"`), "role": json.RawMessage(`"user"`), "content": json.RawMessage(intent.Raw)})
			if err != nil {
				return nil, "serialization"
			}
			wrapped := append(append(append(append([]byte(`{"input":[`), guard...), ','), raw...), ']', '}')
			cleaned, changed := omitOpenAIHistoricalCiphertext(wrapped)
			normalized := gjson.GetBytes(cleaned, "input.1")
			if !changed || !normalized.IsObject() {
				return nil, "agent_envelope_unreadable"
			}
			raw = json.RawMessage(normalized.Raw)
		} else if kind == "compaction" || kind == "compaction_summary" || kind == "item_reference" {
			// Assistant attribution prevents a history placeholder from becoming a task.
			content := openAIHistoricalCiphertextOmitted
			if kind != "item_reference" {
				var metadata map[string]any
				if err := decodeOpenAIJSONUseNumber(raw, &metadata); err != nil {
					return nil, "serialization"
				}
				readable, err := json.Marshal(openAI409ReadableMetadata(map[string]any{"content": metadata["content"], "summary": metadata["summary"]}))
				if err != nil {
					return nil, "serialization"
				}
				content = "Historical compaction context (JSON): " + string(readable) + "\n" + openAIHistoricalCiphertextOmitted
			}
			var err error
			raw, err = json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": content})
			if err != nil {
				return nil, "serialization"
			}
		}
		if !openAIWSPlainItem(raw) {
			return nil, openAIWSHistoryItemFailure(item)
		}
		size += len(raw)
		if size > openAIWSLocalHistoryLimit {
			return nil, "input_limit"
		}
		items[i] = raw
	}
	return items, ""
}

// Fixed enums only: unknown type names, roles and user content never enter logs.
func openAIWSHistoryItemFailure(item gjson.Result) string {
	if !item.IsObject() {
		return "item_shape"
	}
	if openAIWSHasOpaqueField(item, 0) {
		return "opaque_item"
	}
	switch item.Get("type").String() {
	case "", "message":
		switch item.Get("role").String() {
		case "user", "assistant", "system", "developer":
			return "message_nontext"
		default:
			return "message_role"
		}
	case "function_call", "custom_tool_call":
		return "tool_call_shape"
	case "function_call_output", "custom_tool_call_output":
		if item.Get("call_id").Type != gjson.String || item.Get("call_id").String() == "" {
			return "tool_result_identity"
		}
		return "tool_result_nontext"
	case "agent_message":
		return "agent_envelope"
	case "tool_search_call", "tool_search_output":
		return "tool_search_history"
	case "web_search_call":
		return "web_search_history"
	case "image_generation_call":
		return "image_history"
	default:
		return "unknown_item"
	}
}
