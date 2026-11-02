package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// Preserve native signatures and references for assistant history replay.
const mistralContentField = "mistral_content"

var errMistralContent = errors.New("invalid or unsupported Mistral chat content")

type mistralObject map[string]json.RawMessage

func mistralString(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	err := json.Unmarshal(raw, &s)
	return s, err == nil
}

func mistralBlocks(raw json.RawMessage, bridge bool) (string, string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return "", "", errMistralContent
	}
	var blocks []mistralObject
	if json.Unmarshal(raw, &blocks) != nil {
		return "", "", errMistralContent
	}
	var text, reasoning strings.Builder
	for _, block := range blocks {
		typ, _ := mistralString(block["type"])
		switch typ {
		case "text":
			value, ok := mistralString(block["text"])
			if !ok {
				return "", "", errMistralContent
			}
			text.WriteString(value)
		case "thinking":
			var parts []mistralObject
			if len(block["thinking"]) == 0 || json.Unmarshal(block["thinking"], &parts) != nil {
				return "", "", errMistralContent
			}
			for _, part := range parts {
				kind, _ := mistralString(part["type"])
				switch kind {
				case "text":
					value, ok := mistralString(part["text"])
					if !ok {
						return "", "", errMistralContent
					}
					reasoning.WriteString(value)
				case "reference", "tool_reference":
					if bridge {
						return "", "", errMistralContent
					}
				default:
					return "", "", errMistralContent
				}
			}
		case "reference", "tool_reference":
			if bridge {
				return "", "", errMistralContent
			}
		default:
			return "", "", errMistralContent
		}
	}
	return text.String(), reasoning.String(), nil
}

func normalizeMistralChatResponse(account *Account, body []byte, bridge bool) ([]byte, error) {
	if account == nil || account.Platform != PlatformMistral {
		return body, nil
	}
	if bytes.Equal(bytes.TrimSpace(body), []byte("[DONE]")) {
		return body, nil
	}
	var root mistralObject
	if json.Unmarshal(body, &root) != nil || root == nil {
		return nil, errMistralContent
	}
	var choices []mistralObject
	if raw, ok := root["choices"]; ok && json.Unmarshal(raw, &choices) != nil {
		return nil, errMistralContent
	}
	changed := false
	for _, choice := range choices {
		for _, field := range []string{"message", "delta"} {
			raw, exists := choice[field]
			if !exists {
				continue
			}
			var msg mistralObject
			if json.Unmarshal(raw, &msg) != nil || msg == nil {
				return nil, errMistralContent
			}
			content := bytes.TrimSpace(msg["content"])
			if len(content) == 0 || bytes.Equal(content, []byte("null")) {
				continue
			}
			if _, ok := mistralString(content); ok {
				continue
			}
			if content[0] != '[' {
				return nil, errMistralContent
			}
			text, reasoning, err := mistralBlocks(content, bridge)
			if err != nil {
				return nil, err
			}
			// A pre-existing extension or reasoning field must not be overwritten ambiguously.
			if _, ok := msg[mistralContentField]; ok {
				return nil, errMistralContent
			}
			msg[mistralContentField] = msg["content"]
			msg["content"], _ = json.Marshal(text)
			if reasoning != "" {
				if existing, ok := msg["reasoning_content"]; ok && !bytes.Equal(bytes.TrimSpace(existing), []byte("null")) {
					value, valid := mistralString(existing)
					if !valid || value != "" {
						return nil, errMistralContent
					}
				}
				msg["reasoning_content"], _ = json.Marshal(reasoning)
			}
			choice[field], _ = json.Marshal(msg)
			changed = true
		}
	}
	if !changed {
		return body, nil
	}
	root["choices"], _ = json.Marshal(choices)
	return json.Marshal(root)
}

func normalizeMistralChatSSELine(account *Account, line string) (string, error) {
	if account == nil || account.Platform != PlatformMistral {
		return line, nil
	}
	payload, ok := extractOpenAISSEDataLine(line)
	if !ok || strings.TrimSpace(payload) == "" {
		return line, nil
	}
	normalized, err := normalizeMistralChatResponse(account, []byte(payload), false)
	if err != nil {
		return "", err
	}
	if bytes.Equal(normalized, []byte(payload)) {
		return line, nil
	}
	return "data: " + string(normalized), nil
}

