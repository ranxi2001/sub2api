package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
)

// JSON Lines of native arrays: SDK string accumulation preserves every field
// without recursively merging nested arrays, signatures or closed flags.
// Requests also accept the legacy native-array extension.
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
			_, _ = text.WriteString(value)
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
					_, _ = reasoning.WriteString(value)
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
			var frame bytes.Buffer
			if json.Compact(&frame, content) != nil {
				return nil, errMistralContent
			}
			msg[mistralContentField], _ = json.Marshal(frame.String() + "\n")
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
	// Match the existing nested-before-flat request/usage precedence, then
	// remove the Responses-style object that native Mistral Chat cannot accept.
	if raw, exists := root["reasoning"]; exists {
		var reasoning mistralObject
		if json.Unmarshal(raw, &reasoning) == nil {
			if effort, ok := mistralString(reasoning["effort"]); ok && strings.TrimSpace(effort) != "" {
				root["reasoning_effort"], _ = json.Marshal(strings.ToLower(strings.TrimSpace(effort)))
			}
		}
		delete(root, "reasoning")
		changed = true
	}
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
	// Map the effort onto the native vocabulary of the final upstream model.
	// Only families with a confirmed Mistral allow-list are rewritten (GLM and
	// DeepSeek); see mistralReasoningEffortFamilyFor / mapMistralReasoningEffort.
	if applyMistralReasoningEffort(root, model) {
		changed = true
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
			restored, err := restoreMistralHistoryContent(content, saved)
			if err != nil {
				return nil, err
			}
			msg["content"] = restored
			native = true
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

// Decode whole native frames, preserving their order and opaque fields. They
// are replayed as separate blocks; generic SDK delta merging is never applied.
func restoreMistralHistoryContent(content, saved json.RawMessage) (json.RawMessage, error) {
	var blocks []json.RawMessage
	appendFrame := func(frame json.RawMessage) error {
		if _, _, err := mistralBlocks(frame, false); err != nil {
			return err
		}
		var parts []json.RawMessage
		if json.Unmarshal(frame, &parts) != nil {
			return errMistralContent
		}
		blocks = append(blocks, parts...)
		return nil
	}
	if encoded, ok := mistralString(saved); ok {
		frames := 0
		for _, line := range strings.Split(encoded, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if err := appendFrame(json.RawMessage(line)); err != nil {
				return nil, err
			}
			frames++
		}
		if frames == 0 {
			return nil, errMistralContent
		}
	} else if err := appendFrame(saved); err != nil {
		return nil, err
	}
	if blocks == nil {
		blocks = []json.RawMessage{}
	}
	native, _ := json.Marshal(blocks)
	nativeText, _, err := mistralBlocks(native, false)
	if err != nil {
		return nil, err
	}
	if len(content) > 0 && content[0] == '[' {
		// Two different native histories have no safe implicit precedence.
		var current, restored any
		currentDecoder := json.NewDecoder(bytes.NewReader(content))
		currentDecoder.UseNumber()
		restoredDecoder := json.NewDecoder(bytes.NewReader(native))
		restoredDecoder.UseNumber()
		if currentDecoder.Decode(&current) != nil || restoredDecoder.Decode(&restored) != nil || !reflect.DeepEqual(current, restored) {
			return nil, errMistralContent
		}
		return content, nil
	}
	if len(content) == 0 || bytes.Equal(content, []byte("null")) {
		return native, nil
	}
	text, ok := mistralString(content)
	if !ok {
		return nil, errMistralContent
	}
	if text == "" {
		return native, nil
	}
	if !strings.HasPrefix(text, nativeText) {
		return nil, errMistralContent
	}
	// The official stream switches from arrays to plain strings. Append only
	// the answer suffix absent from the saved arrays, keeping text references.
	if suffix := strings.TrimPrefix(text, nativeText); suffix != "" {
		block, _ := json.Marshal(map[string]string{"type": "text", "text": suffix})
		blocks = append(blocks, block)
		return json.Marshal(blocks)
	}
	return native, nil
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
			_, _ = text.WriteString(value)
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

// mistralReasoningEffortFamily identifies a Mistral-hosted model family whose
// reasoning_effort vocabulary has been confirmed against the live upstream.
type mistralReasoningEffortFamily int

const (
	mistralEffortFamilyNone mistralReasoningEffortFamily = iota
	// GLM on Mistral (zai-glm-5-3, zai-glm-5-3-flash): native scale low/high/max.
	mistralEffortFamilyGLM
	// DeepSeek on Mistral (e.g. deepseek-v4-1-flash-1017). Upstream 400:
	// "reasoning_effort 'medium' is not supported for this model; supported
	// values: ['none', 'low', 'high', 'xhigh', 'max']".
	mistralEffortFamilyDeepSeek
)

// mistralReasoningEffortFamilyFor classifies the final (post model-mapping)
// upstream model. GLM stays exact-match (ds.2/ds.3 behavior; aliases such as
// zai-glm-5-3-latest are deliberately untouched). Every DeepSeek-named model
// routed to platform=mistral shares one allow-list.
func mistralReasoningEffortFamilyFor(model string) mistralReasoningEffortFamily {
	normalized := strings.TrimSpace(model)
	if normalized == "zai-glm-5-3" || normalized == "zai-glm-5-3-flash" {
		return mistralEffortFamilyGLM
	}
	lower := strings.ToLower(normalized)
	if strings.HasPrefix(lower, "deepseek-") || strings.HasPrefix(lower, "deepseek_") {
		return mistralEffortFamilyDeepSeek
	}
	return mistralEffortFamilyNone
}

// canonicalMistralEffortToken lower-cases and strips separators so that
// "Medium", "x-high", "extra_high" and "X High" compare equal.
func canonicalMistralEffortToken(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
}

// mapMistralReasoningEffort maps one client effort string onto the family's
// native vocabulary. It returns (value, keep); keep=false means the field must
// be removed so the upstream applies its own default instead of answering 400.
//
// DeepSeek on Mistral (allow-list none/low/high/xhigh/max):
//
//	none, low, high, xhigh, max   -> unchanged (canonical lower-case spelling)
//	minimal                       -> low   (same as the GLM table)
//	medium                        -> high  (next supported level; never downgrade)
//	extrahigh / x-high / x_high   -> xhigh
//	ultra                         -> max
//	off / disabled                -> none
//	"" / auto / default / unknown -> removed (upstream default)
//
// GLM on Mistral keeps its ds.2 table (none/minimal->low, medium->high,
// xhigh->max) and keeps forwarding unknown values unchanged.
func mapMistralReasoningEffort(family mistralReasoningEffortFamily, raw string) (string, bool) {
	token := canonicalMistralEffortToken(raw)
	switch family {
	case mistralEffortFamilyGLM:
		switch token {
		case "none", "minimal", "low":
			return "low", true
		case "medium", "high":
			return "high", true
		case "xhigh", "extrahigh", "max":
			return "max", true
		default:
			return raw, true
		}
	case mistralEffortFamilyDeepSeek:
		switch token {
		case "none", "off", "disabled":
			return "none", true
		case "minimal", "low":
			return "low", true
		case "medium", "high":
			return "high", true
		case "xhigh", "extrahigh":
			return "xhigh", true
		case "max", "ultra":
			return "max", true
		default:
			return "", false
		}
	default:
		return raw, true
	}
}

// applyMistralReasoningEffort rewrites root["reasoning_effort"] in place for
// model families with a confirmed Mistral allow-list. JSON null is preserved
// (explicit omission). For DeepSeek, non-string values (numbers, bools,
// objects) can never be valid and are removed instead of being forwarded into
// a 400/422; GLM keeps its ds.2 behavior of preserving them. Returns whether
// root changed.
func applyMistralReasoningEffort(root mistralObject, model string) bool {
	family := mistralReasoningEffortFamilyFor(model)
	if family == mistralEffortFamilyNone {
		return false
	}
	raw, exists := root["reasoning_effort"]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	effort, isString := mistralString(raw)
	if !isString {
		if family == mistralEffortFamilyDeepSeek {
			delete(root, "reasoning_effort")
			return true
		}
		return false
	}
	mapped, keep := mapMistralReasoningEffort(family, effort)
	if !keep {
		delete(root, "reasoning_effort")
		return true
	}
	if mapped == effort {
		return false
	}
	root["reasoning_effort"], _ = json.Marshal(mapped)
	return true
}

// enforceMistralChatReasoningEffort is the last-mile guard at the shared CC
// send point (sendCCUpstreamRequest): whatever ran after
// normalizeMistralChatRequest, a Mistral body never leaves with an effort
// outside the model's allow-list. The mapping is idempotent, so bodies that
// were already normalized are returned byte-for-byte.
func enforceMistralChatReasoningEffort(account *Account, body []byte) []byte {
	if account == nil || account.Platform != PlatformMistral || len(body) == 0 {
		return body
	}
	var root mistralObject
	if json.Unmarshal(body, &root) != nil || root == nil {
		return body
	}
	model, _ := mistralString(root["model"])
	if !applyMistralReasoningEffort(root, model) {
		return body
	}
	out, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return out
}
