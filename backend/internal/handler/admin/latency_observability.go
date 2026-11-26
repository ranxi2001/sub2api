package admin

import (
	"context"

	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

func (h *OpsHandler) NetworkSeries(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, 503, "Ops service not available")
		return
	}
	start, end, err := parseOpsTimeRange(c, "1h")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	result, err := h.opsService.NetworkSeries(ctx, start, end)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
