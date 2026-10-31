package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The production service and request builders run unchanged. Only the lowest
// HTTP port is an ordinary loopback client, with no external network access.
type smartProtocolNetworkHTTP struct{ client *http.Client }

func (u smartProtocolNetworkHTTP) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.client.Do(r)
}
func (u smartProtocolNetworkHTTP) DoWithTLS(r *http.Request, p string, a int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, a, n)
}

type smartProtocolNetwork struct {
	t              *testing.T
	cfg            *config.Config
	svc            *OpenAIGatewayService
	account        *Account
	upstream       *httptest.Server
	ingress        *httptest.Server
	ctx            context.Context
	cancel         context.CancelFunc
	httpCalls      atomic.Int32
	wsCalls        atomic.Int32
	active         atomic.Int32
	mu             sync.Mutex
	bodies         [][]byte
	results        chan *OpenAIForwardResult
	errors         chan error
	timings        chan requesttiming.Snapshot
	onTurn         func(int)
	beforeTurn     func(int) error
	toolEvents     atomic.Int32
	fail           string
	partialRelease chan struct{}
}

func newSmartProtocolNetwork(t *testing.T, mode string, known bool, fail string) *smartProtocolNetwork {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	f := &smartProtocolNetwork{t: t, ctx: ctx, cancel: cancel, results: make(chan *OpenAIForwardResult, 32), errors: make(chan error, 16), timings: make(chan requesttiming.Snapshot, 16), fail: fail, partialRelease: make(chan struct{})}
	cfg := &config.Config{}
	f.cfg = cfg
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
	cfg.Gateway.OpenAIWS.SmartProtocol = config.GatewayOpenAIWSSmartProtocolConfig{Enabled: true, Mode: mode}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 4
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 4
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.active.Add(1)
		defer f.active.Add(-1)
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			conn, err := coderws.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.CloseNow() }()
			for {
				_, body, err := conn.Read(ctx)
				if err != nil {
					return
				}
				f.wsCalls.Add(1)
				f.remember(body)
				for i, event := range f.events(body) {
					if i == 1 || (fail == "content_prefix" && i == 4) {
						time.Sleep(5 * time.Millisecond)
					}
					if err := conn.Write(ctx, coderws.MessageText, []byte(event)); err != nil {
						return
					}
				}
				if fail == "eof" || fail == "partial_usage" || (fail == "p1_eof" && gjson.GetBytes(body, "input").String() == "partial") {
					return
				}
				if fail == "p1_cancel" && gjson.GetBytes(body, "input").String() == "partial" {
					select {
					case <-f.partialRelease:
					case <-ctx.Done():
					}
					return
				}
			}
		}
		f.httpCalls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		f.remember(body)
		if fail == "http_error" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"synthetic unavailable"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for i, event := range f.events(body) {
			if i == 1 || (fail == "content_prefix" && i == 4) {
				time.Sleep(5 * time.Millisecond)
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
			if flushErr := http.NewResponseController(w).Flush(); flushErr != nil {
				return
			}
		}
		if fail == "cancel" {
			select {
			case <-r.Context().Done():
			case <-ctx.Done():
			}
			return
		}
	}))
	f.account = &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 4, Credentials: map[string]any{"api_key": "synthetic-only", "base_url": f.upstream.URL}, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	if known {
		f.account.Extra["openai_responses_supported"] = true
	}
	pool := newOpenAIWSConnPool(cfg)
	f.svc = &OpenAIGatewayService{cfg: cfg, httpUpstream: smartProtocolNetworkHTTP{f.upstream.Client()}, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), openaiWSPool: pool, openaiWSStateStore: NewOpenAIWSStateStore(nil), toolCorrector: NewCodexToolCorrector()}
	f.ingress = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			f.errors <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		conn.SetReadLimit(64 * 1024 * 1024)
		_, first, err := conn.Read(ctx)
		if err != nil {
			f.errors <- err
			return
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r.Clone(ctx)
		collector := requesttiming.New(time.Now(), 0)
		turnCtx := requesttiming.With(ctx, collector)
		hooks := &OpenAIWSIngressHooks{BeforeTurn: func(turn int) error {
			f.mu.Lock()
			hook := f.beforeTurn
			f.mu.Unlock()
			if hook != nil {
				return hook(turn)
			}
			return nil
		}, AfterTurn: func(turn int, result *OpenAIForwardResult, _ error) {
			if result != nil {
				f.results <- result
			}
			f.mu.Lock()
			hook := f.onTurn
			f.mu.Unlock()
			if hook != nil {
				hook(turn)
			}
		}}
		selected := *f.account
		err = f.svc.ProxyResponsesWebSocketFromClient(turnCtx, c, conn, &selected, "synthetic-only", first, hooks)
		collector.WhenFinished(func(s requesttiming.Snapshot) { f.timings <- s })
		collector.Finish(200, false)
		f.errors <- err
	}))
	t.Cleanup(func() { cancel(); f.ingress.Close(); pool.Close(); f.upstream.Close() })
	return f
}
func (f *smartProtocolNetwork) remember(body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, append([]byte(nil), body...))
}
func (f *smartProtocolNetwork) events(body []byte) []string {
	seed := gjson.GetBytes(body, "input").String()
	if seed == "" {
		seed = "tools"
	}
	if len(seed) > 64 {
		seed = "large"
	}
	id := "resp_" + seed
	events := []string{fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, id)}
	if f.fail == "content_prefix" {
		events = append(events, `{"type":"response.output_text.delta","delta":""}`, `{"type":"response.metadata.delta","delta":"working"}`, `{"type":"response.output_item.added","item":{"type":"function_call","name":"lookup","arguments":""}}`)
	}
	if f.fail == "no_content" {
		return append(events, `{"type":"response.output_text.delta","delta":""}`, fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":0}}}`, id))
	}

	if strings.HasPrefix(f.fail, "p1_") && seed == "partial" {
		return append(events, `{"type":"response.in_progress","response":{"id":"resp_partial","usage":{"input_tokens":3,"output_tokens":2}}}`)
	}
	if f.fail == "partial_usage" {
		return append(events, `{"type":"response.in_progress","response":{"id":"resp_partial","usage":{"input_tokens":3,"output_tokens":2}}}`)
	}
	if f.fail == "eof" || f.fail == "cancel" {
		return events
	}
	if f.fail == "event_error" {
		return append(events, `{"type":"response.failed","response":{"id":"resp_failed","status":"failed","error":{"code":"server_error","message":"synthetic failure"},"usage":{"input_tokens":3,"output_tokens":2}}}`)
	}
	events = append(events, `{"type":"response.function_call_arguments.delta","delta":"{}","item_id":"fc_synthetic","output_index":0}`, `{"type":"response.output_item.done","item":{"id":"fc_synthetic","type":"function_call","call_id":"call_synthetic","name":"lookup","arguments":"{}"}}`, `{"type":"response.output_text.delta","delta":"ok"}`)
	if f.fail == "missing_usage" {
		return append(events, fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[]}}`, id))
	}
	events = append(events, fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"model":"gpt-5.1","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":7}}}`, id))
	return events
}
func (f *smartProtocolNetwork) dial() *coderws.Conn {
	f.t.Helper()
	c, _, err := coderws.Dial(f.ctx, "ws"+strings.TrimPrefix(f.ingress.URL, "http"), nil)
	require.NoError(f.t, err)
	f.t.Cleanup(func() { _ = c.CloseNow() })
	return c
}
func (f *smartProtocolNetwork) send(c *coderws.Conn, input, previous string, cap int) {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{"type": "response.create", "model": "gpt-5.1", "input": input, "previous_response_id": previous, "max_output_tokens": cap, "store": true})
	require.NoError(f.t, c.Write(f.ctx, coderws.MessageText, body))
}
func (f *smartProtocolNetwork) terminal(c *coderws.Conn) string {
	f.t.Helper()
	for {
		_, event, err := c.Read(f.ctx)
		require.NoError(f.t, err)
		if gjson.GetBytes(event, "type").String() == "response.function_call_arguments.delta" {
			f.toolEvents.Add(1)
		}
		if gjson.GetBytes(event, "type").String() == "response.completed" {
			return gjson.GetBytes(event, "response.id").String()
		}
	}
}
func (f *smartProtocolNetwork) result() *OpenAIForwardResult {
	f.t.Helper()
	select {
	case r := <-f.results:
		return r
	case <-f.ctx.Done():
		f.t.Fatal("missing actual turn result")
		return nil
	}
}

