package admin

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

// GetConcurrencySummary totals occupied account slots within the caller's account
// scope. It is independent of Ops monitoring and dashboard statistics caches.
func (h *AccountHandler) GetConcurrencySummary(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	accounts, err := h.listAccountsFiltered(ctx, "", "", "", "", 0, "", "id", "asc")
	if err != nil {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("ACCOUNT_CONCURRENCY_UNAVAILABLE", "Current account concurrency is unavailable; retry later").WithCause(err))
		return
	}

	ids := make([]int64, 0, len(accounts))
	seen := make(map[int64]struct{}, len(accounts))
	for _, account := range accounts {
		if _, exists := seen[account.ID]; exists {
			continue
		}
		seen[account.ID] = struct{}{}
		ids = append(ids, account.ID)
	}
	counts, err := h.concurrencyService.GetAccountConcurrencySnapshot(ctx, ids)
	if err != nil {
		response.ErrorFrom(c, infraerrors.ServiceUnavailable("ACCOUNT_CONCURRENCY_UNAVAILABLE", "Current account concurrency is unavailable; retry later").WithCause(err))
		return
	}

	var total int64
	for _, id := range ids {
		total += int64(counts[id])
	}
	response.Success(c, gin.H{"current_concurrency": total})
}
