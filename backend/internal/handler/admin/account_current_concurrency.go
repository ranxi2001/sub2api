package admin

import (
	"context"
	"sort"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// listAccountsByCurrentConcurrency applies the existing filtered/observer-scoped
// listing before sorting and pagination. Reuse one snapshot for order and display
// so a second live read cannot make the returned page contradict its own order.
func (h *AccountHandler) listAccountsByCurrentConcurrency(
	ctx context.Context, page, pageSize int,
	platform, accountType, status, search string, groupID int64, privacyMode, sortOrder string,
) ([]service.Account, int64, map[int64]int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	accounts, err := h.listAccountsFiltered(ctx, platform, accountType, status, search, groupID, privacyMode, "id", "asc")
	if err != nil {
		return nil, 0, nil, err
	}
	ids := make([]int64, len(accounts))
	for i := range accounts {
		ids[i] = accounts[i].ID
	}
	counts, err := h.concurrencyService.GetAccountConcurrencySnapshot(ctx, ids)
	if err != nil {
		return nil, 0, nil, infraerrors.ServiceUnavailable("ACCOUNT_CONCURRENCY_UNAVAILABLE", "Current account concurrency is unavailable; retry or choose another sort order").WithCause(err)
	}
	desc := pagination.NormalizeSortOrder(sortOrder, pagination.SortOrderAsc) == pagination.SortOrderDesc
	sort.Slice(accounts, func(i, j int) bool {
		left, right := counts[accounts[i].ID], counts[accounts[j].ID]
		if left == right {
			if desc {
				return accounts[i].ID > accounts[j].ID
			}
			return accounts[i].ID < accounts[j].ID
		}
		if desc {
			return left > right
		}
		return left < right
	})
	total := int64(len(accounts))
	params := pagination.PaginationParams{Page: page, PageSize: pageSize}
	start := params.Offset()
	if start >= len(accounts) {
		return []service.Account{}, total, counts, nil
	}
	return accounts[start:min(start+params.Limit(), len(accounts))], total, counts, nil
}
