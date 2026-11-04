package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requesttiming"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Replay identical payloads through real SSE handlers and WS adapters. Expected
// endpoints are explicit, not calculated with the production shared helper.
func TestOpenAIResponsesTTFTSharedSemanticForwarders(t *testing.T) {
	cases := []struct {
		name, event string
		want        bool
	}{
		{"reasoning_added", `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`, true},
		{"reasoning_done", `{"type":"response.output_item.done","item":{"type":"reasoning"}}`, true},
		{"summary_added", `{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":""}}`, true},
		{"summary_done", `{"type":"response.reasoning_summary_part.done","part":{"type":"summary_text"}}`, true},
		{"reasoning_part", `{"type":"response.content_part.added","part":{"type":"reasoning_text","text":""}}`, true},
		{"summary_part_done", `{"type":"response.content_part.done","part":{"type":"summary_text"}}`, true},
		{"reasoning_text", `{"type":"response.reasoning_text.delta","delta":"thinking"}`, true},
		{"text", `{"type":"response.output_text.delta","delta":"hello"}`, true},
		{"tool", `{"type":"response.function_call_arguments.delta","delta":"{}"}`, true},
		{"media", `{"type":"response.image_generation_call.partial_image","partial_image_b64":"aGVsbG8="}`, true},
		{"refusal", `{"type":"response.refusal.delta","delta":"cannot"}`, true},
		{"unknown", `{"type":"response.unknown","delta":"diagnostic"}`, false},
		{"untyped_item", `{"type":"response.output_item.added","item":{"id":"synthetic"}}`, false},
		{"created", `{"type":"response.created"}`, false},
		{"heartbeat", `{"type":"keepalive"}`, false},
		{"metadata", `{"type":"response.metadata"}`, false},
		{"metadata_delta", `{"type":"response.metadata.delta","delta":"working"}`, false},
		{"empty_delta", `{"type":"response.output_text.delta","delta":""}`, false},
		{"null_delta", `{"type":"response.output_text.delta","delta":null}`, false},
		{"object_delta", `{"type":"response.output_text.delta","delta":{}}`, false},
		{"completed_empty", `{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1},"output":[]}}`, false},
		{"completed_body", `{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}}`, false},
	}
	setSemanticTTFTTestMode(t)
	for _, path := range []string{"native", "native_guarded", "passthrough", "ws_to_sse", "http_bridge"} {
		for _, tc := range cases {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				gin.SetMode(gin.TestMode)
				events := []string{`{"type":"response.created","response":{"id":"resp_synthetic"}}`, tc.event, `{"type":"response.completed","response":{"id":"resp_synthetic","usage":{"input_tokens":1,"output_tokens":1}}}`}
				wire := "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				collector := requesttiming.New(time.Now(), 0)
				ctx := requesttiming.With(context.Background(), collector)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(wire)), Request: c.Request}
				svc := &OpenAIGatewayService{cfg: &config.Config{}}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
				var first *int
				var err error
				switch path {
				case "native", "native_guarded":
					if path == "native_guarded" {
						svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 30
					}
					r, e := svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "synthetic", "synthetic")
					err = e
					require.NotNil(t, r)
					if r != nil {
						first = r.firstTokenMs
						require.Equal(t, 1, r.usage.InputTokens)
						require.Equal(t, 1, r.usage.OutputTokens)
					}
				case "passthrough":
					r, e := svc.handleStreamingResponsePassthrough(ctx, resp, c, account, time.Now(), "synthetic", "synthetic")
					err = e
					require.NotNil(t, r)
					if r != nil {
						first = r.firstTokenMs
						require.Equal(t, 1, r.usage.InputTokens)
						require.Equal(t, 1, r.usage.OutputTokens)
					}
				case "ws_to_sse":
					conn := &openAIWSCaptureConn{}
					for _, event := range events {
						conn.events = append(conn.events, []byte(event))
					}
					dialer := &wsSSETestDialer{conn: conn}
					upstream := &httpUpstreamRecorder{}
					svc = wsSSETestService(t, dialer, upstream)
					SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
					r, e := svc.Forward(context.Background(), c, wsSSETestAccount(), []byte(wsSSETestRequest))
					err = e
					require.NotNil(t, r)
					require.EqualValues(t, 1, dialer.calls.Load(), "HTTP SSE ingress must really dial the WS upstream")
					require.Empty(t, upstream.requests, "WS-to-SSE must not fall back to HTTP")
					if r != nil {
						first = r.FirstTokenMs
					}
				case "http_bridge":
					upstream := &httpUpstreamRecorder{resp: resp}
					svc = &OpenAIGatewayService{cfg: newOpenAIWSV2TestConfig(), httpUpstream: upstream}
					payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)
					r, e := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5.1", "", "", "", "", 1, func([]byte) error { return nil })
					err = e
					require.NotNil(t, r)
					require.Len(t, upstream.requests, 1, "WS HTTP bridge must submit exactly one HTTP request")
					require.Equal(t, http.MethodPost, upstream.requests[0].Method)
					require.True(t, strings.HasSuffix(upstream.requests[0].URL.Path, "/responses"))
					if r != nil {
						first = r.FirstTokenMs
					}
				}
				require.NoError(t, err)
				if tc.want {
					require.NotNil(t, first, "typed reasoning/content must establish semantic TTFT")
				} else {
					require.Nil(t, first, "metadata, untyped/empty payload and terminal must not establish semantic TTFT")
				}
				if path == "native" || path == "native_guarded" || path == "passthrough" {
					collector.Finish(200, false)
					collector.WhenFinished(func(snapshot requesttiming.Snapshot) {
						_, recorded := snapshot.Events["first_semantic"]
						require.Equal(t, tc.want, recorded, "request timing must use the same semantic endpoint")
					})
				}
			})
		}
	}
}

