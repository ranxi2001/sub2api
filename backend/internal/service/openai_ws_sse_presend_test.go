package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIWSSSEPayloadEncodedLimit(t *testing.T) {
	payload := map[string]any{"type": "response.create", "input": "<>&\u2028你好", "n": json.Number("9007199254740993")}
	var expected bytes.Buffer
	require.NoError(t, json.NewEncoder(&expected).Encode(payload))
	for _, delta := range []int{-1, 0, 1} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			writes := 0
			err := encodeOpenAIWSSSEPayload(payload, int64(expected.Len()+delta), func(p []byte) error {
				writes++
				require.Equal(t, expected.Bytes(), p, "inspect and send identical encoded bytes, including escapes/newline")
				return nil
			})
			if delta < 0 {
				require.ErrorIs(t, err, errOpenAIWSSSEPayloadTooLarge)
				require.Zero(t, writes)
			} else {
				require.NoError(t, err)
				require.Equal(t, 1, writes)
			}
		})
	}
	writes := 0
	err := encodeOpenAIWSSSEPayload(map[string]any{"bad": make(chan int)}, 1024, func([]byte) error { writes++; return nil })
	require.Error(t, err)
	require.NotErrorIs(t, err, errOpenAIWSSSEPayloadTooLarge)
	require.Zero(t, writes)
	err = encodeOpenAIWSSSEPayload(payload, 1024, func([]byte) error { return io.ErrClosedPipe })
	require.ErrorIs(t, err, io.ErrClosedPipe)
	require.NotErrorIs(t, err, errOpenAIWSSSEPayloadTooLarge)
}

