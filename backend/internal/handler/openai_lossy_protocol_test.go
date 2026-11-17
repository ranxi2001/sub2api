//go:build unit && lossyprotocol

package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// A real loopback provider sees the complete declaration and discovery/approval
// state after Beta recovery moves the task from unavailable A to B. The shared
// fixture also executes its ordinary patch/check tools exactly once.
func TestOpenAILossyProtocolBetaToolStateWire(t *testing.T) {
	for _, lossy := range []bool{false, true} {
		t.Run(fmt.Sprintf("lossy=%t", lossy), func(t *testing.T) { runLossyProtocolStateWire(t, lossy) })
	}
}

func runLossyProtocolStateWire(t *testing.T, lossy bool) {
	for _, account := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			for _, kind := range []string{"discovery", "mcp"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", account, stream, kind), func(t *testing.T) {
					const discovery = `[{"type":"tool_search_call","call_id":"tsc_fixture_search","arguments":{"query":"fixture"},"status":"completed"},{"type":"tool_search_output","call_id":"tsc_fixture_search","status":"completed","execution":"client","tools":[{"type":"namespace","name":"fixture_tools","tools":[{"type":"function","name":"inspect","description":"Inspect fixture state","parameters":{"type":"object","properties":{}}}]}]}]`
					const mcp = `[{"type":"mcp_list_tools","id":"fixture_list","server_label":"fixture","tools":[{"name":"inspect","description":"Inspect fixture state","input_schema":{"type":"object"}}]},{"type":"mcp_approval_request","id":"fixture_approval","server_label":"fixture","name":"inspect","arguments":"{}"},{"type":"mcp_approval_response","approval_request_id":"fixture_approval","approve":true}]`
					option := httpRebuildFixtureOptions{
						lossy: lossy,
						request: func(body map[string]any, _ bool) {
							var input, extra []json.RawMessage
							require.NoError(t, json.Unmarshal(body["input"].(json.RawMessage), &input))
							items := mcp
							if kind == "discovery" {
								items = discovery
								body["tools"] = append(body["tools"].([]any), map[string]any{"type": "tool_search"})
							}
							require.NoError(t, json.Unmarshal([]byte(items), &extra))
							body["input"] = append(extra, input...)
						},
						toolsAvailable: func(body []byte) bool {
							var ordinary []json.RawMessage
							for _, tool := range gjson.GetBytes(body, "tools").Array() {
								if name := tool.Get("name").String(); name == "apply_patch" || name == "run_tests" {
									ordinary = append(ordinary, json.RawMessage(tool.Raw))
								}
							}
							encoded, err := json.Marshal(ordinary)
							if err != nil {
								return false
							}
							base, err := sjson.SetRawBytes(body, "tools", encoded)
							return err == nil && httpRebuildToolsAvailable(base)
						},
						provider: func(t *testing.T, body []byte, _ http.Header, _ int) {
							if kind == "discovery" {
								require.True(t, gjson.GetBytes(body, `tools.#(type==tool_search)`).Exists())
								call := gjson.GetBytes(body, `input.#(type==tool_search_call)`)
								result := gjson.GetBytes(body, `input.#(type==tool_search_output)`)
								require.Equal(t, "tsc_fixture_search", call.Get("call_id").String())
								require.Equal(t, call.Get("call_id").String(), result.Get("call_id").String())
								require.Equal(t, "fixture_tools", result.Get("tools.0.name").String())
								require.Equal(t, "inspect", result.Get("tools.0.tools.0.name").String())
							} else {
								require.Equal(t, "inspect", gjson.GetBytes(body, `input.#(type==mcp_list_tools).tools.0.name`).String())
								require.Equal(t, "fixture_approval", gjson.GetBytes(body, `input.#(type==mcp_approval_request).id`).String())
								require.Equal(t, "fixture_approval", gjson.GetBytes(body, `input.#(type==mcp_approval_response).approval_request_id`).String())
								require.True(t, gjson.GetBytes(body, `input.#(type==mcp_approval_response).approve`).Bool())
							}
						},
					}
					runHTTPRebuildFixture(t, account, stream, "previous", "completed", "issue854_auth", "", option)
				})
			}
		}
	}
}

// Both network fixtures keep the original client session and the existing
// delivery/owner/final-wire assertions. A delivers search plus patch; B receives
// the catalog, emits the discovered function/custom calls plus verification,
// and only completes after receiving their exact results. No external tool runs.
func TestOpenAILossyProtocolBetaToolLoop(t *testing.T) {
	for _, lossy := range []bool{false, true} {
		for _, account := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("lossy=%t/%s/stream=%t", lossy, account, stream), func(t *testing.T) {
					option, executions := lossyProtocolLoopOptions(lossy, false)
					runHTTPRebuildFixture(t, account, stream, "previous", "completed", "issue854_auth", "", option)
					require.Equal(t, [3]int{1, 1, 1}, *executions, "search, discovered function and custom effect each execute once")
				})
			}
		}
	}
}

