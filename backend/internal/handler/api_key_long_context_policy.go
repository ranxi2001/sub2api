package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// CheckAPIKeyLongContext exposes the gateway's pricing-aware local admission
// check to the shared HTTP middleware without exposing its dependencies.
func (h *GatewayHandler) CheckAPIKeyLongContext(ctx context.Context, key *service.APIKey, body []byte, models ...string) error {
	var gateway *service.GatewayService
	if h != nil {
		gateway = h.gatewayService
	}
	return gateway.CheckAPIKeyLongContext(ctx, key, body, models...)
}

func (h *OpenAIGatewayHandler) handleAnthropicAPIKeyLongContextError(c *gin.Context, err error, streamStarted bool) bool {
	var contextErr *service.APIKeyLongContextError
	if !errors.As(err, &contextErr) {
		return false
	}
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
	if streamStarted {
		h.anthropicStreamingAwareError(c, http.StatusBadRequest, "invalid_request_error", contextErr.Error(), true)
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"type": "error", "error": gin.H{
			"type": "invalid_request_error", "code": service.APIKeyContextLengthExceededCode, "message": contextErr.Error(),
		}})
	}
	return true
}