func TestOpenAIResponsesTTFTForceOutputIsDeliveryOnly(t *testing.T) {
	for _, event := range []string{`{"type":"response.failed","response":{"error":{"code":"invalid_request_error"}}}`, `{"type":"error","error":{"code":"invalid_request_error"}}`, `{"type":"response.output_text.delta","delta":""}`, `not-json`} {
		require.False(t, openAIStreamDataStartsTTFT(event, "", true, OpenAITTFTModeSemantic), "forced delivery cannot invent semantic output: %s", event)
	}
}

func setSemanticTTFTTestMode(t *testing.T) {
	t.Helper()
	previous, _ := gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings)
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{openAITTFTMode: OpenAITTFTModeSemantic})
	t.Cleanup(func() { gatewayForwardingCache.Store(previous) })
}

func TestOpenAIResponsesTTFTMultilineSemanticEvent(t *testing.T) {
	setSemanticTTFTTestMode(t)
	for _, path := range []string{"native", "native_guarded", "passthrough"} {
		for _, prefix := range []string{"", "event: response.output_text.delta\n"} {
			t.Run(fmt.Sprintf("%s/header=%t", path, prefix != ""), func(t *testing.T) {
				first := `data: {"type":"response.output_text.delta",`
				if prefix != "" {
					first = "data: {"
				}
				multiline := prefix + first + "\ndata: \"delta\":\"hello\"}\n\n"
				wire := multiline + `data: {"type":"response.completed","response":{"created_at":1700000000,"output":[],"usage":{"input_tokens":3,"output_tokens":2}}}` + "\n\n"
				collector := requesttiming.New(time.Now(), 0)
				ctx := requesttiming.With(context.Background(), collector)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(wire)), Request: c.Request}
				svc := &OpenAIGatewayService{cfg: &config.Config{}}
				if path == "native_guarded" {
					svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 30
				}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
				var firstToken *int
				if path == "passthrough" {
					result, err := svc.handleStreamingResponsePassthrough(ctx, response, c, account, time.Now(), "synthetic", "synthetic")
					require.NoError(t, err)
					require.NotNil(t, result)
					firstToken = result.firstTokenMs
				} else {
					result, err := svc.handleStreamingResponse(ctx, response, c, account, time.Now(), "synthetic", "synthetic")
					require.NoError(t, err)
					require.NotNil(t, result)
					firstToken = result.firstTokenMs
				}
				require.True(t, strings.HasPrefix(recorder.Body.String(), multiline), "metric accumulation must preserve the exact multiline SSE bytes")
				require.Equal(t, wire, recorder.Body.String(), "existing terminal normalization must preserve this complete fixture")
				require.NotNil(t, firstToken, "valid multiline SSE event must establish semantic TTFT")
				collector.Finish(200, false)
				collector.WhenFinished(func(s requesttiming.Snapshot) {
					require.Contains(t, s.Events, "first_semantic", "valid multiline SSE event must reach requesttiming.Output")
				})
			})
		}
	}
}

