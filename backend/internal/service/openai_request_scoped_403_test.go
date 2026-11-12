//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const relayContentPolicy403 = `{"error":{"message":"内容审计命中风险规则，请调整输入后重试","type":"content_policy_violation"}}`
const relayWebSocket403 = `{"error":{"message":"failed to WebSocket dial: expected handshake response status code 101 but got 403","type":"upstream_error"}}`
const cyberAccessModel403 = `{"error":{"message":"The requested Cyber access program is not authorized for this model."}}`

func TestOpenAIRequestScoped403NeverPenalizesCredential(t *testing.T) {
	bodies := map[string]string{
		"cyber_access_model":             cyberAccessModel403,
		"cyber_access_detail":            `{"detail":"The requested Cyber access program is not authorized for this model."}`,
		"cyber_access_message":           `{"message":"The requested Cyber access program is not authorized for this model."}`,
		"cyber_access_permission":        `{"error":{"code":"permission_denied","type":"permission_error","message":"The requested Cyber access program is not authorized for this model."}}`,
		"cyber_access_invalid_request":   `{"error":{"code":null,"type":"invalid_request_error","message":"The requested Cyber access program is not authorized for this model."}}`,
		"cyber_access_json_then_sse":     cyberAccessModel403 + "\nevent: response.failed\ndata: {}\n\n",
		"content_type":                   relayContentPolicy403,
		"content_with_structured_detail": `{"error":{"type":"content_policy_violation"},"detail":{"status":403}}`,
		"content_code":                   `{"error":{"code":"content_policy_violation","type":"invalid_request_error","message":"blocked"}}`,
		"websocket":                      relayWebSocket403,
		"websocket_json_then_sse": relayWebSocket403 + "event: response.failed\n" +
			`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"upstream_error","message":"Upstream request failed"}}}` + "\n\n",
	}
	policies := map[string]map[string]any{
		"default_without_pool_mode": nil,
		"custom_error_code": {
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(http.StatusForbidden)},
		},
		"explicit_account_cooldown": {
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{map[string]any{
				"error_code": float64(http.StatusForbidden), "keywords": []any{"error"},
				"duration_minutes": float64(10), "scope": TempUnschedScopeAccount,
			}},
		},
	}
	for bodyName, body := range bodies {
		for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
			for policyName, credentials := range policies {
				t.Run(bodyName+"/"+accountType+"/"+policyName, func(t *testing.T) {
					h := newOpenAI403TestHarness(t, 601, 1, 2, 3, 4, 5)
					h.account.Type = accountType
					h.account.Credentials = credentials
					gateway := &OpenAIGatewayService{rateLimitService: h.svc}
					for range openAI403DisableThreshold + 1 {
						require.Equal(t, ErrorPolicySkipped, h.svc.CheckErrorPolicy(
							context.Background(), h.account, http.StatusForbidden, []byte(body), "test-model",
						))
						require.False(t, h.handle(body))
						require.False(t, gateway.handleOpenAIAccountUpstreamError(
							context.Background(), h.account, http.StatusForbidden, nil, []byte(body), "test-model",
						))
					}
					h.requireNoAccountPenalty(t)
					require.Zero(t, h.repo.rateLimitedCalls)
					require.False(t, gateway.isOpenAIAccountRuntimeBlocked(h.account))
					require.False(t, gateway.isOpenAIAccountModelRuntimeBlocked(h.account, "test-model"))
					// Ignored relay errors must not spend a real account denial's
					// strike budget or prevent it from receiving its first strike.
					h.account.Credentials = nil
					require.True(t, h.handle(`{"error":{"code":"account_deactivated","message":"Account disabled"}}`))
					require.Equal(t, 1, h.counter.increments)
					require.Equal(t, 1, h.repo.tempCalls)
					require.Zero(t, h.repo.setErrorCalls)
				})
			}
		}
	}
}

func TestOpenAIRequestScoped403PreservesRequestFailover(t *testing.T) {
	for _, body := range []string{relayContentPolicy403, relayWebSocket403, cyberAccessModel403} {
		for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
			h := newOpenAI403TestHarness(t, 602, 1)
			h.account.Type = accountType
			gateway := &OpenAIGatewayService{rateLimitService: h.svc, cfg: &config.Config{}}
			ctx, _ := newOpenAIUpstreamErrorTestContext(t)
			failover := gateway.failoverOpenAIUpstreamHTTPError(
				context.Background(), ctx, h.account,
				newOpenAIUpstreamErrorResponse(http.StatusForbidden, body), []byte(body), "", "test-model",
			)
			require.NotNil(t, failover, "only credential penalties change, not the current request's failover")
			require.Equal(t, http.StatusForbidden, failover.StatusCode)
			require.Equal(t, body, string(failover.ResponseBody))
			require.False(t, failover.RetryableOnSameAccount)
			require.Nil(t, GetOpsCyberPolicy(ctx), "an entitlement denial must not become a user cyber_policy violation")
			h.requireNoAccountPenalty(t)
			require.False(t, gateway.isOpenAIAccountRuntimeBlocked(h.account))
		}
	}
}

