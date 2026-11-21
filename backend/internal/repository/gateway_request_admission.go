package repository

import (
	"context"
	"database/sql"
	"errors"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.GatewayRequestAccountReader = (*accountRepository)(nil)

func (r *accountRepository) GatewayRequestAccount(ctx context.Context, accountID, groupID int64) (*service.Account, *service.Group, error) {
	if dbent.TxFromContext(ctx) == nil {
		tx, err := r.client.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
		if errors.Is(err, dbent.ErrTxStarted) {
			return r.gatewayRequestAccount(ctx, accountID, groupID)
		}
		if err != nil {
			return nil, nil, err
		}
		defer func() { _ = tx.Rollback() }()
		account, group, err := r.gatewayRequestAccount(dbent.NewTxContext(ctx, tx), accountID, groupID)
		if err != nil {
			return nil, nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, err
		}
		return account, group, nil
	}
	return r.gatewayRequestAccount(ctx, accountID, groupID)
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
