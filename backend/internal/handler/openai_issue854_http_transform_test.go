//go:build unit && issue854matrix

package handler

import (
	"context"
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

// The same authenticated A -> completed patch -> B -> verification -> final
// fixture exercises the real HTTP repository and a loopback provider. These
// cases add only the missing transformations identified in comment6882215071.
func TestIssue854HTTPTransformMatrix(t *testing.T) {
	for _, account := range []string{service.AccountTypeAPIKey, service.AccountTypeOAuth} {
		for _, stream := range []bool{false, true} {
			for _, transform := range []string{"metadata", "message_namespace", "tool_namespace", "empty_image", "reasoning_summary", "compaction_order", "timezone_manual", "timezone_auto", "lite", "reserved_alias", "namespace_declaration", "namespace_flatten", "schema_null", "new_task"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", account, stream, transform), func(t *testing.T) {
					option := httpRebuildFixtureOptions{
						request: func(body map[string]any, continuation bool) {
							if transform == "reserved_alias" || transform == "namespace_declaration" || transform == "namespace_flatten" {
								extra := map[string]any{"type": "function", "name": "python", "parameters": map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}}, "required": []any{"code"}, "additionalProperties": false}}
								if strings.HasPrefix(transform, "namespace_") {
									extra = map[string]any{"type": "namespace", "name": "fixture_tools", "tools": []any{extra}}
								}
								body["tools"] = append(body["tools"].([]any), extra)
							}
							if !continuation && !strings.HasPrefix(transform, "timezone_") {
								return
							}
							if transform == "schema_null" {
								body["tools"].([]any)[0].(map[string]any)["parameters"].(map[string]any)["type"] = nil
							}
							var input []map[string]any
							require.NoError(t, json.Unmarshal(body["input"].(json.RawMessage), &input))
							for _, item := range input {
								switch transform {
								case "metadata":
									if item["role"] == "user" {
										item["internal_chat_message_metadata_passthrough"] = map[string]any{"fixture": true}
									}
								case "message_namespace":
									if item["role"] == "user" {
										item["namespace"] = "fixture_messages"
									}
								case "tool_namespace":
									if item["type"] == "function_call" {
										item["namespace"] = "functions"
									}
								case "empty_image":
									if item["role"] == "user" {
										if text, ok := item["content"].(string); ok {
											item["content"] = []any{map[string]any{"type": "input_text", "text": text}}
										}
										if content, ok := item["content"].([]any); ok {
											item["content"] = append(content, map[string]any{"type": "input_image", "image_url": "data:image/png;base64,"})
										}
									}
								}
							}
							if transform == "reasoning_summary" {
								input = append(input, map[string]any{"type": "reasoning", "encrypted_content": "fixture-reasoning-old", "summary": []any{map[string]any{"type": "summary_text", "text": "fixture-readable-summary"}}})
							}
							if transform == "compaction_order" {
								input = append(input, map[string]any{"type": "compaction", "encrypted_content": "fixture-compaction-old"}, map[string]any{"type": "message", "role": "user", "content": "fixture-after-compaction: reuse the recorded result"})
							}
							if strings.HasPrefix(transform, "timezone_") {
								input = append(input, map[string]any{"type": "message", "role": "user", "content": "<environment_context><timezone>Asia/Shanghai</timezone></environment_context>"})
							}
							if transform == "new_task" {
								input = append(input, map[string]any{"type": "message", "role": "user", "content": "fixture-new-task: document the completed fix without repeating it"})
							}
							body["input"] = input
						},
						provider: func(t *testing.T, body []byte, header http.Header, n int) {
							if n < 2 && !strings.HasPrefix(transform, "timezone_") {
								return
							}
							switch transform {
							case "timezone_manual", "timezone_auto":
								// JSON escaping of XML is irrelevant; inspect decoded physical input.
								var wire map[string]any
								require.NoError(t, json.Unmarshal(body, &wire))
								text := fmt.Sprint(wire["input"])
								want, absent := "Asia/Tokyo", "Asia/Shanghai"
								if transform == "timezone_auto" && n > 1 {
									want, absent = absent, want
								}
								require.Contains(t, text, "<timezone>"+want+"</timezone>", "ISSUE854_HTTP_TIMEZONE_LAST_BYTES")
								require.NotContains(t, text, "<timezone>"+absent+"</timezone>")
								require.Equal(t, "Asia/Tokyo", header.Get("X-Timezone"), "header still follows the configured egress")
							case "lite":
								if account == service.AccountTypeOAuth {
									require.Equal(t, "all_turns", gjson.GetBytes(body, "reasoning.context").String())
								} else {
									require.False(t, gjson.GetBytes(body, "reasoning.context").Exists())
								}
								require.Equal(t, gjson.False, gjson.GetBytes(body, "parallel_tool_calls").Type)
							case "new_task":
								require.Contains(t, string(body), "fixture-new-task")
							case "metadata":
								if account == service.AccountTypeOAuth {
									require.NotContains(t, string(body), "internal_chat_message_metadata_passthrough", "ISSUE854_HTTP_METADATA_PHYSICAL_WIRE")
								}
							case "message_namespace":
								require.NotContains(t, string(body), "fixture_messages", "ISSUE854_HTTP_MESSAGE_NAMESPACE_PHYSICAL_WIRE")
							case "tool_namespace":
								for _, item := range gjson.GetBytes(body, "input").Array() {
									if item.Get("type").String() == "function_call" {
										if account == service.AccountTypeOAuth {
											require.Equal(t, "functions", item.Get("namespace").String())
										} else {
											require.False(t, item.Get("namespace").Exists())
										}
									}
								}
							case "empty_image":
								require.NotContains(t, string(body), "data:image/png;base64,", "ISSUE854_HTTP_EMPTY_IMAGE_PHYSICAL_WIRE")
							case "reasoning_summary":
								require.Contains(t, string(body), "fixture-readable-summary")
								require.NotContains(t, string(body), "fixture-reasoning-old")
							case "compaction_order":
								require.NotContains(t, string(body), "fixture-compaction-old")
								require.Greater(t, strings.Index(string(body), "fixture-after-compaction"), strings.Index(string(body), httpRebuildPatch))
							}
						},
					}
					if strings.HasPrefix(transform, "timezone_") {
						if transform == "timezone_auto" {
							option.physicalBody = func(t *testing.T, body []byte, index int) []byte {
								if index > 0 {
									return body
								}
								// Independent oracle for the only permitted repository-layer
								// rewrite on ordinary A only. Rebuilt B bodies stay exact. Compare ALL
								// other fields, tool declarations, arguments/results unchanged.
								for i, item := range gjson.GetBytes(body, "input").Array() {
									if item.Get("content").String() == "<environment_context><timezone>Asia/Shanghai</timezone></environment_context>" {
										var err error
										body, err = sjson.SetBytes(body, fmt.Sprintf("input.%d.content", i), "<environment_context><timezone>Asia/Tokyo</timezone></environment_context>")
										require.NoError(t, err)
									}
								}
								return body
							}
						}
						option.headers = func(r *http.Request) { r.Header.Set("X-Timezone", "Asia/Shanghai") }
						option.settings = &issue854TimezoneSettings{omit409SettingsRepo: omit409SettingsRepo{enabled: true}, auto: transform == "timezone_auto"}
						option.accounts = func(accounts []service.Account) {
							for i := range accounts {
								if accounts[i].Extra == nil {
									accounts[i].Extra = map[string]any{}
								}
								accounts[i].Extra["openai_request_timezone"] = "Asia/Tokyo"
							}
						}
						option.gateway = func(h *OpenAIGatewayHandler, accounts []service.Account, upstream service.HTTPUpstream, cache service.GatewayCache, settings *service.SettingService, usage service.UsageLogRepository) {
							h.gatewayService = service.ProvideOpenAIGatewayService(service.OpenAIGatewayDependencies{Accounts: openAIImagesFailoverAccountRepo{accounts: accounts}, Config: h.cfg, Upstream: upstream, Cache: cache, Settings: settings, UsageLogs: usage, Billing: service.NewBillingService(h.cfg, nil), Deferred: &service.DeferredService{}, TimezoneProber: issue854TimezoneProbe{}})
						}
					}
					if transform == "namespace_flatten" {
						option.accounts = func(accounts []service.Account) {
							for i := range accounts {
								if accounts[i].Extra == nil {
									accounts[i].Extra = map[string]any{}
								}
								accounts[i].Extra["openai_responses_flatten_namespaces"] = true
							}
						}
					}
					if transform == "lite" {
						option.headers = func(r *http.Request) { r.Header.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true") }
					}
					if transform == "reserved_alias" || transform == "namespace_declaration" || transform == "namespace_flatten" {
						option.toolsAvailable = func(body []byte) bool {
							tools := gjson.GetBytes(body, "tools").Array()
							if len(tools) != 3 {
								return false
							}
							extra := tools[2]
							name := "python"
							if account == service.AccountTypeOAuth {
								name = "python__sub2api"
							}
							if strings.HasPrefix(transform, "namespace_") && !(transform == "namespace_flatten" && account == service.AccountTypeOAuth) {
								if extra.Get("type").String() != "namespace" || extra.Get("name").String() != "fixture_tools" {
									return false
								}
								extra = extra.Get("tools.0")
							}
							if transform == "namespace_flatten" && account == service.AccountTypeOAuth {
								name = "fixture_tools__python"
							}
							if extra.Get("name").String() != name || extra.Get("parameters.properties.code.type").String() != "string" {
								return false
							}
							core, err := sjson.DeleteBytes(body, "tools.2")
							return err == nil && httpRebuildToolsAvailable(core)
						}
					}
					runHTTPRebuildFixture(t, account, stream, "previous", "completed", "issue854_auth", "", option)
				})
			}
		}
	}
}