func TestSmartProtocolHTTPFirstSession(t *testing.T) {
	f := newSmartProtocolNetwork(t, "auto", true, "")
	c := f.dial()
	changed := make(chan struct{})
	var changeOnce sync.Once
	f.mu.Lock()
	f.onTurn = func(turn int) {
		if turn == 1 {
			changeOnce.Do(func() { f.cfg.Gateway.OpenAIWS.SmartProtocol.Mode = "ws"; close(changed) })
		}
	}
	f.mu.Unlock()
	f.send(c, "first", "", 8000)
	id := f.terminal(c)
	<-changed
	first := f.result()
	require.Equal(t, "http", first.UpstreamProtocol)
	require.NotNil(t, first.FirstUpstreamEventMs)
	require.NotNil(t, first.FirstTokenMs)
	require.Less(t, *first.FirstUpstreamEventMs, *first.FirstTokenMs)
	f.send(c, "second", id, 100)
	f.terminal(c)
	second := f.result()
	require.Equal(t, "http", second.UpstreamProtocol)
	require.EqualValues(t, 2, f.httpCalls.Load())
	require.Zero(t, f.wsCalls.Load())
	require.EqualValues(t, 2, f.toolEvents.Load(), "tool argument deltas reach downstream on both turns")
	f.mu.Lock()
	body := append([]byte(nil), f.bodies[1]...)
	f.mu.Unlock()
	require.Equal(t, id, gjson.GetBytes(body, "previous_response_id").String())
	require.Equal(t, "second", gjson.GetBytes(body, "input").String())
	_ = c.CloseNow()
	select {
	case err := <-f.errors:
		require.NoError(t, err)
	case <-f.ctx.Done():
		t.Fatal("session cleanup did not finish")
	}
	timing := <-f.timings
	require.Len(t, timing.WSTurns, 2)
	require.Equal(t, "http", timing.WSTurns[1].Upstream)
	require.NotNil(t, timing.WSTurns[1].FirstUpstreamEventMS)
	require.NotNil(t, timing.WSTurns[1].FirstActualContentMS)
	require.NotNil(t, timing.ProtocolDecision)
	require.Equal(t, "ws", timing.ProtocolDecision.Ingress)
	require.Equal(t, "http", timing.ProtocolDecision.Upstream)
	// The changed global manual control applies to a new connection only.
	next := f.dial()
	f.send(next, "new", "", 8000)
	f.terminal(next)
	require.Equal(t, "ws", f.result().UpstreamProtocol)
	require.EqualValues(t, 1, f.wsCalls.Load())
}
func TestSmartProtocolNetworkModes(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		known      bool
		cap        int
		protocol   string
	}{{"ws_first", "auto", true, 100, "ws"}, {"unknown", "auto", false, 8000, "ws"}, {"negative_http", "http", false, 8000, "ws"}, {"manual_ws", "ws", true, 8000, "ws"}, {"manual_http", "http", true, 100, "http"}, {"shadow", "shadow", true, 8000, "ws"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSmartProtocolNetwork(t, tc.mode, tc.known, "")
			if tc.name == "negative_http" {
				f.account.Extra["openai_responses_supported"] = false
			}
			c := f.dial()
			f.send(c, "first", "", tc.cap)
			id := f.terminal(c)
			require.Equal(t, tc.protocol, f.result().UpstreamProtocol)
			f.send(c, "second", id, 8000)
			f.terminal(c)
			require.Equal(t, tc.protocol, f.result().UpstreamProtocol)
			if tc.protocol == "ws" {
				require.EqualValues(t, 2, f.wsCalls.Load())
				require.Zero(t, f.httpCalls.Load())
			} else {
				require.EqualValues(t, 2, f.httpCalls.Load())
				require.Zero(t, f.wsCalls.Load())
			}
		})
	}
}
func TestSmartProtocolNoReplayAndCleanup(t *testing.T) {
	for _, mode := range []string{"http", "ws"} {
		for _, failure := range []string{"eof", "event_error", "cancel", "http_error", "partial_usage"} {
			if mode == "ws" && failure == "http_error" {
				continue
			}
			t.Run(mode+"_"+failure, func(t *testing.T) {
				f := newSmartProtocolNetwork(t, mode, true, failure)
				c := f.dial()
				f.send(c, "failure", "", 100)
				if failure == "cancel" {
					require.Eventually(t, func() bool { return f.httpCalls.Load()+f.wsCalls.Load() == 1 }, 2*time.Second, time.Millisecond)
					_ = c.CloseNow()
				} else {
					for {
						_, body, err := c.Read(f.ctx)
						if err == nil && gjson.GetBytes(body, "type").String() == "response.failed" {
							_ = c.CloseNow()
							break
						}
						if err != nil {
							break
						}
					}
				}
				select {
				case <-f.errors:
				case <-f.ctx.Done():
					t.Fatal("failed turn cleanup stalled")
				}
				f.svc.CloseOpenAIWSPool()
				require.Eventually(t, func() bool { return f.active.Load() == 0 }, 2*time.Second, time.Millisecond, "upstream connections must clean up")
				require.EqualValues(t, 1, f.httpCalls.Load()+f.wsCalls.Load(), "submitted request must execute exactly once")
				if failure == "partial_usage" {
					result := f.result()
					require.True(t, result.ObservedUsageOnError)
					require.False(t, result.UsageUnavailable)
					require.Equal(t, 2, result.Usage.OutputTokens)
				}
			})
		}
	}
}
func TestSmartProtocolForeignContinuation(t *testing.T) {
	f := newSmartProtocolNetwork(t, "http", true, "")
	c := f.dial()
	f.send(c, "first", "", 100)
	f.terminal(c)
	f.result()
	f.send(c, "second", "resp_foreign", 100)
	_, _, err := c.Read(f.ctx)
	require.Error(t, err)
	require.EqualValues(t, 1, f.httpCalls.Load())
}
func TestSmartProtocolConcurrentSticky(t *testing.T) {
	f := newSmartProtocolNetwork(t, "auto", true, "")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, _, err := coderws.Dial(f.ctx, "ws"+strings.TrimPrefix(f.ingress.URL, "http"), &coderws.DialOptions{HTTPHeader: http.Header{"Session-Id": []string{"shared-synthetic"}}})
			if err != nil {
				t.Error("loopback dial failed")
				return
			}
			defer func() { _ = c.CloseNow() }()
			for turn := 0; turn < 2; turn++ {
				cap := 100
				if i%2 == 0 {
					cap = 8000
				}
				raw := fmt.Sprintf(`{"type":"response.create","model":"gpt-5.1","input":"session%dturn%d","max_output_tokens":%d,"store":true}`, i, turn, cap)
				if err := c.Write(f.ctx, coderws.MessageText, []byte(raw)); err != nil {
					t.Error("loopback write failed")
					return
				}
				for {
					_, body, err := c.Read(f.ctx)
					if err != nil {
						t.Error("loopback stream failed")
						return
					}
					if gjson.GetBytes(body, "type").String() == "response.completed" {
						break
					}
				}
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 4, f.httpCalls.Load())
	require.EqualValues(t, 4, f.wsCalls.Load())
}

