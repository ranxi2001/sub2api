package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Serialize dashboard sessions and credential rotation across instances. The
// row lock also prevents a concurrent edit from changing the destination while
// a credential is in flight. Never use a process mutex for single-use tokens.
func (r *channelRepository) WithMatureProviderSession(parent context.Context, id, revision int64, run func(context.Context, *service.MatureProvider, string) (service.ProviderSessionUpdate, error)) (*service.MatureProvider, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 35*time.Second)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var cipher string
	p, err := scanMatureProvider(tx.QueryRowContext(ctx, `SELECT `+matureProviderColumns+`,credential_cipher FROM mature_providers WHERE id=$1 FOR UPDATE`, id), &cipher)
	if err != nil {
		return nil, err
	}
	if revision > 0 && p.Revision != revision {
		return nil, service.ErrMatureProviderConflict
	}
	if err = parent.Err(); err != nil {
		return nil, err
	}
	// Lock acquisition consumes this transaction's lifetime too. Never start
	// authentication with a fresh independent timeout after a long row wait:
	// single-use refresh tokens must still have time to reach durable storage.
	const persistenceReserve = 8 * time.Second
	const minimumRemoteBudget = time.Second
	txDeadline, _ := ctx.Deadline()
	remoteDeadline := txDeadline.Add(-persistenceReserve)
	if limit := time.Now().Add(25 * time.Second); limit.Before(remoteDeadline) {
		remoteDeadline = limit
	}
	if deadline, ok := parent.Deadline(); ok && deadline.Before(remoteDeadline) {
		remoteDeadline = deadline
	}
	if time.Until(remoteDeadline) < minimumRemoteBudget {
		return nil, context.DeadlineExceeded
	}
	operationCtx, stop := context.WithDeadline(parent, remoteDeadline)
	defer stop()
	u, operationErr := run(operationCtx, p, cipher)
	// Finish remote work before using the reserved, cancellation-independent
	// transaction budget. A cancelled caller must not strand a rotated token.
	if operationErr == nil {
		operationErr = operationCtx.Err()
	}
	stop()
	if u.Cipher == nil && u.Metrics == nil && u.Status == "" {
		return p, operationErr
	}
	var metrics any
	if u.Metrics != nil {
		raw, e := json.Marshal(u.Metrics)
		if e != nil {
			return nil, e
		}
		metrics = string(raw)
	}
	p, err = scanMatureProvider(tx.QueryRowContext(ctx, `UPDATE mature_providers SET credential_cipher=COALESCE($2,credential_cipher),metrics=COALESCE($3::jsonb,metrics),status=CASE WHEN $4='' THEN status ELSE $4 END,last_error=$5,observed_at=CASE WHEN $3::jsonb IS NULL THEN observed_at ELSE clock_timestamp() END,revision=revision+1,updated_at=clock_timestamp() WHERE id=$1 RETURNING `+matureProviderColumns, id, u.Cipher, metrics, u.Status, u.LastError))
	if err != nil {
		return nil, err
	}
	if u.Metrics != nil {
		if err = updateMatureProviderAlert(ctx, tx, p); err != nil {
			return nil, err
		}
	}
	// Persist a rotated refresh token even when the following metadata request
	// fails. Otherwise a successful exchange would strand the saved credential.
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return p, operationErr
}
