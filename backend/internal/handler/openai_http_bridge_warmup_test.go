//go:build unit && bridgewarmup

package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	ws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestHTTPBridgeWarmupThenToolContinuation(t *testing.T) {
	runHTTPBridgeWarmupToolContinuation(t, false)
}

func TestHTTPBridgeWarmupWithPriorCompletedToolHistory(t *testing.T) {
	runHTTPBridgeWarmupToolContinuation(t, true)
}

func runHTTPBridgeWarmupToolContinuation(t *testing.T, priorTools bool) {
	var calls atomic.Int32
	bodies := make(chan []byte, 4)
	const tool = `[{"type":"function","name":"fixture_once","parameters":{"type":"object","properties":{"n":{"type":"integer","maximum":9007199254740993}},"required":["n"],"additionalProperties":false},"strict":true}]`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		bodies <- raw
		n := calls.Add(1)
		if len(gjson.GetBytes(raw, "input").Array()) == 0 || gjson.GetBytes(raw, "previous_response_id").Exists() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"code":"fixture_unexpected_generation","message":"empty warmup or provider pointer is forbidden"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		output := `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture task complete"}]}]`
		if n == 1 {
			output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture plan retained"}]},{"type":"function_call","id":"fc_fixture","call_id":"call_fixture_once","name":"fixture_once","arguments":"{\"n\":9007199254740993}","status":"completed"}]`
		}
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture_%d\",\"status\":\"completed\",\"output\":%s,\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n", n, output)
	}))
	defer upstream.Close()
	h := newOpenAIWSPassthroughHandlerHarnessWithKey(t, upstream.URL, http.Header{"Session-Id": {"fixture-warmup"}},
		map[string]string{service.SettingKeyCyberSessionBlockEnabled: "false", service.SettingKeyOpenAI409OmitEncryptedHistoryEnabled: "true"}, nil,
		func(k *service.APIKey) { k.AllowLongContext = true },
		func(a *service.Account, cfg *config.Config, _ *service.HTTPUpstream, _ *service.AccountRepository) {
			a.Extra["openai_apikey_responses_websockets_v2_mode"] = service.OpenAIWSIngressModeHTTPBridge
			cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 15
		})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	send := func(raw string) { require.NoError(t, h.clientConn.Write(ctx, ws.MessageText, []byte(raw))) }
	read := func() []byte {
		_, raw, err := h.clientConn.Read(ctx)
		require.NoError(t, err, "HTTP_WARMUP_SENT_UPSTREAM: empty prefill must be handled without provider generation")
		return raw
	}
	send(`{"type":"response.create","generate":false,"model":"gpt-5.1","store":false,"input":[],"instructions":"fixture instructions","tools":` + tool + `,"max_output_tokens":256}`)
	created := read()
	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String(), "HTTP_WARMUP_SENT_UPSTREAM")
	prefilled := read()
	require.Equal(t, "response.completed", gjson.GetBytes(prefilled, "type").String(), "HTTP_WARMUP_SENT_UPSTREAM")
	localID := gjson.GetBytes(prefilled, "response.id").String()
	require.NotEmpty(t, localID)
	require.Equal(t, localID, gjson.GetBytes(created, "response.id").String())
	require.Empty(t, gjson.GetBytes(prefilled, "response.output").Array())
	require.Zero(t, calls.Load(), "HTTP_WARMUP_SENT_UPSTREAM")
	owner, err := service.NewOpenAIWSStateStore(h.gatewayCache).GetResponseAccount(ctx, *h.apiKey.GroupID, localID)
	require.NoError(t, err)
	require.Zero(t, owner, "local prefill must not mint provider ownership")
	require.Empty(t, h.usageLogs, "local prefill must not create usage")
	prior := ""
	if priorTools {
		prior = `,{"type":"function_call","call_id":"call_prior_completed","name":"fixture_once","arguments":"{\"n\":1}","status":"completed"},{"type":"function_call_output","call_id":"call_prior_completed","output":"prior receipt"}`
	}
	send(fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":[{"role":"user","content":"fixture original task"}%s]}`, localID, prior))
	first := read()
	require.Equal(t, "response.completed", gjson.GetBytes(first, "type").String())
	require.Equal(t, "call_fixture_once", gjson.GetBytes(first, "response.output.1.call_id").String())
	send(`{"type":"response.create","model":"gpt-5.1","store":false,"instructions":"fixture instructions","previous_response_id":"resp_fixture_1","input":[{"type":"function_call_output","call_id":"call_fixture_once","output":"fixture result"}]}`)
	final := read()
	require.Equal(t, "response.completed", gjson.GetBytes(final, "type").String())
	require.Equal(t, "fixture task complete", gjson.GetBytes(final, "response.output.0.content.0.text").String())
	require.NoError(t, h.clientConn.Close(ws.StatusNormalClosure, ""))
	select {
	case <-h.handlerDone:
	case <-ctx.Done():
		t.Fatal("bridge did not finish")
	}
	require.EqualValues(t, 2, calls.Load(), "only the two actual generations may reach upstream")
	firstBody, secondBody := <-bodies, <-bodies
	require.False(t, gjson.GetBytes(firstBody, "previous_response_id").Exists())
	require.False(t, gjson.GetBytes(firstBody, "generate").Exists())
	require.Equal(t, "fixture instructions", gjson.GetBytes(firstBody, "instructions").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(firstBody, "tools.0.parameters.properties.n.maximum").Raw)
	require.Equal(t, gjson.False, gjson.GetBytes(firstBody, "store").Type)
	items := gjson.GetBytes(secondBody, "input").Array()
	offset := 0
	if priorTools {
		offset = 2
	}
	require.Len(t, items, 4+offset)
	require.Equal(t, "fixture original task", items[0].Get("content").String())
	require.Equal(t, "fixture plan retained", items[1+offset].Get("content.0.text").String())
	require.Equal(t, "call_fixture_once", items[2+offset].Get("call_id").String())
	require.Equal(t, `{"n":9007199254740993}`, items[2+offset].Get("arguments").String())
	require.Equal(t, "call_fixture_once", items[3+offset].Get("call_id").String())
	require.Equal(t, "fixture result", items[3+offset].Get("output").String())
	require.False(t, gjson.GetBytes(secondBody, "previous_response_id").Exists())
	require.Eventually(t, func() bool { return len(h.usageLogs) == 2 }, time.Second, 5*time.Millisecond, "only actual generations may be billed")
}