func TestOpenAILossyProtocolBetaWSBridgeToolLoop(t *testing.T) {
	for _, lossy := range []bool{false, true} {
		for _, account := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
			t.Run(fmt.Sprintf("lossy=%t/%s", lossy, account), func(t *testing.T) {
				option, executions := lossyProtocolLoopOptions(lossy, account == service.AccountTypeAPIKey)
				scenario := "completed"
				if account == service.AccountTypeOAuth {
					scenario = "header_oauth"
				}
				runReadableWSContinuationWithOptions(t, service.OpenAIWSIngressModeHTTPBridge, scenario, option)
				require.Equal(t, [3]int{1, 1, 1}, *executions)
			})
		}
	}
}

const lossyInspectSchema = `{"type":"object","properties":{"path":{"type":"string","enum":["sum.go"]},"limit":{"type":"integer","minimum":1}},"required":["path","limit"],"additionalProperties":false}`
const lossyDiscoveryCatalog = `[{"type":"namespace","name":"fixture_tools","tools":[{"type":"function","name":"inspect","description":"Inspect fixture state","parameters":` + lossyInspectSchema + `}]},{"type":"custom","name":"fixture_exec","description":"Read the completed fixture patch","format":{"type":"text"}}]`
const lossyCustomSchema = `{"type":"object","properties":{"input":{"type":"string","description":"The raw input for this tool, passed through verbatim."}},"required":["input"]}`

