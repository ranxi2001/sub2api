//go:build unit && bridgewarmup

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	ws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHTTPBridgeLargeHistoryToolResult(t *testing.T) {
	for _, tc := range []struct {
		name, account, previous, call string
		size                          int
		accept                        bool
	}{
		{"api_key_large", service.AccountTypeAPIKey, "resp_one", "turn_one", (8 << 20) + 1024, true},
		{"oauth_large", service.AccountTypeOAuth, "resp_one", "turn_one", (16 << 20) + 1024, true},
		{"oauth_wrong_response", service.AccountTypeOAuth, "resp_foreign", "turn_one", 256, false},
		{"oauth_unobserved_call", service.AccountTypeOAuth, "resp_one", "never_delivered", 256, false},
		{"oauth_old_call", service.AccountTypeOAuth, "resp_one", "prior", 256, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				n := calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				if n == 1 {
					fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_one","status":"completed","output":[{"type":"function_call","name":"lookup","call_id":"turn_one","arguments":"{}","status":"completed"}],"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n\n")
					return
				}
				if !tc.accept || n != 2 || gjson.GetBytes(body, "previous_response_id").Exists() ||
					gjson.GetBytes(body, `input.#(call_id=="turn_one")#.type`).Raw != `["function_call","function_call_output"]` ||
					gjson.GetBytes(body, `input.#(call_id=="prior")#.type`).Raw != `["function_call","function_call_output"]` ||
					gjson.GetBytes(body, `input.#(type=="function_call_output")#.output`).Raw != `["old receipt","new receipt"]` {
					t.Error("unexpected bridge payload or repeated dispatch")
				}
				fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_two","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`+"\n\n")
			}))
			defer upstream.Close()
			h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, upstream.URL, http.Header{"Session-Id": {"fixture-tool-result"}}, map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}, nil, func(k *service.APIKey) { k.AllowLongContext = true }, func(a *service.Account, cfg *config.Config, transport *service.HTTPUpstream, _ *service.AccountRepository) {
				a.Extra["openai_apikey_responses_websockets_v2_mode"] = service.OpenAIWSIngressModeHTTPBridge
				if tc.account == service.AccountTypeOAuth {
					a.Type = tc.account
					a.Credentials = map[string]any{"access_token": "fixture-token", "chatgpt_account_id": "fixture-account"}
					a.Extra["openai_oauth_responses_websockets_v2_enabled"] = true
					a.Extra["openai_oauth_responses_websockets_v2_mode"] = service.OpenAIWSIngressModeCtxPool
					cfg.Gateway.OpenAIWS.OAuthEnabled = true
					cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
					cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 128
					target, err := url.Parse(upstream.URL)
					require.NoError(t, err)
					*transport = &httpRebuildTransport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
				}
			})
			defer h.clientConn.CloseNow()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			first := `{"type":"response.create","model":"gpt-5.1","store":false,"input":[{"role":"user","content":"` + strings.Repeat("x", tc.size) + `"},{"type":"function_call","name":"lookup","call_id":"prior","arguments":"{}","status":"completed"},{"type":"function_call_output","call_id":"prior","output":"old receipt"}]}`
			require.NoError(t, h.clientConn.Write(ctx, ws.MessageText, []byte(first)))
			_, raw, err := h.clientConn.Read(ctx)
			require.NoError(t, err)
			require.Equal(t, "resp_one", gjson.GetBytes(raw, "response.id").String())
			next := fmt.Sprintf(`{"type":"response.create","model":"gpt-5.1","store":false,"previous_response_id":%q,"input":[{"type":"function_call_output","call_id":%q,"output":"new receipt"}]}`, tc.previous, tc.call)
			require.NoError(t, h.clientConn.Write(ctx, ws.MessageText, []byte(next)))
			_, raw, err = h.clientConn.Read(ctx)
			if tc.accept {
				require.NoError(t, err, "BRIDGE_TOOL_RESULT_REFUSED")
				require.Equal(t, "resp_two", gjson.GetBytes(raw, "response.id").String(), "BRIDGE_TOOL_RESULT_REFUSED")
				require.EqualValues(t, 2, calls.Load())
				return
			}
			if err == nil {
				require.True(t, json.Valid(raw))
				require.Equal(t, "error", gjson.GetBytes(raw, "type").String())
			} else {
				require.NotEqual(t, ws.StatusCode(-1), ws.CloseStatus(err))
			}
			require.EqualValues(t, 1, calls.Load())
		})
	}
}