func TestOpenAIRequestScoped403DoesNotTrustEchoedOrCredentialErrors(t *testing.T) {
	for name, body := range map[string]string{
		"cyber_access_with_credential_code":        `{"error":{"code":"account_deactivated","message":"The requested Cyber access program is not authorized for this model."}}`,
		"cyber_access_with_credential_type":        `{"error":{"type":"authentication_error","message":"The requested Cyber access program is not authorized for this model."}}`,
		"cyber_access_detail_with_credential_code": `{"code":"account_deactivated","detail":"The requested Cyber access program is not authorized for this model."}`,
		"cyber_access_detail_with_credential_type": `{"type":"authentication_error","detail":"The requested Cyber access program is not authorized for this model."}`,
		"cyber_access_echoed_input":                `{"error":{"message":"Account disabled"},"input":{"detail":"The requested Cyber access program is not authorized for this model."}}`,
		"cyber_access_detail_with_error":           `{"error":{"code":"account_deactivated","message":"Account disabled"},"detail":"The requested Cyber access program is not authorized for this model."}`,
		"cyber_access_in_later_event":              `{"error":{"message":"Account disabled"}}` + "\nevent: error\ndata: " + cyberAccessModel403,
		"generic_model_permission":                 `{"error":{"message":"You are not authorized for this model."}}`,
		"cyber_access_quoted_in_message":           `{"error":{"message":"Account disabled. Input mentioned: The requested Cyber access program is not authorized for this model."}}`,
		"echoed_content_code":                      `{"error":{"code":"account_deactivated","message":"Account disabled"},"input":{"error":{"type":"content_policy_violation"}}}`,
		"content_code_in_message":                  `{"error":{"message":"workspace forbidden; input mentioned content_policy_violation"}}`,
		"content_type_with_credential_code":        `{"error":{"type":"content_policy_violation","code":"account_deactivated","message":"Account disabled"}}`,
		"wrong_websocket_error_type":               `{"error":{"type":"permission_error","message":"failed to WebSocket dial: expected handshake response status code 101 but got 403"}}`,
		"websocket_with_credential_code":           `{"error":{"type":"upstream_error","code":"account_deactivated","message":"failed to WebSocket dial: expected handshake response status code 101 but got 403"}}`,
		"unrelated_websocket_error":                `{"error":{"type":"upstream_error","message":"WebSocket account access revoked"}}`,
		"error_in_later_event":                     `{"error":{"message":"Account disabled"}}` + "\nevent: error\ndata: " + relayContentPolicy403,
		"malformed_json":                           `{"error":{"type":"content_policy_violation"}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newOpenAI403TestHarness(t, 603, int64(openAI403DisableThreshold))
			h.account.Type = AccountTypeAPIKey
			require.True(t, h.handle(body))
			require.Equal(t, 1, h.counter.increments)
			require.Equal(t, 1, h.repo.setErrorCalls)
		})
	}
}

func TestOpenAIRequestScoped403KeepsOtherPlatformsAndStatuses(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	for _, status := range []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusTooManyRequests, http.StatusBadGateway} {
		require.False(t, isOpenAIRequestScoped403(account, status, []byte(cyberAccessModel403)))
		require.False(t, isOpenAIRequestScoped403(account, status, []byte(relayContentPolicy403)))
		require.False(t, isOpenAIRequestScoped403(account, status, []byte(relayWebSocket403)))
	}
	for _, platform := range []string{PlatformAnthropic, PlatformGemini, PlatformGrok} {
		require.False(t, isOpenAIRequestScoped403(&Account{Platform: platform}, http.StatusForbidden, []byte(cyberAccessModel403)))
		h := newOpenAI403TestHarness(t, 604, 1)
		h.account.Platform = platform
		h.account.Type = AccountTypeAPIKey
		require.True(t, h.handle(relayContentPolicy403))
		require.Equal(t, 1, h.repo.setErrorCalls)
	}
	require.False(t, isOpenAIRequestScoped403(nil, http.StatusForbidden, []byte(relayContentPolicy403)))
}
