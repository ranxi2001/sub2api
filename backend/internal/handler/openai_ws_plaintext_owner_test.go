//go:build unit

package handler

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Real client and upstream sockets: the second frame carries only a completed
// tool result and the response pointer, never a fabricated full-history pair.
func TestOpenAIWSPlaintextToolContinuation(t *testing.T) {
	for _, mode := range readableWSModes {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			var frames [][]byte
			response := func(body []byte) []byte {
				mu.Lock()
				defer mu.Unlock()
				frames = append(frames, append([]byte(nil), body...))
				if len(frames) == 1 {
					return []byte(`{"type":"response.completed","response":{"id":"resp_plaintext_first","model":"gpt-5.1","status":"completed","output":[{"type":"function_call","call_id":"call_plaintext_once","name":"fixture","arguments":"{}","status":"completed"}],"usage":{"input_tokens":5,"output_tokens":3}}}`)
				}
				return []byte(`{"type":"response.completed","response":{"id":"resp_plaintext_second","model":"gpt-5.1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture complete"}]}],"usage":{"input_tokens":6,"output_tokens":2}}}`)
			}
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", response(body))
					return
				}
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				for {
					_, body, err := conn.Read(r.Context())
					if err != nil {
						return
					}
					if err := conn.Write(r.Context(), coderws.MessageText, response(body)); err != nil {
						return
					}
				}
			}))
			defer provider.Close()
			clients := issue805Redis(t)
			headers := http.Header{"Session_id": {"fixture-plaintext-session"}, "Thread-Id": {"fixture-plaintext-thread"}}
			h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, provider.URL, headers,
				map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"},
				repository.NewGatewayCache(clients()), func(key *service.APIKey) { key.AllowLongContext = true },
				func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, _ *service.AccountRepository) {
					a.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
					a.Extra["codex_fingerprint_mode"] = "off"
					cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 15
				})
			defer issue600Finish(t, h, headers)
			issue715WSSend(t, h.clientConn, `{"type":"response.create","model":"gpt-5.1","store":true,"input":[{"role":"user","content":"Run the fixture once"}],"tools":[{"type":"function","name":"fixture","parameters":{"type":"object","properties":{}}}]}`)
			first := issue715WSRead(t, h.clientConn)
			require.Equal(t, "response.completed", gjson.GetBytes(first, "type").String())
			require.Equal(t, "call_plaintext_once", gjson.GetBytes(first, "response.output.0.call_id").String())
			issue715WSSend(t, h.clientConn, `{"type":"response.create","model":"gpt-5.1","store":true,"previous_response_id":"resp_plaintext_first","input":[{"type":"function_call_output","call_id":"call_plaintext_once","output":"fixture applied once"}]}`)
			second := issue715WSRead(t, h.clientConn)
			require.Equal(t, "response.completed", gjson.GetBytes(second, "type").String(), "PLAINTEXT_TOOL_CONTINUATION_REFUSED: %s", second)
			require.Equal(t, "resp_plaintext_second", gjson.GetBytes(second, "response.id").String())
			mu.Lock()
			actual := append([][]byte(nil), frames...)
			mu.Unlock()
			require.Len(t, actual, 2, "exactly one generation per client turn; never replay the executed tool")
			require.Contains(t, string(actual[1]), "fixture applied once")
			if mode == service.OpenAIWSIngressModePassthrough {
				require.Equal(t, "resp_plaintext_first", gjson.GetBytes(actual[1], "previous_response_id").String())
				require.Len(t, gjson.GetBytes(actual[1], "input").Array(), 1, "native delta must remain a delta")
			}
			require.Zero(t, h.requestLogs.FilterMessage("openai.agent_recovery_refused").Len())
		})
	}
}
