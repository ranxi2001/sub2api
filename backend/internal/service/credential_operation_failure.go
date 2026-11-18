package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// CredentialOperationFailure is a projection of one persisted operation, never
// of the account's current status. Every string is an allowlisted enum.
type CredentialOperationFailure struct {
	Category   string     `json:"category"`
	Code       string     `json:"code"`
	Stage      string     `json:"stage"`
	Action     string     `json:"action"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"`
}

// CredentialOperationError carries a safe code from the actual token response.
// No response body or exception text is retained in the error chain.
type CredentialOperationError struct {
	Code  string
	cause error
}

func (e *CredentialOperationError) Error() string { return e.cause.Error() }
func (e *CredentialOperationError) Unwrap() error { return e.cause }

func NewCredentialOperationRefreshError(status int, body []byte) error {
	rawCode, code := credentialOperationEnvelopeSignal(body)
	if code == "unknown" {
		switch {
		case status == 429:
			code = "rate_limited"
		case status >= 500:
			code = "service_error"
		}
	}
	// Retain the invalid_grant marker used by the existing refresh race recovery
	// and retry policy. Classification does not change that policy's semantics.
	marker := code
	if safe := credentialOperationRefreshMarker(rawCode); safe != "" {
		marker = safe
	}
	return &CredentialOperationError{Code: code, cause: infraerrors.New(502, "OPENAI_OAUTH_TOKEN_REFRESH_FAILED", fmt.Sprintf("token refresh failed: status %d, code %s", status, marker))}
}

func credentialOperationRefreshMarker(code string) string {
	if credentialOperationCode(code) != "unknown" {
		return code
	}
	switch code {
	case "invalid_client", "unauthorized_client", "invalid_scope":
		return code
	}
	return ""
}

type CredentialOperationRefreshRecorder interface {
	RecordRefreshFailure(context.Context, int64, string, time.Time) error
}

func refreshOperationFailure(err error, at *time.Time) *CredentialOperationFailure {
	var operationError *CredentialOperationError
	var networkError interface{ Timeout() bool }
	code := "unknown"
	if errors.As(err, &operationError) {
		code = operationError.Code
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "timeout"
	} else if errors.As(err, &networkError) {
		code = "transport_error"
		if networkError.Timeout() {
			code = "timeout"
		}
	} else if infraerrors.Reason(err) == "OPENAI_OAUTH_REQUEST_FAILED" || infraerrors.Reason(err) == "OPENAI_OAUTH_PROXY_REQUIRED" {
		code = "transport_error"
	} else if err != nil {
		code = credentialOperationText(err.Error())
	}
	return credentialOperationFailure(code, "refresh_token", at)
}

// Keep provider aliases at the input boundary; only canonical codes are stored.
func credentialOperationCode(code string) string {
	switch code {
	case "phone_verification_required", "phone_number_required":
		return "phone_verification_required"
	case "account_deactivated", "account_banned", "account_disabled":
		return "account_deactivated"
	case "account_deleted":
		return "account_deleted"
	case "account_retired":
		return "account_retired"
	case "auth_failed", "invalid_token", "token_invalid", "requires_relogin", "invalid_grant", "revoked",
		"token_invalidated", "token_revoked", "refresh_token_reused", "refresh_token_invalidated",
		"invalid_refresh_token", "token_expired", "app_session_terminated", "session_unavailable":
		return "requires_relogin"
	case "password_rejected", "invalid_password", "incorrect_password":
		return "password_rejected"
	case "email_otp_required":
		return "email_otp_required"
	case "totp_rejected", "mfa_required", "otp_required", "email_verification_required", "interaction_required":
		return "mfa_required"
	case "security_check", "security_check_required", "challenge_required", "session_blocked_by_cyber_policy", "cyber_policy":
		return "security_check"
	case "access_denied", "authorization_denied", "permission_denied":
		return "authorization_denied"
	case "rate_limit_exceeded", "rate_limit_error", "rate_limited", "too_many_requests":
		return "rate_limited"
	case "timeout", "request_timeout", "worker_timeout", "idle_timeout":
		return "timeout"
	case "transport_error", "connection_error", "proxy_unavailable":
		return "transport_error"
	case "server_error", "service_unavailable", "service_error":
		return "service_error"
	}
	return "unknown"
}

// Exact full-message compatibility only. Substrings in prose, negations,
// quoted user input, URLs and arbitrary provider messages are never evidence.
func credentialOperationText(text string) string {
	switch text {
	case "You must provide a phone number to continue.", "Phone verification required.":
		return "phone_verification_required"
	case "Your account has been deactivated.":
		return "account_deactivated"
	case "Your account has been deleted.":
		return "account_deleted"
	case "Your account has been deleted or deactivated.", "This account has been deleted or deactivated.":
		return "account_retired"
	case "invalid token", "unauthorized", "account has no access token", "credential was rejected", "upstream returned 401":
		return "requires_relogin"
	case "worker timeout", "password/TOTP protocol timed out":
		return "timeout"
	case accountTokenGuardV2PolicyBlocked:
		return "authorization_denied"
	}
	return credentialOperationCode(text)
}

