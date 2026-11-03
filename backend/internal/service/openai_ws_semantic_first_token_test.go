package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWSSemanticFirstTokenForwarders(t *testing.T) {
	cases := []struct {
		name, event string
		want        bool
	}{
		{"typed_reasoning", `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`, true},
		{"reasoning_part", `{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":""}}`, true},
		{"reasoning_delta", `{"type":"response.reasoning_text.delta","delta":"thinking"}`, true},
		{"text", `{"type":"response.output_text.delta","delta":"hello"}`, true},
		{"body", `{"type":"response.output_text.done","text":"hello"}`, true},
		{"tool", `{"type":"response.function_call_arguments.delta","delta":"{}"}`, true},
		{"metadata", `{"type":"response.metadata"}`, false},
		{"heartbeat", `{"type":"keepalive"}`, false},
		{"empty_delta", `{"type":"response.output_text.delta","delta":""}`, false},
		{"object_delta", `{"type":"response.output_text.delta","delta":{}}`, false},
		{"empty_message", `{"type":"response.output_item.added","item":{"type":"message","content":[]}}`, false},
	}
	for _, path := range []string{"ws_to_sse", "http_bridge"} {
		for _, tc := range cases {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				gin.SetMode(gin.TestMode)
				events := []string{`{"type":"response.created","response":{"id":"resp_synthetic"}}`, tc.event, `{"type":"response.completed","response":{"id":"resp_synthetic","usage":{"input_tokens":1,"output_tokens":1}}}`}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				var result *OpenAIForwardResult
				var err error
				if path == "ws_to_sse" {
					conn := &openAIWSCaptureConn{}
					for _, event := range events {
						conn.events = append(conn.events, []byte(event))
					}
					s := wsSSETestService(t, &wsSSETestDialer{conn: conn}, &httpUpstreamRecorder{})
					SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
					result, err = s.Forward(context.Background(), c, wsSSETestAccount(), []byte(wsSSETestRequest))
					require.Contains(t, rec.Body.String(), tc.event)
				} else {
					body := "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
					s := &OpenAIGatewayService{cfg: newOpenAIWSV2TestConfig(), httpUpstream: &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}}}
					payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"synthetic"}`)
					result, err = s.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, "sk-test", payload, len(payload), "gpt-5.1", "", "", "", "", 1, func([]byte) error { return nil })
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				if tc.want {
					require.NotNil(t, result.FirstTokenMs, "semantic event must establish first token")
				} else {
					require.Nil(t, result.FirstTokenMs, "metadata or empty payload must leave first token unknown")
				}
			})
		}
	}
}
