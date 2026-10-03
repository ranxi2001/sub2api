//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGrokForbiddenForwardingCarriesPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse_precommit"}[stream], func(t *testing.T) {
			body := []byte(`{"model":"grok-4.5","input":"synthetic test"}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			account := healthyGrokOAuthGatewayTestAccount(991123, "synthetic-access-token")
			account.Extra = map[string]any{"grok_skip_forbidden_pause": true}
			repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(nil))}}
			svc := &OpenAIGatewayService{httpUpstream: upstream, grokTokenProvider: NewGrokTokenProvider(repo, nil), accountRepo: repo}
			_, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok-4.5", stream, time.Now())
			var failoverErr *UpstreamFailoverError
			require.True(t, errors.As(err, &failoverErr))
			require.Equal(t, GrokUnknownForbiddenReason, failoverErr.Reason)
			require.Zero(t, repo.tempUnschedCalls)
			require.Zero(t, repo.rateLimitedCalls)
			require.False(t, c.Writer.Written(), "a pre-commit refusal must not write semantic output")
		})
	}
}

func TestGrokContentErrorsKeepScopeAcrossProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, code := range []string{"content_policy", "cyber_policy"} {
		for _, compat := range []bool{false, true} {
			t.Run(code+map[bool]string{false: "_responses", true: "_chat"}[compat], func(t *testing.T) {
				body := `{"error":{"code":"` + code + `","message":"request rejected"}}`
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				account := &Account{ID: 991125, Platform: PlatformGrok, Type: AccountTypeOAuth}
				svc := &OpenAIGatewayService{}
				resp := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: io.NopCloser(bytes.NewBufferString(body))}
				var err error
				if compat {
					_, err = svc.handleCompatErrorResponse(resp, c, account, writeChatCompletionsError, "grok-4.5")
				} else {
					_, err = svc.handleErrorResponse(context.Background(), resp, c, account, nil, "grok-4.5")
				}
				require.Error(t, err)
				require.True(t, isGrokRequestScopedFailure(err))
				require.Equal(t, http.StatusForbidden, c.Writer.Status())
			})
		}
	}
}

func TestGrokWSHTTPBridgeContentErrorRetainsScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, sse := range []bool{false, true} {
		t.Run(map[bool]string{false: "http403", true: "sse200"}[sse], func(t *testing.T) {
			body := `{"error":{"code":"content_policy","message":"request rejected"}}`
			status := http.StatusForbidden
			if sse {
				status = http.StatusOK
				body = "data: {\"type\":\"error\",\"error\":{\"code\":\"content_policy\",\"message\":\"request rejected\"}}\n\n"
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewBufferString(body))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 991126, Platform: PlatformGrok, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"grok-4.5","input":"synthetic test"}`)
			writes := 0
			_, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "synthetic-token", payload, len(payload), "grok-4.5", "", "", "", "", 2, func([]byte) error { writes++; return nil })
			require.Error(t, err)
			require.True(t, isGrokRequestScopedFailure(err))
			require.Positive(t, writes, "terminal error remains on the existing connection")
		})
	}
}

func TestGrokGenericChatFailoverCarriesPolicyAndModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	account := &Account{ID: 991124, Platform: PlatformGrok, Type: AccountTypeOAuth, Extra: map[string]any{"grok_skip_forbidden_pause": true}}
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	err := svc.failoverOpenAIUpstreamHTTPError(context.Background(), c, account, &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}}, nil, "upstream forbidden", "grok-4.5")
	require.NotNil(t, err)
	require.Equal(t, GrokUnknownForbiddenReason, err.Reason)
	require.False(t, err.ShouldReportAccountScheduleFailure())
	require.Zero(t, repo.tempUnschedCalls)
}

func TestGrokNativeSSEContentFailureDoesNotPenalizeAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, code := range []string{"content_policy", "new_sensitive"} {
			for _, message := range []string{"request rejected", "please retry"} {
				t.Run(fmt.Sprintf("%v_%s_%s", stream, code, message), func(t *testing.T) {
					requestBody := []byte(fmt.Sprintf(`{"model":"grok-4.5","input":"synthetic test","stream":%v}`, stream))
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(requestBody))
					account := healthyGrokOAuthGatewayTestAccount(991127, "synthetic-token")
					repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}}
					sse := fmt.Sprintf("event: error\ndata: {\"type\":\"error\",\"error\":{\"code\":\"%s\",\"message\":\"%s\"}}\n\n", code, message)
					upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(bytes.NewBufferString(sse))}}
					svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, grokTokenProvider: NewGrokTokenProvider(repo, nil), accountRepo: repo}
					_, err := svc.forwardGrokResponses(context.Background(), c, account, requestBody, "grok-4.5", stream, time.Now())
					require.Error(t, err)
					require.True(t, isGrokRequestScopedFailure(err))
					var failoverErr *UpstreamFailoverError
					require.False(t, errors.As(err, &failoverErr), "content refusal is terminal regardless of retry wording")
					require.Zero(t, repo.tempUnschedCalls)
					require.Zero(t, repo.rateLimitedCalls)
				})
			}
		}
	}
}
