//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type readableWSOracle struct {
	mu                   sync.Mutex
	frames               [][]byte
	identities           []string
	headers              []http.Header
	httpSends, wsSends   int
	validatedDisconnects int
	disconnectsInjected  int
	validationFailures   int
	scenario             string
	unrelatedEOF         bool
}

func (u *readableWSOracle) response(r *http.Request, body []byte, ws bool) []byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.frames = append(u.frames, append([]byte(nil), body...))
	u.identities = append(u.identities, r.Header.Get("Authorization"))
	u.headers = append(u.headers, r.Header.Clone())
	if ws {
		u.wsSends++
	} else {
		u.httpSends++
	}
	n := len(u.frames)
	if u.scenario == "native_namespace" && (!ws || !reflect.DeepEqual(gjson.GetBytes(body, "tools").Value(), gjson.Parse(httpRebuildJSON(readableWSNativeNamespaceTools())).Value())) {
		u.validationFailures++
		return []byte(`{"type":"response.failed","response":{"status":"failed","error":{"code":"fixture_native_namespace_changed"}}}`)
	}
	if u.scenario == "bridge_model" && n == 2 {
		if r.Header.Get("Authorization") != "Bearer fixture-readable-ws-A" || gjson.GetBytes(body, "model").String() != "gpt-5.4" || !strings.Contains(string(body), "model-switch-warmup") {
			u.validationFailures++
			return []byte(`{"type":"response.failed","response":{"status":"failed","error":{"code":"fixture_model_warmup_failed"}}}`)
		}
		return []byte(`{"type":"response.completed","response":{"id":"resp_model_warmup","status":"completed","model":"gpt-5.4","error":null,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"model-switch-complete"}]}],"usage":{"input_tokens":5,"output_tokens":3}}}`)
	}
	logicalTurn := n
	if u.scenario == "bridge_model" && n > 2 {
		logicalTurn--
	}
	output := []json.RawMessage{json.RawMessage(httpRebuildAgentA), json.RawMessage(httpRebuildCall("apply_patch", "call_ws_patch", map[string]any{"path": "sum.go", "source": httpRebuildSource}))}
	customTools := strings.HasPrefix(u.scenario, "tools_")
	if customTools && n == 1 {
		output = append(output, json.RawMessage(httpRebuildCall("exec", "call_ws_exec", map[string]any{"input": "read sum.go"})))
	}
	if n > 1 {
		if u.unrelatedEOF {
			return nil
		}
		validTools := httpRebuildToolsAvailable(body)
		if customTools {
			validTools = readableWSCustomToolsAvailable(body)
		}
		if u.scenario == "native_namespace" {
			validTools = true // exact native declarations/schema checked above, including A
		}
		valid := r.Header.Get("Authorization") == "Bearer fixture-readable-ws-B" &&
			strings.Contains(string(body), httpRebuildTask) && validTools &&
			httpRebuildToolPair(body, "apply_patch", httpRebuildPatch) &&
			!strings.Contains(string(body), "fixture-opaque-A") &&
			!gjson.GetBytes(body, "previous_response_id").Exists() && !gjson.GetBytes(body, "conversation").Exists()
		if u.scenario == "bridge_model" {
			valid = valid && gjson.GetBytes(body, "model").String() == "gpt-5.4"
		}
		if !valid {
			u.validationFailures++
			return []byte(`{"type":"response.failed","response":{"status":"failed","error":{"code":"fixture_wrong_task_or_pointer","message":"wrong WS task, result, owner or pointer"}}}`)
		}
		if u.scenario == "ambiguous_B" {
			// This observation is after every wire check and immediately before
			// the server intentionally closes the accepted B generation.
			u.validatedDisconnects++
			return nil
		}
		output = []json.RawMessage{json.RawMessage(httpRebuildAgentB), json.RawMessage(httpRebuildCall("run_tests", "call_ws_verify", map[string]any{"path": "sum.go"}))}
		if logicalTurn == 3 && httpRebuildToolPair(body, "run_tests", httpRebuildCheck) {
			output = []json.RawMessage{json.RawMessage(httpRebuildJSON(map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "ws-coding-task-complete"}}}))}
		} else if logicalTurn > 2 {
			return []byte(`{"type":"response.failed","response":{"status":"failed","error":{"code":"fixture_duplicate_or_missing_tool"}}}`)
		}
	}
	return []byte(httpRebuildJSON(map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("resp_readable_ws_%d", n), "status": "completed", "model": gjson.GetBytes(body, "model").String(), "error": nil, "output": output, "usage": map[string]int{"input_tokens": 5, "output_tokens": 3}}}))
}

