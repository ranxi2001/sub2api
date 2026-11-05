package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.SchedulerOutboxRetryRepository = (*schedulerOutboxRepository)(nil)

func (r *schedulerOutboxRepository) DeferOutboxEvent(ctx context.Context, eventID int64) error {
	// Copy from PostgreSQL, not a caller-supplied/deserialized payload. The unique
	// event ID makes retries after an ambiguous commit idempotent. Do not reset an
	// existing task's attempts, due time or lease when the main watermark lags.
	var copiedID int64
	return r.db.QueryRowContext(ctx, `
		INSERT INTO scheduler_outbox_retries (event_id, event_type, account_id, group_id, payload, created_at)
		SELECT id, event_type, account_id, group_id, payload, created_at
		FROM scheduler_outbox WHERE id = $1
		ON CONFLICT (event_id) DO UPDATE SET event_id = EXCLUDED.event_id
		RETURNING event_id
	`, eventID).Scan(&copiedID)
}

func (r *schedulerOutboxRepository) ClaimOutboxRetry(ctx context.Context, lease time.Duration) (*service.SchedulerOutboxRetry, error) {
	var (
		retry      service.SchedulerOutboxRetry
		accountID  sql.NullInt64
		groupID    sql.NullInt64
		payloadRaw []byte
	)
	err := r.db.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT event_id FROM scheduler_outbox_retries
			WHERE next_retry_at <= NOW() AND (lease_until IS NULL OR lease_until <= NOW())
			ORDER BY next_retry_at, event_id
			LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		UPDATE scheduler_outbox_retries AS r
		SET attempts = r.attempts + 1,
			lease_generation = nextval('scheduler_outbox_retry_lease_generation_seq'),
			lease_until = NOW() + $1 * INTERVAL '1 second'
		FROM candidate AS c WHERE r.event_id = c.event_id
		RETURNING r.event_id, r.event_type, r.account_id, r.group_id, r.payload, r.created_at,
			r.attempts, r.lease_generation
	`, lease.Seconds()).Scan(&retry.Event.ID, &retry.Event.EventType, &accountID, &groupID,
		&payloadRaw, &retry.Event.CreatedAt, &retry.Attempts, &retry.Generation)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if accountID.Valid {
		retry.Event.AccountID = &accountID.Int64
	}
	if groupID.Valid {
		retry.Event.GroupID = &groupID.Int64
	}
	if len(payloadRaw) > 0 {
		if err := json.Unmarshal(payloadRaw, &retry.Event.Payload); err != nil {
			return nil, fmt.Errorf("decode scheduler retry event %d: %w", retry.Event.ID, err)
		}
	}
	return &retry, nil
}

func (r *schedulerOutboxRepository) CompleteOutboxRetry(ctx context.Context, eventID, generation int64) error {
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM scheduler_outbox_retries
		WHERE event_id = $1 AND lease_generation = $2 AND lease_until > NOW()
	`, eventID, generation)
	return schedulerOutboxRetryWriteResult(result, err)
}

func (r *schedulerOutboxRepository) RescheduleOutboxRetry(ctx context.Context, eventID, generation int64, delay time.Duration) error {
	result, err := r.db.ExecContext(ctx, `
		UPDATE scheduler_outbox_retries SET next_retry_at = NOW() + $3 * INTERVAL '1 second', lease_until = NULL
		WHERE event_id = $1 AND lease_generation = $2 AND lease_until > NOW()
	`, eventID, generation, delay.Seconds())
	return schedulerOutboxRetryWriteResult(result, err)
}

func schedulerOutboxRetryWriteResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("scheduler outbox retry ownership changed")
	}
	return nil
}
