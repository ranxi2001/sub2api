package service

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// PrepareOpenAIAgentReplayBody is a pre-snapshot stage, never a final-wire
// exemption. Only output metadata is portable here. Readable reasoning,
// ciphertext, tool relationships, array order and JSON numbers remain intact.
// The caller must forward these bytes and freeze them before account builders.
func (s *OpenAIGatewayService) PrepareOpenAIAgentReplayBody(body []byte) ([]byte, error) {
	if !s.preserveOpenAIAgentHistory(body) {
		return body, nil
	}
	if !utf8.Valid(body) || !openAIEncryptedJSONUnambiguous(body) {
		return nil, openAIAgentRecoveryError("wire_changed")
	}
	return normalizeOpenAIReplayMetadata(body)
}

// Shared with readable Beta preparation. This deliberately excludes OAuth
// system promotion, namespace flattening, timezone and call reconstruction.
func normalizeOpenAIReplayMetadata(body []byte) ([]byte, error) {
	clean, _, err := sanitizeOpenAIResponsesInputItemIDs(body)
	if err != nil {
		return nil, err
	}
	var source map[string]any
	if err := decodeOpenAIJSONUseNumber(clean, &source); err != nil {
		return nil, err
	}
	input, _ := source["input"].([]any)
	storeFalse := gjson.GetBytes(clean, "store").Type == gjson.False
	changed := false
	for _, value := range input {
		item, ok := value.(map[string]any)
		if !ok || strings.TrimSpace(firstNonEmptyString(item["type"])) != "reasoning" {
			continue
		}
		if _, exists := item["status"]; exists {
			delete(item, "status")
			changed = true
		}
		encrypted, _ := item["encrypted_content"].(string)
		if !storeFalse || strings.TrimSpace(encrypted) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(firstNonEmptyString(item["id"])), "rs_") {
			delete(item, "id")
			changed = true
		}
		if item["summary"] == nil {
			item["summary"] = []any{}
			changed = true
		}
	}
	if !changed {
		return clean, nil
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(clean, "input", encoded)
}
