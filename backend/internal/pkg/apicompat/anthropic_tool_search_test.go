//go:build unit

package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const toolSearchTestSchema = `{"type":"object","properties":{"query":{"type":"string"},"max_results":{"type":"number"}},"required":["query","max_results"]}`

func toolSearchTestRequest(t *testing.T, raw string) *AnthropicRequest {
	t.Helper()
	var req AnthropicRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &req))
	return &req
}

func nativeToolSearchJSON(t *testing.T, req *AnthropicRequest) []byte {
	t.Helper()
	out, err := AnthropicToResponses(req)
	require.NoError(t, err)
	require.NoError(t, ApplyNativeToolSearch(req, out))
	body, err := json.Marshal(out)
	require.NoError(t, err)
	return body
}

// Before the reveal Claude Code sends Bash + ToolSearch; after it, the revealed
// WebFetch schema joins tools[] with defer_loading:true and the history carries
// the ToolSearch call plus its tool_reference result.
const toolSearchBeforeReveal = `{"model":"gpt-6-astra","max_tokens":64,
 "tools":[
  {"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}},
  {"name":"ToolSearch","description":"search deferred tools","input_schema":` + toolSearchTestSchema + `}],
 "messages":[{"role":"user","content":"fetch the page"}]}`

const toolSearchAfterReveal = `{"model":"gpt-6-astra","max_tokens":64,
 "tools":[
  {"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}},
  {"name":"ToolSearch","description":"search deferred tools","input_schema":` + toolSearchTestSchema + `},
  {"name":"WebFetch","description":"fetch a url","input_schema":{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]},"defer_loading":true}],
 "messages":[
  {"role":"user","content":"fetch the page"},
  {"role":"assistant","content":[{"type":"tool_use","id":"toolu_search","name":"ToolSearch","input":{"query":"select:WebFetch","max_results":5}}]},
  {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_search","content":[{"type":"tool_reference","tool_name":"WebFetch"},{"type":"tool_reference","tool_name":"WebFetch"},{"type":"tool_reference","tool_name":"Unknown"}]}]}]}`

func TestApplyNativeToolSearch_TopLevelToolsStableAcrossReveal(t *testing.T) {
	before := nativeToolSearchJSON(t, toolSearchTestRequest(t, toolSearchBeforeReveal))
	after := nativeToolSearchJSON(t, toolSearchTestRequest(t, toolSearchAfterReveal))

	require.JSONEq(t, gjson.GetBytes(before, "tools").Raw, gjson.GetBytes(after, "tools").Raw,
		"top-level tools must not change when a deferred tool is revealed (prompt cache prefix)")

	tools := gjson.GetBytes(after, "tools").Array()
	require.Len(t, tools, 2)
	require.Equal(t, "function", tools[0].Get("type").String())
	require.Equal(t, "Bash", tools[0].Get("name").String())

	search := tools[1]
	require.Equal(t, "tool_search", search.Get("type").String())
	require.Equal(t, "client", search.Get("execution").String())
	require.Equal(t, "search deferred tools", search.Get("description").String())
	require.JSONEq(t, toolSearchTestSchema, search.Get("parameters").Raw)
	require.False(t, search.Get("name").Exists(), "tool_search carries no function name")
	require.False(t, search.Get("strict").Exists(), "tool_search is declared without strict")
}

