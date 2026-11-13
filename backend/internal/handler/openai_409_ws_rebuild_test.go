//go:build unit

package handler

import (
	"context"
	"encoding/json"
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
)

var readableWSModes = []string{service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModeCtxPool,
	service.OpenAIWSIngressModeShared, service.OpenAIWSIngressModeDedicated, service.OpenAIWSIngressModePassthrough}

// Mutate administrative scheduling state under a lock while a real client
// connection remains open. Every authoritative repository read sees the update.
type readableWSAccounts struct {
	*issue600SwitchRepo
	mu sync.Mutex
}

func (r *readableWSAccounts) disable(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			r.accounts[i].Schedulable = false
		}
	}
}
func (r *readableWSAccounts) GetByID(ctx context.Context, id int64) (*service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.issue600SwitchRepo.GetByID(ctx, id)
}
func (r *readableWSAccounts) GetOpenAITurnAdmission(ctx context.Context, id int64) (*service.Account, *service.Account, error) {
	a, err := r.GetByID(ctx, id)
	return a, nil, err
}
func (r *readableWSAccounts) ListSchedulableByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.issue600SwitchRepo.ListSchedulableByPlatform(ctx, platform)
}
func (r *readableWSAccounts) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, platform string) ([]service.Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}
func (r *readableWSAccounts) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}
func (r *readableWSAccounts) SetRateLimited(ctx context.Context, id int64, until time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.issue600SwitchRepo.SetRateLimited(ctx, id, until)
}

func TestOpenAI409WSReadableContinuation(t *testing.T) {
	for _, mode := range readableWSModes {
		t.Run(mode, func(t *testing.T) { runReadableWSContinuation(t, mode, "completed") })
	}
}

func TestOpenAI409WSReadableBoundaries(t *testing.T) {
	for _, mode := range readableWSModes {
		for _, boundary := range []string{"off", "no_intent", "b_disabled", "pending_owner", "failed_owner", "ambiguous_B"} {
			t.Run(mode+"/"+boundary, func(t *testing.T) { runReadableWSContinuation(t, mode, boundary) })
		}
	}
}

