//go:build unit && httpprojection

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

func TestOpenAI409NativeControlsOAuthToolHistory(t *testing.T) {
	t.Run("legacy_named_function", func(t *testing.T) {
		option := httpRebuildFixtureOptions{
			request: func(body map[string]any, _ bool) {
				if body["previous_response_id"] != "resp_rebuild_A" {
					return
				}
				functions := httpRebuildToolDefinitions()
				for _, f := range functions {
					delete(f.(map[string]any), "type")
				}
				delete(body, "tools")
				body["functions"] = functions
				body["function_call"] = map[string]any{"name": "run_tests"}
			},
			provider: func(t *testing.T, body []byte, _ http.Header, n int) {
				if n == 2 {
					require.Equal(t, "run_tests", gjson.GetBytes(body, "tool_choice.name").String())
					require.False(t, gjson.GetBytes(body, "functions").Exists())
					require.False(t, gjson.GetBytes(body, "function_call").Exists())
				}
			},
		}
		runHTTPRebuildFixture(t, service.AccountTypeOAuth, false, "previous", "completed", "issue854_auth", "", option)
	})
	t.Run("historical_search_only", func(t *testing.T) {
		option := httpRebuildFixtureOptions{
			request: func(body map[string]any, _ bool) {
				if body["previous_response_id"] != "resp_rebuild_A" {
					return
				}
				body["tools"] = []any{}
				var input []json.RawMessage
				require.NoError(t, json.Unmarshal(body["input"].(json.RawMessage), &input))
				body["input"] = append(input, json.RawMessage(`{"type":"web_search_call","id":"ws_fixture","status":"completed"}`))
			},
			toolsAvailable: func(body []byte) bool {
				return httpRebuildToolsAvailable(body) || (gjson.GetBytes(body, "tools.#").Int() == 1 &&
					gjson.GetBytes(body, "tools.0.type").String() == "web_search" && gjson.GetBytes(body, "tool_choice").String() == "none")
			},
			provider: func(t *testing.T, body []byte, _ http.Header, n int) {
				if n == 2 {
					require.Equal(t, "none", gjson.GetBytes(body, "tool_choice").String())
					require.Equal(t, "web_search", gjson.GetBytes(body, "tools.0.type").String())
				}
			},
		}
		// The provider rejects B after recording the physical request. The
		// fixture asserts one B send and no subsequent generation or retry.
		runHTTPRebuildFixture(t, service.AccountTypeOAuth, false, "previous", "rejected", "issue854_auth", "", option)
	})
}

// Real authentication, scheduler, native builders, Redis delivery fences and
// a loopback provider. A emits a patch once, becomes unavailable, then B must
// reuse the result, verify once and finish the same client task.
func TestOpenAI409NativeControlsContinuation(t *testing.T) {
	for _, account := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
		for _, stream := range []bool{false, true} {
			for _, shape := range []string{"web_search", "image_aliases", "image_bridge", "image_strip", "combined"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", account, stream, shape), func(t *testing.T) {
					search := shape == "web_search" || shape == "combined"
					bridge := shape == "image_bridge" || shape == "combined"
					explicitImage := shape == "image_aliases" || shape == "image_strip" || shape == "combined"
					option := httpRebuildFixtureOptions{
						apiKey: func(key *service.APIKey) { key.Group.AllowImageGeneration = true },
						headers: func(r *http.Request) {
							r.Header.Set("User-Agent", "codex_cli_rs/0.110.0")
							r.Header.Set("originator", "codex_cli_rs")
						},
						accounts: func(accounts []service.Account) {
							for i := range accounts {
								accounts[i].Extra = map[string]any{"codex_image_generation_bridge": bridge}
								if shape == "image_strip" {
									accounts[i].Extra["codex_image_generation_explicit_tool_policy"] = "strip"
								}
							}
						},
						request: func(body map[string]any, continuation bool) {
							tools := body["tools"].([]any)
							if search {
								tools = append(tools, map[string]any{"type": "web_search", "external_web_access": false, "search_context_size": "low"})
							}
							if explicitImage {
								tools = append(tools, map[string]any{"type": "image_generation", "model": "gpt-image-2", "format": "jpeg", "compression": 73, "input_fidelity": "high"})
							}
							body["tools"] = tools
						},
						toolsAvailable: func(body []byte) bool {
							// The provider validates the original executable function
							// declarations separately from hosted compatibility tools.
							tools := gjson.GetBytes(body, "tools").Array()
							for i := len(tools) - 1; i >= 2; i-- {
								kind := tools[i].Get("type").String()
								if kind != "web_search" && kind != "image_generation" {
									return false
								}
								var err error
								body, err = sjson.DeleteBytes(body, fmt.Sprintf("tools.%d", i))
								if err != nil {
									return false
								}
							}
							return httpRebuildToolsAvailable(body)
						},
						provider: func(t *testing.T, body []byte, _ http.Header, n int) {
							searchTool := gjson.GetBytes(body, `tools.#(type==web_search)`)
							require.Equal(t, search, searchTool.Exists())
							if search {
								require.Equal(t, "low", searchTool.Get("search_context_size").String())
								require.Equal(t, account == service.AccountTypeAPIKey, searchTool.Get("external_web_access").Exists())
								if account == service.AccountTypeAPIKey {
									require.Equal(t, gjson.False, searchTool.Get("external_web_access").Type)
								}
							}
							imageTool := gjson.GetBytes(body, `tools.#(type==image_generation)`)
							wantImage := (explicitImage || bridge) && shape != "image_strip"
							require.Equal(t, wantImage, imageTool.Exists())
							wantTools := 2
							if search {
								wantTools++
							}
							if wantImage {
								wantTools++
							}
							require.Len(t, gjson.GetBytes(body, "tools").Array(), wantTools)
							if wantImage && explicitImage {
								require.Equal(t, "jpeg", imageTool.Get("output_format").String())
								require.Equal(t, int64(73), imageTool.Get("output_compression").Int())
								for _, field := range []string{"format", "compression", "input_fidelity"} {
									require.False(t, imageTool.Get(field).Exists())
								}
							}
							if bridge {
								require.Equal(t, "auto", gjson.GetBytes(body, "tool_choice").String())
								require.NotEmpty(t, gjson.GetBytes(body, "instructions").String())
							}
							if n > 1 {
								require.NotContains(t, string(body), "fixture-opaque-A")
							}
						},
					}
					runHTTPRebuildFixture(t, account, stream, "previous", "completed", "issue854_auth", "", option)
				})
			}
		}
	}
}