func TestApplyNativeToolSearch_HistoryMapsToNativeItems(t *testing.T) {
	body := nativeToolSearchJSON(t, toolSearchTestRequest(t, toolSearchAfterReveal))
	input := gjson.GetBytes(body, "input").Array()
	require.Len(t, input, 3)

	call := input[1]
	require.Equal(t, "tool_search_call", call.Get("type").String())
	require.Equal(t, "toolu_search", call.Get("call_id").String())
	require.Equal(t, "client", call.Get("execution").String())
	require.Equal(t, "completed", call.Get("status").String())
	require.True(t, call.Get("arguments").IsObject(), "tool_search_call arguments are a JSON object")
	require.Equal(t, "select:WebFetch", call.Get("arguments.query").String())

	output := input[2]
	require.Equal(t, "tool_search_output", output.Get("type").String())
	require.Equal(t, "toolu_search", output.Get("call_id").String())
	require.Equal(t, "client", output.Get("execution").String())
	require.Equal(t, "completed", output.Get("status").String())
	revealed := output.Get("tools").Array()
	require.Len(t, revealed, 1, "duplicate and unknown references are dropped")
	require.Equal(t, "function", revealed[0].Get("type").String())
	require.Equal(t, "WebFetch", revealed[0].Get("name").String())
	require.Equal(t, "fetch a url", revealed[0].Get("description").String())
	require.True(t, revealed[0].Get("defer_loading").Bool())
	require.False(t, revealed[0].Get("strict").Bool())
	require.True(t, revealed[0].Get("strict").Exists())
	require.Equal(t, "url", revealed[0].Get("parameters.required.0").String())
}

func TestApplyNativeToolSearch_NoToolSearchDeclaredKeepsFunctionTools(t *testing.T) {
	req := toolSearchTestRequest(t, toolSearchAfterReveal)
	tools := req.Tools[:0:0]
	for _, tool := range req.Tools {
		if tool.Name != "ToolSearch" {
			tools = append(tools, tool)
		}
	}
	req.Tools = tools

	body := nativeToolSearchJSON(t, req)
	var names []string
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		require.Equal(t, "function", tool.Get("type").String())
		names = append(names, tool.Get("name").String())
	}
	require.Equal(t, []string{"Bash", "WebFetch"}, names)
	require.NotContains(t, string(body), "tool_search_call")
}

func TestResponsesToAnthropic_RestoresToolSearchCall(t *testing.T) {
	var resp ResponsesResponse
	require.NoError(t, json.Unmarshal([]byte(`{"id":"resp_1","status":"completed","output":[
		{"type":"tool_search_call","call_id":"call_search","execution":"client","status":"completed","arguments":{"query":"select:WebFetch","max_results":5}}]}`), &resp))

	out := ResponsesToAnthropic(&resp, "gpt-6-astra")
	require.Len(t, out.Content, 1)
	block := out.Content[0]
	require.Equal(t, "tool_use", block.Type)
	require.Equal(t, "call_search", block.ID)
	require.Equal(t, "ToolSearch", block.Name)
	require.JSONEq(t, `{"query":"select:WebFetch","max_results":5}`, string(block.Input))
	require.Equal(t, "tool_use", AnthropicStopReasonString(out.StopReason))
}

func TestResponsesEventToAnthropic_RestoresToolSearchCall(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	var events []AnthropicStreamEvent
	for _, raw := range []string{
		`{"type":"response.created","response":{"id":"resp_1","model":"gpt-6-astra"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"tool_search_call","call_id":"call_search","execution":"client"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"tool_search_call","call_id":"call_search","execution":"client","status":"completed","arguments":{"query":"docs","max_results":5}}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`,
	} {
		var evt ResponsesStreamEvent
		require.NoError(t, json.Unmarshal([]byte(raw), &evt))
		events = append(events, ResponsesEventToAnthropicEvents(&evt, state)...)
	}

	var start, delta, stop *AnthropicStreamEvent
	for i := range events {
		switch events[i].Type {
		case "content_block_start":
			start = &events[i]
		case "content_block_delta":
			delta = &events[i]
		case "content_block_stop":
			stop = &events[i]
		}
	}
	require.NotNil(t, start)
	require.Equal(t, "tool_use", start.ContentBlock.Type)
	require.Equal(t, "call_search", start.ContentBlock.ID)
	require.Equal(t, "ToolSearch", start.ContentBlock.Name)
	require.NotNil(t, delta)
	require.Equal(t, "input_json_delta", delta.Delta.Type)
	require.JSONEq(t, `{"query":"docs","max_results":5}`, delta.Delta.PartialJSON)
	require.NotNil(t, stop)
	require.Equal(t, *start.Index, *stop.Index)

	last := events[len(events)-2]
	require.Equal(t, "message_delta", last.Type)
	require.Equal(t, "tool_use", last.Delta.StopReason)
}
