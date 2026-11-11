//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIGPT61CompatEffortValidationHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"chat-responses", "chat-raw", "responses-chat"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				model  string
				effort string
				deny   bool
			}{
				{model: "gpt-6.1-terra", effort: "minimal", deny: true},
				{model: "openai/GPT_6.1_TERRA", effort: "minimal", deny: true},
				{model: "gpt-6.1-terra-minimal", effort: "low", deny: true},
				{model: "openai/GPT_6.1_TERRA_MINIMAL", effort: "low", deny: true},
				{model: "gpt-6.1-terra", effort: "none"},
				{model: "gpt-6.1-terra", effort: "high"},
				{model: "gpt-6.1-sol", effort: "minimal", deny: true},
				{model: "gpt-6.1-sol", effort: "none", deny: true},
				{model: "gpt-6.1-sol", effort: "low"},
			} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s/%s", route, stream, tc.model, tc.effort), func(t *testing.T) {
					model := "gpt-6.1-terra"
					if strings.Contains(tc.model, "sol") {
						model = "gpt-6.1-sol"
					}
					account := rawGPT56ResponsesAPIKeyAccount(tc.model, model)
					body := []byte(fmt.Sprintf(`{"model":%q,"stream":%t,"reasoning_effort":%q,"messages":[{"role":"user","content":"hello"}]}`, tc.model, stream, tc.effort))
					path := "/v1/chat/completions"
					if route == "responses-chat" {
						path = "/v1/responses"
						body = []byte(fmt.Sprintf(`{"model":%q,"stream":%t,"reasoning":{"effort":%q},"input":"hello"}`, tc.model, stream, tc.effort))
					}
					response := openAICompatSSECompletedResponse("resp_gpt61", model)
					if route != "chat-responses" {
						payload := `{"id":"chat_gpt61","model":"` + model + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`
						contentType := "application/json"
						if stream {
							payload = "data: " + `{"id":"chat_gpt61","model":"` + model + `","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}` + "\n\ndata: [DONE]\n\n"
							contentType = "text/event-stream"
						}
						response = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(payload))}
					}
					upstream := &httpUpstreamRecorder{resp: response}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
					c := adaptiveProtocolTestContext(path, body)
					var result *OpenAIForwardResult
					var err error
					switch route {
					case "chat-responses":
						result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
					case "chat-raw":
						result, err = svc.forwardAsRawChatCompletions(context.Background(), c, account, body, "")
					case "responses-chat":
						result, err = svc.forwardResponsesViaRawChatCompletions(context.Background(), c, account, body, time.Now())
					}
					if tc.deny {
						require.ErrorContains(t, err, "does not support reasoning effort")
						require.Equal(t, http.StatusBadRequest, c.Writer.Status())
						require.Nil(t, result)
						require.Empty(t, upstream.requests)
						return
					}
					require.NoError(t, err)
					require.Len(t, upstream.requests, 1)
					effortPath := "reasoning_effort"
					if route == "chat-responses" {
						effortPath = "reasoning.effort"
					}
					require.Equal(t, model, gjson.GetBytes(upstream.lastBody, "model").String())
					require.Equal(t, tc.effort, gjson.GetBytes(upstream.lastBody, effortPath).String())
					require.Equal(t, tc.effort, optionalStringValue(result.ReasoningEffort))
				})
			}
		}
	}
}
