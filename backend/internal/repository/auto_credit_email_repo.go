package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type autoCreditEmailOutboxRepository struct { db *sql.DB }

func NewAutoCreditEmailOutboxRepository(db *sql.DB) service.AutoCreditEmailOutboxRepository {
	return &autoCreditEmailOutboxRepository{db: db}
}

func (r *autoCreditEmailOutboxRepository) DeliverNext(ctx context.Context, send func(context.Context, service.AutoCreditEmail) error) (bool, error) {
	if err := ctx.Err(); err != nil { return false, err }
	// Do not let parent cancellation release the lock while SMTP is still in
	// flight. Stop joins the sender; SMTP itself has bounded I/O deadlines.
	// This transaction never locks/writes users, ledger or billing tables.
	tx, err := r.db.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil { return false, err }
	defer func() { _ = tx.Rollback() }()
	var event service.AutoCreditEmail
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT o.ledger_id,l.user_id,o.recipient_email,l.amount::text,l.balance_after::text,o.attempts FROM auto_credit_email_outbox o JOIN auto_credit_ledger l ON l.id=o.ledger_id WHERE o.sent_at IS NULL AND o.available_at<=NOW() ORDER BY o.available_at,o.ledger_id LIMIT 1 FOR UPDATE OF o SKIP LOCKED`).Scan(&event.LedgerID, &event.UserID, &event.RecipientEmail, &event.Amount, &event.BalanceAfter, &attempts)
	if errors.Is(err, sql.ErrNoRows) { return false, nil }
	if err != nil { return false, err }
	if err = ctx.Err(); err != nil { return false, err }
	sendCtx, cancel := context.WithTimeout(ctx, time.Minute)
	if strings.TrimSpace(event.RecipientEmail) == "" {
		err = errors.New("VIP recipient is empty")
	} else {
		err = send(sendCtx, event)
	}
	cancel()
	// A canceled worker must still commit a completed send or persist retry.
	ackCtx, ackCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ackCancel()
	if err != nil {
		delay := time.Second * time.Duration(1<<min(attempts+1, 8))
		_, updateErr := tx.ExecContext(ackCtx, `UPDATE auto_credit_email_outbox SET attempts=attempts+1,available_at=NOW()+$2*INTERVAL '1 second',last_error='delivery_failed' WHERE ledger_id=$1`, event.LedgerID, int(delay/time.Second))
		if updateErr != nil { return true, updateErr }
		if commitErr := tx.Commit(); commitErr != nil { return true, commitErr }
		// Do not log sender errors: SMTP diagnostics can contain addresses.
		return true, fmt.Errorf("auto credit email ledger %d delivery failed (attempt %d)", event.LedgerID, attempts+1)
	}
	if _, err = tx.ExecContext(ackCtx, `UPDATE auto_credit_email_outbox SET sent_at=NOW(),last_error='' WHERE ledger_id=$1`, event.LedgerID); err != nil { return true, err }
	return true, tx.Commit()
}
