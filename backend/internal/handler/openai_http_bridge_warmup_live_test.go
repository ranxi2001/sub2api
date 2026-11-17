//go:build unit && bridgewarmuplive

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	ws "github.com/coder/websocket"
	"github.com/tidwall/gjson"
)

// Explicitly invoked only on the supplier-facing host. CI compiles this probe
// but never executes it. No customer task, key or supplier output is printed.
func TestHTTPBridgeWarmupLive(t *testing.T) {
	if os.Getenv("ISSUE905_BRIDGE_PAID_TESTS_APPROVED") != "yes" {
		t.Fatal("paid validation authorization required")
	}
	var credential struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if json.NewDecoder(io.LimitReader(os.Stdin, 16384)).Decode(&credential) != nil || credential.APIKey == "" {
		t.Fatal("scoped credential unavailable")
	}
	base := strings.TrimSuffix(credential.BaseURL, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	endpoint, err := url.Parse(base + "/responses")
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" {
		t.Fatal("TLS endpoint required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	var sends atomic.Int32
	seen := make(chan []byte, 2)
	report := map[string]any{"started_at": time.Now().UTC().Format(time.RFC3339Nano), "second_completed": false, "max_output_tokens": 256}
	defer func() {
		report["upstream_calls"] = sends.Load()
		report["finished_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		raw, _ := json.Marshal(report)
		fmt.Println("ISSUE905_BRIDGE_PROBE " + string(raw))
	}()
	client := &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if e != nil {
			return
		}
		if gjson.GetBytes(raw, "max_output_tokens").Int() != 256 || gjson.GetBytes(raw, "previous_response_id").Exists() || gjson.GetBytes(raw, "generate").Exists() || len(gjson.GetBytes(raw, "input").Array()) == 0 {
			http.Error(w, "probe rejected invalid bridge output", 400)
			return
		}
		for {
			n := sends.Load()
			if n >= 2 {
				http.Error(w, "probe budget exhausted", 503)
				return
			}
			if sends.CompareAndSwap(n, n+1) {
				break
			}
		}
		seen <- append([]byte(nil), raw...)
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(string(raw)))
		if e != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+credential.APIKey)
		req.Header.Set("Content-Type", "application/json")
		for _, key := range []string{"Session-Id", "Session_id", "Thread-Id", "OpenAI-Beta", "X-Codex-Beta-Features", "User-Agent"} {
			if v := r.Header.Get(key); v != "" {
				req.Header.Set(key, v)
			}
		}
		response, e := client.Do(req)
		if e != nil {
			http.Error(w, "probe transport failed", 502)
			return
		}
		defer response.Body.Close()
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		w.WriteHeader(response.StatusCode)
		buffer := make([]byte, 32<<10)
		for {
			n, e := response.Body.Read(buffer)
			if n > 0 {
				_, _ = w.Write(buffer[:n])
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			if e != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	defer cancel()
	session := fmt.Sprintf("fixture-bridge-%d", time.Now().UnixNano())
	h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, upstream.URL, http.Header{"Session-Id": {session}, "Thread-Id": {session}},
		map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}, nil,
		func(k *service.APIKey) { k.AllowLongContext = true },
		func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, _ *service.AccountRepository) {
			a.Extra["openai_apikey_responses_websockets_v2_mode"] = service.OpenAIWSIngressModeHTTPBridge
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 90
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 30
		})
	defer h.clientConn.CloseNow()
	send := func(body map[string]any) bool {
		raw, _ := json.Marshal(body)
		if h.clientConn.Write(ctx, ws.MessageText, raw) != nil {
			report["outcome"] = "client_write_failed"
			return false
		}
		return true
	}
	read := func(turn string) []byte {
		for n := 0; n < 10000; n++ {
			_, raw, e := h.clientConn.Read(ctx)
			if e != nil {
				report[turn+"_close"] = int(ws.CloseStatus(e))
				report["outcome"] = "closed_or_timeout"
				return nil
			}
			kind := gjson.GetBytes(raw, "type").String()
			if kind == "response.completed" {
				report[turn+"_event"] = kind
				return raw
			}
			if kind == "error" || kind == "response.failed" || kind == "response.incomplete" {
				report[turn+"_event"] = kind
				prefix := "error"
				if kind == "response.failed" {
					prefix = "response.error"
				}
				code := gjson.GetBytes(raw, prefix+".code").String()
				report[turn+"_error_code"] = "other_or_absent"
				for _, known := range []string{"encrypted_agent_recovery_owner_unavailable", "encrypted_agent_recovery_wire_changed", "invalid_request_error", "previous_response_not_found", "upstream_error"} {
					if code == known {
						report[turn+"_error_code"] = known
					}
				}
				report["outcome"] = "upstream_error"
				return nil
			}
		}
		report["outcome"] = "frame_limit"
		return nil
	}
	tools := json.RawMessage(`[{"type":"function","name":"fixture_echo","description":"Return a fixed synthetic receipt; no external action","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false},"strict":true}]`)
	if !send(map[string]any{"type": "response.create", "generate": false, "input": []any{}, "model": "gpt-6-astra", "store": false, "tools": tools, "max_output_tokens": 256, "prompt_cache_key": session, "parallel_tool_calls": true, "client_metadata": map[string]string{"session_id": session, "thread_id": session}}) {
		return
	}
	warm := read("warmup")
	if warm == nil {
		return
	}
	report["warmup_upstream_calls"] = sends.Load()
	localID := gjson.GetBytes(warm, "response.id").String()
	owner, e := service.NewOpenAIWSStateStore(h.gatewayCache).GetResponseAccount(ctx, *h.apiKey.GroupID, localID)
	report["warmup_owner_absent"] = e == nil && owner == 0
	if sends.Load() != 0 {
		report["outcome"] = "warmup_called_provider"
		return
	}
	if !send(map[string]any{"type": "response.create", "previous_response_id": localID, "tool_choice": map[string]string{"type": "function", "name": "fixture_echo"}, "input": []any{map[string]string{"role": "user", "content": "Call fixture_echo with text ping exactly once. This is a synthetic protocol test."}}}) {
		return
	}
	first := read("turn1")
	if first == nil {
		return
	}
	callID := ""
	callCount := 0
	for _, item := range gjson.GetBytes(first, "response.output").Array() {
		if item.Get("type").String() == "function_call" && item.Get("name").String() == "fixture_echo" {
			callID = item.Get("call_id").String()
			callCount++
		}
	}
	if callID == "" || callCount != 1 {
		report["outcome"] = "missing_tool_call"
		return
	}
	if !send(map[string]any{"type": "response.create", "model": "gpt-6-astra", "store": false, "max_output_tokens": 256, "prompt_cache_key": session, "client_metadata": map[string]string{"session_id": session, "thread_id": session}, "tool_choice": "none", "previous_response_id": gjson.GetBytes(first, "response.id").String(), "input": []any{map[string]string{"type": "function_call_output", "call_id": callID, "output": "fixture receipt: ping"}}}) {
		return
	}
	final := read("turn2")
	if final == nil {
		return
	}
	answer, newCall := false, false
	for _, item := range gjson.GetBytes(final, "response.output").Array() {
		if item.Get("type").String() == "function_call" {
			newCall = true
		}
		for _, part := range item.Get("content").Array() {
			if part.Get("type").String() == "output_text" && strings.TrimSpace(part.Get("text").String()) != "" {
				answer = true
			}
		}
	}
	report["second_has_answer"], report["second_has_new_tool_call"] = answer, newCall
	report["second_completed"] = answer && !newCall
	if len(seen) == 2 {
		<-seen
		body := <-seen
		call, result := false, false
		for _, item := range gjson.GetBytes(body, "input").Array() {
			if item.Get("call_id").String() == callID {
				call = call || item.Get("type").String() == "function_call"
				result = result || item.Get("type").String() == "function_call_output" && item.Get("output").String() == "fixture receipt: ping"
			}
		}
		report["second_has_complete_tool_pair"] = call && result
		report["second_has_provider_pointer"] = gjson.GetBytes(body, "previous_response_id").Exists()
	}
	report["outcome"] = "completed"
}
