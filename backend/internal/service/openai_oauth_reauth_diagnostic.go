package service

import (
	"fmt"
	"strconv"
	"strings"
)

// ReauthDiagnostic is derived from a bounded, validated worker error.
// It contains no provider text and does not replace the task's ownership or state.
type ReauthDiagnostic struct {
	Identity   *ReauthIdentityChecks `json:"identity,omitempty"`
	Engine     string                `json:"engine,omitempty"`
	Stage      string                `json:"stage"`
	Reason     string                `json:"reason"`
	ElapsedMS  int                   `json:"elapsed_ms"`
	ExitCode   *int                  `json:"exit_code,omitempty"`
	HTTPStatus int                   `json:"http_status,omitempty"`
}

func localProtocolError(raw string) (string, *ReauthDiagnostic, bool) {
	msg := strings.TrimPrefix(strings.TrimSpace(raw), "WorkerError: ")
	if !strings.HasPrefix(msg, "local_protocol_failed") {
		return "", nil, false
	}
	invalid := "local protocol failed; invalid diagnostic omitted"
	if len(msg) > 256 {
		return invalid, nil, true
	}
	parts := strings.Split(msg, " ")
	if len(parts) != 5 || parts[0] != "local_protocol_failed" {
		return invalid, nil, true
	}
	stage, okStage := strings.CutPrefix(parts[1], "stage=")
	reason, okReason := strings.CutPrefix(parts[2], "reason=")
	elapsed, okElapsed := strings.CutPrefix(parts[3], "elapsed_ms=")
	exit, okExit := strings.CutPrefix(parts[4], "exit=")
	elapsedMS, elapsedErr := strconv.Atoi(elapsed)
	exitCode, exitErr := strconv.Atoi(exit)
	if !okStage || !okReason || !okElapsed || !okExit || elapsedErr != nil || exitErr != nil ||
		elapsedMS < 0 || elapsedMS > 3600000 || exitCode < -255 || exitCode > 255 ||
		!validLocalProtocolStage(stage) || !validLocalProtocolReason(reason) {
		return invalid, nil, true
	}
	diagnostic := &ReauthDiagnostic{Stage: stage, Reason: reason, ElapsedMS: elapsedMS, ExitCode: &exitCode}
	return fmt.Sprintf("local_protocol_failed stage=%s reason=%s elapsed_ms=%d exit=%d", stage, reason, elapsedMS, exitCode), diagnostic, true
}

func sessionStudioError(raw string) (string, *ReauthDiagnostic, bool) {
	msg := strings.TrimPrefix(strings.TrimSpace(raw), "WorkerError: ")
	if !strings.HasPrefix(msg, "session_studio_failed") {
		return "", nil, false
	}
	invalid := "Session Studio failed; invalid diagnostic omitted"
	parts := strings.Split(msg, " ")
	if len(msg) > 256 || len(parts) != 5 || parts[0] != "session_studio_failed" {
		return invalid, nil, true
	}
	stage, okStage := strings.CutPrefix(parts[1], "stage=")
	reason, okReason := strings.CutPrefix(parts[2], "reason=")
	elapsed, okElapsed := strings.CutPrefix(parts[3], "elapsed_ms=")
	status, okStatus := strings.CutPrefix(parts[4], "http_status=")
	elapsedMS, elapsedErr := strconv.Atoi(elapsed)
	httpStatus, statusErr := strconv.Atoi(status)
	validPhase := false
	switch stage {
	case "http_request":
		validPhase = reason == "http_rejected" || reason == "transport_error"
	case "event_stream":
		switch reason {
		case "duplicate_field", "invalid_json", "unsupported_event", "timeout", "missing_result", "response_too_large", "transport_error":
			validPhase = true
		}
	case "terminal_result":
		validPhase = reason == "invalid_result" || reason == "provider_failed" || credentialOperationCode(reason) == reason && reason != "unknown"
	case "credentials_validation":
		validPhase = reason == "credentials_incomplete"
	case "credentials_submit":
		validPhase = reason == "submission_failed" || reason == "credentials_not_accepted"
	}
	validStatus := httpStatus == 0
	if reason == "http_rejected" {
		validStatus = httpStatus >= 100 && httpStatus <= 599
	}
	if !okStage || !okReason || !okElapsed || !okStatus || elapsedErr != nil || statusErr != nil ||
		elapsedMS < 0 || elapsedMS > 3600000 || !validPhase || !validStatus {
		return invalid, nil, true
	}
	diagnostic := &ReauthDiagnostic{Engine: OpenAIOAuthReauthEngineSessionStudio, Stage: stage, Reason: reason, ElapsedMS: elapsedMS, HTTPStatus: httpStatus}
	return fmt.Sprintf("session_studio_failed stage=%s reason=%s elapsed_ms=%d http_status=%d", stage, reason, elapsedMS, httpStatus), diagnostic, true
}

func validLocalProtocolStage(stage string) bool {
	switch stage {
	case "starting", "web_login", "password_accepted", "mfa_challenge", "mfa_accepted", "web_redirect",
		"oauth_authorize", "session_select", "workspace_select", "callback_validation", "token_exchange", "oauth_output", "completed":
		return true
	}
	return false
}

func validLocalProtocolReason(reason string) bool {
	if reason != "unknown" && credentialOperationCode(reason) == reason {
		return true
	}
	switch reason {
	case "protocol_error", "password_rejected", "totp_rejected", "interaction_required", "security_check",
		"session_unavailable", "workspace_mismatch", "callback_missing", "callback_invalid", "callback_state_mismatch",
		"pkce_invalid", "token_exchange_failed", "credentials_incomplete", "transport_error", "runtime_error",
		"process_failed", "timeout", "diagnostic_invalid", "output_invalid":
		return true
	}
	return false
}