func TestOpenAIWSSSEUnsupportedTools(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"none", `{}`, false},
		{"empty", `{"tools":[]}`, false},
		{"null", `{"tools":null}`, false},
		{"hosted", `{"tools":[{"type":"web_search"}]}`, true},
		{"preview", `{"tools":[{"type":"web_search_preview"}]}`, true},
		{"dated_preview", `{"tools":[{"type":"web_search_preview_2025_03_11"}]}`, true},
		{"mixed", `{"tools":[{"type":"function","name":"f"},{"type":"web_search"}]}`, true},
		{"function_name_is_not_type", `{"tools":[{"type":"function","name":"web_search"}]}`, false},
		{"custom", `{"tools":[{"type":"custom","name":"web_search"}]}`, false},
		{"other_hosted", `{"tools":[{"type":"image_generation"}]}`, false},
		{"nested_data", `{"tools":[{"type":"function","name":"f","parameters":{"type":"web_search"}}]}`, false},
		{"malformed_not_reclassified", `{"tools":[null,1,{"type":123}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(tc.body), &payload))
			require.Equal(t, tc.want, hasOpenAIWSSSEUnsupportedTool(payload))
		})
	}
}

func wsSSEGuardHTTPUpstream() *httpUpstreamRecorder {
	return &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK,
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_http\",\"usage\":{\"input_tokens\":7,\"output_tokens\":2}}}\n\n"))}}
}

func TestOpenAIWSSSEAccelerationPreSendFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	escaped := strings.Replace(wsSSETestRequest, "hello", strings.Repeat("<", 1024), 1)
	tools := strings.TrimSuffix(wsSSETestRequest, "}") + `,"tools":[{"type":"web_search","search_context_size":"high"}],"tool_choice":"required"}`
	for _, tc := range []struct {
		name, body, reason string
		limit              int64
		prewarm            bool
		wantDials          int32
	}{
		{"size", wsSSETestRequest, "oauth_ws_sse_payload_too_large", 64, false, 1},
		{"size_before_prewarm", wsSSETestRequest, "oauth_ws_sse_payload_too_large", 64, true, 1},
		{"encoded_growth", escaped, "oauth_ws_sse_payload_too_large", int64(len(escaped) + 500), false, 1},
		{"hosted_tool", tools, "oauth_ws_sse_unsupported_tool", 1 << 20, false, 0},
		{"hosted_tool_before_prewarm", tools, "oauth_ws_sse_unsupported_tool", 1 << 20, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			conn := &openAIWSCaptureConn{}
			d := &wsSSETestDialer{conn: conn}
			httpUpstream := wsSSEGuardHTTPUpstream()
			s := wsSSETestService(t, d, httpUpstream)
			s.cfg.Gateway.OpenAIWS.SSEAccelerationMaxPayloadBytes = tc.limit
			s.cfg.Gateway.OpenAIWS.PrewarmGenerateEnabled = tc.prewarm
			account := wsSSETestAccount()
			account.Extra["base_rpm"] = 1
			rpm := &openAIRPMTestCache{counts: map[int64]int{}}
			s.rpmCache = rpm
			result, err := s.Forward(context.Background(), c, account, []byte(tc.body))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.False(t, result.OpenAIWSMode)
			require.Equal(t, 7, result.Usage.InputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)
			require.Equal(t, tc.reason, c.GetString("openai_ws_transport_reason"))
			require.Len(t, httpUpstream.requests, 1)
			require.Equal(t, 1, rpm.counts[account.ID], "only the actual HTTP send consumes RPM")
			require.Equal(t, tc.wantDials, d.calls.Load())
			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Body.String(), "response.completed")
			require.False(t, gjson.GetBytes(httpUpstream.lastBody, "type").Exists(), "do not leak the WS envelope into HTTP")
			require.JSONEq(t, gjson.Get(tc.body, "input").Raw, gjson.GetBytes(httpUpstream.lastBody, "input").Raw)
			if strings.Contains(tc.name, "hosted_tool") {
				require.JSONEq(t, gjson.Get(tc.body, "tools").Raw, gjson.GetBytes(httpUpstream.lastBody, "tools").Raw)
				require.Equal(t, "required", gjson.GetBytes(httpUpstream.lastBody, "tool_choice").String())
			}
			conn.mu.Lock()
			writes, closed := len(conn.writes), conn.closed
			conn.mu.Unlock()
			require.Zero(t, writes, "not even generate=false may be sent before a local bypass")
			require.False(t, closed, "a locally rejected payload must not evict a healthy socket")
		})
	}
}

func TestOpenAIWSSSEAccelerationPreservesSmallRequests(t *testing.T) {
	for _, body := range []string{
		wsSSETestRequest,
		strings.TrimSuffix(wsSSETestRequest, "}") + `,"tools":[{"type":"function","name":"web_search","parameters":{"type":"object","properties":{}}}]}`,
		strings.Repeat(" ", 8192) + wsSSETestRequest,
	} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			conn := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_ws","usage":{"input_tokens":7,"output_tokens":2}}}`)}}
			d := &wsSSETestDialer{conn: conn}
			h := &httpUpstreamRecorder{}
			s := wsSSETestService(t, d, h)
			s.cfg.Gateway.OpenAIWS.SSEAccelerationMaxPayloadBytes = 4096
			result, err := s.Forward(context.Background(), c, wsSSETestAccount(), []byte(body))
			require.NoError(t, err)
			require.True(t, result.OpenAIWSMode)
			require.Empty(t, h.requests)
			conn.mu.Lock()
			writes := len(conn.writes)
			conn.mu.Unlock()
			require.Equal(t, 1, writes)
		})
	}
}

func TestOpenAIWSSSEPreSendFallbackSafety(t *testing.T) {
	for _, localErr := range []error{errOpenAIWSSSEPayloadTooLarge, errOpenAIWSSSEUnsupportedTool} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		require.NotEmpty(t, openAIWSSSEFallbackReason(context.Background(), c, localErr))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.Empty(t, openAIWSSSEFallbackReason(ctx, c, localErr))
		c.Writer.WriteHeaderNow()
		require.Empty(t, openAIWSSSEFallbackReason(context.Background(), c, localErr))
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	for _, err := range []error{io.EOF, io.ErrClosedPipe, errors.New(errOpenAIWSSSEPayloadTooLarge.Error()),
		wrapOpenAIWSFallback("message_too_big", coderws.CloseError{Code: coderws.StatusMessageTooBig})} {
		require.Empty(t, openAIWSSSEFallbackReason(context.Background(), c, err), "peer errors never prove a request was unsent")
	}
}

