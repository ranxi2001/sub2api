package service

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Inspect deterministic context rejections before retry/cooldown policy. Keep
// the original response available when it is not a recognized request error.
func (s *GatewayService) interceptAnthropicContextRejection(c *gin.Context, account *Account, resp *http.Response) bool {
	if account == nil || account.Platform != PlatformAnthropic || resp == nil || resp.Body == nil ||
		(resp.StatusCode != 400 && resp.StatusCode != 422 && resp.StatusCode != 502) {
		return false
	}
	original := resp.Body
	const limit = 64 * 1024
	body, err := io.ReadAll(io.LimitReader(original, limit+1))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(body), original), original}
	if err != nil || len(body) > limit || !IsUpstreamContextRejection(resp.StatusCode, body) {
		return false
	}
	_ = resp.Body.Close()
	writeGatewayContextRejection(c, resp.StatusCode)
	return true
}

func writeGatewayContextRejection(c *gin.Context, upstreamStatus int) {
	const code = "context_length_exceeded"
	const message = "Input exceeds the model context window. Shorten or compact the conversation and try again."
	setOpsUpstreamError(c, upstreamStatus, code+": "+message, "")
	MarkOpsRequestScopedError(c, "invalid_request_error")
	MarkResponseCommitted(c)
	payload := gin.H{"type": "error", "error": gin.H{"type": "invalid_request_error", "code": code, "message": message}}
	if gatewaySSEStarted(c) {
		MarkOpsStreamErrorValue(c, OpsStreamError{ErrType: "invalid_request_error", Code: code,
			Message: message, IntendedStatus: http.StatusBadRequest, RequestScoped: true})
		c.SSEvent("error", payload)
		c.Writer.Flush()
	} else {
		c.JSON(http.StatusBadRequest, payload)
	}
}

var errGatewayContextRejection = errors.New("request context limit exceeded")
