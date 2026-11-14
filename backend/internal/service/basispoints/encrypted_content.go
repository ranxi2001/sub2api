package basispoints

import (
	"encoding/json"
	"fmt"
)

const encryptedContentOmitted = "[Encrypted content omitted: only the native Codex channel can read it, so it cannot be forwarded through Excel / BPS.]"

// StripEncryptedContent replaces encrypted message and tool-result parts with a
// fixed notice before validation. Codex multi-agent histories carry sub-agent
// payloads this way, and a client resends them on every turn, so an old
// conversation would otherwise fail permanently. Each part keeps its position,
// so a preceding sub-agent header still introduces a truthful marker. Reasoning
// items keep their existing handling; tool arguments and text are not inspected.
func StripEncryptedContent(raw []byte) ([]byte, error) {
	var source object
	if err := decode(raw, &source); err != nil || source == nil {
		return nil, fmt.Errorf("invalid Basispoints request JSON")
	}
	if err := validateNewAgentMessage(source["input"]); err != nil {
		return nil, err
	}
	input, _ := source["input"].([]any)
	current := lastMeaningfulInput(input)
	changed := false
	for itemIndex, rawItem := range input {
		item, _ := rawItem.(object)
		field := "content"
		switch text(item["type"]) {
		case "", "message", "agent_message":
		case "function_call_output", "custom_tool_call_output":
			field = "output"
		default:
			continue
		}
		if text(item["type"]) == "agent_message" {
			if value, exists := item["encrypted_content"]; exists && value != nil && value != "" {
				if itemIndex == current {
					return nil, currentEncryptedInputError(fmt.Sprintf("input[%d].encrypted_content", itemIndex))
				}
				marker := object{"type": "input_text", "text": encryptedContentOmitted}
				switch content := item["content"].(type) {
				case []any:
					item["content"] = append(content, marker)
				case string:
					item["content"] = []any{object{"type": "input_text", "text": content}, marker}
				default:
					item["content"] = []any{marker}
				}
				delete(item, "encrypted_content")
				changed = true
			}
		}
		parts, ok := item[field].([]any)
		if !ok {
			continue
		}
		kind := "input_text"
		if field == "content" && text(item["role"]) == "assistant" {
			kind = "output_text"
		}
		for index, rawPart := range parts {
			if part, _ := rawPart.(object); text(part["type"]) == "encrypted_content" {
				if itemIndex == current {
					return nil, currentEncryptedInputError(fmt.Sprintf("input[%d].%s[%d]", itemIndex, field, index))
				}
				parts[index] = object{"type": kind, "text": encryptedContentOmitted}
				changed = true
			}
		}
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(source)
}

func currentEncryptedInputError(path string) error {
	return &ContentValidationError{Path: path, ContentType: "encrypted_content", message: "basispoints cannot omit encrypted content from the current task; resend the original plaintext"}
}
