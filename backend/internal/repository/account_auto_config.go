package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// Persist progress and concurrency together under a row lock. Only the one
// owned JSON key is patched, preserving concurrent token and account changes.
func (r *accountRepository) RecordConcurrencyResult(ctx context.Context, event service.AccountConcurrencyResult, c service.OAuthAutoConfig) error {
	db, ok := r.sql.(*sql.DB)
	if !ok {
		return errors.New("concurrency upgrade requires transactional database")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var raw string
	err = tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1 FOR SHARE", service.SettingKeyOAuthAutoConfig).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	actual := service.DefaultOAuthAutoConfig()
	if err = json.Unmarshal([]byte(raw), &actual); err != nil {
		return err
	}
	if !actual.UpgradeEnabled || actual.Revision != c.Revision || event.StartedAt.Before(actual.UpdatedAt) {
		return nil
	}
	var current int
	var accountName, platform string
	var extra, admission []byte
	err = tx.QueryRowContext(ctx, `SELECT concurrency,COALESCE(extra->'auto_config_concurrency','{}'::jsonb),name,platform,COALESCE(extra->'openai_two_fa_admission','null'::jsonb) FROM accounts a
 WHERE id=$1 AND deleted_at IS NULL AND parent_account_id IS NULL
 AND concurrency>0 AND (NOT $3 OR (status='active' AND schedulable=true))
 AND (NOT $3 OR ((rate_limit_reset_at IS NULL OR rate_limit_reset_at<=NOW()) AND (overload_until IS NULL OR overload_until<=NOW()) AND (temp_unschedulable_until IS NULL OR temp_unschedulable_until<=NOW())))
 AND (expires_at IS NULL OR NOT auto_pause_on_expired OR expires_at>NOW())
 AND EXISTS(SELECT 1 FROM account_groups ag JOIN groups g ON g.id=ag.group_id WHERE ag.account_id=a.id AND ag.group_id=ANY($2) AND g.deleted_at IS NULL AND g.status='active')
 FOR UPDATE`, event.AccountID, pq.Array(actual.UpgradeGroupIDs), event.Success).Scan(&current, &extra, &accountName, &platform, &admission)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if string(admission) != "null" {
		var gate service.OpenAITwoFAAdmissionState
		if err := json.Unmarshal(admission, &gate); err != nil {
			return err
		}
		if gate.Phase != "passed" || gate.ApprovedAt == nil || event.StartedAt.Before(*gate.ApprovedAt) {
			return nil
		}
	}
	state := service.AutoConfigConcurrencyState{}
	if err = json.Unmarshal(extra, &state); err != nil {
		return err
	}
	if !state.DegradationHold && state.Concurrency != 0 && state.Concurrency != current {
		// A manual change starts a fresh cycle; old in-flight samples cannot fill it.
		state = service.AutoConfigConcurrencyState{Revision: actual.Revision, Concurrency: current, PausedUntil: time.Now().UTC().Add(time.Duration(actual.CooldownSeconds) * time.Second)}
	}
	if event.Reset {
		state.Successes = 0
	}
	now := time.Now().UTC()
	// Repeated failures within one cooldown do not flood history.
	logFailure := !event.Success && event.FailureCategory != "transport_error" && (state.Revision != actual.Revision || state.LastFailureAt == nil || !now.Before(state.PausedUntil) || state.Successes > 0)
	state, next := service.AdvanceConcurrency(state, current, actual, event, now)
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET concurrency=$2,
 extra=jsonb_set(COALESCE(extra,'{}'::jsonb),'{auto_config_concurrency}',$3::jsonb),
 updated_at=CASE WHEN concurrency<>$2 THEN NOW() ELSE updated_at END WHERE id=$1`, event.AccountID, next, string(encoded))
	if err != nil {
		return err
	}
	if next != current {
		if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &event.AccountID, nil, nil); err != nil {
			return err
		}
	}
	if next != current || logFailure {
		kind := service.AutoConfigEventCooldown
		if next != current {
			kind = service.AutoConfigEventUpgrade
		}
		if err = insertAutoConfigEvent(ctx, tx, service.AutoConfigEvent{
			AccountID: event.AccountID, AccountName: accountName, Platform: platform, Kind: kind,
			Details: service.AutoConfigEventDetails{PreviousConcurrency: current, Concurrency: next, CooldownSeconds: actual.CooldownSeconds},
		}); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if next != current {
		r.syncSchedulerAccountSnapshot(ctx, event.AccountID)
	}
	return nil
}