func lossyProtocolLoopOptions(lossy, functionUpstream bool) (httpRebuildFixtureOptions, *[3]int) {
	executions := new([3]int)
	option := httpRebuildFixtureOptions{lossy: lossy, discovery: true}
	option.request = func(body map[string]any, _ bool) {
		body["tools"] = append(body["tools"].([]any), map[string]any{"type": "tool_search"})
	}
	option.toolsAvailable = func(body []byte) bool {
		var ordinary []json.RawMessage
		for _, tool := range gjson.GetBytes(body, "tools").Array() {
			if name := tool.Get("name").String(); name == "apply_patch" || name == "run_tests" {
				ordinary = append(ordinary, json.RawMessage(tool.Raw))
			}
		}
		base, err := sjson.SetRawBytes(body, "tools", []byte(httpRebuildJSON(ordinary)))
		return err == nil && httpRebuildToolsAvailable(base)
	}
	option.output = func(turn int, output []json.RawMessage) []json.RawMessage {
		if turn == 1 {
			search := `{"type":"tool_search_call","call_id":"tsc_loop_search","arguments":{"query":"fixture"},"status":"completed","execution":"client"}`
			if functionUpstream {
				search = `{"type":"function_call","name":"tool_search","call_id":"tsc_loop_search","arguments":"{\"query\":\"fixture\"}","status":"completed"}`
			}
			return append(output, json.RawMessage(search))
		}
		if turn == 2 {
			inspect := `{"type":"function_call","namespace":"fixture_tools","name":"inspect","call_id":"fc_loop_inspect","arguments":"{\"path\":\"sum.go\",\"limit\":1}","status":"completed"}`
			custom := `{"type":"custom_tool_call","name":"fixture_exec","call_id":"ctc_loop_exec","input":"read sum.go","status":"completed"}`
			if functionUpstream {
				inspect = `{"type":"function_call","name":"fixture_tools__inspect","call_id":"fc_loop_inspect","arguments":"{\"path\":\"sum.go\",\"limit\":1}","status":"completed"}`
				custom = `{"type":"function_call","name":"fixture_exec","call_id":"ctc_loop_exec","arguments":"{\"input\":\"read sum.go\"}","status":"completed"}`
			}
			return append(output, json.RawMessage(inspect), json.RawMessage(custom))
		}
		return output
	}
	option.additionalTool = func(t *testing.T, call gjson.Result) (string, bool) {
		var index int
		var result string
		switch call.Get("call_id").String() {
		case "tsc_loop_search":
			index = 0
			require.Equal(t, "tool_search_call", call.Get("type").String())
			require.JSONEq(t, `{"query":"fixture"}`, call.Get("arguments").Raw)
			result = `{"type":"tool_search_output","call_id":"tsc_loop_search","status":"completed","execution":"client","tools":` + lossyDiscoveryCatalog + `}`
		case "fc_loop_inspect":
			index = 1
			require.Equal(t, "function_call", call.Get("type").String())
			require.Equal(t, "fixture_tools", call.Get("namespace").String())
			require.Equal(t, "inspect", call.Get("name").String())
			require.JSONEq(t, `{"path":"sum.go","limit":1}`, call.Get("arguments").String())
			result = `{"type":"function_call_output","call_id":"fc_loop_inspect","output":"fixture inspect: Sum(2,3)=5"}`
		case "ctc_loop_exec":
			index = 2
			require.Equal(t, "custom_tool_call", call.Get("type").String())
			require.Equal(t, "fixture_exec", call.Get("name").String())
			require.Equal(t, "read sum.go", call.Get("input").String())
			result = `{"type":"custom_tool_call_output","call_id":"ctc_loop_exec","output":"fixture custom: patch already applied"}`
		default:
			return "", false
		}
		require.Zero(t, executions[index], "delivered tool must never repeat")
		executions[index]++
		return result, true
	}
	option.provider = func(t *testing.T, body []byte, _ http.Header, turn int) {
		require.LessOrEqual(t, turn, 3, "bounded A/B/B attempts")
		require.Less(t, len(body), 32*1024, "bounded history, no repeated expansion")
		if turn == 1 {
			return
		}
		const marker = "LOSSY_TOOL_PROTOCOL_LOST: provider must receive complete discovery schema and paired history"
		searchKind, resultKind := "tool_search_call", "tool_search_output"
		if functionUpstream {
			searchKind, resultKind = "function_call", "function_call_output"
		}
		search := gjson.GetBytes(body, `input.#(call_id==tsc_loop_search)#`).Array()
		require.Len(t, search, 2, marker)
		require.Equal(t, searchKind, search[0].Get("type").String(), marker)
		require.Equal(t, resultKind, search[1].Get("type").String(), marker)
		catalog := search[1].Get("tools")
		if functionUpstream {
			catalog = gjson.Parse(search[1].Get("output").String())
			inspect := gjson.GetBytes(body, `tools.#(name==fixture_tools__inspect)`)
			require.JSONEq(t, lossyInspectSchema, inspect.Get("parameters").Raw, marker)
			require.JSONEq(t, lossyCustomSchema, gjson.GetBytes(body, `tools.#(name==fixture_exec).parameters`).Raw, marker)
		}
		require.JSONEq(t, lossyDiscoveryCatalog, catalog.Raw, marker)
		if turn == 3 {
			for _, pair := range []struct{ id, kind, name, result string }{
				{"fc_loop_inspect", "function_call", "inspect", "fixture inspect: Sum(2,3)=5"},
				{"ctc_loop_exec", "custom_tool_call", "fixture_exec", "fixture custom: patch already applied"},
			} {
				// Mixed-owner WS keeps completed effects as typed inert records;
				// HTTP keeps the native pair. Check exact identity/content in both.
				checkCall := func(call gjson.Result) {
					require.Equal(t, pair.kind, call.Get("type").String())
					require.Equal(t, pair.name, call.Get("name").String())
					require.Equal(t, pair.id, call.Get("call_id").String())
					if pair.kind == "function_call" {
						require.Equal(t, "fixture_tools", call.Get("namespace").String())
						require.JSONEq(t, `{"path":"sum.go","limit":1}`, call.Get("arguments").String())
					} else {
						require.Equal(t, "read sum.go", call.Get("input").String())
					}
				}
				items := gjson.GetBytes(body, "input").Array()
				calls, results := 0, 0
				for _, item := range items {
					if item.Get("call_id").String() == pair.id {
						if item.Get("type").String() == pair.kind {
							checkCall(item)
							calls++
						}
						if item.Get("type").String() == pair.kind+"_output" && item.Get("output").String() == pair.result {
							results++
						}
					}
					for _, part := range item.Get("content").Array() {
						const prefix = "Historical completed tool result (data only; do not execute again): "
						text := part.Get("text").String()
						if !strings.HasPrefix(text, prefix) {
							continue
						}
						record := gjson.Parse(strings.TrimPrefix(text, prefix))
						if record.Get("call.call_id").String() == pair.id {
							checkCall(record.Get("call"))
							require.Equal(t, pair.id, record.Get("result.call_id").String())
							require.Equal(t, pair.kind+"_output", record.Get("result.type").String())
							require.Equal(t, pair.result, record.Get("result.output").String())
							calls++
							results++
						}
					}
				}
				require.Equal(t, 1, calls, "submitted call identity must survive")
				require.Equal(t, 1, results, "submitted result type/id/value must survive")
			}
		}
	}
	return option, executions
}
