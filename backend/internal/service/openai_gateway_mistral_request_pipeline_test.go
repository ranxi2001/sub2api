//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestMistralRequestCompatibilityAcrossEndpoints(t *testing.T) {
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			name := endpoint + "/buffered"
			if stream {
				name = endpoint + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				request := map[string]any{"model": "glm-alias", "stream": stream}
				switch endpoint {
				case "/v1/chat/completions":
					request["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hello", "cache_control": map[string]any{"type": "ephemeral"}}}}}
					request["reasoning_effort"] = "none"
					request["store"] = false
					request["enable_thinking"] = false
					request["thinking"] = map[string]any{"type": "disabled"}
				case "/v1/responses":
					request["input"] = "hello"
					request["store"] = false
					request["reasoning"] = map[string]any{"effort": "none", "summary": "auto"}
				case "/v1/messages":
					request["messages"] = []any{map[string]any{"role": "user", "content": "hello"}}
					request["max_tokens"] = 64
					request["thinking"] = map[string]any{"type": "disabled"}
				}
				body, err := json.Marshal(request)
				require.NoError(t, err)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
				contentType := "application/json"
				response := `{"id":"fixture","model":"zai-glm-5-3","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
				if stream {
					contentType = "text/event-stream"
					response = "data: {\"id\":\"fixture\",\"model\":\"zai-glm-5-3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\ndata: [DONE]\n\n"
				}
				upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response))}}
				account := rawChatCompletionsTestAccount()
				account.Platform = PlatformMistral
				account.Credentials["api_protocol"] = APIProtocolAdaptive
				account.Credentials["model_mapping"] = map[string]any{"glm-alias": "zai-glm-5-3"}
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
				require.Equal(t, "zai-glm-5-3", gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, "low", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
				require.NotNil(t, result.ReasoningEffort)
				require.Equal(t, "low", *result.ReasoningEffort)
				for _, field := range []string{"store", "thinking", "enable_thinking", "messages.0.content.0.cache_control"} {
					require.False(t, gjson.GetBytes(upstream.lastBody, field).Exists(), field)
				}
			})
		}
	}
}

func TestMistralMaxEffortMatchesUsage(t *testing.T) {
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		t.Run(endpoint, func(t *testing.T) {
			request := map[string]any{"model": "zai-glm-5-3", "stream": false}
			switch endpoint {
			case "/v1/chat/completions":
				request["messages"] = []any{map[string]any{"role": "user", "content": "hello"}}
				request["reasoning_effort"] = "xhigh"
			case "/v1/responses":
				request["input"] = "hello"
				request["reasoning"] = map[string]any{"effort": "max"}
			case "/v1/messages":
				request["messages"] = []any{map[string]any{"role": "user", "content": "hello"}}
				request["max_tokens"] = 64
				request["output_config"] = map[string]any{"effort": "max"}
			}
			body, err := json.Marshal(request)
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
			upstream := &httpUpstreamRecorder{resp: mistralTestResponse(`{"id":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)}
			account := rawChatCompletionsTestAccount()
			account.Platform = PlatformMistral
			account.Credentials["api_protocol"] = APIProtocolAdaptive
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
			require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
			require.NotNil(t, result.ReasoningEffort)
			require.Equal(t, "max", *result.ReasoningEffort)
		})
	}
}

func TestMistralAccountTestUsesRequestCompatibility(t *testing.T) {
	c, _ := mistralTestContext()
	c.Request = c.Request.WithContext(withPelicanTestOptions(c.Request.Context(), pelicanTestOptions{reasoningEffort: "none"}))
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))}}
	svc := &AccountTestService{httpUpstream: upstream}
	account := &Account{ID: 900, Platform: PlatformMistral, Type: AccountTypeAPIKey}
	require.NoError(t, svc.testOpenAIChatCompletionsConnection(c, account, "zai-glm-5-3", "hello", "https://upstream.example/v1", "fixture-key"))
	require.Equal(t, "low", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
	require.Len(t, upstream.requests, 1)
}

func TestMistralInvalidRequestDoesNotCallUpstream(t *testing.T) {
	body := []byte(`{"model":"zai-glm-5-3","messages":[{"role":"assistant","content":"hello","reasoning_content":42}],"stream":false}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	account := rawChatCompletionsTestAccount()
	account.Platform = PlatformMistral
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	_, err := svc.forwardAsRawChatCompletions(context.Background(), c, account, body, "")
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Empty(t, upstream.requests)
	require.NotContains(t, w.Body.String(), "hello")
}

func TestMistralDeepSeekMultiTurnOmitsReasoningContentAcrossEndpoints(t *testing.T) {
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		t.Run(endpoint, func(t *testing.T) {
			request := map[string]any{"model": "deepseek-v4.1-flash", "stream": false}
			switch endpoint {
			case "/v1/chat/completions":
				request["messages"] = []any{
					map[string]any{"role": "user", "content": "hi"},
					map[string]any{"role": "assistant", "content": "hello"},
					map[string]any{"role": "user", "content": "follow up"},
				}
			case "/v1/responses":
				request["input"] = []any{
					map[string]any{"type": "message", "role": "user", "content": "hi"},
					map[string]any{"type": "reasoning", "id": "rs_missing", "encrypted_content": "enc"},
					map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "hello"}}},
					map[string]any{"type": "message", "role": "user", "content": "follow up"},
				}
			case "/v1/messages":
				request["max_tokens"] = 64
				request["messages"] = []any{
					map[string]any{"role": "user", "content": "hi"},
					map[string]any{"role": "assistant", "content": "hello"},
					map[string]any{"role": "user", "content": "follow up"},
				}
			}
			body, err := json.Marshal(request)
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
			upstream := &httpUpstreamRecorder{resp: mistralTestResponse(`{"id":"fixture","model":"deepseek-v4-1-flash-1017","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)}
			account := rawChatCompletionsTestAccount()
			account.Platform = PlatformMistral
			account.Credentials["api_protocol"] = APIProtocolAdaptive
			account.Credentials["model_mapping"] = map[string]any{"deepseek-v4.1-flash": "deepseek-v4-1-flash-1017"}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			switch endpoint {
			case "/v1/chat/completions":
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
			case "/v1/responses":
				_, err = svc.Forward(context.Background(), c, account, body)
			case "/v1/messages":
				_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
			}
			require.NoError(t, err)
			require.Len(t, upstream.requests, 1)
			require.Equal(t, "deepseek-v4-1-flash-1017", gjson.GetBytes(upstream.lastBody, "model").String())
			require.False(t, gjson.GetBytes(upstream.lastBody, "messages.1.reasoning_content").Exists(), "Mistral rejects assistant.reasoning_content with HTTP 422 extra_forbidden")
		})
	}
}
