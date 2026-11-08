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
	var reason string
	switch e.Reason {
	case "owner_conflict":
		reason = "The encrypted agent items have conflicting compatible owners."
	case "route_changed":
		reason = "The proven encrypted agent route or credentials changed."
	case "wire_changed":
		reason = "The proven encrypted agent HTTP transport changed."
	case "store_unavailable":
		reason = "Encrypted agent compatibility records are temporarily unavailable."
	default:
		reason = "The proven encrypted agent owner is currently unavailable."
	}
	return reason + " Preserve this task and context; restore the original compatible route before continuing."
}

func openAIAgentRecoveryError(reason string) error {
	return &OpenAIAgentRecoveryError{Reason: reason}
}
