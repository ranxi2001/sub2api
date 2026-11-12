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
	key := getAPIKeyFromContext(c)
	// Background account probes have no API key and may omit the request URL.
	// They do not inherit a customer's per-key policy.
	if !key.RequiresContextCheck() {
		return nil
	}
	ws := GetOpenAIClientTransport(c) == OpenAIClientTransportWS
	// A restricted key with no URL still needs its final payload checked.
	if !ws && c.Request.URL != nil && !IsAPIKeyModelPolicyPath(c.Request.URL.Path) {
		return nil
	}
	if err := s.CheckAPIKeyLongContext(ctx, key, body, model); err != nil {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
		if ws {
			return NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, APIKeyContextLengthExceededMessage, err)
		}
		return &OpenAITurnAdmissionError{Reason: err.Error(), cause: err}
	}
	return nil
}
