//go:build unit && plaintextowner

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Every assertion observes a later real handler turn and the HTTP provider
// wire, including refusal before a second generation can start.
func TestOpenAIWSPlaintextRepair870(t *testing.T) {
	for _, scenario := range []string{"assistant_history", "wrong_response", "terminal_excludes", "ordinary_tool", "empty_terminal", "duplicate_call", "stored_pointer", "unknown_pointer"} {
		t.Run(scenario, func(t *testing.T) {
			const message = `{"type":"message","id":"msg_fixture_exact","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Remember fixture M","annotations":[]}],"fixture_number":9007199254740993}`
			const call = `{"type":"function_call","call_id":"call_fixture_repair","name":"fixture","arguments":"{}","status":"completed"}`
			const tools = `[{"type":"function","name":"fixture","parameters":{"type":"object","properties":{"n":{"type":"integer","maximum":9007199254740993}}}}]`
			var mu sync.Mutex
			var frames [][]byte
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				frames = append(frames, append([]byte(nil), body...))
				n := len(frames)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				output := message
				if n == 1 && scenario == "ordinary_tool" {
					output = call
				}
				if n <= 2 && scenario == "duplicate_call" {
					output = call
				}
				if n == 1 && scenario == "assistant_history" {
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"item\":%s}\n\n", strings.Replace(message, "Remember fixture M", "Earlier streamed text", 1))
				}
				if n == 1 && (scenario == "terminal_excludes" || scenario == "empty_terminal") {
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"item\":%s}\n\n", call)
					if scenario == "empty_terminal" {
						output = ""
					}
				}
				_, _ = fmt.Fprintf(w, `data: {"type":"response.completed","response":{"id":"resp_fixture_repair_%d","model":"gpt-5.1","status":"completed","output":[%s],"usage":{"input_tokens":5,"output_tokens":3}}}`+"\n\n", n, output)
			}))
			defer provider.Close()
			clients := issue805Redis(t)
			cache := repository.NewGatewayCache(clients())
			headers := http.Header{"Session_id": {"fixture-repair-session"}, "Thread-Id": {"fixture-repair-thread"}}
			var accountID int64
			h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, provider.URL, headers,
				map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}, cache,
				func(key *service.APIKey) { key.AllowLongContext = true },
				func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, _ *service.AccountRepository) {
					accountID = a.ID
					a.Extra["openai_apikey_responses_websockets_v2_mode"] = service.OpenAIWSIngressModeHTTPBridge
					a.Extra["codex_fingerprint_mode"] = "off"
					cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 15
					cfg.Gateway.MaxAccountSwitches = 2
				})
			// The harness sends one completion value. Consume it once and
			// broadcast completion so refusal and deferred cleanup both retain
			// their release/header checks without competing for that value.
			completion := h.handlerDone
			finished := make(chan struct{})
			h.handlerDone = finished
			go func() {
				<-completion
				close(finished)
			}()
			defer issue600Finish(t, h, headers)
			require.Positive(t, accountID, "capture the configured upstream account")
			stored := scenario == "stored_pointer"
			issue715WSSend(t, h.clientConn, fmt.Sprintf(`{"type":"response.create","model":"gpt-5.1","store":%t,"input":[{"role":"user","content":"fixture U"}],"tools":%s}`, stored, tools))
			first := issue715WSRead(t, h.clientConn)
			require.Equal(t, "response.completed", gjson.GetBytes(first, "type").String())
			if scenario == "ordinary_tool" || scenario == "empty_terminal" || scenario == "duplicate_call" {
				// These positive fixtures contain no encrypted agent item or
				// shared anchor. A local plaintext call must still continue.
				require.NotContains(t, string(first), "encrypted_content")
				require.False(t, stored)
			}
			previous := "resp_fixture_repair_1"
			if scenario == "wrong_response" {
				previous = "resp_fixture_other_same_account"
				require.NoError(t, service.NewOpenAIWSStateStore(cache).BindResponseAccount(context.Background(), *h.apiKey.GroupID, previous, accountID, time.Minute))
			}
			if scenario == "unknown_pointer" {
				previous = "resp_fixture_unknown"
			}
			input := `{"role":"user","content":"fixture V"}`
			if scenario == "terminal_excludes" || scenario == "ordinary_tool" || scenario == "empty_terminal" || scenario == "duplicate_call" {
				input = `{"type":"function_call_output","call_id":"call_fixture_repair","output":"fixture result"}`
			}
			if scenario == "terminal_excludes" {
				// Resend the actually delivered call with its result. Even when
				// terminal history excludes C, the local proof consumer must
				// reject C rather than accepting this complete client pair.
				input = `{"role":"user","content":"fixture U"},` + call + "," + input
			}
			toolsField := ""
			if scenario == "assistant_history" {
				// Plain function declarations are supplied per turn; exercise
				// precision of the current schema, not implicit inheritance.
				toolsField = `,"tools":` + tools
			}
			secondFrame := fmt.Sprintf(`{"type":"response.create","model":"gpt-5.1","store":%t,"previous_response_id":%q,"input":[%s]%s}`, stored, previous, input, toolsField)
			if scenario == "assistant_history" {
				require.Equal(t, "9007199254740993", gjson.Get(secondFrame, "tools.0.parameters.properties.n.maximum").Raw)
			}
			issue715WSSend(t, h.clientConn, secondFrame)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			var terminal []byte
			var readErr error
			for {
				_, terminal, readErr = h.clientConn.Read(ctx)
				if readErr != nil || gjson.GetBytes(terminal, "type").String() == "response.completed" || gjson.GetBytes(terminal, "type").String() == "response.failed" || gjson.GetBytes(terminal, "type").String() == "error" {
					break
				}
			}
			if scenario == "duplicate_call" {
				require.NoError(t, readErr, "F3_DUPLICATE_WARMUP_REFUSED")
				require.Equal(t, "response.completed", gjson.GetBytes(terminal, "type").String(), "F3_DUPLICATE_WARMUP_REFUSED")
				issue715WSSend(t, h.clientConn, fmt.Sprintf(`{"type":"response.create","model":"gpt-5.1","store":false,"previous_response_id":"resp_fixture_repair_2","input":[%s]}`, input))
				for {
					_, terminal, readErr = h.clientConn.Read(ctx)
					if readErr != nil || gjson.GetBytes(terminal, "type").String() == "response.completed" || gjson.GetBytes(terminal, "type").String() == "response.failed" || gjson.GetBytes(terminal, "type").String() == "error" {
						break
					}
				}
			}
			refused := scenario == "wrong_response" || scenario == "terminal_excludes" || scenario == "duplicate_call" || scenario == "unknown_pointer"
			if refused && gjson.GetBytes(terminal, "type").String() != "response.completed" {
				select {
				case <-h.handlerDone:
				case <-ctx.Done():
					t.Fatal("refusal did not finish the handler")
				}
			}
			mu.Lock()
			actual := append([][]byte(nil), frames...)
			mu.Unlock()
			if refused {
				marker := "UNKNOWN_POINTER_SENT"
				generations := 1
				if scenario == "wrong_response" {
					marker = "F2_WRONG_RESPONSE_SENT"
				}
				if scenario == "terminal_excludes" {
					marker = "F3_EXCLUDED_TOOL_SENT"
				}
				if scenario == "duplicate_call" {
					marker = "F3_DUPLICATE_TOOL_SENT"
					generations = 2
				}
				require.Len(t, actual, generations, marker+": refused turn must never start another upstream generation")
				require.NotEqual(t, "response.completed", gjson.GetBytes(terminal, "type").String())
				if readErr != nil {
					require.NotEqual(t, context.DeadlineExceeded, ctx.Err(), "refusal must be explicit")
				}
				return
			}
			marker := "plaintext continuation refused"
			if scenario == "ordinary_tool" {
				marker = "F3_PLAINTEXT_TOOL_REFUSED"
			} else if scenario == "empty_terminal" {
				marker = "F3_EMPTY_TERMINAL_REFUSED"
			}
			require.NoError(t, readErr, marker)
			require.Equal(t, "response.completed", gjson.GetBytes(terminal, "type").String(), marker)
			require.Len(t, actual, 2, "exactly one generation per client turn")
			if stored {
				require.Equal(t, previous, gjson.GetBytes(actual[1], "previous_response_id").String())
				require.Len(t, gjson.GetBytes(actual[1], "input").Array(), 1)
			} else if scenario == "assistant_history" {
				items := gjson.GetBytes(actual[1], "input").Array()
				require.Len(t, items, 3, "F1_ASSISTANT_HISTORY_LOST: final wire must contain U, M, V")
				require.JSONEq(t, message, items[1].Raw, "F1_ASSISTANT_HISTORY_LOST")
				require.Equal(t, "9007199254740993", items[1].Get("fixture_number").Raw)
				require.Equal(t, "fixture U", items[0].Get("content").String())
				require.Equal(t, "fixture V", items[2].Get("content").String())
				require.False(t, gjson.GetBytes(actual[1], "previous_response_id").Exists())
				require.Equal(t, "9007199254740993", gjson.GetBytes(actual[1], "tools.0.parameters.properties.n.maximum").Raw)
			} else {
				require.Len(t, gjson.GetBytes(actual[1], "input").Array(), 3, "plaintext history must contain U, C and C's result")
				require.JSONEq(t, call, gjson.GetBytes(actual[1], "input.1").Raw)
				require.Equal(t, "call_fixture_repair", gjson.GetBytes(actual[1], "input.1.call_id").String())
				require.Equal(t, "call_fixture_repair", gjson.GetBytes(actual[1], "input.2.call_id").String())
				require.False(t, gjson.GetBytes(actual[1], "previous_response_id").Exists())
			}
		})
	}
}