func TestPriorityResponsesSemanticEventContext(t *testing.T) {
	cases := []struct {
		name, wire string
		want       bool
	}{
		{"header_text", "event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n", true},
		{"header_reasoning", "event: response.output_item.added\ndata: {\"item\":{\"type\":\"reasoning\",\"summary\":[]}}\n\n", true},
		{"multiline", "event: response.output_text.delta\ndata: {\ndata: \"delta\":\"hello\"}\n\n", true},
		{"payload_precedence", "event: response.created\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n", true},
		{"payload_metadata_precedence", "event: response.output_text.delta\ndata: {\"type\":\"response.created\",\"delta\":\"diagnostic\"}\n\n", false},
		{"blank_resets", "event: response.output_text.delta\n\ndata: {\"delta\":\"untyped\"}\n\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin := time.Now()
			now := origin
			stats := newOpenAIAccountRuntimeStats()
			stats.now = func() time.Time { return now }
			body := newPriorityAttemptBody(io.NopCloser(strings.NewReader(tc.wire)), stats, priorityAttemptKey{endpoint: "/v1/responses"}, context.Background(), 200, true)
			now = origin.Add(100 * time.Millisecond)
			actual, err := io.ReadAll(body)
			require.NoError(t, err)
			require.Equal(t, tc.wire, string(actual))
			if tc.want {
				require.NotNil(t, body.firstSemanticMS, "priority observer must use effective SSE event type and complete payload")
				require.Equal(t, 100.0, *body.firstSemanticMS)
			} else {
				require.Nil(t, body.firstSemanticMS)
			}
			require.NoError(t, body.Close())
		})
	}
}

func TestOpenAIResponsesTTFTForcedFailureDelivery(t *testing.T) {
	setSemanticTTFTTestMode(t)
	for _, passthrough := range []bool{false, true} {
		for _, shape := range []string{"failed", "error", "error_then_failed"} {
			t.Run(fmt.Sprintf("passthrough=%t/%s", passthrough, shape), func(t *testing.T) {
				failed := `{"type":"response.failed","response":{"id":"resp_failure","error":{"code":"invalid_request","message":"synthetic bad request"},"usage":{"input_tokens":9,"output_tokens":2}}}`
				bare := `{"type":"error","error":{"code":"invalid_request","message":"synthetic bad request"},"usage":{"input_tokens":9,"output_tokens":2}}`
				events := []string{failed}
				if shape == "error" {
					events = []string{bare}
				}
				if shape == "error_then_failed" {
					events = []string{bare, failed}
				}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: " + strings.Join(events, "\n\ndata: ") + "\n\n"))}
				svc := &OpenAIGatewayService{cfg: &config.Config{}}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
				var first *int
				var usage *OpenAIUsage
				var err error
				if passthrough {
					r, e := svc.handleStreamingResponsePassthrough(context.Background(), response, c, account, time.Now(), "synthetic", "synthetic")
					require.NotNil(t, r)
					first = r.firstTokenMs
					usage = r.usage
					err = e
				} else {
					r, e := svc.handleStreamingResponse(context.Background(), response, c, account, time.Now(), "synthetic", "synthetic")
					require.NotNil(t, r)
					first = r.firstTokenMs
					usage = r.usage
					err = e
				}
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover), "non-retryable error must be delivered without failover")
				require.Nil(t, first, "forced failed/error delivery must not establish semantic TTFT")
				require.Equal(t, 9, usage.InputTokens)
				require.Equal(t, 2, usage.OutputTokens)
				require.Equal(t, 1, strings.Count(recorder.Body.String(), `"type":"response.failed"`))
				require.Contains(t, recorder.Body.String(), "synthetic bad request")
				require.NotContains(t, recorder.Body.String(), `"type":"error"`, "Codex bare error must preserve existing failed-terminal synthesis")
				require.True(t, recorder.Flushed)
			})
		}
	}
}

