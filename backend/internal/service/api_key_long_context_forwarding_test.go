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
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A public model can admit the visible input while the selected account maps
// it to a model with a lower long-context boundary. Exercise real forwarding
// paths so an ingress-only check would leave these regressions failing.
func TestAPIKeyLongContextForwardingUsesAccountMappedModel(t *testing.T) {
	const publicModel = "context-public"
	const mappedModel = "context-limited"
	content := strings.Repeat("context ", 300)
	chatBody := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}]}`, publicModel, content))
	messagesBody := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":64,"messages":[{"role":"user","content":%q}]}`, publicModel, content))
	responsesBody := []byte(fmt.Sprintf(`{"model":%q,"input":%q}`, publicModel, content))
	type forward func(context.Context, *gin.Context, *Account, []byte) (*OpenAIForwardResult, error)
	tests := []struct {
		name   string
		path   string
		body   []byte
		native bool
		call   func(*OpenAIGatewayService) forward
	}{
		{"raw chat", "/v1/chat/completions", chatBody, false, func(s *OpenAIGatewayService) forward {
			return func(ctx context.Context, c *gin.Context, a *Account, body []byte) (*OpenAIForwardResult, error) {
				return s.forwardAsRawChatCompletions(ctx, c, a, body, "")
			}
		}},
		{"chat to responses", "/v1/chat/completions", chatBody, false, func(s *OpenAIGatewayService) forward {
			return func(ctx context.Context, c *gin.Context, a *Account, body []byte) (*OpenAIForwardResult, error) {
				return s.forwardAsChatCompletions(ctx, c, a, body, "", "", false)
			}
		}},
		{"messages to responses", "/v1/messages", messagesBody, false, func(s *OpenAIGatewayService) forward {
			return func(ctx context.Context, c *gin.Context, a *Account, body []byte) (*OpenAIForwardResult, error) {
				return s.forwardAsAnthropic(ctx, c, a, body, "", "")
			}
		}},
		{"responses", "/v1/responses", responsesBody, false, func(s *OpenAIGatewayService) forward {
			return s.Forward
		}},
		{"responses to chat", "/v1/responses", responsesBody, false, func(s *OpenAIGatewayService) forward {
			return func(ctx context.Context, c *gin.Context, a *Account, body []byte) (*OpenAIForwardResult, error) {
				return s.forwardResponsesViaRawChatCompletions(ctx, c, a, body, time.Now())
			}
		}},
		{"messages to chat", "/v1/messages", messagesBody, false, func(s *OpenAIGatewayService) forward {
			return func(ctx context.Context, c *gin.Context, a *Account, body []byte) (*OpenAIForwardResult, error) {
				return s.forwardAnthropicViaRawChatCompletions(ctx, c, a, body, "")
			}
		}},
		{"responses to native anthropic", "/v1/responses", responsesBody, true, func(s *OpenAIGatewayService) forward {
			return func(ctx context.Context, c *gin.Context, a *Account, body []byte) (*OpenAIForwardResult, error) {
				return s.forwardResponsesViaNativeAnthropic(ctx, c, a, body, "")
			}
		}},
		{"chat to native anthropic", "/v1/chat/completions", chatBody, true, func(s *OpenAIGatewayService) forward {
			return func(ctx context.Context, c *gin.Context, a *Account, body []byte) (*OpenAIForwardResult, error) {
				return s.forwardChatCompletionsViaNativeAnthropic(ctx, c, a, body, "")
			}
		}},
		{"messages to native anthropic", "/v1/messages", messagesBody, true, func(s *OpenAIGatewayService) forward {
			return func(ctx context.Context, c *gin.Context, a *Account, body []byte) (*OpenAIForwardResult, error) {
				return s.forwardAnthropicViaNativeAnthropicEndpoint(ctx, c, a, body, "")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{err: fmt.Errorf("unexpected upstream call")}
			s := &OpenAIGatewayService{
				cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream,
				billingService: NewBillingService(&config.Config{}, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
					publicModel: {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, LongContextInputTokenThreshold: 10_000, LongContextInputCostMultiplier: 2},
					mappedModel: {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, LongContextInputTokenThreshold: 100, LongContextInputCostMultiplier: 2},
				}}),
			}
			key := &APIKey{ID: 87, AllowLongContext: false}
			// The public input passes before the selected account changes its model.
			require.NoError(t, s.CheckAPIKeyLongContext(context.Background(), key, tt.body, publicModel))
			account := rawChatCompletionsTestAccount()
			account.Credentials["model_mapping"] = map[string]any{publicModel: mappedModel}
			account.Extra = map[string]any{"openai_responses_supported": true}
			if tt.native {
				account.Platform = PlatformKimi
				account.Credentials["api_protocol"] = APIProtocolAnthropic
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(tt.body))
			c.Set("api_key", key)
			result, err := tt.call(s)(context.Background(), c, account, tt.body)
			var policyErr *APIKeyLongContextError
			require.ErrorAs(t, err, &policyErr)
			require.Equal(t, "context_length_exceeded", policyErr.Code)
			require.Equal(t, 100, policyErr.Limit)
			require.Nil(t, result)
			require.Nil(t, upstream.lastReq, "mapped long context must be rejected before contacting upstream")
		})
	}
}

func TestAPIKeyLongContextForwardingAllowedKeyKeepsAccountMapping(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"` + strings.Repeat("context ", 300) + `"}]}`)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"chatcmpl_context","model":"context-limited","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":301,"completion_tokens":1,"total_tokens":302}}`)),
	}}
	s := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream, billingService: longContextTestBilling(100, false)}
	account := rawChatCompletionsTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"gpt-5.4": "context-policy-test"}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 88, AllowLongContext: true})
	result, err := s.forwardAsRawChatCompletions(context.Background(), c, account, body, "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "context-policy-test", gjson.GetBytes(upstream.lastBody, "model").String())
}
