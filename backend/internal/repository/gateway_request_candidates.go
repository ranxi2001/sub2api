package repository

import (
	"context"
	"database/sql"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbaccountgroup "github.com/Wei-Shaw/sub2api/ent/accountgroup"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.GatewayRequestCandidateReader = (*accountRepository)(nil)

// This request-only SELECT never expands the platform bucket queries. It reads
// policy, explicit membership, credentials and runtime state in one SQL snapshot.
func (r *accountRepository) ListGatewayRequestCandidates(ctx context.Context, groupID int64, target string) ([]service.Account, *service.Group, error) {
	if groupID <= 0 || target != service.PlatformAnthropic {
		return nil, nil, nil
	}
	tx, err := r.client.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	reader := &accountRepository{client: tx.Client()}
	txctx := dbent.NewTxContext(ctx, tx)
	g, err := reader.client.Group.Get(txctx, groupID)
	if err != nil {
		return nil, nil, translatePersistenceError(err, service.ErrGroupNotFound, nil)
	}
	group := groupEntityToService(g)
	if g.DeletedAt != nil || !group.IsActive() || group.Platform != target || group.RequireOAuthOnly {
		return nil, group, nil
	}
	ids, err := observerAccountQuery(txctx, reader.client.Account.Query()).Where(
		dbaccount.PlatformEQ(service.PlatformGateway), dbaccount.TypeEQ(service.AccountTypeGateway),
		dbaccount.StatusEQ(service.StatusActive), dbaccount.SchedulableEQ(true),
		dbaccount.HasAccountGroupsWith(dbaccountgroup.GroupIDEQ(groupID)),
	).Order(dbent.Asc(dbaccount.FieldPriority), dbent.Asc(dbaccount.FieldID)).IDs(txctx)
	if err != nil {
		return nil, nil, err
	}
	accounts := make([]service.Account, 0, len(ids))
	for _, id := range ids {
		account, _, err := reader.gatewayRequestAccount(txctx, id, groupID)
		if err != nil {
			return nil, nil, err
		}
		accounts = append(accounts, *account)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return accounts, group, nil
}
