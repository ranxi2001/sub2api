package repository

import (
	"context"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// No status or schedulable filter: this is an authenticated admin inventory,
// never an eligible routing pool. Ent's existing deleted-row interceptor stays.
func (r *accountRepository) ListPriorityLifecycleAccounts(ctx context.Context) ([]service.Account, error) {
	rows, err := r.client.Account.Query().Where(dbaccount.PlatformEQ(service.PlatformOpenAI)).Order(dbent.Asc(dbaccount.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	return r.accountsToService(ctx, rows)
}