func runReadableWSContinuation(t *testing.T, mode, scenario string) {
	t.Helper()
	clients := issue805Redis(t)
	probe := clients()
	var mu sync.Mutex
	var frames [][]byte
	var identities []string
	var httpSends, wsSends int
	response := func(r *http.Request, body []byte, ws bool) []byte {
		mu.Lock()
		defer mu.Unlock()
		frames = append(frames, append([]byte(nil), body...))
		identities = append(identities, r.Header.Get("Authorization"))
		if ws {
			wsSends++
		} else {
			httpSends++
		}
		n := len(frames)
		output := []json.RawMessage{json.RawMessage(httpRebuildAgentA), json.RawMessage(httpRebuildCall("apply_patch", "call_ws_patch", map[string]any{"path": "sum.go", "source": httpRebuildSource}))}
		if n > 1 {
			valid := r.Header.Get("Authorization") == "Bearer fixture-readable-ws-B" &&
				strings.Contains(string(body), httpRebuildTask) && httpRebuildToolsAvailable(body) &&
				httpRebuildToolPair(body, "apply_patch", httpRebuildPatch) &&
				!strings.Contains(string(body), "fixture-opaque-A") &&
				!gjson.GetBytes(body, "previous_response_id").Exists() && !gjson.GetBytes(body, "conversation").Exists()
			if !valid {
				return []byte(`{"type":"response.failed","response":{"status":"failed","error":{"code":"fixture_wrong_task_or_pointer","message":"wrong WS task, result, owner or pointer"}}}`)
			}
			if scenario == "ambiguous_B" {
				return nil // Actual B accepted the frame; delivery/execution unknown.
			}
			output = []json.RawMessage{json.RawMessage(httpRebuildAgentB), json.RawMessage(httpRebuildCall("run_tests", "call_ws_verify", map[string]any{"path": "sum.go"}))}
			if n == 3 && httpRebuildToolPair(body, "run_tests", httpRebuildCheck) {
				output = []json.RawMessage{json.RawMessage(httpRebuildJSON(map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "ws-coding-task-complete"}}}))}
			} else if n > 2 {
				return []byte(`{"type":"response.failed","response":{"status":"failed","error":{"code":"fixture_duplicate_or_missing_tool"}}}`)
			}
		}
		return []byte(httpRebuildJSON(map[string]any{"type": "response.completed", "response": map[string]any{
			"id": fmt.Sprintf("resp_readable_ws_%d", n), "status": "completed", "model": "gpt-5.1", "error": nil,
			"output": output, "usage": map[string]int{"input_tokens": 5, "output_tokens": 3},
		}}))
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			answer := response(r, body, false)
			if answer == nil {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
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
			answer := response(r, body, true)
			if answer == nil || conn.Write(r.Context(), coderws.MessageText, answer) != nil {
				return
			}
		}
	}))
	defer provider.Close()
	headers := http.Header{"Session_id": {"fixture-readable-ws-session"}, "Thread-Id": {"fixture-readable-ws-thread"}}
	var accounts *readableWSAccounts
	h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, provider.URL, headers,
		map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: fmt.Sprint(scenario != "off")},
		repository.NewGatewayCache(clients()), func(key *service.APIKey) { key.AllowLongContext = true },
		func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, repo *service.AccountRepository) {
			a.ID, a.Priority = 1, 0
			a.Credentials["api_key"] = "fixture-readable-ws-A"
			a.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			a.Extra["codex_fingerprint_mode"] = "off"
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 15
			cfg.Gateway.MaxAccountSwitches = 2
			require.Equal(t, mode, a.ResolveOpenAIResponsesWebSocketV2Mode(cfg.Gateway.OpenAIWS.IngressModeDefault))
			other := *a
			other.ID, other.Priority = 2, 1
			other.Credentials = map[string]any{"api_key": "fixture-readable-ws-B", "base_url": provider.URL}
			accounts = &readableWSAccounts{issue600SwitchRepo: &issue600SwitchRepo{&openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{*a, other}}}}
			*repo = accounts
		})
	defer issue600Finish(t, h, headers)
	create := func(input []string, previous string) string {
		body := map[string]any{"type": "response.create", "model": "gpt-5.1", "store": false,
			"tools": httpRebuildToolDefinitions(), "client_metadata": map[string]string{"thread_id": "fixture-readable-ws-thread"},
			"input": json.RawMessage("[" + strings.Join(input, ",") + "]")}
		if previous != "" {
			body["previous_response_id"] = previous
		}
		return httpRebuildJSON(body)
	}
	history := []string{httpRebuildMessage("user", httpRebuildTask)}
	if scenario == "no_intent" {
		history = nil
	} // No earlier readable intent exists in the connection either.
	issue715WSSend(t, h.clientConn, create(history, ""))
	first := gjson.GetBytes(issue715WSRead(t, h.clientConn), "response")
	require.Equal(t, "completed", first.Get("status").String())
	tools := &httpRebuildTools{}
	for _, item := range first.Get("output").Array() {
		history = append(history, item.Raw)
	}
	history = append(history, tools.execute(t, first)...)
	require.Eventually(t, func() bool {
		owners := issue805Owners(t, probe)
		if len(owners) != 1 {
			return false
		}
		for _, raw := range owners {
			if !json.Valid([]byte(raw)) || !gjson.Parse(raw).IsArray() || gjson.Get(raw, "1").Int() != 1 {
				return false
			}
		}
		return true
	}, time.Second, 5*time.Millisecond)
	owners := issue805Owners(t, probe)
	accounts.disable(1)
	if scenario == "b_disabled" {
		accounts.disable(2)
	}
	if scenario == "no_intent" {
		history = []string{httpRebuildAgentA}
	}
	if scenario == "pending_owner" || scenario == "failed_owner" {
		for key, raw := range owners {
			value := "failed"
			if scenario == "pending_owner" {
				value = "pending:fixture-unknown:" + raw
			}
			require.NoError(t, probe.Set(context.Background(), key, value, time.Minute).Err())
			owners[key] = value
		}
	}
	readTerminal := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		for {
			_, raw, err := h.clientConn.Read(ctx)
			if err != nil {
				return raw, err
			}
			kind := gjson.GetBytes(raw, "type").String()
			if kind == "response.completed" || kind == "response.failed" || kind == "error" {
				return raw, nil
			}
		}
	}
	issue715WSSend(t, h.clientConn, create(history, first.Get("id").String()))
	secondRaw, readErr := readTerminal()
	mu.Lock()
	sends := len(frames)
	mu.Unlock()
	if scenario != "completed" {
		require.NotErrorIs(t, readErr, context.DeadlineExceeded)
		if scenario == "ambiguous_B" {
			require.Equal(t, 2, sends, "B must receive the actual uncertain generation exactly once")
		} else {
			require.Equal(t, 1, sends, "unsafe task or current eligibility must refuse before B")
		}
		require.NotContains(t, string(secondRaw), "response.completed")
		for key, raw := range owners {
			require.Equal(t, raw, issue805Owners(t, probe)[key])
		}
		require.Equal(t, 1, tools.patches)
		require.Zero(t, tools.verifications)
		return
	}
	require.Equal(t, 2, sends, "WS_READABLE_B_DISPATCH_MISSING: same client must automatically replace disabled A before any next generation; close=%v terminal=%s", readErr, secondRaw)
	require.NoError(t, readErr)
	second := gjson.GetBytes(secondRaw, "response")
	require.Equal(t, "completed", second.Get("status").String(), string(secondRaw))
	for _, item := range second.Get("output").Array() {
		history = append(history, item.Raw)
	}
	history = append(history, tools.execute(t, second)...)
	issue715WSSend(t, h.clientConn, create(history, second.Get("id").String()))
	final, err := readTerminal()
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(final, "type").String(), string(final))
	require.Contains(t, string(final), "ws-coding-task-complete")
	require.Empty(t, tools.execute(t, gjson.GetBytes(final, "response")))
	require.Equal(t, 1, tools.patches)
	require.Equal(t, 1, tools.verifications)
	a, err := accounts.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, a.Schedulable)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"Bearer fixture-readable-ws-A", "Bearer fixture-readable-ws-B", "Bearer fixture-readable-ws-B"}, identities)
	if mode == service.OpenAIWSIngressModeHTTPBridge {
		require.Equal(t, 3, httpSends)
		require.Zero(t, wsSends)
	} else {
		require.Equal(t, 3, wsSends)
		require.Zero(t, httpSends)
	}
	for key, raw := range owners {
		require.Equal(t, raw, issue805Owners(t, probe)[key], "A owner is never overwritten")
	}
}
