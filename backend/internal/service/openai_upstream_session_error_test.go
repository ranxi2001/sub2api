//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const upstreamSessionBlockedBody = `{"error":{"code":"session_blocked_by_cyber_policy","message":"该会话已被网络安全策略屏蔽，请开启新会话 / This session is blocked by cyber-security policy, please start a new session","type":"permission_error"}}`

func TestUpstreamSessionBlockedNeverPenalizesCredential(t *testing.T) {
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(accountType, func(t *testing.T) {
			h := newOpenAI403TestHarness(t, 551, 1, 2, 3, 4)
			h.account.Type = accountType
			gateway := &OpenAIGatewayService{rateLimitService: h.svc}
			for i := 0; i < openAI403DisableThreshold+1; i++ {
				require.False(t, h.handle(upstreamSessionBlockedBody))
				require.False(t, gateway.handleOpenAIAccountUpstreamError(
					context.Background(), h.account, http.StatusForbidden, nil, []byte(upstreamSessionBlockedBody), "gpt-6.1-sol",
				))
			}
			h.requireNoAccountPenalty(t)
			require.False(t, gateway.isOpenAIAccountRuntimeBlocked(h.account))

			// Genuine credential-level denials must still receive their first
			// strike, unaffected by the frozen conversation's repeated failures.
			require.True(t, h.handle(`{"error":{"code":"account_deactivated","message":"Account disabled"}}`))
			require.Equal(t, 1, h.counter.increments)
			require.Equal(t, 1, h.repo.tempCalls)
			require.Zero(t, h.repo.setErrorCalls)
		})
	}
}

func TestUpstreamSessionBlockedBypassesCustomAccountPolicies(t *testing.T) {
	for _, credentials := range []map[string]any{
		{"custom_error_codes_enabled": true, "custom_error_codes": []any{float64(http.StatusForbidden)}},
		{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{map[string]any{
				"error_code": float64(http.StatusForbidden), "keywords": []any{"session"}, "duration_minutes": float64(10),
			}},
		},
	} {
		h := newOpenAI403TestHarness(t, 551, 1)
		h.account.Type = AccountTypeAPIKey
		h.account.Credentials = credentials
		require.Equal(t, ErrorPolicySkipped, h.svc.CheckErrorPolicy(
			context.Background(), h.account, http.StatusForbidden, []byte(upstreamSessionBlockedBody),
		))
		require.False(t, h.handle(upstreamSessionBlockedBody))
		h.requireNoAccountPenalty(t)
	}
}

func TestUpstreamSessionBlockedIsTerminalAndPreservesClientGuidance(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{ID: 551, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	for _, status := range []int{http.StatusForbidden, http.StatusBadGateway} {
		require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(account, status, "", []byte(upstreamSessionBlockedBody)))
		c, rec := newOpenAIUpstreamErrorTestContext(t)
		_, err := svc.handleErrorResponse(context.Background(), newOpenAIUpstreamErrorResponse(status, upstreamSessionBlockedBody), c, account, nil)
		require.Error(t, err)
		var failover *UpstreamFailoverError
		require.False(t, errors.As(err, &failover))
		require.Equal(t, status, rec.Code)
		require.Equal(t, "session_blocked_by_cyber_policy", gjson.Get(rec.Body.String(), "error.code").String())
		require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), "开启新会话")
		require.True(t, IsResponseCommitted(c))
		require.Nil(t, GetOpsCyberPolicy(c), "an already blocked session is not a new local cyber violation")

		compatCtx, _ := newOpenAIUpstreamErrorTestContext(t)
		called := false
		_, err = svc.handleCompatErrorResponse(newOpenAIUpstreamErrorResponse(status, upstreamSessionBlockedBody), compatCtx, account,
			func(_ *gin.Context, code int, errorType, message string) {
				called = true
				require.Equal(t, status, code)
				require.Equal(t, "permission_error", errorType)
				require.Contains(t, message, "开启新会话")
			})
		require.Error(t, err)
		require.False(t, errors.As(err, &failover))
		require.True(t, called)
		require.True(t, IsResponseCommitted(compatCtx))
		require.Nil(t, GetOpsCyberPolicy(compatCtx))
	}
}

func TestOAuthPassthroughSessionBlockedNeverEntersAffinityRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, rec := newOpenAIUpstreamErrorTestContext(t)
	upstream := &httpUpstreamRecorder{resp: newOpenAIUpstreamErrorResponse(http.StatusBadGateway, upstreamSessionBlockedBody)}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID: 551, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"access_token": "fixture-access", "chatgpt_account_id": "fixture-account"},
		Extra:       map[string]any{"openai_passthrough": true},
	}
	require.False(t, shouldFailoverOpenAIPassthroughResponse(account, 502, []byte(upstreamSessionBlockedBody)))
	_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","input":"synthetic","stream":false}`))
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.True(t, IsResponseCommitted(c))
	require.Len(t, upstream.requests, 1)
}

func TestUpstreamSessionBlockedRequiresExactErrorCode(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"cyber_policy","message":"blocked"}}`,
		`{"error":{"code":"account_deactivated","message":"session_blocked_by_cyber_policy"}}`,
		`{"error":{"code":"permission_error","message":"session blocked"},"input":{"error":{"code":"session_blocked_by_cyber_policy"}}}`,
	} {
		require.False(t, isOpenAIUpstreamSessionBlocked([]byte(body)))
	}
}
