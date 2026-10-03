package service

import (
	"errors"
	"net/http"
)

const GrokUnknownForbiddenReason GatewayFailureReason = "grok_unknown_forbidden"

// Realtime handshake refusals use the same body-aware terminal verdict as HTTP
// inference; a content rejection must not visit every account in the pool.
func GrokRealtimeFailoverError(account *Account, status int, headers http.Header, body []byte) *UpstreamFailoverError {
	err := (&UpstreamFailoverError{StatusCode: status, ResponseHeaders: headers, ResponseBody: body}).WithGrokForbiddenPolicy(account)
	if isGrokContentPolicyRejection(status, body) {
		err.NextAccountAction = NextAccountStop
		err.Scope = GatewayFailureScopeRequest
	}
	return err
}

// WithGrokForbiddenPolicy keeps ambiguous inference refusals out of account
// health. The handler owns the request-local alternate-account budget.
func (e *UpstreamFailoverError) WithGrokForbiddenPolicy(account *Account) *UpstreamFailoverError {
	if e == nil || e.IsCredentialFailure() || account == nil || !account.IsGrok() || !account.SkipGrokForbiddenPause() || !isGrokUnknownForbidden(e.StatusCode, e.ResponseBody) {
		return e
	}
	// An explicit administrator cooldown rule remains an account decision.
	if len(matchTempUnschedulableRules(account, e.StatusCode, e.ResponseBody)) > 0 {
		return e
	}
	e.Stage = GatewayFailureStageInference
	e.Scope = GatewayFailureScopeRequest
	e.Reason = GrokUnknownForbiddenReason
	e.RetryableOnSameAccount = false
	e.RequestScopedTransient = true
	return e
}

type grokContentPolicyError struct {
	message string
}

func grokRequestScopedResponseError(account *Account, status int, body []byte, message string) error {
	if account == nil || !account.IsGrok() {
		return nil
	}
	if isGrokContentPolicyRejection(status, body) {
		return &grokContentPolicyError{message: message}
	}
	err := (&UpstreamFailoverError{StatusCode: status, ResponseBody: body}).WithGrokForbiddenPolicy(account)
	if err.Reason == GrokUnknownForbiddenReason {
		// The caller has already communicated this error. Keep its health
		// classification, but never replay after semantic output.
		err.NextAccountAction = NextAccountStop
		return err
	}
	return nil
}

func (e *grokContentPolicyError) Error() string {
	return "grok content policy rejection: " + e.message
}

func isGrokRequestScopedFailure(err error) bool {
	var contentErr *grokContentPolicyError
	if errors.As(err, &contentErr) {
		return true
	}
	var failoverErr *UpstreamFailoverError
	return errors.As(err, &failoverErr) && failoverErr.Reason == GrokUnknownForbiddenReason
}
