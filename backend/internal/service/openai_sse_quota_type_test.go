//go:build unit && retrydeadline

package service

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSSETypeOnlyQuotaReaders(t *testing.T) {
	for _, kind := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		for _, quota := range []string{"insufficient_quota", "quota_exceeded", "usage_limit_reached"} {
			for _, passthrough := range []bool{false, true} {
				for _, pool := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/passthrough=%t/pool502=%t", kind, quota, passthrough, pool), func(t *testing.T) {
						c, rec := newOpenAIUpstreamErrorTestContext(t)
						cfg := &config.Config{}
						repo := &oauth429RateLimitRepo{}
						rateLimits := NewRateLimitService(repo, nil, cfg, nil, nil)
						svc := &OpenAIGatewayService{cfg: cfg, rateLimitService: rateLimits}
						rateLimits.SetAccountRuntimeBlocker(svc)
						account := &Account{ID: 42, Platform: PlatformOpenAI, Type: kind, Credentials: map[string]any{
							"pool_mode": pool, "pool_mode_retry_status_codes": []any{float64(http.StatusBadGateway)},
						}}
						payload := fmt.Sprintf(`{"type":"response.failed","response":{"status":"failed","error":{"type":%q,"message":"Quota exhausted"}}}`, quota)
						resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + payload + "\n\n"))}
						var err error
						if passthrough {
							_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "gpt-5.1", "gpt-5.1")
						} else {
							_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "gpt-5.1", "gpt-5.1")
						}
						var failure *UpstreamFailoverError
						require.ErrorAs(t, err, &failure)
						t.Logf("SSE quota observation: type=%s semantic_status=%d", quota, failure.StatusCode)
						require.Equal(t, http.StatusTooManyRequests, failure.StatusCode, "terminal type-only quota must preserve semantic 429")
						require.False(t, failure.RetryableOnSameAccount, "explicit pool502 retry must not resend the exhausted owner")
						require.True(t, failure.SameAccountRetryDeadline.IsZero())
						require.False(t, failure.QuotaRejectedBeforeExecution, "HTTP200 SSE is not HTTP429 pre-execution rejection proof")
						require.Empty(t, rec.Body.String(), "the pre-output terminal is still eligible for bounded account failover")
						_, retryStarted := svc.openaiOAuth429RetryStartedAt.Load(account.ID)
						require.False(t, retryStarted)
						// API-key pools deliberately opt out of persistent health updates.
						if !pool || kind == AccountTypeOAuth {
							require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
							require.Equal(t, 1, repo.setRateLimitedCalls)
							require.True(t, repo.lastRateLimitedUntil.After(time.Now()))
						}
					})
				}
			}
		}
	}
}

func TestSSEQuotaBareErrorCanComplete(t *testing.T) {
	for _, quota := range []string{"insufficient_quota", "quota_exceeded", "usage_limit_reached"} {
		t.Run(quota, func(t *testing.T) {
			c, rec := newOpenAIUpstreamErrorTestContext(t)
			cfg := &config.Config{}
			repo := &oauth429RateLimitRepo{}
			rateLimits := NewRateLimitService(repo, nil, cfg, nil, nil)
			svc := &OpenAIGatewayService{cfg: cfg, rateLimitService: rateLimits}
			rateLimits.SetAccountRuntimeBlocker(svc)
			account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			body := fmt.Sprintf("data: {\"type\":\"error\",\"error\":{\"type\":%q,\"message\":\"Quota exhausted\"}}\n\n", quota) +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_recovered\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n"
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
			_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "gpt-5.1", "gpt-5.1")
			require.NoError(t, err)
			require.Contains(t, rec.Body.String(), `"type":"response.completed"`)
			require.NotContains(t, rec.Body.String(), `"type":"response.failed"`)
			require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
			require.Zero(t, repo.setRateLimitedCalls)
		})
	}
}

func TestSSEQuotaClassificationRetainsExplicitCodeAndIntegrity(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.failed","response":{"error":{"type":"insufficient_quota","code":"credit_balance_exhausted"}}}`,
		`{"type":"response.failed","response":{"error":{"type":"insufficient_quota","type":"server_error"}}}`,
	} {
		require.Equal(t, http.StatusBadGateway, openAIStreamFailedEventSemanticStatus([]byte(payload), "Quota exhausted"))
	}
}
