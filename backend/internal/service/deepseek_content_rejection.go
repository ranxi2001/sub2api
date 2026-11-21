package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const DeepSeekInputContentRejectionMessage = "Input text data may contain inappropriate content."

// Only this explicit input refusal is request-scoped. Generic DeepSeek 400s,
// credential denials and provider failures must retain their existing handling.
func IsDeepSeekInputContentRejection(platform string, status int, messages ...string) bool {
	if platform != PlatformDeepseek || (status != http.StatusBadRequest && status != http.StatusForbidden) {
		return false
	}
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message), DeepSeekInputContentRejectionMessage) {
			return true
		}
	}
	return false
}

func isDeepSeekInputContentResponse(account *Account, status int, body []byte) bool {
	return account != nil && IsDeepSeekInputContentRejection(account.Platform, status, extractUpstreamErrorMessage(body))
}

func writeDeepSeekInputContentRejection(c *gin.Context, account *Account, resp *http.Response, body []byte, writeError compatErrorWriter) bool {
	if !isDeepSeekInputContentResponse(account, resp.StatusCode, body) {
		return false
	}
	// Preserve the actual upstream status, while returning a terminal 403 in
	// the client's protocol. This is not a local cyber-policy violation.
	setOpsUpstreamError(c, resp.StatusCode, DeepSeekInputContentRejectionMessage, "")
	MarkResponseCommitted(c)
	writeError(c, http.StatusForbidden, "permission_error", DeepSeekInputContentRejectionMessage)
	return true
}
