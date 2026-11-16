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
	"reflect"
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
	runUnsentDeltaWithInput(t, mode, switchTurn, fence, "")
}
func runUnsentDeltaWithInput(t *testing.T, mode string, switchTurn int, fence, extraInput string) {
	runUnsentDeltaWithPolicy(t, mode, switchTurn, fence, extraInput, false)
}
func runUnsentDeltaWithPolicy(t *testing.T, mode string, switchTurn int, fence, extraInput string, clientHistory bool) {
	runUnsentDeltaWithLossyPolicy(t, mode, switchTurn, fence, extraInput, clientHistory, false)
}
func runUnsentDeltaWithLossyPolicy(t *testing.T, mode string, switchTurn int, fence, extraInput string, clientHistory, lossy bool) {
	t.Helper()
	var mu sync.Mutex
	var frames [][]byte
	var owners []string
	response := func(body []byte, auth string) [][]byte {
		mu.Lock()
		defer mu.Unlock()
		frames = append(frames, append([]byte(nil), body...))
		owners = append(owners, auth)
		n := len(frames)
		output := fmt.Sprintf(`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture history %d"}]}]`, n)
		if n == switchTurn-1 {
			output = `[{"type":"function_call","call_id":"call_takeover_once","name":"fixture","arguments":"{\"n\":9007199254740993}","status":"completed"}]`
		}
		var events [][]byte
		if n == switchTurn-1 && fence == "terminal_excludes" {
			// Deliver the real call first. The nonempty terminal is authoritative
			// and excludes that call even though the client already received it.
			events = append(events, []byte(`{"type":"response.output_item.done","item":`+gjson.Get(output, "0").Raw+`}`))
			output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture terminal excludes delivered tool"}]}]`
		}
		return append(events, []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_delta_%d","model":"gpt-5.1","status":"completed","output":%s,"usage":{"input_tokens":5,"output_tokens":3}}}`, n, output)))
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			body, e := io.ReadAll(r.Body)
			if e != nil {
				t.Error(e)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, event := range response(body, r.Header.Get("Authorization")) {
				fmt.Fprintf(w, "data: %s\n\n", event)
			}
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
			for _, event := range response(body, r.Header.Get("Authorization")) {
				if conn.Write(r.Context(), coderws.MessageText, event) != nil {
					return
				}
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
			if clientHistory {
				setClientHistory := reflect.ValueOf(&cfg.Gateway.OpenAIWS).Elem().FieldByName("ClientHistoryRecoveryEnabled")
				require.True(t, setClientHistory.IsValid())
				setClientHistory.SetBool(true)
			}
			// Optional interface keeps historical test overlays buildable.
			if lossy {
				setLossy := reflect.ValueOf(&cfg.Gateway.OpenAIWS).Elem().FieldByName("LossyHistoryRestartEnabled")
				require.True(t, setLossy.IsValid())
				setLossy.SetBool(true)
			}
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
	completion := h.handlerDone
	finished := make(chan struct{})
	h.handlerDone = finished
	go func() { <-completion; close(finished) }()
	defer issue600Finish(t, h, headers)
	awaitUsage := func() {
		select {
		case <-h.usageLogs:
		case <-time.After(5 * time.Second):
			t.Fatal("completed fixture turn did not settle usage")
		}
	}
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
	issue715WSSend(t, h.clientConn, create(`[{"role":"user","content":"Perform fixture exactly once and retain the task"},{"type":"reasoning","encrypted_content":"fixture-history-state","summary":[{"type":"summary_text","text":"fixture readable progress"}]}`+extraInput+`]`, ""))
	executions := 0
	consume := func(raw []byte) {
		items := gjson.GetBytes(raw, "response.output").Array()
		if gjson.GetBytes(raw, "type").String() == "response.output_item.done" {
			items = append(items, gjson.GetBytes(raw, "item"))
		}
		for _, item := range items {
			if item.Get("type").String() == "function_call" {
				require.Equal(t, "call_takeover_once", item.Get("call_id").String())
				executions++
			}
		}
	}
	excludedToolDeliveries := 0
	readTurn := func() []byte {
		if fence != "terminal_excludes" {
			return issue715WSRead(t, h.clientConn)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		for {
			_, event, err := h.clientConn.Read(ctx)
			require.NoError(t, err)
			kind := gjson.GetBytes(event, "type").String()
			if kind == "response.output_item.done" && gjson.GetBytes(event, "item.type").String() == "function_call" {
				require.Equal(t, "call_takeover_once", gjson.GetBytes(event, "item.call_id").String())
				excludedToolDeliveries++
				consume(event)
			}
			if kind == "response.completed" || kind == "response.failed" || kind == "error" {
				return event
			}
		}
	}
	first := readTurn()
	consume(first)
	awaitUsage()
	require.Equal(t, "response.completed", gjson.GetBytes(first, "type").String())
	if fence == "terminal_excludes" {
		require.Equal(t, 1, excludedToolDeliveries, "terminal exclusion must follow actual tool item delivery to the client")
		require.Equal(t, 1, executions)
		require.NotContains(t, string(first), "call_takeover_once", "the nonempty terminal must exclude the delivered call")
	}
	for n := 2; n < switchTurn; n++ {
		issue715WSSend(t, h.clientConn, create(fmt.Sprintf(`[{"role":"user","content":"fixture next %d"}]`, n), fmt.Sprintf("resp_delta_%d", n-1)))
		round := readTurn()
		consume(round)
		awaitUsage()
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
		if e == nil {
			require.Equal(t, "error", gjson.GetBytes(raw, "type").String())
			_, _, e = h.clientConn.Read(readCtx)
		}
		select {
		case <-finished:
		case <-readCtx.Done():
			t.Fatal("refusal did not finish; no post-check generation permitted")
		}
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
	awaitUsage()
	mu.Lock()
	sent := append([][]byte(nil), frames...)
	selected := append([]string(nil), owners...)
	mu.Unlock()
	require.Len(t, sent, switchTurn)
	require.Equal(t, "Bearer fixture-B", selected[switchTurn-1])
	resumed := sent[switchTurn-1]
	require.NotContains(t, string(resumed), "resp_delta_", "old provider pointer must not reach B")
	if !lossy {
		require.Contains(t, string(resumed), "Perform fixture exactly once")
	}
	require.Contains(t, string(resumed), "fixture applied exactly once")
	if !lossy {
		require.Contains(t, string(resumed), "fixture readable progress")
	}
	require.NotContains(t, string(resumed), "fixture-history-state")
	if !lossy {
		require.Contains(t, string(resumed), "9007199254740993")
	}
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
	awaitUsage()
	require.Equal(t, "response.completed", gjson.GetBytes(round, "type").String())
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, frames, switchTurn+1)
	require.Equal(t, "Bearer fixture-B", owners[switchTurn])
	require.Equal(t, 1, executions)
}

// Raw custom output IDs use a different prefix from replayable output IDs.
// The source remains immutable; only the existing top-level ID sanitizer may
// explain the difference when the bridge mints a current-turn projection.
func TestOpenAIWSBridgeNormalizedDeltaID(t *testing.T) {
	for _, id := range []string{"foreign-provider-id", "ctc_original_output"} {
		t.Run(id, func(t *testing.T) {
			var mu sync.Mutex
			var frames [][]byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, e := io.ReadAll(r.Body)
				if e != nil {
					t.Error(e)
					return
				}
				mu.Lock()
				frames = append(frames, body)
				n := len(frames)
				mu.Unlock()
				output := `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture previous"}]}]`
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, `data: {"type":"response.completed","response":{"id":"resp_normalized_%d","model":"gpt-5.1","status":"completed","output":%s}}`+"\n\n", n, output)
			}))
			defer upstream.Close()
			clients := issue805Redis(t)
			headers := http.Header{"Session_id": {"fixture-normalized-session"}, "Thread-Id": {"fixture-normalized-thread"}}
			h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, upstream.URL, headers, map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}, repository.NewGatewayCache(clients()), func(k *service.APIKey) { k.AllowLongContext = true }, func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, _ *service.AccountRepository) {
				a.Extra["openai_apikey_responses_websockets_v2_mode"] = service.OpenAIWSIngressModeHTTPBridge
				a.Extra["codex_fingerprint_mode"] = "off"
			})
			defer issue600Finish(t, h, headers)
			issue715WSSend(t, h.clientConn, `{"type":"response.create","model":"gpt-5.1","store":false,"input":[{"role":"user","content":"fixture original"}]}`)
			require.Equal(t, "response.completed", gjson.GetBytes(issue715WSRead(t, h.clientConn), "type").String())
			issue715WSSend(t, h.clientConn, fmt.Sprintf(`{"type":"response.create","model":"gpt-5.1","store":false,"previous_response_id":"resp_normalized_1","input":[{"type":"message","role":"user","id":%q,"content":"fixture next","number":9007199254740993}]}`, id))
			result := issue715WSRead(t, h.clientConn)
			require.Equal(t, "response.completed", gjson.GetBytes(result, "type").String(), "NORMALIZED_DELTA_REFUSED: %s", result)
			mu.Lock()
			defer mu.Unlock()
			require.Len(t, frames, 2)
			items := gjson.GetBytes(frames[1], "input").Array()
			require.Len(t, items, 3)
			require.Equal(t, "fixture next", items[2].Get("content").String())
			require.Equal(t, "9007199254740993", items[2].Get("number").Raw)
			require.False(t, items[2].Get("id").Exists())
			require.False(t, gjson.GetBytes(frames[1], "previous_response_id").Exists())
		})
	}
}