// Match wsjson.Write's baseline Encoder -> WriterFunc path without network I/O.
// io.Discard avoids benchmarking an artificial copy or a second JSON encode.
type wsSSEBenchmarkWriter func([]byte) (int, error)

func (w wsSSEBenchmarkWriter) Write(p []byte) (int, error) { return w(p) }
func BenchmarkOpenAIWSSSEPayload(b *testing.B) {
	for _, size := range []int{1024, 64 << 10, 1 << 20, 8 << 20} {
		payload := map[string]any{"type": "response.create", "input": strings.Repeat("x", size)}
		for _, guarded := range []bool{false, true} {
			name := fmt.Sprintf("%d/guarded=%v", size, guarded)
			run := func() error {
				if guarded {
					return encodeOpenAIWSSSEPayload(payload, 15<<20, func(p []byte) error { _, err := io.Discard.Write(p); return err })
				}
				return json.NewEncoder(wsSSEBenchmarkWriter(io.Discard.Write)).Encode(payload)
			}
			b.Run(name, func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(size))
				if err := run(); err != nil {
					b.Fatal(err)
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := run(); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(name+"/parallel", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(size))
				if err := run(); err != nil {
					b.Fatal(err)
				}
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						if err := run(); err != nil {
							b.Error(err)
							return
						}
					}
				})
			})
		}
	}
}

func BenchmarkOpenAIWSSSEToolGuard(b *testing.B) {
	tools := make([]any, 128)
	for i := range tools {
		tools[i] = map[string]any{"type": "function", "name": fmt.Sprint("tool", i), "parameters": map[string]any{"description": strings.Repeat("schema", 1024)}}
	}
	payload := map[string]any{"tools": tools}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if hasOpenAIWSSSEUnsupportedTool(payload) {
				b.Error("unexpected rejection")
				return
			}
		}
	})
}

func TestOpenAIWSSSEGuardsDoNotChangeNativeWS(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	SetOpenAIClientTransport(c, OpenAIClientTransportWS)
	conn := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_native","usage":{"input_tokens":7,"output_tokens":2}}}`)}}
	d := &wsSSETestDialer{conn: conn}
	h := &httpUpstreamRecorder{}
	s := wsSSETestService(t, d, h)
	s.cfg.Gateway.OpenAIWS.SSEAccelerationMaxPayloadBytes = 1
	body := strings.TrimSuffix(wsSSETestRequest, "}") + `,"tools":[{"type":"web_search"}]}`
	result, err := s.Forward(context.Background(), c, wsSSETestAccount(), []byte(body))
	require.NoError(t, err)
	require.True(t, result.OpenAIWSMode)
	require.Empty(t, h.requests)
	conn.mu.Lock()
	writes := len(conn.writes)
	conn.mu.Unlock()
	require.Equal(t, 1, writes)
}

func TestOpenAIWSSSEPayloadParallelIsolation(t *testing.T) {
	for i := 0; i < 32; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			payload := map[string]any{"input": strings.Repeat(fmt.Sprint(i), 4096)}
			var expected bytes.Buffer
			require.NoError(t, json.NewEncoder(&expected).Encode(payload))
			limit := int64(expected.Len())
			if i%2 == 0 {
				limit--
			}
			for j := 0; j < 10; j++ {
				called := false
				err := encodeOpenAIWSSSEPayload(payload, limit, func(p []byte) error {
					called = true
					require.Equal(t, expected.Bytes(), p)
					return nil
				})
				if i%2 == 0 {
					require.ErrorIs(t, err, errOpenAIWSSSEPayloadTooLarge)
					require.False(t, called)
				} else {
					require.NoError(t, err)
					require.True(t, called)
				}
			}
		})
	}
}

