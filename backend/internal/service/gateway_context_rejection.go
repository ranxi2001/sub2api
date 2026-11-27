package service

import (
	"bytes"
	"errors"
	"fmt"
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
	_ = writeContextRejection(c, upstreamStatus, false, "", nil)
}

const OpsContextRejectionStatusKey = "ops_context_rejection_status"

func writeContextRejection(c *gin.Context, upstreamStatus int, chat bool, platform string, terminal []byte) error {
	const code = "context_length_exceeded"
	const diagnostic = "Input exceeds the model context window. Shorten or compact the conversation and try again."
	status, errType, message := http.StatusBadRequest, "invalid_request_error", diagnostic
	outcome := errGatewayContextRejection
	if terminal != nil {
		if customStatus, customType, customMessage, matched := applyOpenAIStreamFailedErrorPassthroughRule(c, platform, terminal, extractOpenAISSEErrorMessage(terminal)); matched {
			status, errType = customStatus, customType
			if customMessage != "" {
				message = customMessage
			}
			outcome = fmt.Errorf("passthrough: %w", outcome)
		}
	}
	c.Set(OpsContextRejectionStatusKey, upstreamStatus)
	setOpsUpstreamError(c, upstreamStatus, code+": "+diagnostic, "")
	MarkOpsRequestScopedError(c, "invalid_request_error")
	MarkResponseCommitted(c)
	payload := gin.H{"type": "error", "error": gin.H{"type": errType, "code": code, "message": message}}
	if gatewaySSEStarted(c) {
		MarkOpsStreamErrorValue(c, OpsStreamError{ErrType: "invalid_request_error", Code: code,
			Message: message, IntendedStatus: http.StatusBadRequest, UpstreamStatus: upstreamStatus, RequestScoped: true})
		if chat {
			c.SSEvent("", gin.H{"error": payload["error"]})
			_, _ = io.WriteString(c.Writer, "data: [DONE]\n\n")
		} else {
			c.SSEvent("error", payload)
		}
		c.Writer.Flush()
	} else {
		c.JSON(status, payload)
	}
	return outcome
}

var errGatewayContextRejection = errors.New("request context limit exceeded")
