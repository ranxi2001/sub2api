package service

import (
	"context"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

// Recheck the actual outbound payload after WS continuation inference and
// HTTP-bridge replay expansion. This guards content absent from the raw frame.
func (s *OpenAIGatewayService) checkAPIKeyLongContextBeforeSend(ctx context.Context, c *gin.Context, body []byte, model string) error {
	if c == nil || c.Request == nil {
		return nil
	}
	ws := GetOpenAIClientTransport(c) == OpenAIClientTransportWS
	if !ws && !IsAPIKeyModelPolicyPath(c.Request.URL.Path) {
		return nil
	}
	if err := s.CheckAPIKeyLongContext(ctx, getAPIKeyFromContext(c), body, model); err != nil {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
		if ws {
			return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "long context is disabled for this API key; send complete visible text", err)
		}
		return &OpenAITurnAdmissionError{Reason: err.Error(), cause: err}
	}
	return nil
}
