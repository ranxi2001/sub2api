package service

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// IsUpstreamContextRejection inspects only error fields, never echoed input.
// Auth, permission and rate-limit responses retain their provider attribution.
func IsUpstreamContextRejection(status int, body []byte) bool {
	if status != 400 && status != 413 && status != 422 && status != 502 {
		return false
	}
	if !gjson.ValidBytes(body) {
		return false
	}
	if explicitContextWindowError(body) {
		return true
	}
	var fields []string
	for _, path := range []string{"error.code", "error.type", "error.message", "response.error.code", "response.error.type", "response.error.message"} {
		fields = append(fields, gjson.GetBytes(body, path).String())
	}
	message := strings.Join(fields, " ")
	return IsOpsContextLimitRejection(status, 0, message) ||
		(status != http.StatusBadGateway && isOpenAIContextWindowError(message, nil))
}

// Error objects can arrive in an HTTP rejection, a response.failed envelope,
// or a top-level Responses error event. Ignore echoed input and output text.
func explicitContextWindowError(body []byte) bool {
	err := gjson.GetBytes(body, "response.error")
	if !err.IsObject() {
		err = gjson.GetBytes(body, "error")
	}
	if !err.IsObject() && gjson.GetBytes(body, "type").String() == "error" {
		err = gjson.ParseBytes(body)
	}
	if !err.IsObject() {
		return false
	}
	for _, key := range []string{"code", "type"} {
		switch err.Get(key).String() {
		case "context_length_exceeded", "context_too_large":
			return true
		}
	}
	// Prefer an explicit provider classification over message-only evidence.
	switch err.Get("code").String() {
	case "", "server_error", "upstream_error", "invalid_request_error":
	default:
		return false
	}
	switch err.Get("type").String() {
	case "", "error", "server_error", "upstream_error", "invalid_request_error":
	default:
		return false
	}
	return strings.EqualFold(strings.TrimSpace(err.Get("message").String()), "Your input exceeds the context window of this model. Please adjust your input and try again.")
}
