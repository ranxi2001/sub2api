package handler

import (
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *OpenAIGatewayHandler) handleOpenAIAgentRecoveryError(c *gin.Context, err error, streamStarted bool) bool {
	var recovery *service.OpenAIAgentRecoveryError
	if !errors.As(err, &recovery) {
		return false
	}
	h.handleStreamingAwareErrorWithCode(c, http.StatusConflict, "invalid_request_error", recovery.ClientCode(), recovery.ClientMessage(), streamStarted || c.Writer.Written(), false)
	return true
}