func TestSmartProtocolLegacy15MiB(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled_%t", enabled), func(t *testing.T) {
			f := newSmartProtocolNetwork(t, "ws", true, "")
			f.cfg.Gateway.OpenAIWS.SmartProtocol.Enabled = enabled
			c := f.dial()
			f.send(c, strings.Repeat("x", 15*1024*1024), "", 100)
			f.terminal(c)
			result := f.result()
			require.Equal(t, "http", result.UpstreamProtocol)
			require.EqualValues(t, 1, f.httpCalls.Load())
			require.Zero(t, f.wsCalls.Load())
		})
	}
}

func TestSmartProtocolTurnGuardBeforeSend(t *testing.T) {
	for _, mode := range []string{"http", "ws"} {
		t.Run(mode, func(t *testing.T) {
			f := newSmartProtocolNetwork(t, mode, true, "")
			f.mu.Lock()
			f.beforeTurn = func(turn int) error {
				if turn == 2 {
					return fmt.Errorf("synthetic turn denied")
				}
				return nil
			}
			f.mu.Unlock()
			c := f.dial()
			f.send(c, "first", "", 100)
			id := f.terminal(c)
			f.result()
			f.send(c, "second", id, 8000)
			for {
				_, _, err := c.Read(f.ctx)
				if err != nil {
					break
				}
			}
			require.EqualValues(t, 1, f.httpCalls.Load()+f.wsCalls.Load())
		})
	}
}

