package apicompat

import (
	"encoding/json"
)

// ToolSearchToolName is Claude Code's client-executed tool-search tool. Under
// the native protocol it maps to the Responses `tool_search` tool, its
// tool_use to a `tool_search_call` and its tool_result to a
// `tool_search_output`; an upstream tool_search_call comes back under this name.
const ToolSearchToolName = "ToolSearch"

// ApplyNativeToolSearch rewrites a Responses request produced by
// AnthropicToResponses so Claude Code's ToolSearch uses the native
// client-executed tool_search protocol of the ChatGPT/Codex backend.
//
// Deferred tools never reach the top-level tools: revealed definitions ride in
// the history's tool_search_output instead, so the top-level tools (the head of
// the prompt-cache prefix) stay identical before and after a reveal.
func ApplyNativeToolSearch(req *AnthropicRequest, out *ResponsesRequest) error {
	declared := false
	for _, t := range req.Tools {
		if t.Name == ToolSearchToolName && !t.DeferLoading {
			declared = true
			break
		}
	}
	if !declared {
		return nil
	}

	schemas := make(map[string]AnthropicTool, len(req.Tools))
	kept := make([]AnthropicTool, 0, len(req.Tools))
	for _, t := range req.Tools {
		schemas[t.Name] = t
		if t.DeferLoading {
			continue
		}
		kept = append(kept, t)
	}
	tools := convertAnthropicToolsToResponses(kept)
	for i, t := range kept {
		if t.Name == ToolSearchToolName {
			// Codex declares this tool without a name or strict.
			tools[i] = ResponsesTool{
				Type:        "tool_search",
				Execution:   "client",
				Description: t.Description,
				Parameters:  normalizeToolParameters(t.InputSchema),
			}
		}
	}
	out.Tools = tools

	revealed := toolSearchReveals(req.Messages)
	if len(revealed) == 0 {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(out.Input, &items); err != nil {
		return err
	}
	for i, raw := range items {
		var item ResponsesInputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		names, isSearch := revealed[item.CallID]
		if !isSearch {
			continue
		}
		var native any
		switch item.Type {
		case "function_call":
			native = map[string]any{
				"type":      "tool_search_call",
				"call_id":   item.CallID,
				"execution": "client",
				"status":    "completed",
				"arguments": jsonObjectOrEmpty(item.Arguments),
			}
		case "function_call_output":
			loaded := make([]any, 0, len(names))
			for _, name := range names {
				t, ok := schemas[name]
				if !ok {
					continue
				}
				loaded = append(loaded, map[string]any{
					"type":          "function",
					"name":          name,
					"description":   t.Description,
					"defer_loading": true,
					"strict":        false,
					"parameters":    normalizeToolParameters(t.InputSchema),
				})
			}
			native = map[string]any{
				"type":      "tool_search_output",
				"call_id":   item.CallID,
				"execution": "client",
				"status":    "completed",
				"tools":     loaded,
			}
		default:
			continue
		}
		encoded, err := json.Marshal(native)
		if err != nil {
			return err
		}
		items[i] = encoded
	}
	input, err := json.Marshal(items)
	if err != nil {
		return err
	}
	out.Input = input
	return nil
}

// toolSearchReveals maps each ToolSearch tool_use id in the history to the
// tool names its tool_result referenced, first occurrence first.
func toolSearchReveals(msgs []AnthropicMessage) map[string][]string {
	reveals := make(map[string][]string)
	for _, m := range msgs {
		var blocks []AnthropicContentBlock
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch {
			case b.Type == "tool_use" && b.Name == ToolSearchToolName && b.ID != "":
				reveals[toResponsesCallID(b.ID)] = nil
			case b.Type == "tool_result":
				id := toResponsesCallID(b.ToolUseID)
				if _, isSearch := reveals[id]; !isSearch {
					continue
				}
				var inner []AnthropicContentBlock
				_ = json.Unmarshal(b.Content, &inner)
				seen := make(map[string]bool)
				for _, ib := range inner {
					if ib.Type == "tool_reference" && ib.ToolName != "" && !seen[ib.ToolName] {
						seen[ib.ToolName] = true
						reveals[id] = append(reveals[id], ib.ToolName)
					}
				}
			}
		}
	}
	return reveals
}

// toolSearchCallToolUse turns an upstream tool_search_call back into the
// ToolSearch tool_use Claude Code executes; its call_id round-trips as the
// tool_use id that the next turn's tool_result answers.
func toolSearchCallToolUse(item ResponsesOutput) AnthropicContentBlock {
	return AnthropicContentBlock{
		Type:  "tool_use",
		ID:    fromResponsesCallID(item.CallID),
		Name:  ToolSearchToolName,
		Input: jsonObjectOrEmpty(item.Arguments),
	}
}

// jsonObjectOrEmpty keeps raw when it is a JSON object; both tool_search_call
// arguments and tool_use input must be objects.
func jsonObjectOrEmpty(raw string) json.RawMessage {
	var probe map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &probe) != nil || probe == nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func resToAnthHandleToolSearchDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	events := closeCurrentBlock(state)
	block := toolSearchCallToolUse(*evt.Item)
	input := block.Input
	block.Input = json.RawMessage(`{}`)
	idx := state.ContentBlockIndex
	state.HasToolCall = true
	events = append(events,
		AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &block},
		AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: string(input)}},
		AnthropicStreamEvent{Type: "content_block_stop", Index: &idx},
	)
	state.ContentBlockIndex++
	return events
}
