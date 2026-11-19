//go:build unit

package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func issue914Policy(t *testing.T, cfg *config.Config, enabled bool) {
	t.Helper()
	require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{"OpenAILegacyCompatibilityMode":%t,"DisableEncryptedAgentOwnerBinding":true}`, enabled)), &cfg.Gateway))
}

func issue914Settings() map[string]string {
	settings := issue892Settings()
	// Legacy takes precedence even when the saved recovery settings are on.
	settings[service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled] = "true"
	settings[service.SettingKeyOpenAI400OmitEncryptedHistoryEnabled] = "true"
	return settings
}

func issue914Body(t *testing.T, change string, stream bool) string {
	t.Helper()
	body := issue715Body(false, stream)
	set := func(path string, value any) {
		var err error
		body, err = sjson.Set(body, path, value)
		require.NoError(t, err)
	}
	raw := func(path, value string) {
		var err error
		body, err = sjson.SetRaw(body, path, value)
		require.NoError(t, err)
	}
	set("store", false)
	set("instructions", "Continue the synthetic task.")
	set("client_metadata.x-codex-window-id", "fixture-window-1")
	switch change {
	case "metadata":
		set("input.0.internal_chat_message_metadata_passthrough", map[string]any{"turn_id": "fixture-turn"})
	case "schema", "reserved_name", "declaration_id":
		name, schema, id := "lookup", `{"type":"object","properties":{}}`, "at_11111111222243338444555555555555"
		if change == "schema" {
			schema = `{"type":null,"required":null}`
		}
		if change == "reserved_name" {
			name = "python"
		}
		if change == "declaration_id" {
			id = "at_11111111-2222-4333-8444-555555555555"
		}
		raw("input.-1", fmt.Sprintf(`{"type":"additional_tools","role":"developer","id":%q,"tools":[{"type":"function","name":%q,"parameters":%s}]}`, id, name, schema))
	case "lite_namespace":
		raw("tools", `[{"type":"namespace","name":"fixture_tools","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}]}]`)
	case "lite_search":
		raw("input.-1", `{"type":"web_search_call","id":"ws_fixture","status":"completed","action":{"type":"search","query":"fixture"}}`)
	case "system":
		set("input.1.role", "system")
	case "namespace":
		set("input.1.namespace", "fixture_namespace")
	case "orphan":
		raw("input.-1", `{"type":"custom_tool_call_output","call_id":"call_fixture_missing","output":"fixture output"}`)
	case "empty_image":
		raw("input.1.content", `[{"type":"input_text","text":"Continue fixture"},{"type":"input_image","image_url":"data:image/png;base64,"}]`)
	case "compaction", "compaction_order":
		set("input.-1", map[string]any{"role": "user", "content": "<environment_context>\n<timezone>Asia/Singapore</timezone>\n</environment_context>"})
		raw("input.-1", `{"type":"compaction_trigger"}`)
		if change == "compaction_order" {
			raw("input.-1", `{"role":"user","content":"Continue fixture"}`)
		}
	case "spark", "spark_image":
		set("model", "gpt-5.3-codex-spark")
		if change == "spark_image" {
			raw("input.-1", `{"type":"additional_tools","role":"developer","id":"at_fixture_image","tools":[{"type":"image_generation"}]}`)
		}
	case "custom_bridge":
		raw("tools", `[{"type":"custom","name":"fixture_custom","format":{"type":"text"}}]`)
		raw("input.-1", `{"type":"custom_tool_call","call_id":"call_fixture_pair","name":"fixture_custom","input":"fixture argument"}`)
		raw("input.-1", `{"type":"custom_tool_call_output","call_id":"call_fixture_pair","output":"fixture result"}`)
	}
	return body
}

func issue914HTTPNode(t *testing.T, u *issue805Oracle, legacy bool, accounts []service.Account) (*OpenAIGatewayHandler, *readable409UsageRepo) {
	t.Helper()
	srv := u.server(t)
	target, err := url.Parse(srv.URL)
	require.NoError(t, err)
	for i := range accounts {
		accounts[i].Credentials = map[string]any{"api_key": fmt.Sprintf("fixture-ws-%d", i), "access_token": fmt.Sprintf("fixture-ws-%d", i), "base_url": srv.URL}
		accounts[i].Extra = map[string]any{"openai_request_timezone": "America/Los_Angeles", "codex_fingerprint_mode": "off"}
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	upstream := &issue842Transport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
	usage := &readable409UsageRepo{}
	clients := issue805Redis(t)
	settings := issue914Settings()
	if !legacy {
		settings = issue892Settings()
	}
	h := newOpenAIResponsesFailoverTestHandlerWithUsage(t, upstream, repository.NewGatewayCache(clients()), service.NewSettingService(&issue842Settings{values: settings}, cfg), usage, accounts...)
	h.cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	issue914Policy(t, h.cfg, legacy)
	return h, usage
}

func TestIssue914LegacyHTTPCompatibility(t *testing.T) {
	for _, change := range []string{"metadata", "schema", "reserved_name", "lite_namespace", "lite_search", "system", "namespace", "orphan", "empty_image", "compaction", "spark", "spark_image"} {
		for _, stream := range []bool{false, true} {
			// Non-streaming compaction is a separate unary endpoint, not native v2.
			if change == "compaction" && !stream {
				continue
			}
			t.Run(fmt.Sprintf("%s/stream=%t", change, stream), func(t *testing.T) {
				u := &issue805Oracle{}
				h, usage := issue914HTTPNode(t, u, true, issue715Accounts()[:1])
				body := issue914Body(t, change, stream)
				c, rec := newIssue715Context(t, body)
				key, exists := c.Get(string(middleware.ContextKeyAPIKey))
				require.True(t, exists)
				key.(*service.APIKey).Group.AllowImageGeneration = true
				c.Request.Header.Set("session_id", "fixture-914")
				if change == "lite_namespace" || change == "lite_search" {
					c.Request.Header.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
				}
				h.Responses(c)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				issue758CompletedResponse(t, rec.Body.String(), stream)
				u.mu.Lock()
				frames := append([][]byte(nil), u.frames...)
				u.mu.Unlock()
				require.Len(t, frames, 1, "exactly one actual upstream request")
				require.NotContains(t, string(frames[0]), "internal_chat_message_metadata_passthrough")
				require.Contains(t, string(frames[0]), "fixture-task-reference-715", "legacy must not activate the saved 409 omission option")
				if change == "compaction" {
					require.Equal(t, "compaction_trigger", gjson.GetBytes(frames[0], "input.@reverse.0.type").String())
				}
				require.Eventually(t, func() bool { return len(usage.snapshot()) == 1 }, 2*time.Second, 10*time.Millisecond)
				row := usage.snapshot()[0]
				require.EqualValues(t, 5, row.InputTokens)
				require.EqualValues(t, 3, row.OutputTokens)
				require.Greater(t, row.TotalCost, 0.0)
			})
		}
	}
}

func TestIssue914DefaultStillRejectsFrozenMutation(t *testing.T) {
	u := &issue805Oracle{}
	h, _ := issue914HTTPNode(t, u, false, issue715Accounts()[:1])
	answer := issue758Request(t, h, issue914Body(t, "metadata", true))
	require.Equal(t, http.StatusConflict, answer.Code, answer.Body.String())
	require.Contains(t, answer.Body.String(), "encrypted_agent_recovery_wire_changed")
	u.mu.Lock()
	defer u.mu.Unlock()
	require.Empty(t, u.frames)
}

func issue914WSNode(t *testing.T, kind, mode, upstream string, cache service.GatewayCache) *openAIWSPassthroughHandlerHarness {
	t.Helper()
	target, err := url.Parse(upstream)
	require.NoError(t, err)
	h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, upstream,
		http.Header{"Session_id": {"fixture-914"}, "Thread-Id": {"fixture-914"}}, issue914Settings(), cache,
		func(key *service.APIKey) { key.AllowLongContext = true },
		func(a *service.Account, cfg *config.Config, httpUpstream *service.HTTPUpstream, _ *service.AccountRepository) {
			a.Type = kind
			a.Credentials = map[string]any{"api_key": "fixture-ws-one", "access_token": "fixture-ws-one", "base_url": upstream, "chatgpt_account_id": "fixture914"}
			a.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			a.Extra["openai_oauth_responses_websockets_v2_enabled"] = true
			a.Extra["openai_oauth_responses_websockets_v2_mode"] = mode
			a.Extra["openai_request_timezone"] = "America/Los_Angeles"
			a.Extra["codex_fingerprint_mode"] = "off"
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 15
			issue914Policy(t, cfg, true)
			*httpUpstream = &issue842Transport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
		})
	if kind == service.AccountTypeOAuth {
		require.NoError(t, service.RouteIssue842OAuthSocketsToLoopbackForTest(h.handler.gatewayService, upstream))
	}
	return h
}

func TestIssue914LegacyWSCompatibility(t *testing.T) {
	for _, kind := range []string{service.AccountTypeOAuth, service.AccountTypeAPIKey} {
		for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeHTTPBridge} {
			for _, change := range []string{"metadata", "declaration_id", "schema", "reserved_name", "lite_namespace", "lite_search", "compaction_order", "custom_bridge", "instructions", "window", "plaintext_instructions"} {
				t.Run(kind+"/"+mode+"/"+change, func(t *testing.T) {
					clients := issue805Redis(t)
					u := &issue805Oracle{}
					srv := u.server(t)
					h := issue914WSNode(t, kind, mode, srv.URL, repository.NewGatewayCache(clients()))
					body := issue914Body(t, change, true)
					if change == "lite_namespace" || change == "lite_search" {
						var err error
						body, err = sjson.Set(body, "client_metadata.ws_request_header_x_openai_internal_codex_responses_lite", "true")
						require.NoError(t, err)
					}
					if change == "plaintext_instructions" {
						var err error
						body, err = sjson.Delete(body, "input.0")
						require.NoError(t, err)
					}
					body, err := sjson.Set(body, "type", "response.create")
					require.NoError(t, err)
					turns := 1
					if change == "instructions" || change == "window" || change == "plaintext_instructions" {
						turns = 2
					}
					for turn := 0; turn < turns; turn++ {
						issue715WSSend(t, h.clientConn, body)
						answer := issue715WSRead(t, h.clientConn)
						require.Equal(t, "response.completed", gjson.GetBytes(answer, "type").String(), string(answer))
						if turns == 2 && turn == 0 {
							body, err = sjson.Set(body, "previous_response_id", gjson.GetBytes(answer, "response.id").String())
							require.NoError(t, err)
							field := "instructions"
							if change == "window" {
								field = "client_metadata.x-codex-window-id"
							}
							body, err = sjson.Set(body, field, "fixture-next-turn")
							require.NoError(t, err)
						}
						select {
						case row := <-h.usageLogs:
							require.EqualValues(t, 5, row.InputTokens)
							require.EqualValues(t, 3, row.OutputTokens)
							require.Greater(t, row.TotalCost, 0.0)
						case <-time.After(3 * time.Second):
							t.Fatal("missing completed-turn usage")
						}
					}
					issue600Finish(t, h, http.Header{"Session_id": {"fixture-914"}, "Thread-Id": {"fixture-914"}})
					u.mu.Lock()
					defer u.mu.Unlock()
					require.Len(t, u.frames, turns)
					if mode == service.OpenAIWSIngressModeHTTPBridge {
						require.Equal(t, turns, u.http)
					} else {
						require.Equal(t, turns, u.ws)
					}
					if change == "custom_bridge" && mode == service.OpenAIWSIngressModeHTTPBridge && kind == service.AccountTypeAPIKey {
						require.Contains(t, string(u.frames[0]), `"type":"function_call"`)
						require.NotContains(t, string(u.frames[0]), `"type":"custom_tool_call"`)
					}
					require.Empty(t, issue805Owners(t, clients()), "legacy must not publish strict ownership evidence")
				})
			}
		}
	}
}

