package handler

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Codex Desktop injects Companion Page context as agent function_call_output
// items without a corresponding model call. Preserve their text as user context
// before ordinary tool-output validation, including when replaying tool history.
// Unrecognized outputs and items carrying a call identity remain untouched.
func normalizeCodexPageContext(body []byte) ([]byte, bool) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false
	}
	hasContext := false
	input.ForEach(func(_, item gjson.Result) bool {
		hasContext = isCodexPageContext(item)
		return !hasContext
	})
	if !hasContext || !hasUniqueJSONMembers(body) {
		return body, false
	}
	var items []json.RawMessage
	changed := false
	input.ForEach(func(_, item gjson.Result) bool {
		if !isCodexPageContext(item) {
			items = append(items, json.RawMessage(item.Raw))
			return true
		}
		message, err := json.Marshal(map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": item.Get("output").String(),
			}},
		})
		if err != nil {
			items = append(items, json.RawMessage(item.Raw))
			return true
		}
		items = append(items, message)
		changed = true
		return true
	})
	if !changed {
		return body, false
	}
	// Raw input items and the untouched request fields keep their exact numbers.
	normalized, err := sjson.SetBytes(body, "input", items)
	if err != nil {
		return body, false
	}
	return normalized, true
}

func isCodexPageContext(item gjson.Result) bool {
	if item.Get("type").String() != "function_call_output" || item.Get("namespace").String() != "agent" {
		return false
	}
	for _, field := range []string{"call_id", "tool_call_id"} {
		id := item.Get(field)
		if id.Exists() && (id.Type != gjson.String || strings.TrimSpace(id.String()) != "") {
			return false
		}
	}
	output := item.Get("output")
	if output.Type != gjson.String {
		return false
	}
	name := item.Get("name").String()
	if name == "codex_apps_open_page" {
		page := gjson.Parse(output.String())
		pageID := page.Get("page_id")
		return page.IsObject() && hasUniqueJSONMembers([]byte(output.String())) && pageID.Exists() &&
			(pageID.Type == gjson.Null || (pageID.Type == gjson.String && strings.TrimSpace(pageID.String()) != ""))
	}
	const prefix = "codex_apps_selected_page_part_"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	part, total, ok := strings.Cut(strings.TrimPrefix(name, prefix), "_of_")
	if !ok {
		return false
	}
	partNumber, partErr := strconv.Atoi(part)
	totalNumber, totalErr := strconv.Atoi(total)
	if partErr != nil || totalErr != nil || partNumber < 1 || partNumber > totalNumber ||
		strconv.Itoa(partNumber) != part || strconv.Itoa(totalNumber) != total {
		return false
	}
	// Snapshot parts may split a JSON document, so validate the matching envelope
	// rather than requiring each part to be independently valid JSON.
	header := "Companion Page snapshot part " + part + " of " + total + ":\n"
	return strings.HasPrefix(output.String(), header) && strings.TrimSpace(strings.TrimPrefix(output.String(), header)) != ""
}