func readableWSCustomToolsAvailable(body []byte) bool {
	tools := gjson.GetBytes(body, "tools").Array()
	if len(tools) != 3 || tools[2].Get("type").String() != "function" || tools[2].Get("name").String() != "exec" {
		return false
	}
	expected := gjson.Parse(`{"type":"object","properties":{"input":{"type":"string","description":"The raw input for this tool, passed through verbatim."}},"required":["input"]}`)
	if !reflect.DeepEqual(expected.Value(), tools[2].Get("parameters").Value()) {
		return false
	}
	functions, err := sjson.DeleteBytes(body, "tools.2")
	if err != nil || !httpRebuildToolsAvailable(functions) {
		return false
	}
	calls, outputs := 0, 0
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("call_id").String() != "call_ws_exec" {
			continue
		}
		if item.Get("type").String() == "function_call" && item.Get("name").String() == "exec" && gjson.Get(item.Get("arguments").String(), "input").String() == "read sum.go" {
			calls++
		}
		if item.Get("type").String() == "function_call_output" && item.Get("output").String() == "custom-exec-once" {
			outputs++
		}
	}
	return calls == 1 && outputs == 1
}

func (u *readableWSOracle) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			answer := u.response(r, body, false)
			if answer == nil {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					u.mu.Lock()
					u.disconnectsInjected++
					u.mu.Unlock()
					_ = conn.Close()
				} else {
					t.Error(err)
				}
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", answer)
			return
		}
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow() //nolint:errcheck // Fixture teardown.
		for {
			_, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			answer := u.response(r, body, true)
			if answer == nil {
				u.mu.Lock()
				u.disconnectsInjected++
				u.mu.Unlock()
				return
			}
			if conn.Write(r.Context(), coderws.MessageText, answer) != nil {
				return
			}
		}
	}))
}

// Called only after the gateway handler has terminated (or the direct oracle
// socket has closed). A second recorded frame alone proves nothing about B.
func (u *readableWSOracle) ambiguousEvidence(mode string, terminal []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.frames) != 2 || len(u.identities) != 2 || u.identities[0] != "Bearer fixture-readable-ws-A" || u.identities[1] != "Bearer fixture-readable-ws-B" ||
		u.validatedDisconnects != 1 || u.disconnectsInjected != 1 || u.validationFailures != 0 || strings.Contains(string(terminal), "fixture_") || strings.Contains(string(terminal), "response.completed") {
		return errors.New("WS_AMBIGUOUS_ORACLE_REJECTED: no exactly-once validated B disconnect")
	}
	if (mode == service.OpenAIWSIngressModeHTTPBridge && (u.httpSends != 2 || u.wsSends != 0)) ||
		(mode != service.OpenAIWSIngressModeHTTPBridge && (u.wsSends != 2 || u.httpSends != 0)) {
		return errors.New("WS_AMBIGUOUS_ORACLE_REJECTED: wrong actual adapter")
	}
	return nil
}

