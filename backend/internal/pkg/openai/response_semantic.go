package openai

import "github.com/tidwall/gjson"

// ResponseStartsSemanticOutput observes reasoning activity or nonempty content.
// It is only a timing endpoint; callers keep their existing retry/flush guards.
func ResponseStartsSemanticOutput(message []byte, eventType string) bool {
	if !gjson.ValidBytes(message) {
		return false
	}
	switch eventType {
	case "response.output_item.added", "response.output_item.done":
		if gjson.GetBytes(message, "item.type").String() == "reasoning" {
			return true
		}
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done", "response.content_part.added", "response.content_part.done":
		switch gjson.GetBytes(message, "part.type").String() {
		case "summary_text", "reasoning_text":
			return true
		}
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return false
	}
	return ResponseContentKind(message, eventType) != ""
}
