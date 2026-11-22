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

const issue999Message = `{"type":"message","id":"msg_fixture_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"synthetic answer 1","annotations":[]}]}`
const issue999Call = `{"type":"function_call","id":"fc_fixture_1","call_id":"call_fixture_1","name":"fixture_lookup","arguments":"{}","status":"completed"}`
const issue999Extra = `{"type":"message","id":"msg_fixture_extra","role":"assistant","status":"completed","content":[{"type":"output_text","text":"streamed extra","annotations":[]}]}`
const issue999Second = `{"type":"message","id":"msg_fixture_2","role":"assistant","status":"completed","content":[{"type":"output_text","text":"synthetic answer 2","annotations":[]}]}`
const issue999Result = `{"type":"function_call_output","call_id":"call_fixture_1","output":"synthetic result"}`
const issue999Tools = `[{"type":"function","name":"fixture_lookup","parameters":{"type":"object","properties":{},"additionalProperties":false},"strict":true}]`

func issue999OAuthHarness(t *testing.T, upstreamURL string, headers http.Header, legacy bool, threshold int64) *openAIWSPassthroughHandlerHarness {
	t.Helper()
	return newOpenAIWSPassthroughHandlerHarnessWithKey(t, upstreamURL, headers,
		map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI400OmitEncryptedHistoryEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "false"}, nil,
		func(k *service.APIKey) { k.AllowLongContext = true },
		func(a *service.Account, cfg *config.Config, transport *service.HTTPUpstream, _ *service.AccountRepository) {
			a.Type = service.AccountTypeOAuth
			a.Credentials = map[string]any{"access_token": "fixture-token", "chatgpt_account_id": "fixture-account"}
			a.Extra["openai_oauth_responses_websockets_v2_enabled"] = true
			a.Extra["openai_oauth_responses_websockets_v2_mode"] = service.OpenAIWSIngressModeCtxPool
			cfg.Gateway.OpenAILegacyCompatibilityMode = legacy
			cfg.Gateway.DisableEncryptedAgentOwnerBinding = true
			cfg.Gateway.OpenAIWS.OAuthEnabled = true
			cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
			cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = threshold
			cfg.Gateway.OpenAIWS.ClientReadLimitBytes = 64 << 20
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 60
			target, err := url.Parse(upstreamURL)
			require.NoError(t, err)
			*transport = &httpRebuildTransport{HTTPUpstream: testutil.NewRealHTTPUpstream(cfg), target: target}
		})
}

func issue999ReadCompleted(t *testing.T, ctx context.Context, conn *ws.Conn, id string) {
	t.Helper()
	for i := 0; i < 16; i++ {
		_, raw, err := conn.Read(ctx)
		require.NoError(t, err, "ISSUE999_CONTINUATION_REFUSED")
		kind := gjson.GetBytes(raw, "type").String()
		require.NotEqual(t, "error", kind, string(raw))
		if kind == "response.completed" {
			require.Equal(t, id, gjson.GetBytes(raw, "response.id").String())
			return
		}
	}
	t.Fatal("missing completed event")
}

