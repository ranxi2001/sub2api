package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (h *OpenAIGatewayHandler) handleOpenAIAgentRecoveryError(c *gin.Context, err error, streamStarted bool) bool {
	var recovery *service.OpenAIAgentRecoveryError
	if !errors.As(err, &recovery) {
		return false
	}
	protocol := "http"
	if streamStarted || c.GetBool(opsStreamKey) {
		protocol = "sse"
	}
	// HTTP forwarders can return a compatibility error after a provider read;
	// the HTTP handler alone cannot prove an unsent generation.
	phase := "before_response"
	if streamStarted || c.Writer.Written() {
		phase = "forwarding"
	}
	markOpenAIRecoveryFailure(c, recovery, protocol, phase, nil)
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
func (h *OpenAIGatewayHandler) handleOpenAIWSAgentRecoveryError(ctx context.Context, conn *coderws.Conn, err error, contexts ...*gin.Context) bool {
	var recovery *service.OpenAIAgentRecoveryError
	if !errors.As(err, &recovery) {
		return false
	}
	if len(contexts) > 0 && contexts[0] != nil {
		phase, written := "before_dispatch", boolPointer(false)
		var closeErr *service.OpenAIWSClientCloseError
		if errors.As(err, &closeErr) {
			phase, written = "forwarding", nil
		}
		if service.OpenAIWSAgentRecoveryBeforeSend(err) {
			phase, written = "before_upstream_write", boolPointer(false)
		}
		markOpenAIRecoveryFailure(contexts[0], recovery, "websocket", phase, written)
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

func boolPointer(value bool) *bool { return &value }

func markOpenAIRecoveryFailure(c *gin.Context, recovery *service.OpenAIAgentRecoveryError, protocol, phase string, written *bool) {
	turn := 0
	if value, ok := c.Get(service.OpsStreamTurnKey); ok {
		turn, _ = value.(int)
	}
	requestID, _ := c.Request.Context().Value(ctxkey.RequestID).(string)
	writeState := "unknown"
	if written != nil {
		writeState = strconv.FormatBool(*written)
	}
	logctx := logger.IntoContext(c.Request.Context(), logger.FromContext(c.Request.Context()).With(zap.String("request_id", requestID), zap.Int("turn", turn), zap.String("protocol", protocol), zap.String("phase", phase), zap.String("request_written", writeState)))
	service.LogOpenAIAgentRecoveryRefusal(logctx, recovery)
	service.MarkOpsStreamErrorValue(c, service.OpsStreamError{
		ErrType: recovery.ClientType(), Code: recovery.ClientCode(), Message: recovery.ClientMessage(),
		IntendedStatus: recovery.HTTPStatus(), CountTowardsSLA: true, NonStream: protocol == "http",
		RecoveryFailure: &service.OpsRecoveryFailure{Protocol: protocol, Phase: phase, RequestWritten: written, Subreason: recovery.InternalSubreason()},
	})
}
