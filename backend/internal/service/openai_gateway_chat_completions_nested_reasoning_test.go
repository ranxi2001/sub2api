//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const nestedReasoningUpstreamSSE = "" +
	`data: {"type":"response.created","response":{"id":"resp_nested","model":"gpt-6-sol"}}` + "\n\n" +
	`data: {"type":"response.reasoning_summary_text.delta","delta":"weighing options"}` + "\n\n" +
	`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
	`data: {"type":"response.completed","response":{"id":"resp_nested","model":"gpt-6-sol","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}` + "\n\n"

// Chat Completions clients may send the Responses-style nested
// reasoning.effort. It must reach the Responses upstream exactly like the flat
// reasoning_effort form, and accounting must agree with what was forwarded.
func TestForwardAsChatCompletions_OAuthForwardsNestedReasoningEffort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name      string
		reasoning string
		want      string
	}{
		{"nested_medium", `"reasoning":{"effort":"medium"}`, "medium"},
		{"flat_high", `"reasoning_effort":"high"`, "high"},
		{"nested_wins_over_flat", `"reasoning_effort":"high","reasoning":{"effort":"medium"}`, "medium"},
		{"nested_high", `"reasoning":{"effort":"high"}`, "high"},
		{"nested_xhigh", `"reasoning":{"effort":"xhigh"}`, "xhigh"},
		{"nested_max", `"reasoning":{"effort":"max"}`, "max"},
		{"flat_max", `"reasoning_effort":"max"`, "max"},
		{"absent", ``, ""},
	}
	for _, model := range []string{"gpt-6-sol", "gpt-6.1-sol"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range cases {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", model, stream, tc.name), func(t *testing.T) {
					extra := ""
					if tc.reasoning != "" {
						extra = "," + tc.reasoning
					}
					body := []byte(fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"hello"}]%s}`, model, stream, extra))

					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
					c.Request.Header.Set("Content-Type", "application/json")

					upstream := &httpUpstreamRecorder{resp: &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_nested_reasoning"}},
						Body:       io.NopCloser(strings.NewReader(nestedReasoningUpstreamSSE)),
					}}
					svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
					account := &Account{
						ID: 7, Name: "openai-oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
						Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
					}

					result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
					require.NoError(t, err)
					require.NotNil(t, result)
					require.NotEmpty(t, upstream.lastBody)

					// 1. What actually went upstream.
					require.Equal(t, tc.want, gjson.GetBytes(upstream.lastBody, "reasoning.effort").String(), "upstream body: %s", upstream.lastBody)
					if tc.want == "" {
						require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning.effort").Exists())
					} else {
						require.Equal(t, "auto", gjson.GetBytes(upstream.lastBody, "reasoning.summary").String())
					}
					// 2. Accounting matches the forwarded effort.
					require.Equal(t, tc.want, optionalStringValue(result.ReasoningEffort))
					// 3. Reasoning reaches the Chat client.
					require.Contains(t, rec.Body.String(), "reasoning_content")
					require.Contains(t, rec.Body.String(), "weighing options")
				})
			}
		}
	}
}
