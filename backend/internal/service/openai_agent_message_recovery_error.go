package service

import "errors"

var errOpenAIAgentRecoveryUnavailable = errors.New("encrypted agent continuation has no unchanged compatible route")

// OpenAIAgentRecoveryError is a local compatibility refusal, not a failure of
// the entire account pool. Reasons contain no route IDs, credentials or input.
type OpenAIAgentRecoveryError struct {
	Reason string
}

func (e *OpenAIAgentRecoveryError) Error() string { return e.ClientMessage() }
func (e *OpenAIAgentRecoveryError) Unwrap() error { return errOpenAIAgentRecoveryUnavailable }
func (e *OpenAIAgentRecoveryError) ClientCode() string {
	return "encrypted_agent_recovery_" + e.Reason
}
func (e *OpenAIAgentRecoveryError) ClientMessage() string {
	var reason, action string
	switch e.Reason {
	case "owner_conflict":
		reason = "The encrypted agent items have conflicting compatible owners."
		action = "Ask the administrator to verify the original compatible owner; do not switch accounts or replay tool results."
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
