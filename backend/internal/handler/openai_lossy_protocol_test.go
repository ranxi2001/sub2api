//go:build unit && lossyprotocol

package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	for _, account := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			for _, kind := range []string{"discovery", "mcp"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", account, stream, kind), func(t *testing.T) {
					const discovery = `[{"type":"tool_search_call","call_id":"call_fixture_search","arguments":{"query":"fixture"},"status":"completed"},{"type":"tool_search_output","call_id":"call_fixture_search","status":"completed","execution":"client","tools":[{"type":"namespace","name":"fixture_tools","tools":[{"type":"function","name":"inspect","description":"Inspect fixture state","parameters":{"type":"object","properties":{}}}]}]}]`
					const mcp = `[{"type":"mcp_list_tools","id":"fixture_list","server_label":"fixture","tools":[{"name":"inspect","description":"Inspect fixture state","input_schema":{"type":"object"}}]},{"type":"mcp_approval_request","id":"fixture_approval","server_label":"fixture","name":"inspect","arguments":"{}"},{"type":"mcp_approval_response","approval_request_id":"fixture_approval","approve":true}]`
					option := httpRebuildFixtureOptions{
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
								require.Equal(t, "call_fixture_search", call.Get("call_id").String())
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
