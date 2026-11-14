package basispoints

import (
	"fmt"
	"strconv"
	"strings"
)

type searchTextKey struct {
	index, part int
	field       string
}
type searchTextObserver struct {
	state    *searchOutputStream
	offset   int
	buffered bool
	deltas   map[searchTextKey]*strings.Builder
	ids      map[int]string
	pending  map[string]bool
}

func newSearchTextObserver(state *searchOutputStream, offset int, buffered bool) *searchTextObserver {
	return &searchTextObserver{state: state, offset: offset, buffered: buffered, deltas: map[searchTextKey]*strings.Builder{}, ids: map[int]string{}, pending: map[string]bool{}}
}
func searchIndex(value any) (int, error) {
	n, err := strconv.Atoi(fmt.Sprint(value))
	if err != nil || n < 0 || n > 65535 {
		return 0, searchRequestError("invalid_output_index")
	}
	return n, nil
}
func (o *searchTextObserver) observe(kind string, payload object) error {
	if response, ok := payload["response"].(object); ok {
		if o.state.id == "" {
			o.state.id = text(response["id"])
			o.state.model = text(response["model"])
			if o.state.id != "" {
				if err := o.state.emit("response.created", object{"response": object{"id": o.state.id, "model": o.state.model, "status": "in_progress", "output": []any{}}}); err != nil {
					return err
				}
			}
		}
		if usage, ok := response["usage"].(object); ok && o.state.id != "" {
			return o.state.emit("response.in_progress", object{"response": object{"id": o.state.id, "model": o.state.model, "status": "in_progress", "output": []any{}, "usage": addRepairUsage(o.state.usage, usage)}})
		}
		return nil
	}
	item, _ := payload["item"].(object)
	if isTool(item) {
		if kind == "response.output_item.done" {
			if len(o.pending) >= 1024 {
				return searchRequestError("too_many_tool_items")
			}
			o.pending[text(item["call_id"])+"\x00"+text(item["id"])] = true
		}
		return nil
	}
	if isToolEvent(kind) {
		return nil
	}
	supported := strings.HasPrefix(kind, "response.output_text.") || strings.HasPrefix(kind, "response.refusal.") || strings.HasPrefix(kind, "response.content_part.") || strings.HasPrefix(kind, "response.reasoning_summary_") || kind == "response.output_item.added" || kind == "response.output_item.done"
	if !supported {
		return nil
	}
	index, err := searchIndex(payload["output_index"])
	if err != nil {
		return err
	}
	id := text(payload["item_id"])
	if item != nil {
		id = text(item["id"])
	}
	if id == "" {
		return searchRequestError("missing_item_id")
	}
	if before, ok := o.ids[index]; ok && before != id {
		return searchRequestError("item_identity_changed")
	}
	o.ids[index] = id
	if strings.HasSuffix(kind, ".delta") {
		field, partField := "text", "content_index"
		if kind == "response.reasoning_summary_text.delta" {
			field, partField = "summary", "summary_index"
		}
		if kind == "response.refusal.delta" {
			field = "refusal"
		}
		part, err := searchIndex(payload[partField])
		if err != nil {
			return err
		}
		delta, ok := payload["delta"].(string)
		if !ok {
			return searchRequestError("invalid_text_delta")
		}
		key := searchTextKey{index: index, part: part, field: field}
		builder := o.deltas[key]
		if builder == nil {
			builder = new(strings.Builder)
			o.deltas[key] = builder
		}
		if builder.Len()+len(delta) > 16<<20 {
			return searchRequestError("text_too_large")
		}
		_, _ = builder.WriteString(delta)
	}
	if o.buffered {
		return nil
	}
	payload["output_index"] = o.offset + index
	return o.state.emit(kind, payload)
}
func (o *searchTextObserver) validate(response object) error {
	output, _ := response["output"].([]any)
	for _, raw := range output {
		item, _ := raw.(object)
		if isTool(item) {
			delete(o.pending, text(item["call_id"])+"\x00"+text(item["id"]))
		}
	}
	if len(o.pending) > 0 {
		return searchRequestError("missing_terminal_tool")
	}
	for index, id := range o.ids {
		if index >= len(output) {
			return searchRequestError("missing_terminal_item")
		}
		item, _ := output[index].(object)
		if text(item["id"]) != id {
			return searchRequestError("terminal_identity_changed")
		}
	}
	for key, value := range o.deltas {
		if key.index >= len(output) {
			return searchRequestError("missing_terminal_item")
		}
		item, _ := output[key.index].(object)
		field := "content"
		if key.field == "summary" {
			field = "summary"
		}
		parts, _ := item[field].([]any)
		if key.part >= len(parts) {
			return searchRequestError("missing_terminal_text")
		}
		part, _ := parts[key.part].(object)
		textField := "text"
		if key.field == "refusal" {
			textField = "refusal"
		}
		if text(part[textField]) != value.String() {
			return searchRequestError("terminal_text_changed")
		}
	}
	return nil
}
func (o *searchTextObserver) flush(output []any, offset int) error {
	for i, raw := range output {
		item, _ := raw.(object)
		switch text(item["type"]) {
		case "message":
			if err := emitStructuredMessage(item, offset+i, o.state.emit); err != nil {
				return err
			}
		case "reasoning":
			id := text(item["id"])
			summary, _ := item["summary"].([]any)
			if id == "" {
				continue
			}
			if err := o.state.emit("response.output_item.added", object{"output_index": offset + i, "item": object{"id": id, "type": "reasoning", "summary": []any{}}}); err != nil {
				return err
			}
			for j, rawPart := range summary {
				part, _ := rawPart.(object)
				value := text(part["text"])
				for _, kind := range []string{"response.reasoning_summary_part.added", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_summary_part.done"} {
					payload := object{"item_id": id, "output_index": offset + i, "summary_index": j}
					switch kind {
					case "response.reasoning_summary_part.added":
						payload["part"] = object{"type": "summary_text", "text": ""}
					case "response.reasoning_summary_text.delta":
						payload["delta"] = value
					case "response.reasoning_summary_text.done":
						payload["text"] = value
					default:
						payload["part"] = part
					}
					if err := o.state.emit(kind, payload); err != nil {
						return err
					}
				}
			}
			if err := o.state.emit("response.output_item.done", object{"output_index": offset + i, "item": item}); err != nil {
				return err
			}
		}
	}
	return nil
}
