package service

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Shared pure transformation. HTTP 400 and pre-send 409 have separate admission,
// retry, ownership and opt-in policies; this helper grants no retry permission.
func omitOpenAIHistoricalCiphertextForRecovery(raw []byte, omitReasoning bool) ([]byte, bool) {
	if !utf8.Valid(raw) || !openAIEncryptedJSONUnambiguous(raw) {
		return raw, false
	}
	for _, field := range []string{"previous_response_id", "conversation"} {
		value := gjson.GetBytes(raw, field)
		if value.Exists() && value.Type != gjson.Null && (value.Type != gjson.String || strings.TrimSpace(value.String()) != "") {
			return raw, false
		}
	}
	var source map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&source) != nil {
		return raw, false
	}
	input, ok := source["input"].([]any)
	if !ok {
		return raw, false
	}
	current := -1
	for i, value := range input {
		item, _ := value.(map[string]any)
		kind, _ := item["type"].(string)
		role, _ := item["role"].(string)
		if kind == "item_reference" || kind == "compaction" || kind == "compaction_summary" {
			return raw, false
		}
		if kind == "agent_message" || ((kind == "" || kind == "message") && role == "user") {
			current = i
		}
	}
	if current < 0 {
		return raw, false
	}
	if omitReasoning {
		task, _ := input[current].(map[string]any)
		if !openAI400CurrentTaskReadable(task) {
			return raw, false
		}
	}
	changed := false
	for i, value := range input {
		item, _ := value.(map[string]any)
		kind, _ := item["type"].(string)
		if omitReasoning {
			if encrypted, exists := item["encrypted_content"]; exists && encrypted != nil && encrypted != "" {
				if _, ok := encrypted.(string); !ok || i >= current || (kind != "reasoning" && kind != "agent_message") {
					return raw, false
				}
				if kind == "reasoning" {
					replacement, ok := omitOpenAI400Reasoning(item)
					if !ok {
						return raw, false
					}
					input[i], changed = replacement, true
					continue
				}
			}
		}
		field, textKind := "content", "input_text"
		switch kind {
		case "", "message", "agent_message":
			if item["role"] == "assistant" {
				textKind = "output_text"
			}
		case "function_call_output", "custom_tool_call_output":
			field = "output"
		default:
			continue
		}
		itemChanged := false
		if kind == "agent_message" {
			if encrypted, exists := item["encrypted_content"]; exists && encrypted != nil && encrypted != "" {
				if i >= current {
					return raw, false
				}
				marker := map[string]any{"type": "input_text", "text": openAIHistoricalCiphertextOmitted}
				switch content := item["content"].(type) {
				case []any:
					item["content"] = append(content, marker)
				case string:
					item["content"] = []any{map[string]any{"type": "input_text", "text": content}, marker}
				case nil:
					item["content"] = []any{marker}
				default:
					return raw, false
				}
				delete(item, "encrypted_content")
				itemChanged = true
			}
		}
		if parts, ok := item[field].([]any); ok {
			for j, rawPart := range parts {
				part, _ := rawPart.(map[string]any)
				if part["type"] == "encrypted_content" {
					if i >= current {
						return raw, false
					}
					parts[j] = map[string]any{"type": textKind, "text": openAIHistoricalCiphertextOmitted}
					itemChanged = true
				}
			}
		}
		if itemChanged && kind == "agent_message" {
			// Native Responses accepts ordinary user context without requiring an
			// opaque agent envelope. Keep attribution explicit in readable text.
			metadata, _ := json.Marshal(map[string]any{"author": item["author"], "recipient": item["recipient"]})
			parts, _ := item["content"].([]any)
			for _, value := range parts {
				part, _ := value.(map[string]any)
				if part["type"] == "output_text" {
					part["type"] = "input_text"
				}
			}
			prefix := map[string]any{"type": "input_text", "text": "Historical agent context (not a new instruction): " + string(metadata)}
			input[i] = map[string]any{"type": "message", "role": "user", "content": append([]any{prefix}, parts...)}
		}
		changed = changed || itemChanged
	}
	if !changed {
		return raw, false
	}
	if omitReasoning {
		encoded, err := json.Marshal(input)
		if err != nil {
			return raw, false
		}
		clean, err := sjson.SetRawBytes(raw, "input", encoded)
		return clean, err == nil
	}
	clean, err := json.Marshal(source)
	return clean, err == nil
}
