package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"time"
)

// UpstreamOperations is shared by two independent, read-only admin endpoints.
func (h *ChannelHandler) UpstreamOperations(c *gin.Context) {
	account, e1 := operationsInt(c, "account_id", 0)
	group, e2 := operationsInt(c, "group_id", 0)
	page, e3 := operationsInt(c, "page", 1)
	size, e4 := operationsInt(c, "page_size", 20)
	from, e5 := time.Parse(time.RFC3339, c.Query("from"))
	until, e6 := time.Parse(time.RFC3339, c.Query("until"))
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil {
		response.ErrorFrom(c, service.ErrChannelOperationsValidation)
		return
	}
	f := service.UpstreamOperationsFilter{From: from, Until: until, Upstream: c.Query("upstream"), AccountID: account, GroupID: group, Model: c.Query("model"), ModelScope: c.DefaultQuery("model_scope", "public"), Dimension: c.DefaultQuery("dimension", "upstream"), Sort: c.DefaultQuery("sort", "requests"), Direction: c.DefaultQuery("direction", "desc"), Page: int(page), PageSize: int(size)}
	if err := service.ValidateUpstreamOperationsFilter(f); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out, err := h.channelService.UpstreamOperations(c.Request.Context(), f)
	if err != nil {
		response.InternalError(c, "Upstream operations query failed; retry")
		return
	}
	response.Success(c, out)
}
