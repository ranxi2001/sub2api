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
	c.Header("Cache-Control", "no-store")
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
	f.AccountUsage = c.Query("basis") == "account_usage"
	if err := service.NormalizeProfitOperationsFilter(&f); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// Refresh (or reuse) each linked provider's wallet for this window before
	// the report subtracts it. A probe failure does not hide unbound estimates.
	force := c.Query("refresh_spend") == "1" || c.Query("refresh_spend") == "true"
	if force || c.Query("refresh_report") == "1" {
		h.profitReports.clear()
	}
	started := time.Now()
	out, err := h.profitReports.read(c.Request.Context(), f, func(ctx context.Context, collect bool) (*service.ProfitOperationsSnapshot, error) {
		if !f.AccountUsage {
			h.refreshBoundProviderSpend(ctx, f.From, f.Until, force)
		}
		if collect {
			ctx = service.WithProfitRowCollection(ctx)
		}
		return h.channelService.ProfitOperations(ctx, f)
	})
	if err != nil {
		status, reason, message := http.StatusInternalServerError, "PROFIT_OPERATIONS_QUERY_FAILED", "Upstream operations query failed; retry"
		if errors.Is(err, service.ErrAccountUsageBuilding) {
			response.ErrorFrom(c, err)
			return
		}
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

func (h *ChannelHandler) ProfitChannelAccountIDs(c *gin.Context) {
	id, err := operationsInt(c, "channel_id", 0)
	if err != nil || id <= 0 {
		response.ErrorFrom(c, service.ErrUpstreamOperationsValidation)
		return
	}
	repo, ok := h.upstreamChannelRepo(c)
	if !ok {
		return
	}
	reader, ok := repo.(interface {
		ProfitChannelAccountIDs(context.Context, int64) ([]int64, error)
	})
	if !ok {
		response.ErrorFrom(c, service.ErrProviderProfitLinkUnavailable)
		return
	}
	ids, err := reader.ProfitChannelAccountIDs(c.Request.Context(), id)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Unable to read channel accounts")
		return
	}
	response.Success(c, gin.H{"account_ids": ids})
}
