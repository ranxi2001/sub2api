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
	var fields []string
	for _, path := range []string{"error.code", "error.type", "error.message", "response.error.code", "response.error.type", "response.error.message"} {
		fields = append(fields, gjson.GetBytes(body, path).String())
	}
	message := strings.Join(fields, " ")
	return IsOpsContextLimitRejection(status, 0, message) ||
		(status != http.StatusBadGateway && isOpenAIContextWindowError(message, nil))
}
