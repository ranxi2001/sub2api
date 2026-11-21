package repository

import (
	"context"
	"database/sql"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.GatewayRequestAccountReader = (*accountRepository)(nil)

func (r *accountRepository) GatewayRequestAccount(ctx context.Context, accountID, groupID int64) (*service.Account, *service.Group, error) {
	return withGatewayReadSnapshot(ctx, r, func(ctx context.Context, reader *accountRepository) (*service.Account, *service.Group, error) {
		return reader.gatewayRequestAccount(ctx, accountID, groupID)
	})
}

// Reuse the caller's transaction when present. Otherwise keep the account,
// bindings and group policy in one repeatable-read snapshot.
func withGatewayReadSnapshot[T any](ctx context.Context, r *accountRepository, read func(context.Context, *accountRepository) (T, *service.Group, error)) (T, *service.Group, error) {
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		tx, _ = r.sql.(*dbent.Tx)
	}
	if tx != nil {
		ctx = dbent.NewTxContext(ctx, tx)
		return read(ctx, r.accountTransactionScope(ctx))
	}
	var zero T
	tx, err := r.client.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return zero, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	ctx = dbent.NewTxContext(ctx, tx)
	value, group, err := read(ctx, r.accountTransactionScope(ctx))
	if err != nil {
		return zero, nil, err
	}
	if err := tx.Commit(); err != nil {
		return zero, nil, err
	}
	return value, group, nil
}

func (r *accountRepository) gatewayRequestAccount(ctx context.Context, accountID, groupID int64) (*service.Account, *service.Group, error) {
	account, err := r.GetByID(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	paused, err := performancePaused(ctx, clientFromContext(ctx, r.client), []int64{accountID})
	if err != nil {
		return nil, nil, err
	}
	account.PerformancePaused = paused[accountID]
	group, err := clientFromContext(ctx, r.client).Group.Get(ctx, groupID)
	if err != nil {
		return nil, nil, translatePersistenceError(err, service.ErrGroupNotFound, nil)
	}
	if group.DeletedAt != nil {
		return nil, nil, service.ErrGroupNotFound
	}
	return account, groupEntityToService(group), nil
}