func TestIssue914LegacyUpstreamRejectionIsPreserved(t *testing.T) {
	u := &issue805Oracle{rejection: "fixture_provider_error", rejectionStatus: 400}
	h, _ := issue914HTTPNode(t, u, true, issue715Accounts()[:1])
	answer := issue758Request(t, h, issue914Body(t, "metadata", false))
	require.Equal(t, http.StatusBadRequest, answer.Code, answer.Body.String())
	require.Contains(t, answer.Body.String(), "fixture_provider_error")
	u.mu.Lock()
	defer u.mu.Unlock()
	require.Len(t, u.frames, 1)
}

func TestIssue914LegacyAccountPoolAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint("canceled=", canceled), func(t *testing.T) {
			u := &openAIResponsesFailoverCancelUpstream{}
			h := newOpenAIResponsesFailoverTestHandler(t, u, issue715Accounts()...)
			issue914Policy(t, h.cfg, true)
			status, answer := issue715Request(t, h, issue715Body(true, false), 99, "fixture-914", canceled)
			require.NotContains(t, answer, "encrypted_agent_recovery_")
			calls := u.calls()
			if canceled {
				require.Empty(t, calls)
			} else {
				require.GreaterOrEqual(t, status, 400)
				require.Contains(t, calls, int64(1))
				require.Contains(t, calls, int64(2), "ordinary pre-output failure must be able to select another eligible account")
			}
		})
	}
}

