package admin

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// UpstreamOperations is shared by two independent, read-only admin endpoints.
func (h *ChannelHandler) ProfitOperations(c *gin.Context) {
	account, e1 := operationsInt(c, "account_id", 0)
	group, e2 := operationsInt(c, "group_id", 0)
	channel, e7 := operationsInt(c, "channel_id", 0)
	page, e3 := operationsInt(c, "page", 1)
	size, e4 := operationsInt(c, "page_size", 20)
	from, e5 := time.Parse(time.RFC3339, c.Query("from"))
	until, e6 := time.Parse(time.RFC3339, c.Query("until"))
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil || e7 != nil || channel < 0 {
		response.ErrorFrom(c, service.ErrUpstreamOperationsValidation)
		return
	}
	f := service.UpstreamOperationsFilter{From: from, Until: until, Upstream: c.Query("upstream"), ChannelID: channel, AccountID: account, GroupID: group, Model: c.Query("model"), ModelScope: c.DefaultQuery("model_scope", "public"), Dimension: c.DefaultQuery("dimension", "upstream"), Sort: c.DefaultQuery("sort", "requests"), Direction: c.DefaultQuery("direction", "desc"), Page: int(page), PageSize: int(size)}
	if err := service.NormalizeProfitOperationsFilter(&f); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	started := time.Now()
	out, err := h.channelService.ProfitOperations(c.Request.Context(), f)
	if err != nil {
		status, reason, message := http.StatusInternalServerError, "PROFIT_OPERATIONS_QUERY_FAILED", "Upstream operations query failed; retry"
		if errors.Is(err, context.DeadlineExceeded) {
			status, reason, message = http.StatusGatewayTimeout, "PROFIT_OPERATIONS_TIMEOUT", "Profit operations query timed out; retry later"
		}
		// Never log driver messages, SQL, model names or URLs: they may contain
		// sensitive account configuration. Validated IDs and enums identify scope.
		log.Printf("[ProfitOperations] query failed reason=%s error_type=%T elapsed_ms=%d account_id=%d group_id=%d channel_id=%d dimension=%s model_scope=%s", reason, err, time.Since(started).Milliseconds(), f.AccountID, f.GroupID, f.ChannelID, f.Dimension, f.ModelScope)
		response.ErrorWithDetails(c, status, message, reason, nil)
		return
	}
	response.Success(c, out)
}