type wsSSEPeerTooBigConn struct{ *openAIWSCaptureConn }

func (c *wsSSEPeerTooBigConn) ReadMessage(context.Context) ([]byte, error) {
	return nil, coderws.CloseError{Code: coderws.StatusMessageTooBig}
}

func TestOpenAIWSSSEPeerTooBigDoesNotReplay(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	capture := &openAIWSCaptureConn{}
	d := &wsSSETestDialer{conn: &wsSSEPeerTooBigConn{capture}}
	h := &httpUpstreamRecorder{}
	s := wsSSETestService(t, d, h)
	_, err := s.Forward(context.Background(), c, wsSSETestAccount(), []byte(wsSSETestRequest))
	require.Error(t, err)
	require.Empty(t, h.requests)
	capture.mu.Lock()
	writes := len(capture.writes)
	capture.mu.Unlock()
	require.Equal(t, 1, writes)
}

// Alternate equal batches so desktop clock/load drift cannot systematically
// favor the baseline, which otherwise runs before every guarded subbenchmark.
// These custom metrics measure per-encode wall latency, not gateway QPS.
func BenchmarkOpenAIWSSSEPayloadPaired(b *testing.B) {
	for _, size := range []int{1024, 64 << 10, 1 << 20, 8 << 20} {
		batch := 8
		if size >= 1<<20 {
			batch = 1
		}
		payload := map[string]any{"type": "response.create", "input": strings.Repeat("x", size)}
		for _, parallel := range []bool{false, true} {
			b.Run(fmt.Sprintf("%d/parallel=%v", size, parallel), func(b *testing.B) {
				run := func(guarded bool) error {
					if guarded {
						return encodeOpenAIWSSSEPayload(payload, 15<<20, func(p []byte) error { _, err := io.Discard.Write(p); return err })
					}
					return json.NewEncoder(wsSSEBenchmarkWriter(io.Discard.Write)).Encode(payload)
				}
				var baseNS, guardNS atomic.Int64
				var workerOrder atomic.Uint32
				measure := func(guarded bool) int64 {
					start := time.Now()
					for j := 0; j < batch; j++ {
						if err := run(guarded); err != nil {
							b.Error(err)
							return 0
						}
					}
					return time.Since(start).Nanoseconds()
				}
				require.NoError(b, run(false))
				require.NoError(b, run(true))
				b.ReportAllocs()
				b.ResetTimer()
				if parallel {
					b.RunParallel(func(pb *testing.PB) {
						var base, guard int64
						reverse := workerOrder.Add(1)%2 == 0
						for pb.Next() {
							if reverse {
								guard += measure(true)
								base += measure(false)
							} else {
								base += measure(false)
								guard += measure(true)
							}
							reverse = !reverse
						}
						baseNS.Add(base)
						guardNS.Add(guard)
					})
				} else {
					for i := 0; i < b.N; i++ {
						if i%2 == 0 {
							baseNS.Add(measure(false))
							guardNS.Add(measure(true))
						} else {
							guardNS.Add(measure(true))
							baseNS.Add(measure(false))
						}
					}
				}
				b.StopTimer()
				n := float64(b.N * batch)
				b.ReportMetric(float64(baseNS.Load())/n, "base-ns/encode")
				b.ReportMetric(float64(guardNS.Load())/n, "guard-ns/encode")
				b.ReportMetric(100*float64(guardNS.Load())/float64(baseNS.Load()), "guard/base-percent")
			})
		}
	}
}
func TestOpenAIWSSSEPrewarmMustRespectEncodedLimit(t *testing.T) {
	// Discover the exact wire size after the gateway's normal transformations.
	// The generate=false prewarm is larger than this otherwise valid message.
	forward := func(prewarm bool, limit int64) (*OpenAIForwardResult, *openAIWSCaptureConn, *httpUpstreamRecorder, error) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
		conn := &openAIWSCaptureConn{events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_pre","usage":{"input_tokens":7,"output_tokens":2}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_main","usage":{"input_tokens":7,"output_tokens":2}}}`),
		}}
		h := wsSSEGuardHTTPUpstream()
		s := wsSSETestService(t, &wsSSETestDialer{conn: conn}, h)
		s.cfg.Gateway.OpenAIWS.PrewarmGenerateEnabled = prewarm
		s.cfg.Gateway.OpenAIWS.SSEAccelerationMaxPayloadBytes = limit
		result, err := s.Forward(context.Background(), c, wsSSETestAccount(), []byte(wsSSETestRequest))
		return result, conn, h, err
	}
	result, conn, _, err := forward(false, 1<<20)
	require.NoError(t, err)
	require.True(t, result.OpenAIWSMode)
	conn.mu.Lock()
	mainPayload := cloneMapStringAny(conn.lastWrite)
	conn.mu.Unlock()
	encoded, err := json.Marshal(mainPayload)
	require.NoError(t, err)
	limit := int64(len(encoded) + 1)
	mainPayload["generate"] = false
	prewarmPayload, err := json.Marshal(mainPayload)
	require.NoError(t, err)
	require.Greater(t, int64(len(prewarmPayload)+1), limit)

	result, conn, h, err := forward(true, limit)
	require.NoError(t, err)
	require.False(t, result.OpenAIWSMode, "an oversized prewarm must be rejected before any WS message is sent")
	require.Len(t, h.requests, 1)
	conn.mu.Lock()
	writes := len(conn.writes)
	conn.mu.Unlock()
	require.Zero(t, writes)
}

func TestOpenAIWSSSEFallbackKeepsHTTPNamespaceContract(t *testing.T) {
	for _, flatten := range []bool{false, true} {
		for _, bypass := range []string{"size", "hosted_tool", "handshake"} {
			t.Run(fmt.Sprintf("flatten=%v/%s", flatten, bypass), func(t *testing.T) {
				body := `{"model":"gpt-5.1","stream":true,"instructions":"test","input":[{"role":"user","content":"hello","namespace":"remove_from_message"},{"type":"function_call","name":"lookup","namespace":"mcp","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok","namespace":"remove_from_output"}],"tools":[{"type":"namespace","name":"mcp","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}]}]}`
				if bypass == "hosted_tool" {
					body = strings.TrimSuffix(body, "]}") + `,{"type":"web_search"}]}`
				}
				body = strings.TrimSuffix(body, "}") + `,"previous_response_id":""}`
				forward := func(accelerated bool) ([]byte, string) {
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
					d := &wsSSETestDialer{conn: &openAIWSCaptureConn{}}
					if bypass == "handshake" {
						d.status = http.StatusUpgradeRequired
						d.err = errors.New("handshake refused")
					}
					h := wsSSEGuardHTTPUpstream()
					item := `{"type":"function_call","name":"lookup","namespace":"mcp","call_id":"fc_1","arguments":"{}"}`
					if flatten {
						item = `{"type":"function_call","name":"mcp__lookup","call_id":"fc_1","arguments":"{}"}`
					}
					h.resp.Body = io.NopCloser(strings.NewReader(`data: {"type":"response.output_item.added","item":` + item + "}\n\n" + `data: {"type":"response.completed","response":{"id":"resp_namespace","output":[` + item + `],"usage":{"input_tokens":7,"output_tokens":2}}}` + "\n\n"))
					s := wsSSETestService(t, d, h)
					if bypass == "size" {
						s.cfg.Gateway.OpenAIWS.SSEAccelerationMaxPayloadBytes = 64
					}
					a := wsSSETestAccount()
					a.Extra["openai_oauth_ws_sse_acceleration"] = accelerated
					a.Extra["openai_responses_flatten_namespaces"] = flatten
					result, err := s.Forward(context.Background(), c, a, []byte(body))
					require.NoError(t, err)
					require.False(t, result.OpenAIWSMode)
					require.Len(t, h.requests, 1)
					return h.lastBody, rec.Body.String()
				}
				normalHTTP, normalResponse := forward(false)
				fallbackHTTP, fallbackResponse := forward(true)
				require.Equal(t, normalResponse, fallbackResponse, "HTTP fallback must restore response namespaces as well")
				if flatten {
					require.NotContains(t, fallbackResponse, "mcp__lookup")
				}
				require.False(t, gjson.GetBytes(fallbackHTTP, "previous_response_id").Exists())
				require.JSONEq(t, gjson.GetBytes(normalHTTP, "input").Raw, gjson.GetBytes(fallbackHTTP, "input").Raw, "fallback must apply the ordinary HTTP input-namespace policy")
				require.JSONEq(t, gjson.GetBytes(normalHTTP, "tools").Raw, gjson.GetBytes(fallbackHTTP, "tools").Raw, "fallback must honor the account's flattening setting")
			})
		}
	}
}