// Virtual time controls time.Now/time.Since too; WithClock alone does not.
// Reading the next event waits for the previous event's downstream flush, so
// neither the native scanner nor the passthrough pump can race ahead in time.
type semanticEventClockReader struct {
	now     time.Time
	events  []string
	at      []time.Duration
	index   int
	pending string
	flushed <-chan int
}

func (r *semanticEventClockReader) Read(p []byte) (int, error) {
	if r.pending == "" {
		if r.index >= len(r.events) {
			return 0, io.EOF
		}
		if r.index > 0 {
			<-r.flushed
		}
		time.Sleep(r.at[r.index] - time.Since(r.now))
		r.pending = "data: " + r.events[r.index] + "\n\n"
		r.index++
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}
func (r *semanticEventClockReader) Close() error { return nil }
func TestOpenAIResponsesTTFTControlledEventClock(t *testing.T) {
	setSemanticTTFTTestMode(t)
	for _, path := range []string{"native", "native_guarded", "passthrough"} {
		t.Run(path, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				for request := 0; request < 2; request++ {
					origin := time.Now()
					collector := requesttiming.New(origin, 0)
					ctx := requesttiming.With(context.Background(), collector)
					rec := newOpenAIResponseFlushRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
					body := &semanticEventClockReader{now: origin, flushed: rec.flushEvents, at: []time.Duration{100 * time.Millisecond, 400 * time.Millisecond, 900 * time.Millisecond}, events: []string{`{"type":"response.content_part.added","part":{"type":"reasoning_text","text":""}}`, `{"type":"response.output_text.delta","delta":"later body"}`, `{"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":2}}}`}}
					response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: body, Request: c.Request}
					svc := &OpenAIGatewayService{cfg: &config.Config{}}
					if path == "native_guarded" {
						svc.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds = 30
					}
					account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
					var first *int
					if path == "passthrough" {
						r, e := svc.handleStreamingResponsePassthrough(ctx, response, c, account, origin, "synthetic", "synthetic")
						require.NoError(t, e)
						require.NotNil(t, r)
						first = r.firstTokenMs
					} else {
						r, e := svc.handleStreamingResponse(ctx, response, c, account, origin, "synthetic", "synthetic")
						require.NoError(t, e)
						require.NotNil(t, r)
						first = r.firstTokenMs
					}
					require.NotNil(t, first)
					require.Equal(t, 100, *first, "later content and completion must not overwrite first token; next request starts fresh")
					require.Equal(t, 900*time.Millisecond, time.Since(origin), "completion remains independent of first token")
					collector.Finish(200, false)
					collector.WhenFinished(func(s requesttiming.Snapshot) {
						for _, key := range []string{"first_semantic", "first_visible", "terminal"} {
							require.Contains(t, s.Events, key)
						}
						require.Equal(t, 100.0, s.Events["first_semantic"])
						require.Equal(t, 400.0, s.Events["first_visible"], "this fixture's nonempty body establishes the existing visible endpoint")
						require.Equal(t, 900.0, s.Events["terminal"])
						require.Equal(t, 900.0, s.TotalMS)
					})
				}
			})
		})
	}
}
