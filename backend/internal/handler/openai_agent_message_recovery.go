package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

func (h *OpenAIGatewayHandler) handleOpenAIAgentRecoveryError(c *gin.Context, err error, streamStarted bool) bool {
	var recovery *service.OpenAIAgentRecoveryError
	if !errors.As(err, &recovery) {
		return false
	}
	committed := streamStarted || c.Writer.Written()
	if !committed && !recovery.RetryAt.IsZero() {
		seconds := max(int64(1), int64((time.Until(recovery.RetryAt)+time.Second-1)/time.Second))
		c.Header("Retry-After", strconv.FormatInt(seconds, 10))
	}
	h.handleStreamingAwareErrorWithCode(c, recovery.HTTPStatus(), recovery.ClientType(), recovery.ClientCode(), recovery.ClientMessage(), committed, false)
	return true
}

// The socket has already upgraded when initial scheduling runs. Send the full
// recovery guidance as an error event; close reasons alone are length limited.
func (h *OpenAIGatewayHandler) handleOpenAIWSAgentRecoveryError(ctx context.Context, conn *coderws.Conn, err error) bool {
	var recovery *service.OpenAIAgentRecoveryError
	if !errors.As(err, &recovery) {
		return false
	}
	payload, _ := json.Marshal(map[string]any{
		"type": "error", "status": recovery.HTTPStatus(),
		"error": map[string]string{"type": recovery.ClientType(), "code": recovery.ClientCode(), "message": recovery.ClientMessage()},
	})
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = conn.Write(writeCtx, coderws.MessageText, payload)
	status, reason := coderws.StatusPolicyViolation, recovery.ClientCode()
	if !recovery.RetryAt.IsZero() {
		status = coderws.StatusTryAgainLater
		reason += "; retry after " + recovery.RetryAt.UTC().Format(time.RFC3339)
	}
	closeOpenAIClientWS(conn, status, reason)
	return true
}
