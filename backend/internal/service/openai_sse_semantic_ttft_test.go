package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{openAITTFTMode: OpenAITTFTModeSemantic})
	t.Cleanup(func() {
		gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{openAITTFTMode: OpenAITTFTModeSemantic})
	})
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
					if r != nil {
						first = r.firstTokenMs
						require.Equal(t, 1, r.usage.InputTokens)
						require.Equal(t, 1, r.usage.OutputTokens)
					}
				case "passthrough":
					r, e := svc.handleStreamingResponsePassthrough(ctx, resp, c, account, time.Now(), "synthetic", "synthetic")
					err = e
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
					svc = wsSSETestService(t, &wsSSETestDialer{conn: conn}, &httpUpstreamRecorder{})
					SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
					r, e := svc.Forward(context.Background(), c, wsSSETestAccount(), []byte(wsSSETestRequest))
					err = e
					if r != nil {
						first = r.FirstTokenMs
					}
				case "http_bridge":
					svc = &OpenAIGatewayService{cfg: newOpenAIWSV2TestConfig(), httpUpstream: &httpUpstreamRecorder{resp: resp}}
					payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)
					r, e := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5.1", "", "", "", "", 1, func([]byte) error { return nil })
					err = e
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