func TestIssue999HTTPBridgeAssistantHistory(t *testing.T) {
	for _, tc := range []struct {
		name         string
		full, legacy bool
		size         int
	}{
		{"empty_terminal", false, true, 24 << 20},
		{"full_terminal", true, true, 24 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			bodies := make(chan []byte, 4)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				bodies <- raw
				n := calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				output := "[]"
				if n == 1 {
					// Deliberately out of order, repeated at the same index and
					// repeated under a different index. All delivered events remain visible.
					for _, e := range []struct {
						index int
						item  string
					}{{2, issue999Extra}, {1, issue999Call}, {0, issue999Message}, {1, issue999Call}, {3, issue999Message}} {
						fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":%s}\n\n", e.index, e.item)
					}
					if tc.full {
						// Terminal order/content/membership override all done events.
						output = "[" + issue999Call + "," + strings.ReplaceAll(issue999Message, "synthetic answer 1", "authoritative answer 1") + "]"
					}
				} else if n == 2 {
					output = "[" + issue999Second + "]"
				}
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture_%d\",\"status\":\"completed\",\"output\":%s,\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", n, output)
			}))
			defer upstream.Close()
			threshold := int64(128)
			if tc.legacy {
				threshold = 15 << 20
			}
			h := issue999OAuthHarness(t, upstream.URL, http.Header{"Session-Id": {"issue999-session"}, "Thread-Id": {"issue999-thread"}}, tc.legacy, threshold)
			defer h.clientConn.CloseNow()
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			prior := []string{
				`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"prior context"}]}`,
				`{"type":"function_call","call_id":"prior","name":"fixture_lookup","arguments":"{}","status":"completed"}`,
				`{"type":"function_call_output","call_id":"prior","output":"prior receipt"}`,
				`{"type":"message","role":"user","content":"` + strings.Repeat("p", tc.size) + `"}`,
			}
			first := `{"type":"response.create","model":"gpt-6.1-sol","stream":true,"store":false,"tools":` + issue999Tools + `,"input":[` + strings.Join(prior, ",") + `]}`
			require.NoError(t, h.clientConn.Write(ctx, ws.MessageText, []byte(first)))
			issue999ReadCompleted(t, ctx, h.clientConn, "resp_fixture_1")
			firstBody := <-bodies
			require.False(t, gjson.GetBytes(firstBody, "previous_response_id").Exists())
			// Capture accepted input after ordinary request normalization. Subsequent
			// exact item comparisons ensure every prior item survives once.
			var want []json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(gjson.GetBytes(firstBody, "input").Raw), &want))
			require.Len(t, want, len(prior))
			if tc.full {
				want = append(want, json.RawMessage(issue999Call), json.RawMessage(strings.ReplaceAll(issue999Message, "synthetic answer 1", "authoritative answer 1")))
			} else {
				want = append(want, json.RawMessage(issue999Message), json.RawMessage(issue999Call), json.RawMessage(issue999Extra))
			}
			want = append(want, json.RawMessage(issue999Result))
			next := `{"type":"response.create","model":"gpt-6.1-sol","stream":true,"store":false,"tools":` + issue999Tools + `,"previous_response_id":"resp_fixture_1","input":[` + issue999Result + `]}`
			require.NoError(t, h.clientConn.Write(ctx, ws.MessageText, []byte(next)))
			issue999ReadCompleted(t, ctx, h.clientConn, "resp_fixture_2")
			assertInput := func(raw []byte) {
				t.Helper()
				require.False(t, gjson.GetBytes(raw, "previous_response_id").Exists())
				var got []json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(gjson.GetBytes(raw, "input").Raw), &got))
				require.Len(t, got, len(want), "ISSUE999_ASSISTANT_HISTORY_LOST")
				for i := range want {
					require.JSONEq(t, string(want[i]), string(got[i]), "ISSUE999_ASSISTANT_HISTORY_LOST item=%d", i)
				}
				require.Equal(t, `["function_call","function_call_output"]`, gjson.GetBytes(raw, `input.#(call_id=="call_fixture_1")#.type`).Raw)
				require.Equal(t, `["function_call","function_call_output"]`, gjson.GetBytes(raw, `input.#(call_id=="prior")#.type`).Raw)
			}
			assertInput(<-bodies)
			const user3 = `{"type":"message","role":"user","content":"third round"}`
			want = append(want, json.RawMessage(issue999Second), json.RawMessage(user3))
			third := `{"type":"response.create","model":"gpt-6.1-sol","stream":true,"store":false,"tools":` + issue999Tools + `,"previous_response_id":"resp_fixture_2","input":[` + user3 + `]}`
			require.NoError(t, h.clientConn.Write(ctx, ws.MessageText, []byte(third)))
			issue999ReadCompleted(t, ctx, h.clientConn, "resp_fixture_3")
			assertInput(<-bodies)
			require.EqualValues(t, 3, calls.Load())
		})
	}
}

func TestIssue999HTTPBridgeRejectsForeignContinuation(t *testing.T) {
	for _, tc := range []struct{ name, previous, thread string }{
		{"wrong_pointer", "resp_foreign", "thread-one"},
		{"wrong_session", "resp_fixture_1", "thread-two"},
		{"omitted_identity", "resp_fixture_1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture_1\",\"status\":\"completed\",\"output\":[%s]}}\n\n", issue999Message)
			}))
			defer upstream.Close()
			h := issue999OAuthHarness(t, upstream.URL, nil, true, 1)
			defer h.clientConn.CloseNow()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			first := `{"type":"response.create","model":"gpt-6.1-sol","store":false,"client_metadata":{"thread_id":"thread-one"},"input":[{"role":"user","content":"fixture"}]}`
			require.NoError(t, h.clientConn.Write(ctx, ws.MessageText, []byte(first)))
			issue999ReadCompleted(t, ctx, h.clientConn, "resp_fixture_1")
			next := fmt.Sprintf(`{"type":"response.create","model":"gpt-6.1-sol","store":false,"previous_response_id":%q,"client_metadata":{"thread_id":%q},"input":[{"role":"user","content":"delta"}]}`, tc.previous, tc.thread)
			require.NoError(t, h.clientConn.Write(ctx, ws.MessageText, []byte(next)))
			if tc.thread == "" {
				issue999ReadCompleted(t, ctx, h.clientConn, "resp_fixture_1")
				require.EqualValues(t, 2, calls.Load())
				return
			}
			_, raw, err := h.clientConn.Read(ctx)
			if err == nil {
				require.Equal(t, "error", gjson.GetBytes(raw, "type").String())
			} else {
				require.NotEqual(t, ws.StatusCode(-1), ws.CloseStatus(err))
			}
			require.EqualValues(t, 1, calls.Load(), "foreign continuation must not dispatch")
		})
	}
}