// Only the top-level error envelope is authoritative. Do not search arbitrary
// nested input/message/debug fields. A nonempty unknown code blocks text fallback.
func credentialOperationEnvelope(body []byte) string {
	_, code := credentialOperationEnvelopeSignal(body)
	return code
}

// Select the authoritative structured signal once for both display and refresh
// policy. An explicit code, even unknown or malformed, blocks type/message.
func credentialOperationEnvelopeSignal(body []byte) (string, string) {
	fields, ok := credentialOperationObject(body)
	if !ok {
		return "", "unknown"
	}
	var oauthCode string
	if json.Unmarshal(fields["error"], &oauthCode) == nil {
		return oauthCode, credentialOperationCode(oauthCode)
	}
	fields, ok = credentialOperationObject(fields["error"])
	if !ok {
		return "", "unknown"
	}
	value := func(key string) string {
		var text string
		_ = json.Unmarshal(fields[key], &text)
		return text
	}
	if _, exists := fields["code"]; exists {
		code := value("code")
		return code, credentialOperationCode(code)
	}
	if kind := value("type"); credentialOperationRefreshMarker(kind) != "" {
		return kind, credentialOperationCode(kind)
	}
	return "", credentialOperationText(value("message"))
}

func credentialOperationObject(body []byte) (map[string]json.RawMessage, bool) {
	if len(body) > 32768 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return fields, true
}

func credentialOperationFailure(code, stage string, at *time.Time) *CredentialOperationFailure {
	code = credentialOperationCode(code)
	category, action := code, "review_diagnostics"
	switch code {
	case "phone_verification_required":
		action = "complete_phone_verification"
	case "account_deactivated", "account_deleted", "account_retired":
		action = "contact_provider"
	case "requires_relogin":
		action = "reauthorize"
	case "password_rejected":
		action = "update_password"
	case "mfa_required":
		action = "complete_verification"
	case "email_otp_required":
		action = "complete_email_verification"
	case "security_check", "authorization_denied":
		action = "review_authorization"
	case "rate_limited", "timeout", "transport_error", "service_error":
		action = "check_service_later"
	}
	if !validLocalProtocolStage(stage) && stage != "inspection" && stage != "refresh_token" && stage != "http_request" && stage != "event_stream" && stage != "terminal_result" && stage != "credentials_validation" && stage != "credentials_submit" {
		stage = "unknown"
	}
	return &CredentialOperationFailure{Category: category, Code: code, Stage: stage, Action: action, OccurredAt: at}
}

// SafeCredentialOperationFailure also protects repository observation writes.
func SafeCredentialOperationFailure(code, stage string, at *time.Time) *CredentialOperationFailure {
	return credentialOperationFailure(code, stage, at)
}

func storedRefreshFailure(record AccountTokenGuardV2Record) *CredentialOperationFailure {
	if record.LastRefreshFailureAt == nil && record.RefreshFailureCode == "" {
		return nil
	}
	return credentialOperationFailure(record.RefreshFailureCode, "refresh_token", record.LastRefreshFailureAt)
}

func credentialOperationDetail(code string) string {
	return "credential_operation_failed stage=inspection code=" + credentialOperationCode(code)
}

func probeOperationFailure(state, detail string, at *time.Time) *CredentialOperationFailure {
	if state == AccountTokenGuardV2ProbeOK || state == AccountTokenGuardV2ProbePending || state == "" {
		return nil
	}
	var code string
	if value, ok := strings.CutPrefix(detail, "credential_operation_failed stage=inspection code="); ok {
		value, _, _ = strings.Cut(value, ";")
		code = credentialOperationCode(value)
	} else {
		code = credentialOperationText(detail)
	}
	return credentialOperationFailure(code, "inspection", at)
}

func safeProbeOperationDetail(state, detail string) string {
	if state == AccountTokenGuardV2ProbeOK {
		return "credential accepted"
	}
	if failure := probeOperationFailure(state, detail, nil); failure != nil {
		return credentialOperationDetail(failure.Code)
	}
	return ""
}

func reauthOperationFailure(record *OpenAIOAuthReauthTaskRecord, diagnostic *ReauthDiagnostic) *CredentialOperationFailure {
	if record.Status != OpenAIOAuthReauthStatusFailed && record.Error == "" {
		return nil
	}
	stage, code := "unknown", credentialOperationText(record.Error)
	if diagnostic != nil {
		stage, code = diagnostic.Stage, credentialOperationCode(diagnostic.Reason)
		if diagnostic.Reason == "http_rejected" {
			switch {
			case diagnostic.HTTPStatus == 429:
				code = "rate_limited"
			case diagnostic.HTTPStatus >= 500:
				code = "service_error"
			case diagnostic.HTTPStatus == 401 || diagnostic.HTTPStatus == 403:
				code = "authorization_denied"
			}
		}
	}
	return credentialOperationFailure(code, stage, record.FinishedAt)
}