func TestIssue914LegacyAPIKeyHTTP(t *testing.T) {
	for _, change := range []string{"schema", "namespace", "empty_image", "custom_bridge"} {
		t.Run(change, func(t *testing.T) {
			u := &issue805Oracle{}
			h, usage := issue914HTTPNode(t, u, true, issue715AccountsOfType(service.AccountTypeAPIKey)[:1])
			answer := issue758Request(t, h, issue914Body(t, change, true))
			require.Equal(t, http.StatusOK, answer.Code, answer.Body.String())
			issue758CompletedResponse(t, answer.Body.String(), true)
			u.mu.Lock()
			count := len(u.frames)
			u.mu.Unlock()
			require.Equal(t, 1, count)
			require.Eventually(t, func() bool { return len(usage.snapshot()) == 1 }, 2*time.Second, 10*time.Millisecond)
			require.EqualValues(t, 5, usage.snapshot()[0].InputTokens)
		})
	}
}

func TestIssue914LegacyKeepsImagePermission(t *testing.T) {
	u := &issue805Oracle{}
	h, _ := issue914HTTPNode(t, u, true, issue715Accounts()[:1])
	body := issue914Body(t, "metadata", false)
	body, err := sjson.SetRaw(body, "tools", `[{"type":"image_generation"}]`)
	require.NoError(t, err)
	answer := issue758Request(t, h, body)
	require.Equal(t, http.StatusForbidden, answer.Code, answer.Body.String())
	u.mu.Lock()
	defer u.mu.Unlock()
	require.Empty(t, u.frames)
}