// Only the geolocation fact is fake. Repository BindOpenAIRequestTimezone runs
// on the actual loopback send after transport selection; provider reads bytes.
type issue854TimezoneSettings struct {
	omit409SettingsRepo
	auto bool
}

func (s *issue854TimezoneSettings) GetValue(ctx context.Context, key string) (string, error) {
	if key == service.SettingKeyOpenAIRequestTimezoneEnabled {
		return "true", nil
	}
	if key == service.SettingKeyOpenAIRequestTimezoneAuto {
		if s.auto {
			return "true", nil
		}
		return "false", nil
	}
	return s.omit409SettingsRepo.GetValue(ctx, key)
}

type issue854TimezoneProbe struct{}

func (issue854TimezoneProbe) ProbeProxy(context.Context, string) (*service.ProxyExitInfo, int64, error) {
	return &service.ProxyExitInfo{IP: "192.0.2.1", Timezone: "Asia/Tokyo"}, 0, nil
}

func TestIssue854HTTPRouteAdmission(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, fallback := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/native_available=%t", stream, fallback), func(t *testing.T) {
				option := httpRebuildFixtureOptions{}
				if fallback {
					extra := issue715AccountsOfType(service.AccountTypeAPIKey)[1]
					extra.ID, extra.Priority, extra.Schedulable = 3, -100, false
					extra.Credentials = map[string]any{"api_key": "fixture-unsupported-never-send"}
					extra.Extra = map[string]any{"openai_copilot_sdk": true}
					option.extraAccounts = []service.Account{extra}
					option.afterFirst = func(accounts []service.Account) {
						accounts[2].Schedulable = true
						accounts[2].Credentials["base_url"] = accounts[1].Credentials["base_url"]
					}
				} else {
					option.afterFirst = func(accounts []service.Account) {
						if accounts[1].Extra == nil {
							accounts[1].Extra = map[string]any{}
						}
						accounts[1].Extra["openai_copilot_sdk"] = true
					}
					option.refuseCode = "Service temporarily unavailable"
				}
				runHTTPRebuildFixture(t, service.AccountTypeAPIKey, stream, "previous", "completed", "issue854_auth", "", option)
			})
		}
	}
}