func readableWSRefusalEvidence(scenario string, terminal []byte, readErr error) error {
	if strings.Contains(string(terminal), "fixture_") || strings.Contains(string(terminal), "response.completed") {
		return errors.New("provider rejection/completion is not local policy evidence")
	}
	codes := map[string][]string{
		"off":           {"encrypted_agent_recovery_owner_unavailable"},
		"no_intent":     {"encrypted_agent_recovery_readable_intent_missing"},
		"b_disabled":    {"encrypted_agent_recovery_owner_unavailable"},
		"pending_owner": {"encrypted_agent_recovery_delivery_pending", "encrypted_agent_recovery_owner_conflict"},
		"failed_owner":  {"encrypted_agent_recovery_owner_unavailable", "encrypted_agent_recovery_owner_conflict"},
	}
	for _, path := range []string{"error.code", "response.error.code"} {
		for _, code := range codes[scenario] {
			if gjson.GetBytes(terminal, path).String() == code {
				return nil
			}
		}
	}
	var closeErr coderws.CloseError
	if errors.As(readErr, &closeErr) {
		if closeErr.Code == coderws.StatusTryAgainLater && (scenario == "off" || scenario == "b_disabled") &&
			(strings.Contains(closeErr.Reason, "account eligibility changed") || strings.Contains(closeErr.Reason, "no available account")) {
			return nil
		}
		if closeErr.Code == coderws.StatusPolicyViolation {
			for _, code := range codes[scenario] {
				if strings.Contains(closeErr.Reason, code) {
					return nil
				}
			}
			reasons := map[string][]string{
				"off":           {"original compatible owner", "owner is currently unavailable"},
				"no_intent":     {"no readable task or summary"},
				"b_disabled":    {"owner is currently unavailable"},
				"pending_owner": {"producer has not confirmed delivery", "conflicting compatible owners"},
				"failed_owner":  {"owner is currently unavailable", "conflicting compatible owners"},
			}
			for _, reason := range reasons[scenario] {
				if strings.Contains(closeErr.Reason, reason) {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("WS_REFUSAL_ORACLE_REJECTED: missing %s policy reason", scenario)
}

// Actual socket/HTTP oracle mutations execute even before WS recovery exists.
// The same accepted-B observation used by the full handler is the authority.
func TestOpenAI409WSAmbiguousOracleMutations(t *testing.T) {
	for _, transport := range []string{"http_bridge", "socket"} {
		for _, mutation := range []string{"valid", "wrong_credential", "old_pointer", "missing_result", "unrelated_eof"} {
			t.Run(transport+"/"+mutation, func(t *testing.T) {
				u := &readableWSOracle{scenario: "ambiguous_B", unrelatedEOF: mutation == "unrelated_eof"}
				srv := u.server(t)
				defer srv.Close()
				send := func(credential, body string) []byte {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					if transport == "http_bridge" {
						req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(body))
						require.NoError(t, err)
						req.Header.Set("Authorization", credential)
						res, err := srv.Client().Do(req)
						if err != nil {
							return nil
						}
						defer res.Body.Close()
						raw, err := io.ReadAll(res.Body)
						require.NoError(t, err)
						return raw
					}
					conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &coderws.DialOptions{HTTPHeader: http.Header{"Authorization": {credential}}})
					require.NoError(t, err)
					defer conn.CloseNow()
					require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(body)))
					_, raw, err := conn.Read(ctx)
					require.NotErrorIs(t, err, context.DeadlineExceeded)
					return raw
				}
				first := send("Bearer fixture-readable-ws-A", `{"type":"response.create","model":"gpt-5.1","input":[]}`)
				require.Contains(t, string(first), "response.completed")
				input := []json.RawMessage{json.RawMessage(httpRebuildMessage("user", httpRebuildTask)), json.RawMessage(httpRebuildCall("apply_patch", "call_ws_patch", map[string]any{"path": "sum.go", "source": httpRebuildSource}))}
				if mutation != "missing_result" {
					input = append(input, json.RawMessage(httpRebuildJSON(map[string]any{"type": "function_call_output", "call_id": "call_ws_patch", "output": httpRebuildPatch})))
				}
				body := map[string]any{"type": "response.create", "model": "gpt-5.1", "input": input, "tools": httpRebuildToolDefinitions()}
				if mutation == "old_pointer" {
					body["previous_response_id"] = "resp_readable_ws_1"
				}
				credential := "Bearer fixture-readable-ws-B"
				if mutation == "wrong_credential" {
					credential = "Bearer fixture-readable-ws-A"
				}
				terminal := send(credential, httpRebuildJSON(body))
				u.mu.Lock()
				frameCount, accepted, rejected := len(u.frames), u.validatedDisconnects, u.validationFailures
				httpCount, wsCount := u.httpSends, u.wsSends
				u.mu.Unlock()
				require.Equal(t, 2, frameCount, "mutation must traverse the actual provider transport")
				if transport == "http_bridge" {
					require.Equal(t, 2, httpCount)
					require.Zero(t, wsCount)
				} else {
					require.Equal(t, 2, wsCount)
					require.Zero(t, httpCount)
				}
				if mutation == "valid" {
					require.Equal(t, 1, accepted)
					require.Zero(t, rejected)
				} else {
					require.Zero(t, accepted, "invalid frame/EOF must never emit a validated B disconnect observation")
					if mutation == "unrelated_eof" {
						require.Zero(t, rejected)
					} else {
						require.Equal(t, 1, rejected)
						require.Contains(t, string(terminal), "fixture_wrong_task_or_pointer")
					}
				}
				err := u.ambiguousEvidence(transport, terminal)
				if mutation == "valid" {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, "WS_AMBIGUOUS_ORACLE_REJECTED")
				}
				for _, scenario := range []string{"off", "no_intent", "b_disabled", "pending_owner", "failed_owner"} {
					require.Error(t, readableWSRefusalEvidence(scenario, nil, io.EOF), "arbitrary EOF cannot pass a refusal boundary")
				}
			})
		}
	}
}
