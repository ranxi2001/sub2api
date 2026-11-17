//go:build unit

package service

import (
	"context"
	"io"
	"math"
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

func TestReclassifyCacheCreationByRequestedTTL_ConservesAggregate(t *testing.T) {
	cases := []struct {
		name                    string
		agg, in5m, in1h         int
		target                  string
		wantAgg, want5m, want1h int
		wantChanged             bool
	}{
		{"reviewer counterexample negative 5m", 1000, -900, 1000, cacheTTLTarget5m, 1000, 1000, 0, true},
		{"negative 5m to 1h", 1000, -900, 1000, cacheTTLTarget1h, 1000, 0, 1000, false},
		{"aggregate only to 5m", 500, 0, 0, cacheTTLTarget5m, 500, 500, 0, false},
		{"aggregate only to 1h", 500, 0, 0, cacheTTLTarget1h, 500, 0, 500, true},
		{"incomplete buckets below aggregate", 1000, 0, 400, cacheTTLTarget5m, 1000, 1000, 0, true},
		{"buckets exceed aggregate", 1000, 800, 800, cacheTTLTarget1h, 1000, 0, 1000, true},
		{"missing aggregate uses buckets", 0, 300, 200, cacheTTLTarget5m, 500, 500, 0, true},
		{"missing aggregate saturates on overflow", 0, math.MaxInt, 10, cacheTTLTarget5m, math.MaxInt, math.MaxInt, 0, true},
		{"all negative", -5, -1, -2, cacheTTLTarget5m, -5, -1, -2, false},
		{"nothing written", 0, 0, 0, cacheTTLTarget1h, 0, 0, 0, false},
		{"already 1h", 700, 0, 700, cacheTTLTarget1h, 700, 0, 700, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := ClaudeUsage{CacheCreationInputTokens: tc.agg, CacheCreation5mTokens: tc.in5m, CacheCreation1hTokens: tc.in1h}
			changed := reclassifyCacheCreationByRequestedTTL(&u, tc.target)
			require.Equal(t, tc.wantChanged, changed)
			require.Equal(t, tc.wantAgg, u.CacheCreationInputTokens)
			require.Equal(t, tc.want5m, u.CacheCreation5mTokens)
			require.Equal(t, tc.want1h, u.CacheCreation1hTokens)
			if u.CacheCreationInputTokens > 0 {
				require.Equal(t, u.CacheCreationInputTokens, u.CacheCreation5mTokens+u.CacheCreation1hTokens, "分桶之和必须等于 aggregate")
			}
		})
	}
	require.False(t, reclassifyCacheCreationByRequestedTTL(nil, cacheTTLTarget5m))
}

func TestRecordUsage_CacheTTLBillingFromRequest_AnomalousUpstreamUsage(t *testing.T) {
	const price5m, price1h = 5e-6, 8e-6 // claude-opus-4-6 默认价卡
	cases := []struct {
		name            string
		agg, in5m, in1h int
		requested       string
		want5m, want1h  int
	}{
		{"negative 5m bucket", 1000, -900, 1000, cacheTTLTarget5m, 1000, 0},
		{"buckets exceed aggregate", 1000, 800, 800, cacheTTLTarget5m, 1000, 0},
		{"aggregate only", 1000, 0, 0, cacheTTLTarget1h, 0, 1000},
		{"incomplete buckets", 1000, 0, 400, cacheTTLTarget5m, 1000, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)
			groupID := int64(77)
			inputPrice, outputPrice, write5m, write1h := 5e-6, 25e-6, price5m, price1h
			apiKey := &APIKey{ID: 10, GroupID: &groupID, Group: &Group{
				ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true, RateMultiplier: 1,
				ModelPricing: []ChannelModelPricing{{
					Models: []string{"claude-opus-4-6"}, BillingMode: BillingModeToken,
					InputPrice: &inputPrice, OutputPrice: &outputPrice, CacheWritePrice: &write5m, CacheWrite1hPrice: &write1h,
				}},
			}}
			account := &Account{ID: 30, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{cacheTTLBillingFromRequestExtraKey: true}}

			require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
				Result: &ForwardResult{
					RequestID: "cache_ttl_anomaly_" + tc.name, Model: "claude-opus-4-6", Duration: time.Second,
					Usage:             ClaudeUsage{CacheCreationInputTokens: tc.agg, CacheCreation5mTokens: tc.in5m, CacheCreation1hTokens: tc.in1h},
					RequestedCacheTTL: tc.requested,
				},
				APIKey: apiKey, User: &User{ID: 20}, Account: account,
			}))
			log := usageRepo.lastLog
			require.NotNil(t, log)
			require.Equal(t, tc.agg, log.CacheCreationTokens)
			require.Equal(t, tc.want5m, log.CacheCreation5mTokens)
			require.Equal(t, tc.want1h, log.CacheCreation1hTokens)
			require.Equal(t, log.CacheCreationTokens, log.CacheCreation5mTokens+log.CacheCreation1hTokens)
			require.InDelta(t, float64(tc.want5m)*price5m+float64(tc.want1h)*price1h, log.CacheCreationCost, 1e-12)
		})
	}
}
