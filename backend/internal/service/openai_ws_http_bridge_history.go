package service

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

// This history belongs to one ordinary OAuth bridge turn. Observe only events
// whose client write succeeded; an upstream completion alone is not delivery.
type openAIWSHTTPBridgeOutputHistory struct {
	done       map[int64]json.RawMessage
	output     []json.RawMessage
	responseID string
	completed  bool
}

func (h *openAIWSHTTPBridgeOutputHistory) delivered(message []byte) {
	switch gjson.GetBytes(message, "type").String() {
	case "response.output_item.done":
		index := gjson.GetBytes(message, "output_index")
		item := gjson.GetBytes(message, "item")
		if index.Type != gjson.Number || index.Int() < 0 || index.Float() != float64(index.Int()) || !openAIWSHTTPBridgeCompletedItem(item) {
			return
		}
		if h.done == nil {
			h.done = make(map[int64]json.RawMessage)
		}
		h.done[index.Int()] = json.RawMessage(item.Raw)
	case "response.completed", "response.done":
		response := gjson.GetBytes(message, "response")
		// Older compatible providers omit status on a completed event. Preserve
		// that convention while rejecting every explicit unsuccessful status.
		implicitCompletion := !response.Get("status").Exists() && response.Get("error").Type == gjson.Null && !response.Get("incomplete_details").Exists()
		if !openAIAgentCompletedResponse(message, true) && !implicitCompletion {
			return
		}
		h.responseID = strings.TrimSpace(gjson.GetBytes(message, "response.id").String())
		if h.responseID == "" {
			return
		}
		h.completed = true
		output := gjson.GetBytes(message, "response.output").Array()
		if len(output) > 0 {
			// The complete terminal array replaces streamed bodies, order and
			// membership. Never append the earlier done items to it.
			for _, item := range output {
				if openAIWSHTTPBridgeCompletedItem(item) {
					h.output = append(h.output, json.RawMessage(item.Raw))
				}
			}
		} else {
			indices := make([]int64, 0, len(h.done))
			for index := range h.done {
				indices = append(indices, index)
			}
			slices.Sort(indices)
			for _, index := range indices {
				h.output = append(h.output, h.done[index])
			}
		}
		h.output = deduplicateOpenAIWSHTTPBridgeOutput(h.output)
	}
}

func openAIWSHTTPBridgeCompletedItem(item gjson.Result) bool {
	if !item.IsObject() || strings.TrimSpace(item.Get("type").String()) == "" {
		return false
	}
	// item.done itself completes items whose type has no status field.
	status := item.Get("status").String()
	return status == "" || status == "completed"
}

func deduplicateOpenAIWSHTTPBridgeOutput(items []json.RawMessage) []json.RawMessage {
	seen := make(map[string]struct{}, len(items))
	output := make([]json.RawMessage, 0, len(items))
	for _, raw := range items {
		item := gjson.ParseBytes(raw)
		key := item.Get("id").String()
		if key == "" {
			key = item.Get("type").String() + "\x00" + item.Get("call_id").String()
			if item.Get("call_id").String() == "" {
				key = string(raw)
			}
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		output = append(output, raw)
	}
	return output
}
