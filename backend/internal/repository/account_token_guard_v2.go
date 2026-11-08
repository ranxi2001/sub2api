package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type accountTokenGuardV2Repository struct {
	db *sql.DB
}

func (r *accountTokenGuardV2Repository) UpdateSwitches(ctx context.Context, id int64, enabled, autoRelogin *bool) error {
	result, err := r.db.ExecContext(ctx, `UPDATE account_token_guard_v2_accounts
		SET enabled = COALESCE($2, enabled), auto_relogin_enabled = COALESCE($3, auto_relogin_enabled),
			next_probe_at = CASE WHEN $2 = TRUE AND NOT enabled THEN LEAST(next_probe_at, NOW()) ELSE next_probe_at END,
			updated_at = NOW() WHERE account_id = $1`, id, enabled, autoRelogin)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return infraerrors.NotFound("TOKEN_GUARD_V2_NOT_FOUND", "Monitored account not found")
	}
	return nil
}

func NewAccountTokenGuardV2Repository(db *sql.DB) service.AccountTokenGuardV2Repository {
	return &accountTokenGuardV2Repository{db: db}
}

func (r *accountTokenGuardV2Repository) UpsertAccount(ctx context.Context, accountID int64, enabled, autoRelogin bool) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO account_token_guard_v2_accounts (account_id, enabled, auto_relogin_enabled, next_probe_at, last_counted_task_id)
		VALUES ($1, $2, $3, NOW(), COALESCE((SELECT MAX(task.id) FROM openai_oauth_reauth_tasks task
            WHERE task.account_id=$1 AND task.oauth_profile='codex' AND task.status IN ('failed','succeeded')
              AND NOT EXISTS(SELECT 1 FROM openai_totp_rotations rotation WHERE rotation.task_id=task.id)),0))
		ON CONFLICT (account_id) DO UPDATE
		SET enabled = EXCLUDED.enabled,
			auto_relogin_enabled = EXCLUDED.auto_relogin_enabled,
			next_probe_at = CASE WHEN EXCLUDED.enabled THEN LEAST(account_token_guard_v2_accounts.next_probe_at, NOW()) ELSE account_token_guard_v2_accounts.next_probe_at END,
			updated_at = NOW()
	`, accountID, enabled, autoRelogin)
	return err
}

func (r *accountTokenGuardV2Repository) DeleteAccount(ctx context.Context, accountID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM account_token_guard_v2_accounts WHERE account_id = $1`, accountID)
	return err
}

// PruneDeletedAccounts handles soft deletion as well as legacy orphaned rows.
// A failed probe or missing login configuration is not evidence of deletion.
func (r *accountTokenGuardV2Repository) PruneDeletedAccounts(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM account_token_guard_v2_accounts AS guard
		WHERE NOT EXISTS (
			SELECT 1 FROM accounts AS account
			WHERE account.id = guard.account_id AND account.deleted_at IS NULL
		)`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *accountTokenGuardV2Repository) GetAccount(ctx context.Context, accountID int64) (*service.AccountTokenGuardV2Record, error) {
	record, err := scanAccountTokenGuardV2(r.db.QueryRowContext(ctx, accountTokenGuardV2Select+` WHERE account_id = $1`, accountID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

func (r *accountTokenGuardV2Repository) ListAccounts(ctx context.Context) ([]service.AccountTokenGuardV2Record, error) {
	rows, err := r.db.QueryContext(ctx, accountTokenGuardV2Select+` ORDER BY account_id ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]service.AccountTokenGuardV2Record, 0)
	for rows.Next() {
		record, scanErr := scanAccountTokenGuardV2(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, *record)
	}
	return result, rows.Err()
}

