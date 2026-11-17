//go:build unit

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
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRequestedAnthropicCacheTTL(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"no cache_control", `{"system":"hi","messages":[{"role":"user","content":"hi"}]}`, cacheTTLTarget5m},
		{"breakpoint without ttl", `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}]}`, cacheTTLTarget5m},
		{"explicit 5m", `{"messages":[{"role":"user","content":[{"type":"text","text":"m","cache_control":{"type":"ephemeral","ttl":"5m"}}]}]}`, cacheTTLTarget5m},
		{"system 1h", `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`, cacheTTLTarget1h},
		{"tools 1h", `{"tools":[{"name":"t","input_schema":{},"cache_control":{"type":"ephemeral","ttl":"1h"}}]}`, cacheTTLTarget1h},
		{"message 1h", `{"messages":[{"role":"user","content":[{"type":"text","text":"m","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`, cacheTTLTarget1h},
		{"top-level 1h", `{"cache_control":{"type":"ephemeral","ttl":"1h"},"messages":[]}`, cacheTTLTarget1h},
		{"mixed 1h and 5m", `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","text":"m","cache_control":{"type":"ephemeral"}}]}]}`, cacheTTLTarget1h},
		{"invalid json", `{not json`, cacheTTLTarget5m},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, requestedAnthropicCacheTTL([]byte(tc.body)))
		})
	}
}

func TestAccount_IsCacheTTLBillingFromRequestEnabled(t *testing.T) {
	enabled := map[string]any{cacheTTLBillingFromRequestExtraKey: true}
	require.True(t, (&Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: enabled}).IsCacheTTLBillingFromRequestEnabled())
	require.False(t, (&Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: enabled}).IsCacheTTLBillingFromRequestEnabled())
	require.False(t, (&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: enabled}).IsCacheTTLBillingFromRequestEnabled())
	require.False(t, (&Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}).IsCacheTTLBillingFromRequestEnabled())
	require.False(t, (&Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{cacheTTLBillingFromRequestExtraKey: "true"}}).IsCacheTTLBillingFromRequestEnabled())
	require.False(t, (*Account)(nil).IsCacheTTLBillingFromRequestEnabled())
}

func TestForward_PassthroughRecordsRequestedCacheTTL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name    string
		enabled bool
		body    string
		want    string
	}{
		{"client asks 1h", true, `{"model":"claude-opus-4-6","stream":true,"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"hi"}]}`, cacheTTLTarget1h},
		{"client omits ttl", true, `{"model":"claude-opus-4-6","stream":true,"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`, cacheTTLTarget5m},
		{"switch off", false, `{"model":"claude-opus-4-6","stream":true,"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"hi"}]}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

			upstreamSSE := strings.Join([]string{
				`data: {"type":"message_start","message":{"usage":{"input_tokens":11,"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":100}}}}`,
				"",
				`data: {"type":"message_delta","usage":{"output_tokens":3}}`,
				"",
				`data: {"type":"message_stop"}`,
				"",
				"data: [DONE]",
				"",
			}, "\n")
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(upstreamSSE)),
			}}
			cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
			svc := &GatewayService{
				cfg: cfg, responseHeaderFilter: compileResponseHeaderFilter(cfg), httpUpstream: upstream,
				rateLimitService: &RateLimitService{}, deferredService: &DeferredService{},
			}
			account := newAnthropicAPIKeyAccountForTest()
			if tc.enabled {
				account.Extra[cacheTTLBillingFromRequestExtraKey] = true
			}

			body := []byte(tc.body)
			result, err := svc.Forward(context.Background(), c, account, &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-4-6", Stream: true})
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tc.want, result.RequestedCacheTTL)
			require.Equal(t, 100, result.Usage.CacheCreation1hTokens, "Forward 本身不改写上游返回的用量")
			require.JSONEq(t, tc.body, string(upstream.lastBody), "透传请求体不应被改写")
		})
	}
}

func TestRecordUsage_CacheTTLBillingFromRequest(t *testing.T) {
	cases := []struct {
		name           string
		enabled        bool
		requestedTTL   string
		want5m, want1h int
		wantOverridden bool
	}{
		{"requested 5m rebills upstream 1h as 5m", true, cacheTTLTarget5m, 1000, 0, true},
		{"requested 1h keeps 1h", true, cacheTTLTarget1h, 0, 1000, false},
		{"unknown request ttl falls back to 5m", true, "", 1000, 0, true},
		{"switch off keeps upstream classification", false, cacheTTLTarget5m, 0, 1000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			subRepo := &openAIRecordUsageSubRepoStub{}
			svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)

			groupID := int64(77)
			apiKey := &APIKey{ID: 10, GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true, RateMultiplier: 1}}
			account := &Account{ID: 30, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{}}
			if tc.enabled {
				account.Extra[cacheTTLBillingFromRequestExtraKey] = true
			}

			require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
				Result: &ForwardResult{
					RequestID: "cache_ttl_from_request_" + tc.name, Model: "claude-opus-4-6", Duration: time.Second,
					Usage: ClaudeUsage{
						InputTokens: 10, OutputTokens: 5,
						CacheCreationInputTokens: 1000, CacheCreation1hTokens: 1000,
					},
					RequestedCacheTTL: tc.requestedTTL,
				},
				APIKey: apiKey, User: &User{ID: 20}, Account: account,
			}))

			require.NotNil(t, usageRepo.lastLog)
			require.Equal(t, tc.want5m, usageRepo.lastLog.CacheCreation5mTokens)
			require.Equal(t, tc.want1h, usageRepo.lastLog.CacheCreation1hTokens)
			require.Equal(t, tc.wantOverridden, usageRepo.lastLog.CacheTTLOverridden)
		})
	}
}
