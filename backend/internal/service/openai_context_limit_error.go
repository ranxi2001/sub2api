package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// writeOpenAIContextLimitError handles a deterministic rejection before generic
// 5xx handling can cool down a credential or hide the actionable error code.
// It is called only after compact fallback and policy handling have had a chance
// to consume the rejection. Upstream status remains evidence, not client status.
func (s *OpenAIGatewayService) writeOpenAIContextLimitError(resp *http.Response, c *gin.Context, account *Account, payload []byte, passthrough bool) bool {
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 400 && resp.StatusCode != 413 && resp.StatusCode != 422 {
		return false
	}
	message := extractOpenAISSEErrorMessage(payload)
	if !isOpenAIContextWindowError(message, payload) {
		return false
	}
	if message == "" {
		message = "Input exceeds the model context window. Compact the conversation and try again."
	}
	const code = "context_length_exceeded"
	// Keep the semantic code even if the provider supplies it only in error.code;
	// Ops can then classify the normalized 400 while retaining the raw 502/200.
	opsMessage := code + ": " + message
	setOpsUpstreamError(c, resp.StatusCode, opsMessage, "")
	if account != nil {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
			ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account),
			UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"),
			Passthrough: passthrough, Kind: "http_error", Message: opsMessage,
		})
	}
	if openAICompactClientWantsStream(c) && StopOpenAICompactSSEKeepaliveCommitted(c) {
		// HTTP headers have already been sent. Finish the existing SSE stream
		// with its semantic error instead of appending a JSON 400 to it.
		writeOpenAICompactSSEFailureMessage(c, http.StatusBadRequest, code, message)
		return true
	}
	status, errorType := http.StatusBadRequest, "invalid_request_error"
	if !passthrough {
		ruleStatus := resp.StatusCode
		if ruleStatus < 400 {
			ruleStatus = http.StatusBadRequest
		}
		status, errorType, message, _ = applyErrorPassthroughRule(c, PlatformOpenAI, ruleStatus, payload, status, errorType, message)
	}
	writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	c.Header("Content-Type", "application/json; charset=utf-8")
	MarkResponseCommitted(c)
	errorPayload := gin.H{"type": errorType, "code": code, "message": message}
	for _, path := range []string{"response.error.param", "error.param"} {
		if param := strings.TrimSpace(gjson.GetBytes(payload, path).String()); param != "" {
			errorPayload["param"] = param
			break
		}
	}
	c.JSON(status, gin.H{"error": errorPayload})
	return true
}
