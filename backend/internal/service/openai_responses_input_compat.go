package service

import (
	"bytes"
	"encoding/json"
	"strings"
)

const openAIResponsesInputTextMaxChars = 10000000

// Only reject the expensive decode when all recognized output types contain
// the common marker. If a future type does not, preserve the full path.
func openAIResponsesMayNeedOrphanCleanup(body []byte) bool {
	return openAIResponsesMayNeedOrphanCleanupWithTypes(body, codexToolCallOutputItemTypes[:])
}

func openAIResponsesMayNeedOrphanCleanupWithTypes(body []byte, outputTypes []string) bool {
	if bytes.Contains(body, []byte("_output")) || bytes.Contains(body, []byte(`\u`)) {
		return true
	}
	for _, outputType := range outputTypes {
		if !strings.Contains(outputType, "_output") {
			return true
		}
	}
	return !json.Valid(body)
}

// sanitizeOpenAIResponsesOrphanToolOutputs removes tool-output items that have
// no matching call or item reference anywhere in the current input. Named
// function outputs without a call ID are standalone inputs, not orphan results.
func sanitizeOpenAIResponsesOrphanToolOutputs(reqBody map[string]any, input []any, hasPreviousResponseID bool) bool {
	if len(input) == 0 || hasPreviousResponseID {
		return false
	}

	// In a paired history, only one of each two items provides a context ID.
	contextIDs := make(map[string]struct{}, len(input)/2)
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemType := firstNonEmptyString(item["type"])
		if itemType == "item_reference" {
			if id := firstNonEmptyString(item["id"]); id != "" {
				contextIDs[id] = struct{}{}
			}
			continue
		}
		if !isCodexToolCallContextItemType(itemType) {
			continue
		}
		if id := firstNonEmptyString(item["call_id"], item["id"]); id != "" {
			contextIDs[id] = struct{}{}
		}
	}

	var normalized []any
	keepFrom := 0
	for i, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		itemType := firstNonEmptyString(item["type"])
		if !isCodexToolCallOutputItemType(itemType) {
			continue
		}

		callID := firstNonEmptyString(item["call_id"])
		// Codex sends externally supplied inputs (such as task delegation) as
		// named function outputs without a preceding function call. Preserve
		// their native type, namespace and output instead of silently dropping
		// the request that started the turn.
		if callID == "" && itemType == "function_call_output" && firstNonEmptyString(item["name"]) != "" {
			continue
		}
		if _, matched := contextIDs[callID]; matched {
			continue
		}

		if normalized == nil {
			normalized = make([]any, 0, len(input)-1)
		}
		normalized = append(normalized, input[keepFrom:i]...)
		keepFrom = i + 1
	}
	if normalized == nil {
		return false
	}
	reqBody["input"] = append(normalized, input[keepFrom:]...)
	return true
}

func truncateOpenAIResponsesInputText(_ map[string]any) bool {
	// Do not silently rewrite client or tool output. If an upstream enforces a
	// text limit, forwarding the original value preserves its explicit error for
	// the client and the normal Ops error pipeline. This compatibility shim is
	// retained until the two callers can remove the old mutation hook together.
	return false
}

func openAIResponsesInputMayNeedTruncation(_ []byte) bool {
	// See truncateOpenAIResponsesInputText. Returning false also avoids decoding
	// very large bodies solely for a mutation that must not happen.
	return false
}
