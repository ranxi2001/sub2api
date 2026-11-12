//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAPIKeyContextTokenLimitOverridesLegacyAndChecksBoundary(t *testing.T) {
	ctx := context.Background()
	body := []byte(`{"model":"context-policy-test","input":"` + strings.Repeat("hello ", 150) + `"}`)
	codec, err := openAIInputTokensCodecForModel("context-policy-test")
	require.NoError(t, err)
	counter := apiKeyContextCounter{ctx: ctx, codec: codec, limit: 10000}
	require.NoError(t, counter.visit(gjson.ParseBytes(body), 0, false, false, ""))
	boundary := counter.total
	for _, allowed := range []bool{false, true} {
		for _, limit := range []int{boundary - 1, boundary, boundary + 1} {
			key := &APIKey{AllowLongContext: allowed, ContextTokenLimit: limit}
			for _, billing := range []*BillingService{nil, longContextTestBilling(100, false), longContextTestBilling(1000, true)} {
				for _, check := range []func(context.Context, *APIKey, []byte, ...string) error{
					(&GatewayService{billingService: billing}).CheckAPIKeyLongContext,
					(&OpenAIGatewayService{billingService: billing}).CheckAPIKeyLongContext,
				} {
					err := check(withAPIKeyLongContextOutboundModel(ctx, "context-policy-test"), key, body, "public-alias")
					if limit >= boundary {
						require.NoError(t, err)
					} else {
						var policyErr *APIKeyLongContextError
						require.ErrorAs(t, err, &policyErr)
						require.Equal(t, limit, policyErr.Limit)
					}
				}
			}
		}
	}
	// Clearing a ceiling restores each key's old switch/model policy.
	gateway := &GatewayService{billingService: longContextTestBilling(100, false)}
	require.Error(t, gateway.CheckAPIKeyLongContext(ctx, &APIKey{}, body))
	require.NoError(t, gateway.CheckAPIKeyLongContext(ctx, &APIKey{AllowLongContext: true}, body))
}

func TestAPIKeyContextTokenLimitFinalForwardAndSSEError(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/responses/compact", "/v1/chat/completions", "/v1/messages", "/v1beta/models/test:generateContent"} {
		for _, streaming := range []bool{false, true} {
			body := []byte(`{"model":"test","input":"` + strings.Repeat(" x", 150) + `"}`)
			c, rec := longContextForwardTestContext(path, body, nil)
			c.Set("api_key", &APIKey{AllowLongContext: true, ContextTokenLimit: 50})
			if strings.Contains(path, "/models/") {
				c.Params = append(c.Params, struct{ Key, Value string }{Key: "modelAction", Value: "test:generateContent"})
			}
			if streaming {
				c.Header("Content-Type", "text/event-stream")
				_, err := c.Writer.WriteString(": ping\n\n")
				require.NoError(t, err)
			}
			require.Error(t, checkAPIKeyLongContextBeforeForward(context.Background(), c, nil, body))
			require.True(t, c.IsAborted())
			if !streaming {
				require.Equal(t, http.StatusBadRequest, rec.Code)
			} else if strings.Contains(path, "/responses") {
				parts := strings.Split(rec.Body.String(), "data: ")
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(parts[len(parts)-1])), &event))
				require.Equal(t, "response.failed", event["type"])
				response := event["response"].(map[string]any)
				require.Equal(t, APIKeyContextLengthExceededCode, response["error"].(map[string]any)["code"])
			}
			require.Contains(t, rec.Body.String(), APIKeyContextLengthExceededMessage)
			require.NotContains(t, rec.Body.String(), "disabled")
			require.NotContains(t, rec.Body.String(), "context_unverifiable")
		}
	}
}

func TestAPIKeyContextPolicyPublicErrorOmitsInternalDetails(t *testing.T) {
	for _, code := range []string{"context_length_exceeded", "context_unverifiable"} {
		err := &APIKeyLongContextError{Code: code, Limit: 12345, EstimatedTokens: 12346, Reason: "private counter diagnostics"}
		require.Equal(t, APIKeyContextLengthExceededMessage, err.Error())
		// Diagnostic fields remain available internally.
		require.Equal(t, "private counter diagnostics", err.Reason)
	}
}

func TestAdminAPIKeyContextTokenLimitSetPreserveClearAndValidation(t *testing.T) {
	repo := &modelPolicyAPIKeyRepoStub{apiKeyRepoStubForGroupUpdate: apiKeyRepoStubForGroupUpdate{
		key: &APIKey{ID: 1, Key: "sk-context-ceiling-fixture", AllowLongContext: true},
	}}
	cache := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{apiKeyRepo: repo, authCacheInvalidator: cache}
	for _, limit := range []int{1234, 0} {
		result, err := svc.AdminUpdateAPIKeySettings(context.Background(), 1, AdminUpdateAPIKeySettingsInput{ContextTokenLimit: &limit})
		require.NoError(t, err)
		require.Equal(t, limit, result.APIKey.ContextTokenLimit)
		require.True(t, result.APIKey.AllowLongContext)
		require.Equal(t, APIKeyUpdateFields{ContextTokenLimit: true}, repo.fields)
		repo.key, repo.fields = result.APIKey, APIKeyUpdateFields{}
		preserved, err := svc.AdminUpdateAPIKeySettings(context.Background(), 1, AdminUpdateAPIKeySettingsInput{})
		require.NoError(t, err)
		require.Equal(t, limit, preserved.APIKey.ContextTokenLimit)
		require.True(t, repo.fields.IsEmpty())
	}
	require.Equal(t, []string{"sk-context-ceiling-fixture", "sk-context-ceiling-fixture"}, cache.keys)
	for _, invalid := range []int{-1, 2147483648} {
		_, err := svc.AdminUpdateAPIKeySettings(context.Background(), 1, AdminUpdateAPIKeySettingsInput{ContextTokenLimit: &invalid, ResetRateLimitUsage: true})
		require.Error(t, err)
		require.True(t, repo.fields.IsEmpty(), "validation must precede all writes")
	}
	require.Len(t, cache.keys, 2)
}