func (r *accountTokenGuardV2Repository) ClaimDue(ctx context.Context, owner string, leaseDuration time.Duration, limit int, dueBefore time.Time) ([]service.AccountTokenGuardV2Record, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH due AS (
			SELECT account_id
			FROM account_token_guard_v2_accounts
			WHERE enabled = TRUE
				AND next_probe_at <= NOW()
				AND next_probe_at <= $4
				AND (lease_until IS NULL OR lease_until < NOW())
			ORDER BY next_probe_at ASC, account_id ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE account_token_guard_v2_accounts AS guard
		SET lease_owner = $2,
			lease_until = NOW() + ($3 * INTERVAL '1 second'),
			updated_at = NOW()
		FROM due
		WHERE guard.account_id = due.account_id
		RETURNING guard.account_id, guard.relogin_fail_streak, guard.last_counted_task_id, guard.auto_relogin_exhausted, guard.enabled, guard.auto_relogin_enabled,
			guard.probe_state, guard.probe_detail, guard.fail_streak,
			guard.last_probe_at, guard.last_reauth_at, guard.next_probe_at,
			guard.cooldown_until, guard.blocked_reason,
			COALESCE(guard.lease_owner, ''), guard.lease_until,
			guard.created_at, guard.updated_at, guard.refresh_failure_code, guard.last_refresh_failure_at
	`, limit, owner, int64(leaseDuration.Seconds()), dueBefore)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]service.AccountTokenGuardV2Record, 0, limit)
	for rows.Next() {
		record, scanErr := scanAccountTokenGuardV2(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, *record)
	}
	return result, rows.Err()
}

func (r *accountTokenGuardV2Repository) ClaimAccount(ctx context.Context, accountID int64, owner string, leaseDuration time.Duration) (*service.AccountTokenGuardV2Record, error) {
	record, err := scanAccountTokenGuardV2(r.db.QueryRowContext(ctx, `
		UPDATE account_token_guard_v2_accounts
		SET lease_owner = $2,
			lease_until = NOW() + ($3 * INTERVAL '1 second'),
			updated_at = NOW()
		WHERE account_id = $1 AND (lease_until IS NULL OR lease_until < NOW())
		RETURNING account_id, relogin_fail_streak, last_counted_task_id, auto_relogin_exhausted, enabled, auto_relogin_enabled,
			probe_state, probe_detail, fail_streak,
			last_probe_at, last_reauth_at, next_probe_at,
			cooldown_until, blocked_reason,
			COALESCE(lease_owner, ''), lease_until, created_at, updated_at, refresh_failure_code, last_refresh_failure_at
	`, accountID, owner, int64(leaseDuration.Seconds())))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

func (r *accountTokenGuardV2Repository) CompleteProbe(ctx context.Context, accountID int64, owner string, result service.AccountTokenGuardV2ProbeCompletion) error {
	queryResult, err := r.db.ExecContext(ctx, `
		UPDATE account_token_guard_v2_accounts
		SET probe_state = $3,
			probe_detail = $4,
			fail_streak = $5,
			last_probe_at = $6,
			last_reauth_at = COALESCE($7, last_reauth_at),
			next_probe_at = $8,
			cooldown_until = GREATEST(cooldown_until, $9::timestamptz),
			blocked_reason = $10,
			lease_owner = NULL,
			lease_until = NULL,
			updated_at = NOW()
		WHERE account_id = $1 AND lease_owner = $2 AND lease_until > NOW()
	`, accountID, owner, result.ProbeState, result.ProbeDetail, result.FailStreak,
		result.LastProbeAt, result.LastReauthAt, result.NextProbeAt,
		result.CooldownUntil, result.BlockedReason)
	if err != nil {
		return err
	}
	affected, err := queryResult.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ReleaseProbe abandons a claim without advancing its schedule or inspection
// history. Each claim has its own owner token, so a late release cannot clear a
// replacement claim after this lease expires.
func (r *accountTokenGuardV2Repository) ReleaseProbe(ctx context.Context, accountID int64, owner string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE account_token_guard_v2_accounts
		SET lease_owner = NULL, lease_until = NULL, updated_at = NOW()
		WHERE account_id = $1 AND lease_owner = $2`, accountID, owner)
	return err
}

func (r *accountTokenGuardV2Repository) MarkReauthRequested(ctx context.Context, accountID int64, requestedAt, cooldownUntil time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE account_token_guard_v2_accounts
		SET last_reauth_at = $2,
			cooldown_until = GREATEST(cooldown_until, $3::timestamptz),
			updated_at = NOW()
		WHERE account_id = $1
	`, accountID, requestedAt, cooldownUntil)
	return err
}

func (r *accountTokenGuardV2Repository) RescheduleEnabled(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE account_token_guard_v2_accounts
		SET next_probe_at = NOW(), updated_at = NOW()
		WHERE enabled = TRUE
	`)
	return err
}

const accountTokenGuardV2Select = `
	SELECT account_id, relogin_fail_streak, last_counted_task_id, auto_relogin_exhausted, enabled, auto_relogin_enabled,
		probe_state, probe_detail, fail_streak,
		last_probe_at, last_reauth_at, next_probe_at,
		cooldown_until, blocked_reason,
		COALESCE(lease_owner, ''), lease_until, created_at, updated_at, refresh_failure_code, last_refresh_failure_at
	FROM account_token_guard_v2_accounts`

type accountTokenGuardV2Row interface {
	Scan(dest ...any) error
}

func scanAccountTokenGuardV2(row accountTokenGuardV2Row) (*service.AccountTokenGuardV2Record, error) {
	var record service.AccountTokenGuardV2Record
	var lastProbeAt, lastReauthAt, cooldownUntil, leaseUntil, lastRefreshFailureAt sql.NullTime
	if err := row.Scan(
		&record.AccountID, &record.ReloginFailStreak, &record.LastCountedTaskID, &record.AutoReloginExhausted, &record.Enabled, &record.AutoReloginEnabled,
		&record.ProbeState, &record.ProbeDetail, &record.FailStreak,
		&lastProbeAt, &lastReauthAt, &record.NextProbeAt,
		&cooldownUntil, &record.BlockedReason,
		&record.LeaseOwner, &leaseUntil, &record.CreatedAt, &record.UpdatedAt, &record.RefreshFailureCode, &lastRefreshFailureAt,
	); err != nil {
		return nil, err
	}
	if lastProbeAt.Valid {
		record.LastProbeAt = &lastProbeAt.Time
	}
	if lastReauthAt.Valid {
		record.LastReauthAt = &lastReauthAt.Time
	}
	if cooldownUntil.Valid {
		record.CooldownUntil = &cooldownUntil.Time
	}
	if leaseUntil.Valid {
		record.LeaseUntil = &leaseUntil.Time
	}
	if lastRefreshFailureAt.Valid {
		record.LastRefreshFailureAt = &lastRefreshFailureAt.Time
	}
	return &record, nil
}

// Recording history never changes scheduling, credentials, leases or budgets.
func (r *accountTokenGuardV2Repository) RecordRefreshFailure(ctx context.Context, id int64, code string, at time.Time) error {
	failure := service.SafeCredentialOperationFailure(code, "refresh_token", &at)
	_, err := r.db.ExecContext(ctx, `UPDATE account_token_guard_v2_accounts
		SET refresh_failure_code = $2, last_refresh_failure_at = $3
		WHERE account_id = $1 AND (last_refresh_failure_at IS NULL OR last_refresh_failure_at < $3)`, id, failure.Code, at)
	return err
}