func TestOpenAIWSSSEFallbackPreservesHTTPValidation(t *testing.T) {
	for _, accelerated := range []bool{false, true} {
		t.Run(fmt.Sprint(accelerated), func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			h := wsSSEGuardHTTPUpstream()
			conn := &openAIWSCaptureConn{}
			s := wsSSETestService(t, &wsSSETestDialer{conn: conn}, h)
			s.cfg.Gateway.OpenAIWS.SSEAccelerationMaxPayloadBytes = 64
			a := wsSSETestAccount()
			a.Extra["openai_oauth_ws_sse_acceleration"] = accelerated
			a.Extra["openai_responses_flatten_namespaces"] = true
			body := strings.TrimSuffix(wsSSETestRequest, "}") + `,"tools":[{"type":"function","name":"mcp__lookup","parameters":{"type":"object","properties":{}}},{"type":"namespace","name":"mcp","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}]}]}`
			_, err := s.Forward(context.Background(), c, a, []byte(body))
			require.Error(t, err)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, "tools", gjson.Get(rec.Body.String(), "error.param").String())
			require.Empty(t, h.requests)
			conn.mu.Lock()
			writes := len(conn.writes)
			conn.mu.Unlock()
			require.Zero(t, writes)
		})
	}
}

type wsSSEGuardHeaderDialer struct {
	*wsSSETestDialer
	headers http.Header
}