func normalizeMistralChatRequest(account *Account, body []byte) ([]byte, error) {
	if account == nil || account.Platform != PlatformMistral {
		return body, nil
	}
	var root mistralObject
	if json.Unmarshal(body, &root) != nil || root == nil {
		return nil, errMistralContent
	}
	var messages []mistralObject
	if json.Unmarshal(root["messages"], &messages) != nil {
		return nil, errMistralContent
	}
	changed := false
	model, _ := mistralString(root["model"])
	if _, explicit := root["reasoning_effort"]; !explicit {
		var thinking mistralObject
		_ = json.Unmarshal(root["thinking"], &thinking)
		kind, _ := mistralString(thinking["type"])
		if kind == "disabled" || bytes.Equal(bytes.TrimSpace(root["enable_thinking"]), []byte("false")) {
			root["reasoning_effort"] = json.RawMessage(`"none"`)
			changed = true
		}
	}
	// Only the confirmed final upstream model has this effort vocabulary.
	if model == "zai-glm-5-3" {
		effort, _ := mistralString(root["reasoning_effort"])
		mapped := ""
		switch effort {
		case "none", "minimal":
			mapped = "low"
		case "medium":
			mapped = "high"
		case "xhigh":
			mapped = "max"
		}
		if mapped != "" {
			root["reasoning_effort"], _ = json.Marshal(mapped)
			changed = true
		}
	}
	for _, key := range []string{"store", "thinking", "enable_thinking"} {
		if _, exists := root[key]; exists {
			delete(root, key)
			changed = true
		}
	}
	for _, msg := range messages {
		role, _ := mistralString(msg["role"])
		if role != "assistant" {
			continue
		}
		content := bytes.TrimSpace(msg["content"])
		native := len(content) > 0 && content[0] == '['
		if saved, ok := msg[mistralContentField]; ok {
			if !native {
				if _, _, err := mistralBlocks(saved, false); err != nil {
					return nil, err
				}
				msg["content"] = saved
				native = true
			}
			delete(msg, mistralContentField)
			changed = true
		}
		rawReasoning, hasReasoning := msg["reasoning_content"]
		if !hasReasoning {
			continue
		}
		delete(msg, "reasoning_content")
		changed = true
		var nativeBlocks []json.RawMessage
		if native {
			if json.Unmarshal(msg["content"], &nativeBlocks) != nil {
				return nil, errMistralContent
			}
			hasThinking := false
			for _, rawBlock := range nativeBlocks {
				var block mistralObject
				if json.Unmarshal(rawBlock, &block) != nil {
					return nil, errMistralContent
				}
				kind, _ := mistralString(block["type"])
				hasThinking = hasThinking || kind == "thinking"
			}
			if hasThinking {
				continue
			}
		}
		reasoning, ok := mistralString(rawReasoning)
		if !ok && !bytes.Equal(bytes.TrimSpace(rawReasoning), []byte("null")) {
			return nil, errMistralContent
		}
		if reasoning == "" {
			continue
		}
		blocks := []any{map[string]any{"type": "thinking", "thinking": []any{map[string]any{"type": "text", "text": reasoning}}}}
		if native {
			for _, block := range nativeBlocks {
				blocks = append(blocks, block)
			}
		} else if len(content) > 0 && !bytes.Equal(content, []byte("null")) {
			text, valid := mistralString(content)
			if !valid {
				return nil, errMistralContent
			}
			blocks = append(blocks, map[string]any{"type": "text", "text": text})
		}
		msg["content"], _ = json.Marshal(blocks)
	}
	// Clean after restoring native history so saved thinking blocks are covered too.
	for _, msg := range messages {
		if msg == nil {
			return nil, errMistralContent
		}
		messageChanged := removeMistralCacheControl(msg)
		content := bytes.TrimSpace(msg["content"])
		if len(content) > 0 && content[0] == '[' {
			cleaned, contentChanged, text, textOnly, err := cleanMistralRequestBlocks(content)
			if err != nil {
				return nil, err
			}
			role, _ := mistralString(msg["role"])
			if (messageChanged || contentChanged) && textOnly && (role == "user" || role == "system" || role == "tool") {
				msg["content"], _ = json.Marshal(text)
			} else if contentChanged {
				msg["content"] = cleaned
			}
			messageChanged = messageChanged || contentChanged
		}
		changed = changed || messageChanged
	}
	if raw, exists := root["tools"]; exists {
		var tools []mistralObject
		if json.Unmarshal(raw, &tools) != nil {
			return nil, errMistralContent
		}
		toolsChanged := false
		for _, tool := range tools {
			if tool == nil {
				return nil, errMistralContent
			}
			toolsChanged = removeMistralCacheControl(tool) || toolsChanged
			if rawFunction, exists := tool["function"]; exists {
				var function mistralObject
				if json.Unmarshal(rawFunction, &function) != nil || function == nil {
					return nil, errMistralContent
				}
				// Do not traverse parameters: cache_control may be a schema property.
				if removeMistralCacheControl(function) {
					tool["function"], _ = json.Marshal(function)
					toolsChanged = true
				}
			}
		}
		if toolsChanged {
			root["tools"], _ = json.Marshal(tools)
			changed = true
		}
	}
	if !changed {
		return body, nil
	}
	root["messages"], _ = json.Marshal(messages)
	return json.Marshal(root)
}

func removeMistralCacheControl(object mistralObject) bool {
	if _, exists := object["cache_control"]; exists {
		delete(object, "cache_control")
		return true
	}
	return false
}

// Only traverse content blocks; embedded payloads and tool schemas remain opaque.
func cleanMistralRequestBlocks(raw json.RawMessage) (json.RawMessage, bool, string, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil, false, "", false, errMistralContent
	}
	var blocks []mistralObject
	if json.Unmarshal(raw, &blocks) != nil {
		return nil, false, "", false, errMistralContent
	}
	changed, textOnly := false, len(blocks) > 0
	var text strings.Builder
	for _, block := range blocks {
		if block == nil {
			return nil, false, "", false, errMistralContent
		}
		changed = removeMistralCacheControl(block) || changed
		kind, _ := mistralString(block["type"])
		if kind == "thinking" {
			cleaned, nestedChanged, _, _, err := cleanMistralRequestBlocks(block["thinking"])
			if err != nil {
				return nil, false, "", false, err
			}
			if nestedChanged {
				block["thinking"] = cleaned
				changed = true
			}
		}
		value, valid := mistralString(block["text"])
		if kind != "text" || !valid || len(block) != 2 {
			textOnly = false
		} else {
			text.WriteString(value)
		}
	}
	if !changed {
		return raw, false, text.String(), textOnly, nil
	}
	cleaned, err := json.Marshal(blocks)
	if err != nil {
		return nil, false, "", false, errMistralContent
	}
	return cleaned, true, text.String(), textOnly, nil
}