func TestSmartProtocolMissingUsageIsUnknown(t *testing.T) {
	for _, mode := range []string{"http", "ws"} {
		t.Run(mode, func(t *testing.T) {
			f := newSmartProtocolNetwork(t, mode, true, "missing_usage")
			c := f.dial()
			f.send(c, "first", "", 100)
			f.terminal(c)
			require.True(t, f.result().UsageUnavailable)
		})
	}
}

func TestSmartProtocolStatelessRequestRetainsWS(t *testing.T) {
	f := newSmartProtocolNetwork(t, "http", true, "")
	c := f.dial()
	require.NoError(t, c.Write(f.ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"first","max_output_tokens":8000,"store":false}`)))
	f.terminal(c)
	require.Equal(t, "ws", f.result().UpstreamProtocol)
	require.EqualValues(t, 1, f.wsCalls.Load())
	require.Zero(t, f.httpCalls.Load())
}

func TestSmartProtocolPassthroughNetwork(t *testing.T) {
	for _, mode := range []string{"auto", "shadow", "http", "ws"} {
		t.Run(mode, func(t *testing.T) {
			f := newSmartProtocolNetwork(t, mode, true, "")
			f.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			f.cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModePassthrough
			f.account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModePassthrough
			c := f.dial()
			f.send(c, "first", "", 8000)
			id := f.terminal(c)
			first := f.result()
			require.NotNil(t, first.FirstUpstreamEventMs)
			want := "http"
			if mode == "shadow" || mode == "ws" {
				want = "ws"
			}
			require.Equal(t, want, first.UpstreamProtocol)
			f.send(c, "second", id, 100)
			f.terminal(c)
			require.Equal(t, want, f.result().UpstreamProtocol)
			if want == "http" {
				require.EqualValues(t, 2, f.httpCalls.Load())
				require.Zero(t, f.wsCalls.Load())
			} else {
				require.EqualValues(t, 2, f.wsCalls.Load())
				require.Zero(t, f.httpCalls.Load())
			}
		})
	}
}

func TestSmartProtocolToolContinuation(t *testing.T) {
	for _, mode := range []string{"http", "ws"} {
		t.Run(mode, func(t *testing.T) {
			f := newSmartProtocolNetwork(t, mode, true, "")
			c := f.dial()
			require.NoError(t, c.Write(f.ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"first","store":true,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}]}`)))
			id := f.terminal(c)
			f.result()
			require.EqualValues(t, 1, f.toolEvents.Load())
			raw, _ := json.Marshal(map[string]any{"type": "response.create", "model": "gpt-5.1", "store": true, "previous_response_id": id, "input": []map[string]any{{"type": "function_call_output", "call_id": "call_synthetic", "output": "synthetic result"}}})
			require.NoError(t, c.Write(f.ctx, coderws.MessageText, raw))
			f.terminal(c)
			f.result()
			f.mu.Lock()
			body := append([]byte(nil), f.bodies[1]...)
			f.mu.Unlock()
			require.Equal(t, id, gjson.GetBytes(body, "previous_response_id").String())
			input := gjson.GetBytes(body, "input").Array()
			require.Len(t, input, 1)
			require.Equal(t, "function_call_output", input[0].Get("type").String())
			require.Equal(t, "call_synthetic", input[0].Get("call_id").String())
			require.EqualValues(t, 2, f.httpCalls.Load()+f.wsCalls.Load())
		})
	}
}