func (d *wsSSEGuardHeaderDialer) Dial(ctx context.Context, url string, headers http.Header, proxy string) (openAIWSClientConn, int, http.Header, error) {
	conn, status, _, err := d.wsSSETestDialer.Dial(ctx, url, headers, proxy)
	return conn, status, d.headers, err
}

func TestOpenAIWSSSEFallbackDiscardsUnsentHandshakeHeader(t *testing.T) {
	for _, httpState := range []string{"", "http-turn-state"} {
		t.Run(httpState, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Header("X-Local-Request", "keep")
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			conn := &openAIWSCaptureConn{}
			d := &wsSSETestDialer{conn: conn}
			h := wsSSEGuardHTTPUpstream()
			if httpState != "" {
				h.resp.Header.Set(openAIWSTurnStateHeader, httpState)
			}
			s := wsSSETestService(t, d, h)
			s.openaiWSPool.setClientDialerForTest(&wsSSEGuardHeaderDialer{d, http.Header{http.CanonicalHeaderKey(openAIWSTurnStateHeader): []string{"unused-ws-state"}}})
			s.cfg.Gateway.OpenAIWS.SSEAccelerationMaxPayloadBytes = 64
			result, err := s.Forward(context.Background(), c, wsSSETestAccount(), []byte(wsSSETestRequest))
			require.NoError(t, err)
			require.False(t, result.OpenAIWSMode)
			want := []string(nil)
			if httpState != "" {
				want = []string{httpState}
			}
			require.Equal(t, want, rec.Result().Header.Values(openAIWSTurnStateHeader), "only the actual HTTP attempt may contribute its turn state")
			require.Equal(t, "keep", rec.Result().Header.Get("X-Local-Request"))
		})
	}
}
