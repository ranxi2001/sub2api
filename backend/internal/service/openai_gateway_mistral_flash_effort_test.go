//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestMistralGLMFlashMaxUsageScope(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  string
	}{
		{"zai-glm-5-3-flash", "max"},
		{"provider/zai_glm_5_3_flash", "max"},
		{"zai-glm-5-3", "max"},
		{"gpt-5.6", "max"},
		{"deepseek-v4-flash", "max"},
		{"kimi-k2", "max"},
		{"k3", "max"},
		{"zai-glm-5-3-flash-latest", "xhigh"},
		{"mistral-small-latest", "xhigh"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			body := []byte(`{"reasoning_effort":"max"}`)
			effort := extractOpenAIReasoningEffortFromBody(body, tc.model)
			require.NotNil(t, effort)
			require.Equal(t, tc.want, *effort)
		})
	}
}

func TestMistralGLMFlashMaxEffortMatchesUsageAcrossEndpoints(t *testing.T) {
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			for _, model := range []string{"zai-glm-5-3-flash", "glm-flash-alias"} {
				for _, effort := range []string{"xhigh", "max"} {
					t.Run(fmt.Sprintf("%s/stream=%t/model=%s/effort=%s", endpoint, stream, model, effort), func(t *testing.T) {
						request := map[string]any{"model": model, "stream": stream}
						switch endpoint {
						case "/v1/chat/completions":
							request["messages"] = []any{map[string]any{"role": "user", "content": "hello"}}
							request["reasoning_effort"] = effort
						case "/v1/responses":
							request["input"] = "hello"
							request["reasoning"] = map[string]any{"effort": effort}
						case "/v1/messages":
							request["messages"] = []any{map[string]any{"role": "user", "content": "hello"}}
							request["max_tokens"] = 64
							request["output_config"] = map[string]any{"effort": effort}
						}
						body, err := json.Marshal(request)
						require.NoError(t, err)
						c, _ := gin.CreateTestContext(httptest.NewRecorder())
						c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
						contentType := "application/json"
						response := `{"id":"fixture","model":"zai-glm-5-3-flash","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
						if stream {
							contentType = "text/event-stream"
							response = "data: {\"id\":\"fixture\",\"model\":\"zai-glm-5-3-flash\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\ndata: [DONE]\n\n"
						}
						upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response))}}
						account := rawChatCompletionsTestAccount()
						account.Platform = PlatformMistral
						account.Credentials["api_protocol"] = APIProtocolAdaptive
						account.Credentials["model_mapping"] = map[string]any{"glm-flash-alias": "zai-glm-5-3-flash"}
						svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
						var result *OpenAIForwardResult
						switch endpoint {
						case "/v1/chat/completions":
							result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
						case "/v1/responses":
							result, err = svc.Forward(context.Background(), c, account, body)
						case "/v1/messages":
							result, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
						}
						require.NoError(t, err)
						require.NotNil(t, result)
						require.Len(t, upstream.requests, 1)
						require.Equal(t, "/v1/chat/completions", upstream.lastReq.URL.Path)
						require.Equal(t, "zai-glm-5-3-flash", gjson.GetBytes(upstream.lastBody, "model").String())
						require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
						require.NotNil(t, result.ReasoningEffort)
						require.Equal(t, "max", *result.ReasoningEffort)
					})
				}
			}
		}
	}
}