func TestSmartProtocolLegacyNoReplay(t *testing.T) {
	f := newSmartProtocolNetwork(t, "auto", true, "http_error")
	f.cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 1
	c := f.dial()
	f.send(c, "first", "", 100)
	for {
		_, _, err := c.Read(f.ctx)
		if err != nil {
			break
		}
	}
	select {
	case err := <-f.errors:
		var failover *UpstreamFailoverError
		require.False(t, errors.As(err, &failover))
	case <-f.ctx.Done():
		t.Fatal("legacy failure cleanup stalled")
	}
	require.EqualValues(t, 1, f.httpCalls.Load())
	require.Zero(t, f.wsCalls.Load())
}

func TestSmartProtocolActualContentNetwork(t *testing.T) {
	for _, path := range []string{"http", "ws", "passthrough"} {
		for _, failure := range []string{"content_prefix", "no_content"} {
			t.Run(path+"_"+failure, func(t *testing.T) {
				mode := path
				if path == "passthrough" {
					mode = "ws"
				}
				f := newSmartProtocolNetwork(t, mode, true, failure)
				if path == "passthrough" {
					f.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
					f.cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModePassthrough
					f.account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModePassthrough
				}
				c := f.dial()
				f.send(c, "first", "", 100)
				f.terminal(c)
				result := f.result()
				require.NotNil(t, result.FirstUpstreamEventMs)
				if failure == "no_content" {
					require.Nil(t, result.FirstActualContentMs)
					require.Empty(t, result.FirstActualContentKind)
				} else {
					require.NotNil(t, result.FirstTokenMs)
					require.NotNil(t, result.FirstActualContentMs)
					require.Less(t, *result.FirstTokenMs, *result.FirstActualContentMs, "legacy first-token keeps its original empty-delta semantics")
					require.Equal(t, "tool_input", result.FirstActualContentKind)
				}
				_ = c.CloseNow()
				select {
				case <-f.errors:
				case <-f.ctx.Done():
					t.Fatal("content session cleanup stalled")
				}
				timing := <-f.timings
				require.Len(t, timing.WSTurns, 1)
				require.Equal(t, result.FirstActualContentMs, timing.WSTurns[0].FirstActualContentMS)
				require.Equal(t, result.FirstActualContentKind, timing.WSTurns[0].FirstActualContentKind)
			})
		}
	}
}

