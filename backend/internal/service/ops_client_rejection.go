package service

import "strings"

// These are explicit capability rejections, not generic provider 400/404s.
// Keep the SQL compatibility filter on the same vocabulary as new log writes.
var OpsModelCapabilityRejectionNeedles = []string{
	"model_not_found",
	"model_not_supported",
	"model not found",
	"model does not exist",
	"is not supported by the selected account",
	"does not support the requested model",
	"not supported by any configured account",
	"is not available for this group",
	"is not available for your account in this group",
	"is not supported when using codex with a chatgpt account",
}

var OpsContextLimitRejectionNeedles = []string{
	"request payload is too large",
	"prompt is too long",
	"context length exceeded",
	"context_length_exceeded",
	"maximum context length",
	"maximum prompt length",
}

func IsOpsContextLimitRejection(status, upstreamStatus int, message string) bool {
	// A gateway-normalized 400 may retain the relay's original 5xx status.
	// Require the canonical context code, not generic payload/length wording.
	if status == 400 && strings.Contains(strings.ToLower(message), "context_length_exceeded") {
		return true
	}
	if upstreamStatus > 0 {
		status = upstreamStatus
	}
	if status != 400 && status != 413 && status != 422 {
		return false
	}
	text := strings.ToLower(message)
	for _, needle := range OpsContextLimitRejectionNeedles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func IsOpsModelCapabilityRejection(status, upstreamStatus int, message string) bool {
	if upstreamStatus > 0 {
		status = upstreamStatus
	}
	if status != 400 && status != 404 {
		return false
	}
	text := strings.ToLower(message)
	for _, needle := range OpsModelCapabilityRejectionNeedles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// Mark explicit user rejections without discarding the provider evidence needed
// to diagnose a wrong model mapping. Provider balances/auth/capacity stay errors.
func NormalizeOpsClientRejection(entry *OpsInsertErrorLogInput) {
	if entry == nil || entry.StatusCode < 400 || entry.ErrorPhase == "account_auth" {
		return
	}
	upstreamStatus := 0
	if entry.UpstreamStatusCode != nil {
		upstreamStatus = *entry.UpstreamStatusCode
	}
	message := entry.ErrorMessage
	if entry.UpstreamErrorMessage != nil {
		message += " " + *entry.UpstreamErrorMessage
	}
	modelRejected := IsOpsModelCapabilityRejection(entry.StatusCode, upstreamStatus, message)
	contextRejected := IsOpsContextLimitRejection(entry.StatusCode, upstreamStatus, message)
	if modelRejected || contextRejected {
		entry.IsBusinessLimited = true
		// The upstream may be the layer that emitted the rejection, but the
		// responsibility is still the request: unsupported model selection and
		// oversized context are caused by the caller's input/capability choice.
		entry.ErrorOwner = "client"
		entry.ErrorPhase = "request"
		if modelRejected {
			entry.ErrorType = "model_not_found"
		} else {
			entry.ErrorType = "context_limit"
		}
		return
	}
	localType := entry.ErrorType == "invalid_request_error" || entry.ErrorType == "model_not_found" || entry.ErrorType == "billing_error"
	if entry.StatusCode < 500 && entry.ErrorOwner == "client" && entry.ErrorPhase == "request" &&
		localType && upstreamStatus == 0 &&
		entry.ErrorSource != "upstream_http" && len(entry.UpstreamErrors) == 0 {
		entry.IsBusinessLimited = true
	}
}
