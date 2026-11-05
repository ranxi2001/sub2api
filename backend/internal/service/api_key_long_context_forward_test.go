//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func longContextForwardTestContext(path string, body []byte, billing *BillingService) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Set("api_key", &APIKey{})
	SetAPIKeyLongContextForwardChecker(c, func(ctx context.Context, key *APIKey, body []byte, models ...string) error {
		return checkAPIKeyLongContext(ctx, billing, key, body, models...)
	})
	return c, rec
}

func TestAPIKeyLongContextAnthropicBuildersRecheckFinalBody(t *testing.T) {
	billing := longContextTestBilling(100, false)
	svc := &GatewayService{cfg: &config.Config{}, billingService: billing}
	body := []byte(`{"model":"context-policy-test","max_tokens":16,"messages":[{"role":"user","content":"` + strings.Repeat("context ", 150) + `"}]}`)
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	for name, build := range map[string]func(*gin.Context) error{
		"standard and protocol bridges": func(c *gin.Context) error {
			_, _, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "fixture", "apikey", "context-policy-test", false, false)
			return err
		},
		"API key passthrough": func(c *gin.Context) error {
			_, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(context.Background(), c, account, body, "fixture")
			return err
		},
		"Bedrock before signing or send": func(c *gin.Context) error {
			_, err := svc.executeBedrockUpstream(context.Background(), c, account, body, "context-policy-test", "fixture-region", false, nil, "", "")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, rec := longContextForwardTestContext("/v1/messages", body, billing)
			var policyErr *APIKeyLongContextError
			require.ErrorAs(t, build(c), &policyErr)
			require.Equal(t, "context_length_exceeded", policyErr.Code)
			require.Equal(t, 100, policyErr.Limit)
			require.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestAPIKeyLongContextGeminiAccountMappingBeforeSend(t *testing.T) {
	billing := longContextTestBilling(100, false)
	large := strings.Repeat("context ", 150)
	claudeBody := []byte(`{"model":"public-alias","max_tokens":16,"messages":[{"role":"user","content":"` + large + `"}]}`)
	account := &Account{ID: 2, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "fixture", "model_mapping": map[string]any{"public-alias": "context-policy-test"},
	}}
	for _, protocol := range []string{"messages", "chat/completions", "native"} {
		t.Run(protocol, func(t *testing.T) {
			body, path := claudeBody, "/v1/"+protocol
			if protocol == "native" {
				body = []byte(`{"contents":[{"role":"user","parts":[{"text":"` + large + `"}]}]}`)
				path = "/v1beta/models/public-alias:generateContent"
			}
			// Public input fits the ordinary limit; the selected account lowers it.
			require.NoError(t, checkAPIKeyLongContext(context.Background(), billing, &APIKey{}, body, "public-alias"))
			c, rec := longContextForwardTestContext(path, body, billing)
			upstream := &geminiCompatHTTPUpstreamStub{}
			svc := &GeminiMessagesCompatService{cfg: &config.Config{}, httpUpstream: upstream}
			var err error
			switch protocol {
			case "messages":
				_, err = svc.Forward(context.Background(), c, account, body)
			case "chat/completions":
				_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body)
			case "native":
				c.Params = gin.Params{{Key: "modelAction", Value: "public-alias:generateContent"}}
				_, err = svc.ForwardNative(context.Background(), c, account, "public-alias", "generateContent", false, body)
			}
			var policyErr *APIKeyLongContextError
			require.ErrorAs(t, err, &policyErr)
			require.Equal(t, "context_length_exceeded", policyErr.Code)
			require.Equal(t, 100, policyErr.Limit)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Zero(t, upstream.calls, "local policy must block before network traffic")
		})
	}
}

func TestAPIKeyLongContextAntigravityChecksTransformedBody(t *testing.T) {
	body := []byte(`{"model":"context-policy-test","request":{"contents":[{"parts":[{"text":"` + strings.Repeat("context ", 150) + `"}]}]}}`)
	c, rec := longContextForwardTestContext("/v1/messages", body, longContextTestBilling(100, false))
	upstream := &geminiCompatHTTPUpstreamStub{}
	_, err := (&AntigravityGatewayService{}).antigravityRetryLoop(antigravityRetryLoopParams{
		ctx: context.Background(), c: c, account: &Account{ID: 3, Platform: PlatformAntigravity}, body: body,
		httpUpstream: upstream, requestedModel: "public-alias", action: "streamGenerateContent",
	})
	var policyErr *APIKeyLongContextError
	require.ErrorAs(t, err, &policyErr)
	require.Equal(t, "context_length_exceeded", policyErr.Code)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Zero(t, upstream.calls)
}

func TestAPIKeyLongContextForwardPreservesBodyAndRevalidatedKey(t *testing.T) {
	body := []byte(`{"model":"context-policy-test","input":"hello"}`)
	c, rec := longContextForwardTestContext("/v1/responses", body, longContextTestBilling(100, false))
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://fixture/responses", bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, checkAPIKeyLongContextRequestBeforeForward(context.Background(), c, request, "context-policy-test"))
	unchanged, err := io.ReadAll(request.Body)
	require.NoError(t, err)
	require.Equal(t, body, unchanged)
	require.Equal(t, http.StatusOK, rec.Code)

	opaque := []byte(`{"previous_response_id":"resp_fixture"}`)
	c.Set("api_key", &APIKey{AllowLongContext: true})
	require.NoError(t, checkAPIKeyLongContextBeforeForward(context.Background(), c, nil, opaque))
	c.Set("api_key", &APIKey{AllowLongContext: false})
	var policyErr *APIKeyLongContextError
	require.ErrorAs(t, checkAPIKeyLongContextBeforeForward(context.Background(), c, nil, opaque), &policyErr)
	require.Equal(t, "context_unverifiable", policyErr.Code)
}
