package openai

import "github.com/tidwall/gjson"

// ResponseContentKind classifies actual nonempty Responses content. It is an
// observation only: it must not change legacy TTFT, failover or stream flushing.
// Reasoning text and tool input are semantic content, distinct from visible text.
// Lifecycle metadata, empty/null/object deltas and encrypted reasoning are not
// content. Whitespace inside a real text delta remains a content character.
func ResponseContentKind(message []byte, eventType string) string {
	field, kind := "", ""
	switch eventType {
	case "response.output_text.delta", "response.refusal.delta", "response.audio_transcript.delta", "response.output_audio_transcript.delta":
		field, kind = "delta", "visible_text"
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		field, kind = "delta", "reasoning_text"
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		field, kind = "delta", "tool_input"
	case "response.output_text.done", "response.audio_transcript.done", "response.output_audio_transcript.done":
		field, kind = "text", "visible_text"
	case "response.refusal.done":
		field, kind = "refusal", "visible_text"
	case "response.reasoning_text.done", "response.reasoning_summary_text.done":
		field, kind = "text", "reasoning_text"
	case "response.function_call_arguments.done":
		field, kind = "arguments", "tool_input"
	case "response.custom_tool_call_input.done":
		field, kind = "input", "tool_input"
	case "response.audio.delta", "response.output_audio.delta":
		field, kind = "delta", "media"
	case "response.image_generation_call.partial_image":
		field, kind = "partial_image_b64", "media"
	case "response.content_part.added", "response.content_part.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		return responsePartContentKind(gjson.GetBytes(message, "part"))
	case "response.output_item.added", "response.output_item.done":
		return responseItemContentKind(gjson.GetBytes(message, "item"))
	case "response.completed", "response.done":
		for _, item := range gjson.GetBytes(message, "response.output").Array() {
			if k := responseItemContentKind(item); k != "" {
				return k
			}
		}
	}
	if field != "" && responseNonemptyString(gjson.GetBytes(message, field)) {
		return kind
	}
	return ""
}
func responseNonemptyString(v gjson.Result) bool { return v.Type == gjson.String && v.Str != "" }
func responsePartContentKind(part gjson.Result) string {
	switch part.Get("type").String() {
	case "output_text", "text":
		if responseNonemptyString(part.Get("text")) {
			return "visible_text"
		}
	case "refusal":
		if responseNonemptyString(part.Get("refusal")) {
			return "visible_text"
		}
	case "summary_text", "reasoning_text":
		if responseNonemptyString(part.Get("text")) {
			return "reasoning_text"
		}
	case "audio":
		if responseNonemptyString(part.Get("data")) {
			return "media"
		}
	}
	return ""
}
func responseItemContentKind(item gjson.Result) string {
	switch item.Get("type").String() {
	case "function_call":
		if responseNonemptyString(item.Get("arguments")) {
			return "tool_input"
		}
	case "custom_tool_call":
		if responseNonemptyString(item.Get("input")) {
			return "tool_input"
		}
	case "image_generation_call":
		if responseNonemptyString(item.Get("result")) {
			return "media"
		}
	case "message":
		for _, part := range item.Get("content").Array() {
			if k := responsePartContentKind(part); k != "" {
				return k
			}
		}
	case "reasoning":
		for _, part := range item.Get("summary").Array() {
			if responsePartContentKind(part) == "reasoning_text" {
				return "reasoning_text"
			}
		}
	}
	return ""
}
