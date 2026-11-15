package service

import (
	"errors"
	"net/http"
	"time"
)

var errOpenAIAgentRecoveryUnavailable = errors.New("encrypted agent continuation has no unchanged compatible route")

// OpenAIAgentRecoveryError is a local compatibility refusal, not a failure of
// the entire account pool. Reasons contain no route IDs, credentials or input.
type OpenAIAgentRecoveryError struct {
	Reason    string
	subreason string // fixed internal discriminator, never client data
	RetryAt   time.Time
}

func (e *OpenAIAgentRecoveryError) Error() string { return e.ClientMessage() }
func (e *OpenAIAgentRecoveryError) Unwrap() error { return errOpenAIAgentRecoveryUnavailable }
func (e *OpenAIAgentRecoveryError) ClientCode() string {
	return "encrypted_agent_recovery_" + e.Reason
}
func (e *OpenAIAgentRecoveryError) HTTPStatus() int {
	switch e.Reason {
	case "readable_intent_missing", "readable_history_unsupported":
		return http.StatusUnprocessableEntity
	case "owner_rate_limited":
		return http.StatusTooManyRequests
	case "owner_overloaded":
		return http.StatusServiceUnavailable
	default:
		return http.StatusConflict
	}
}
func (e *OpenAIAgentRecoveryError) ClientType() string {
	switch e.HTTPStatus() {
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable:
		return "server_error"
	default:
		return "invalid_request_error"
	}
}
func (e *OpenAIAgentRecoveryError) ClientMessage() string {
	var reason, action string
	switch e.Reason {
	case "readable_intent_missing":
		return "The current request has no readable task or summary from which a new provider conversation can be built. The opaque contents are unavailable; no generation or tool was replayed."
	case "readable_history_unsupported":
		return "The supplied history has an unsupported readable representation. No provider state was transferred and no generation or tool was replayed."
	case "http_previous_response_unsupported":
		reason = "HTTP previous_response_id continuation is unsupported for this encrypted agent owner."
		action = "For HTTP continuation, send the complete original history without previous_response_id; current permissions and route compatibility still apply. Waiting alone will not make this request supported. Do not replay tool results while an earlier delivery is uncertain."
	case "owner_rate_limited", "owner_overloaded":
		reason = "The proven encrypted agent owner is temporarily rate limited."
		if e.Reason == "owner_overloaded" {
			reason = "The proven encrypted agent owner is temporarily overloaded."
		}
		action = "Wait until " + e.RetryAt.UTC().Format(time.RFC3339) + " (estimated), then resume this same conversation with its complete history. Compatibility records are retained through that window; current permissions and route compatibility will be checked again. Do not resubmit tool results while an earlier delivery is uncertain."
	case "owner_conflict":
		reason = "The supplied continuation cannot satisfy its ownership, delivery or history integrity checks."
		action = "Ask the administrator to inspect the refusal details; do not replay completed tools or bypass continuation checks."
	case "route_changed":
		reason = "The proven encrypted agent route or credentials changed."
		action = "Ask the administrator to verify the original compatible route and current authorized credentials before continuing."
	case "wire_changed":
		reason = "The proven encrypted agent transport changed."
		action = "Ask the administrator to verify the original endpoint, adapter and model before continuing."
	case "delivery_pending":
		reason = "The encrypted agent producer has not confirmed delivery yet."
		action = "Wait for the producer's current request to finish and check its outcome before continuing; do not resend tool results while delivery is uncertain."
	case "store_unavailable":
		reason = "Encrypted agent compatibility records are temporarily unavailable."
		action = "Ask the administrator to check the shared compatibility store; do not clear its records or switch accounts to bypass this refusal."
	default:
		reason = "The proven encrypted agent owner is currently unavailable."
		action = "Ask the administrator to check the original account's access and availability; ordinary account switching cannot establish compatibility."
	}
	return reason + " Preserve this task and context. " + action
}

func openAIAgentRecoveryError(reason string) error {
	return &OpenAIAgentRecoveryError{Reason: reason}
}

// Preserve completed upstream accounting while propagating downstream failure.
// This is never eligible for retry/failover, even if no bytes were delivered.
type openAINonStreamingDeliveryError struct{ err error }

func (e *openAINonStreamingDeliveryError) Error() string { return e.err.Error() }
func (e *openAINonStreamingDeliveryError) Unwrap() error { return e.err }

// InternalSubreason is intentionally absent from client envelopes. Revalidate
// this closed list at persistence boundaries so upstream/client text cannot leak.
func (e *OpenAIAgentRecoveryError) InternalSubreason() string {
	return OpenAIRecoverySubreason(e.subreason)
}
func OpenAIRecoverySubreason(reason string) string {
	switch reason {
	case "mixed_completed_owners", "owner_record_conflicted", "owner_record_pending", "owner_record_failed", "owner_route_changed", "owner_record_lost", "delivery_pending", "tool_missing_id", "tool_incomplete", "tool_duplicate_call", "tool_duplicate_result", "tool_result_missing", "tool_unpaired", "shadow_changed", "shadow_epoch_changed", "shadow_fence_invalid", "shadow_fence_changed", "input_integrity", "body_unreadable", "unsupported_adapter", "route_identity":
		return reason
	default:
		return "unknown"
	}
}
func openAIAgentRecoveryDetailedError(reason, detail string) error {
	return &OpenAIAgentRecoveryError{Reason: reason, subreason: OpenAIRecoverySubreason(detail)}
}

// Only the observed completed-owner route mismatch creates this marker. Public
// owner_conflict alone and post-admission state changes never grant a retry.
type openAIWSMixedCompletedOwnersError struct{ cause error }

func (e *openAIWSMixedCompletedOwnersError) Error() string { return e.cause.Error() }
func (e *openAIWSMixedCompletedOwnersError) Unwrap() error { return e.cause }
func openAIRecoveryRecordConflict(b openAIAgentRecoveryBinding) error {
	detail := "owner_route_changed"
	switch {
	case b.conflicted:
		detail = "owner_record_conflicted"
	case b.failed:
		detail = "owner_record_failed"
	case b.pending != "":
		detail = "owner_record_pending"
	}
	return openAIAgentRecoveryDetailedError("owner_conflict", detail)
}
