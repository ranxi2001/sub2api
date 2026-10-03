package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGrokForbiddenPolicyMetadata(t *testing.T) {
	account := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Extra: map[string]any{"grok_skip_forbidden_pause": true}}
	err := (&UpstreamFailoverError{StatusCode: http.StatusForbidden, ResponseBody: []byte(`{"code":"permission-denied","error":"Access to the chat endpoint is denied"}`)}).WithGrokForbiddenPolicy(account)
	require.Equal(t, GrokUnknownForbiddenReason, err.Reason)
	require.Equal(t, GatewayFailureScopeRequest, err.Scope)
	require.False(t, err.ShouldReportAccountScheduleFailure())
	require.True(t, err.ShouldRetryNextAccount())

	for _, body := range []string{
		`{"code":"content_policy","error":"Content violates usage guidelines"}`,
		`{"code":"account_suspended","error":"Account suspended"}`,
		`{"code":"entitlement_required","error":"Entitlement required"}`,
		`{"error":{"code":"deactivated_account","message":"denied"}}`,
		`{"error":{"code":"token_revoked","message":"denied"}}`,
		`{"error":{"code":"invalid_token","message":"denied"}}`,
		`{"code":"subscription:free-usage-exhausted","error":"Used all free usage for model grok-4.5"}`,
	} {
		t.Run(body, func(t *testing.T) {
			e := (&UpstreamFailoverError{StatusCode: http.StatusForbidden, ResponseBody: []byte(body)}).WithGrokForbiddenPolicy(account)
			require.Empty(t, e.Reason)
		})
	}
	account.Extra["grok_skip_forbidden_pause"] = false
	legacy := (&UpstreamFailoverError{StatusCode: http.StatusForbidden}).WithGrokForbiddenPolicy(account)
	require.Empty(t, legacy.Reason)
	require.True(t, legacy.ShouldReportAccountScheduleFailure())
	credential := (&UpstreamFailoverError{StatusCode: http.StatusForbidden, Stage: GatewayFailureStageAccountAuth}).WithGrokForbiddenPolicy(account)
	require.True(t, credential.IsCredentialFailure())
}

func TestGrokRequestScopedErrorsDoNotReportAccountHealth(t *testing.T) {
	account := &Account{ID: 991122, Platform: PlatformGrok, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{}
	for _, err := range []error{
		&grokContentPolicyError{message: "Content rejected"},
		fmt.Errorf("wrapped: %w", &grokContentPolicyError{message: "Content rejected"}),
		&UpstreamFailoverError{Reason: GrokUnknownForbiddenReason},
	} {
		require.True(t, isGrokRequestScopedFailure(err))
		require.False(t, svc.ReportOpenAIAccountScheduleResult(account, "grok-4.5", false, nil, err))
		require.False(t, svc.ObserveOpenAIAccountHealthFailure(context.Background(), account, err))
	}
	require.False(t, isGrokRequestScopedFailure(fmt.Errorf("ordinary upstream failure")))
}

func TestGrokRealtimeContentHandshakeIsTerminal(t *testing.T) {
	account := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Extra: map[string]any{"grok_skip_forbidden_pause": true}}
	err := GrokRealtimeFailoverError(account, http.StatusForbidden, nil, []byte(`{"error":{"code":"content_policy","message":"please retry"}}`))
	require.False(t, err.ShouldRetryNextAccount())
	require.Equal(t, GatewayFailureScopeRequest, err.Scope)
	require.Empty(t, err.Reason)
}