// Real client and upstream sockets: the second frame carries only a completed
// tool result and the response pointer, never a fabricated full-history pair.
func TestOpenAIWSPlaintextToolContinuation(t *testing.T) { runPlaintextToolContinuation(t, true) }
func TestOpenAIWSPlaintextToolContinuationWithoutStorage(t *testing.T) {
	runPlaintextToolContinuation(t, false)
}
func TestOpenAIWSPlaintextToolAfterRebuild(t *testing.T) { runPlaintextToolContinuation(t, true, true) }
func runPlaintextToolContinuation(t *testing.T, stored bool, rebuilt ...bool) {
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
					cfg.Gateway.MaxAccountSwitches = 2
				})
			defer issue600Finish(t, h, headers)
			firstFrame := fmt.Sprintf(`{"type":"response.create","model":"gpt-5.1","store":%t,"input":[{"role":"user","content":"Run the fixture once"}],"tools":[{"type":"function","name":"fixture","parameters":{"type":"object","properties":{}}}]}`, stored)
			if len(rebuilt) > 0 && rebuilt[0] {
				var err error
				firstFrame, err = sjson.Set(firstFrame, "conversation", "conv_fixture_expired")
				require.NoError(t, err)
			}
			issue715WSSend(t, h.clientConn, firstFrame)
			first := issue715WSRead(t, h.clientConn)
			require.Equal(t, "response.completed", gjson.GetBytes(first, "type").String())
			require.Equal(t, "call_plaintext_once", gjson.GetBytes(first, "response.output.0.call_id").String())
			issue715WSSend(t, h.clientConn, fmt.Sprintf(`{"type":"response.create","model":"gpt-5.1","store":%t,"previous_response_id":"resp_plaintext_first","input":[{"type":"function_call_output","call_id":"call_plaintext_once","output":"fixture applied once"}]}`, stored))
			second := issue715WSRead(t, h.clientConn)
			if gjson.GetBytes(second, "type").String() != "response.completed" {
				logReadableWSRecoveryDetails(t, h)
			}
			require.Equal(t, "response.completed", gjson.GetBytes(second, "type").String(), "PLAINTEXT_TOOL_CONTINUATION_REFUSED: %s", second)
			require.Equal(t, "resp_plaintext_second", gjson.GetBytes(second, "response.id").String())
			mu.Lock()
			actual := append([][]byte(nil), frames...)
			mu.Unlock()
			if len(rebuilt) > 0 && rebuilt[0] {
				require.False(t, gjson.GetBytes(actual[0], "conversation").Exists(), "first generation must actually use rebuilt history")
			}
			require.Len(t, actual, 2, "exactly one generation per client turn; never replay the executed tool")
			require.Contains(t, string(actual[1]), "fixture applied once")
			if mode == service.OpenAIWSIngressModePassthrough || (mode == service.OpenAIWSIngressModeHTTPBridge && stored) {
				require.Equal(t, "resp_plaintext_first", gjson.GetBytes(actual[1], "previous_response_id").String())
				require.Len(t, gjson.GetBytes(actual[1], "input").Array(), 1, "native delta must remain a delta")
			}
			require.Zero(t, h.requestLogs.FilterMessage("openai.agent_recovery_refused").Len())
		})
	}
}
