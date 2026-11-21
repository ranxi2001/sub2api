package repository

import (
	"context"
	"errors"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.GatewayAccountTransactionRepository = (*accountRepository)(nil)

// WithinGatewayAccountTransaction reuses the established repository tx scope:
// billing settings still use updateLockedAccount and its managed-state fences,
// bulk still uses its existing SQL merge, and bindings retain their live checks.
func (r *accountRepository) WithinGatewayAccountTransaction(ctx context.Context, id int64, groups []int64, run func(context.Context) error) (writeErr error) {
	tx := dbent.TxFromContext(ctx)
	owned := tx == nil
	if owned {
		var err error
		tx, err = r.client.Tx(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
	} else {
		// Bulk callers may keep their outer transaction after a failed item.
		// Roll back this item's staged fields even in that caller-owned scope.
		if _, err := tx.Client().ExecContext(ctx, "SAVEPOINT gateway_account_update"); err != nil {
			return err
		}
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if writeErr != nil {
				_, err := tx.Client().ExecContext(cleanupCtx, "ROLLBACK TO SAVEPOINT gateway_account_update")
				writeErr = errors.Join(writeErr, err)
				if err != nil {
					_ = tx.Rollback()
					return
				}
			}
			_, err := tx.Client().ExecContext(cleanupCtx, "RELEASE SAVEPOINT gateway_account_update")
			writeErr = errors.Join(writeErr, err)
			if err != nil {
				_ = tx.Rollback()
			}
		}()
	}
	txctx := dbent.NewTxContext(ctx, tx)
	// Match the existing binding lock order: group policies, then account row.
	if err := lockLiveGroups(txctx, tx.Client(), groups); err != nil {
		return err
	}
	if _, err := lockOAuthDegradationGroupBindings(txctx, tx.Client(), id, groups); err != nil {
		return err
	}
	account, err := r.GetByID(txctx, id)
	if err != nil {
		return err
	}
	if !account.IsGateway() || account.Platform != service.PlatformGateway {
		return service.ErrGatewayAccountIdentity
	}
	if err := run(txctx); err != nil {
		return err
	}
	// Context-scoped repositories suppress precommit cache publication. Reuse
	// the existing commit hook for caller-owned transactions as well as ours.
	service.AfterAccountCommit(txctx, func(committed context.Context) {
		r.syncSchedulerAccountSnapshot(committed, id)
	})
	if owned {
		return tx.Commit()
	}
	return nil
}
