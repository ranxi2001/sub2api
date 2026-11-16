//go:build unit && unsentdelta

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenAIWSUnsentDeltaTakeover(t *testing.T) {
	for _, mode := range readableWSModes {
		for _, turn := range []int{2, 53} {
			t.Run(fmt.Sprintf("%s/turn_%d", mode, turn), func(t *testing.T) { runUnsentDelta(t, mode, turn, "") })
		}
	}
}
func TestOpenAIWSUnsentDeltaFences(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModePassthrough} {
		for _, fence := range []string{"unknown_pointer", "unknown_call", "terminal_excludes", "beta_off"} {
			t.Run(mode+"/"+fence, func(t *testing.T) { runUnsentDelta(t, mode, 2, fence) })
		}
	}
}
func runUnsentDelta(t *testing.T, mode string, switchTurn int, fence string) {
	t.Helper()
	var mu sync.Mutex
	var frames [][]byte
	var owners []string
	response := func(body []byte, auth string) []byte {
		mu.Lock()
		defer mu.Unlock()
		frames = append(frames, append([]byte(nil), body...))
		owners = append(owners, auth)
		n := len(frames)
		output := fmt.Sprintf(`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture history %d"}]}]`, n)
		if n == switchTurn-1 {
			output = `[{"type":"function_call","call_id":"call_takeover_once","name":"fixture","arguments":"{\"n\":9007199254740993}","status":"completed"}]`
		}
		if n == switchTurn-1 && fence == "terminal_excludes" {
			output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"no tool delivered"}]}]`
		}
		return []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_delta_%d","model":"gpt-5.1","status":"completed","output":%s,"usage":{"input_tokens":5,"output_tokens":3}}}`, n, output))
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			body, e := io.ReadAll(r.Body)
			if e != nil {
				t.Error(e)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", response(body, r.Header.Get("Authorization")))
			return
		}
		conn, e := coderws.Accept(w, r, nil)
		if e != nil {
			t.Error(e)
			return
		}
		defer conn.CloseNow()
		for {
			_, body, e := conn.Read(r.Context())
			if e != nil {
				return
			}
			if conn.Write(r.Context(), coderws.MessageText, response(body, r.Header.Get("Authorization"))) != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	clients := issue805Redis(t)
	headers := http.Header{"Session_id": {"fixture-delta-session"}, "Thread-Id": {"fixture-delta-thread"}}
	var accounts *readableWSAccounts
	h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, upstream.URL, headers,
		map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: fmt.Sprint(fence != "beta_off")},
		repository.NewGatewayCache(clients()), func(k *service.APIKey) { k.AllowLongContext = true },
		func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, repo *service.AccountRepository) {
			a.ID = 1
			a.Priority = 0
			a.Credentials["api_key"] = "fixture-A"
			a.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			a.Extra["codex_fingerprint_mode"] = "off"
			cfg.Gateway.MaxAccountSwitches = 2
			cfg.Gateway.Scheduling.OpenAITurnAdmissionCacheTTLSeconds = 0
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 20
			b := *a
			b.ID = 2
			b.Priority = 1
			b.Credentials = map[string]any{"api_key": "fixture-B", "base_url": upstream.URL}
			accounts = &readableWSAccounts{issue600SwitchRepo: &issue600SwitchRepo{&openAIWSFailoverHandlerAccountRepoStub{accounts: []service.Account{*a, b}}}}
			*repo = accounts
		})
	defer issue600Finish(t, h, headers)
	create := func(input, previous string) string {
		b := map[string]any{"type": "response.create", "model": "gpt-5.1", "store": true, "input": json.RawMessage(input)}
		if previous != "" {
			b["previous_response_id"] = previous
		} else {
			b["tools"] = []any{map[string]any{"type": "function", "name": "fixture", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}
		}
		raw, _ := json.Marshal(b)
		return string(raw)
	}
	issue715WSSend(t, h.clientConn, create(`[{"role":"user","content":"Perform fixture exactly once and retain the task"},{"type":"reasoning","encrypted_content":"fixture-history-state","summary":[{"type":"summary_text","text":"fixture readable progress"}]}]`, ""))
	executions := 0
	consume := func(raw []byte) {
		for _, item := range gjson.GetBytes(raw, "response.output").Array() {
			if item.Get("type").String() == "function_call" {
				require.Equal(t, "call_takeover_once", item.Get("call_id").String())
				executions++
			}
		}
	}
	first := issue715WSRead(t, h.clientConn)
	consume(first)
	require.Equal(t, "response.completed", gjson.GetBytes(first, "type").String())
	for n := 2; n < switchTurn; n++ {
		issue715WSSend(t, h.clientConn, create(fmt.Sprintf(`[{"role":"user","content":"fixture next %d"}]`, n), fmt.Sprintf("resp_delta_%d", n-1)))
		round := issue715WSRead(t, h.clientConn)
		consume(round)
		require.Equal(t, "response.completed", gjson.GetBytes(round, "type").String())
	}
	accounts.disable(1)
	previous := fmt.Sprintf("resp_delta_%d", switchTurn-1)
	if fence == "unknown_pointer" {
		previous = "resp_foreign"
	}
	call := "call_takeover_once"
	if fence == "unknown_call" {
		call = "call_fabricated"
	}
	issue715WSSend(t, h.clientConn, create(fmt.Sprintf(`[{"type":"function_call_output","call_id":%q,"output":"fixture applied exactly once"}]`, call), previous))
	if fence != "" {
		readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, raw, e := h.clientConn.Read(readCtx)
		mu.Lock()
		n := len(frames)
		mu.Unlock()
		require.Equal(t, switchTurn-1, n, "unsafe delta cannot emit another generation")
		require.True(t, e != nil || gjson.GetBytes(raw, "type").String() == "error")
		return
	}
	result := issue715WSRead(t, h.clientConn)
	consume(result)
	if gjson.GetBytes(result, "type").String() != "response.completed" {
		logReadableWSRecoveryDetails(t, h)
	}
	require.Equal(t, "response.completed", gjson.GetBytes(result, "type").String(), "UNSENT_DELTA_NOT_RESUMED: %s", result)
	mu.Lock()
	sent := append([][]byte(nil), frames...)
	selected := append([]string(nil), owners...)
	mu.Unlock()
	require.Len(t, sent, switchTurn)
	require.Equal(t, "Bearer fixture-B", selected[switchTurn-1])
	resumed := sent[switchTurn-1]
	require.NotContains(t, string(resumed), "resp_delta_", "old provider pointer must not reach B")
	require.Contains(t, string(resumed), "Perform fixture exactly once")
	require.Contains(t, string(resumed), "fixture applied exactly once")
	require.Contains(t, string(resumed), "fixture readable progress")
	require.NotContains(t, string(resumed), "fixture-history-state")
	require.Contains(t, string(resumed), "9007199254740993")
	require.True(t, gjson.GetBytes(resumed, "tools").IsArray(), "effective tool declarations must survive takeover")
	for _, item := range gjson.GetBytes(resumed, "input").Array() {
		require.NotContains(t, item.Get("type").String(), "call", "completed tools must be inert history")
	}
	if switchTurn == 53 {
		require.Contains(t, string(resumed), "fixture history 1")
		require.Contains(t, string(resumed), "fixture next 52")
	}
	require.Equal(t, 1, executions)
	issue715WSSend(t, h.clientConn, create(`[{"role":"user","content":"fixture final followup"}]`, fmt.Sprintf("resp_delta_%d", switchTurn)))
	round := issue715WSRead(t, h.clientConn)
	consume(round)
	require.Equal(t, "response.completed", gjson.GetBytes(round, "type").String())
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, frames, switchTurn+1)
	require.Equal(t, "Bearer fixture-B", owners[switchTurn])
	require.Equal(t, 1, executions)
}
