package handler

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Codex desktop emits page context as synthetic tool outputs without a matching
// call. Normalize only those context records; ordinary tool results must retain
// their original call IDs and still pass the regular Responses validation.
func normalizeCodexPageContext(body []byte) ([]byte, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false
	}
	type replacement struct {
		index int
		body  []byte
	}
	var replacements []replacement
	for i, item := range input.Array() {
		if !item.IsObject() || item.Get("type").String() != "function_call_output" || !isCodexPageContextName(item.Get("name").String()) {
			continue
		}
		if namespace := item.Get("namespace"); namespace.Exists() && (namespace.Type != gjson.String || namespace.String() != "agent") {
			continue
		}
		if callID := item.Get("call_id"); callID.Exists() && (callID.Type != gjson.String || strings.TrimSpace(callID.String()) != "") {
			continue
		}
		output := item.Get("output")
		if output.Type != gjson.String {
			continue
		}
		message, err := json.Marshal(map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": output.String()}},
		})
		if err != nil {
			return body, false
		}
		replacements = append(replacements, replacement{index: i, body: message})
	}
	if len(replacements) == 0 || !hasUniqueJSONMembers(body) {
		return body, false
	}
	normalized := body
	for _, item := range replacements {
		var err error
		normalized, err = sjson.SetRawBytes(normalized, "input."+strconv.Itoa(item.index), item.body)
		if err != nil {
			return body, false
		}
	}
	return normalized, true
}

var codexSelectedPagePartName = regexp.MustCompile(`^codex_apps_selected_page_part_([1-9][0-9]*)_of_([1-9][0-9]*)$`)

func isCodexPageContextName(name string) bool {
	if name == "codex_apps_open_page" {
		return true
	}
	parts := codexSelectedPagePartName.FindStringSubmatch(name)
	if parts == nil {
		return false
	}
	index, indexErr := strconv.Atoi(parts[1])
	total, totalErr := strconv.Atoi(parts[2])
	return indexErr == nil && totalErr == nil && index <= total
}