func TestSmartProtocolPassthroughNoReplay(t *testing.T) {
	for _, failure := range []string{"eof", "event_error", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			f := newSmartProtocolNetwork(t, "ws", true, failure)
			f.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			f.cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModePassthrough
			f.account.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModePassthrough
			c := f.dial()
			f.send(c, "first", "", 100)
			if failure == "cancel" {
				require.Eventually(t, func() bool { return f.wsCalls.Load() == 1 }, 2*time.Second, time.Millisecond)
				_ = c.CloseNow()
			} else {
				for {
					_, raw, e := c.Read(f.ctx)
					if e != nil {
						break
					}
					if gjson.GetBytes(raw, "type").String() == "response.failed" {
						_ = c.CloseNow()
						break
					}
				}
			}
			select {
			case err := <-f.errors:
				var retry *UpstreamFailoverError
				require.False(t, errors.As(err, &retry))
			case <-f.ctx.Done():
				t.Fatal("passthrough cleanup stalled")
			}
			require.EqualValues(t, 1, f.wsCalls.Load())
			require.Zero(t, f.httpCalls.Load())
			f.svc.CloseOpenAIWSPool()
			require.Eventually(t, func() bool { return f.active.Load() == 0 }, 2*time.Second, time.Millisecond)
		})
	}
}
