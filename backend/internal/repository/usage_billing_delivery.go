package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.DurableUsageBillingRepository = (*usageBillingRepository)(nil)

func (r *usageBillingRepository) StageUsageBilling(ctx context.Context, cmd *service.UsageBillingCommand, usage *service.UsageLog) error {
	_, err := r.StageUsageBillingForReceipt(ctx, cmd, usage)
	return err
}

func (r *usageBillingRepository) StageUsageBillingForReceipt(ctx context.Context, cmd *service.UsageBillingCommand, usage *service.UsageLog) (bool, error) {
	if cmd == nil || usage == nil || cmd.RequestID == "" || cmd.APIKeyID <= 0 || cmd.UserID <= 0 ||
		usage.RequestID != cmd.RequestID || usage.APIKeyID != cmd.APIKeyID || usage.UserID != cmd.UserID || usage.AccountID != cmd.AccountID {
		return false, service.ErrUsageBillingRequestConflict
	}
	cmd.Normalize()
	e := service.UsageBillingEvent{Version: 1, Command: *cmd, Usage: *usage}
	e.Usage.User, e.Usage.APIKey, e.Usage.Account, e.Usage.Group, e.Usage.Subscription = nil, nil, nil, nil, nil
	if e.Usage.CreatedAt.IsZero() {
		e.Usage.CreatedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	command, err := json.Marshal(e.Command)
	if err != nil {
		return false, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	inserted, err := tx.ExecContext(ctx, `INSERT INTO usage_billing_events
		(request_id,api_key_id,request_fingerprint,schema_version,payload,observed_at)
		VALUES($1,$2,$3,1,$4::jsonb,$5) ON CONFLICT(request_id,api_key_id) DO NOTHING`,
		cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint, string(raw), e.Usage.CreatedAt)
	if err != nil {
		return false, err
	}
	n, err := inserted.RowsAffected()
	if err != nil {
		return false, err
	}
	// Keep the first immutable usage snapshot. Repeated delivery may observe a
	// later wall clock, but must never change the command, owner or monetary basis.
	var same bool
	err = tx.QueryRowContext(ctx, `SELECT schema_version=1 AND request_fingerprint=$3 AND ((payload->'command')-'PlatformUsageAt')=($4::jsonb-'PlatformUsageAt')
		FROM usage_billing_events WHERE request_id=$1 AND api_key_id=$2`, cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint, string(command)).Scan(&same)
	if err != nil {
		return false, err
	}
	if !same {
		return false, service.ErrUsageBillingRequestConflict
	}
	if err = tx.Commit(); err == nil {
		return n == 1, nil
	}
	// A lost COMMIT response is not proof of rollback. Check a fresh connection
	// before returning; do not re-run either the model or a monetary operation.
	check, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var stored bool
	checkErr := r.db.QueryRowContext(check, `SELECT schema_version=1 AND request_fingerprint=$3 AND ((payload->'command')-'PlatformUsageAt')=($4::jsonb-'PlatformUsageAt')
		FROM usage_billing_events WHERE request_id=$1 AND api_key_id=$2`, cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint, string(command)).Scan(&stored)
	if checkErr == nil && stored {
		// Another transaction could have inserted the same command after a
		// rollback. The read proves the command exists, not who created it.
		return false, nil
	}
	return false, errors.Join(service.ErrUsageBillingOutcomeUnknown, err, checkErr)
}

func validateUsageBillingEvent(e *service.UsageBillingEvent, key service.UsageBillingEventKey, fingerprint string) error {
	if e.Version != 1 {
		return service.ErrUsageBillingEventVersion
	}
	c, u := &e.Command, &e.Usage
	if c.RequestID != key.RequestID || c.APIKeyID != key.APIKeyID || c.RequestFingerprint != fingerprint ||
		u.RequestID != c.RequestID || u.APIKeyID != c.APIKeyID || u.UserID != c.UserID || u.AccountID != c.AccountID ||
		u.CreatedAt.IsZero() || u.User != nil || u.APIKey != nil || u.Account != nil || u.Group != nil || u.Subscription != nil {
		return service.ErrUsageBillingRequestConflict
	}
	return nil
}

func (r *usageBillingRepository) ApplyStagedUsageBilling(ctx context.Context, key service.UsageBillingEventKey) (_ *service.UsageBillingRecovery, err error) {
	// Use the immutable staged owner for both live delivery and recovery. This
	// indexed lookup releases its connection before a hot wallet starts waiting.
	var owner string
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(payload #>> '{command,UserID}', '')
		FROM usage_billing_events WHERE request_id=$1 AND api_key_id=$2`, key.RequestID, key.APIKeyID).Scan(&owner); err != nil {
		return nil, err
	}
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	defer func() {
		if err == nil {
			return
		}
		code, state := "apply_failed", "pending"
		if errors.Is(err, service.ErrUsageBillingRequestConflict) || errors.Is(err, service.ErrUsageBillingEventVersion) {
			code, state = "identity_or_version_conflict", "blocked"
		}
		retry, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		// No monetary mutation here. Only schedule a still-pending event; never
		// overwrite an applied/complete result after an ambiguous commit.
		_, _ = r.db.ExecContext(retry, `UPDATE usage_billing_events SET state=$3,failure_code=$4,
			attempts=attempts+1,next_attempt_at=NOW()+INTERVAL '30 seconds'
			WHERE request_id=$1 AND api_key_id=$2 AND state='pending'`, key.RequestID, key.APIKeyID, state, code)
	}()
	// Admission can consume the recovery cycle's deadline before a transaction
	// starts. Back off that pending event too, so the next cycle can move on.
	release, err = r.admission.acquire(ctx, owner, r.db.Stats().MaxOpenConnections)
	if err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var raw, resultRaw []byte
	var state, fingerprint string
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT schema_version,payload,state,result,request_fingerprint
		FROM usage_billing_events WHERE request_id=$1 AND api_key_id=$2 FOR UPDATE`, key.RequestID, key.APIKeyID).
		Scan(&version, &raw, &state, &resultRaw, &fingerprint); err != nil {
		return nil, err
	}
	recovered := &service.UsageBillingRecovery{}
	if err = json.Unmarshal(raw, &recovered.Event); err != nil {
		return nil, err
	}
	if version != recovered.Event.Version {
		return nil, service.ErrUsageBillingEventVersion
	}
	if err = validateUsageBillingEvent(&recovered.Event, key, fingerprint); err != nil {
		return nil, err
	}
	if state == "applied" || state == "complete" || state == "legacy" {
		if len(resultRaw) > 0 {
			if err = json.Unmarshal(resultRaw, &recovered.Result); err != nil {
				return nil, err
			}
		}
		recovered.Settled = state != "legacy" && recovered.Result.Applied
		recovered.Result.Applied = false
		recovered.NeedsCache = state == "applied"
		return recovered, nil
	}
	if state != "pending" {
		return nil, service.ErrUsageBillingRequestConflict
	}
	cmd := &recovered.Event.Command
	applied, err := r.claimUsageBillingKey(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	if !applied {
		// An old hot/archive claim may predate this journal. Do not reconstruct
		// or alter historical usage or reapply its post-commit monetary effects.
		_, err = tx.ExecContext(ctx, `UPDATE usage_billing_events SET state='legacy',completed_at=NOW(),result='{}'
			WHERE request_id=$1 AND api_key_id=$2`, key.RequestID, key.APIKeyID)
		if err != nil {
			return nil, err
		}
		return recovered, tx.Commit()
	}
	var existing bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_logs WHERE request_id=$1 AND api_key_id=$2)`, key.RequestID, key.APIKeyID).Scan(&existing); err != nil {
		return nil, err
	}
	if existing {
		// A pre-existing usage row without our committed claim is not authority
		// to back-charge an old failed/zero-cost request automatically.
		return nil, service.ErrUsageBillingRequestConflict
	}
	recovered.Result.Applied = true
	if err = r.applyUsageBillingEffects(ctx, tx, cmd, &recovered.Result); err != nil {
		return nil, err
	}
	if err = insertUsageBillingQuote(ctx, tx, cmd); err != nil {
		return nil, err
	}
	if err = execUsageLogInsertNoResult(ctx, tx, prepareUsageLogInsert(&recovered.Event.Usage)); err != nil {
		return nil, err
	}
	encodedResult, err := json.Marshal(recovered.Result)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE usage_billing_events SET state='applied',result=$3::jsonb,
		applied_at=NOW(),failure_code=NULL,next_attempt_at=NOW() WHERE request_id=$1 AND api_key_id=$2`,
		key.RequestID, key.APIKeyID, string(encodedResult))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err == nil {
		recovered.Settled = true
		recovered.NeedsCache = true
		return recovered, nil
	}
	// Inspect durable evidence before any replay. A later retry takes the row
	// lock again, so even a transaction still resolving here cannot double debit.
	check, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var committedState string
	var committedResult []byte
	checkErr := r.db.QueryRowContext(check, `SELECT state,result FROM usage_billing_events
		WHERE request_id=$1 AND api_key_id=$2`, key.RequestID, key.APIKeyID).Scan(&committedState, &committedResult)
	if checkErr == nil && (committedState == "applied" || committedState == "complete") {
		if checkErr = json.Unmarshal(committedResult, &recovered.Result); checkErr == nil {
			recovered.Settled = recovered.Result.Applied
			recovered.NeedsCache = committedState == "applied"
			return recovered, nil
		}
	}
	return nil, errors.Join(service.ErrUsageBillingOutcomeUnknown, err, checkErr)
}

func (r *usageBillingRepository) PendingUsageBilling(ctx context.Context, limit int) ([]service.UsageBillingEventKey, error) {
	if limit < 1 || limit > 256 {
		return nil, errors.New("invalid usage recovery batch size")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT request_id,api_key_id FROM usage_billing_events
		WHERE state IN ('pending','applied') AND next_attempt_at<=NOW()
		ORDER BY CASE WHEN state='pending' THEN 0 ELSE 1 END,next_attempt_at,staged_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var keys []service.UsageBillingEventKey
	for rows.Next() {
		var key service.UsageBillingEventKey
		if err := rows.Scan(&key.RequestID, &key.APIKeyID); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (r *usageBillingRepository) AcknowledgeUsageBillingCache(ctx context.Context, key service.UsageBillingEventKey) error {
	_, err := r.db.ExecContext(ctx, `UPDATE usage_billing_events SET state='complete',completed_at=NOW(),failure_code=NULL
		WHERE request_id=$1 AND api_key_id=$2 AND state='applied'`, key.RequestID, key.APIKeyID)
	return err
}

func (r *usageBillingRepository) DeferUsageBillingCache(ctx context.Context, key service.UsageBillingEventKey) error {
	_, err := r.db.ExecContext(ctx, `UPDATE usage_billing_events SET next_attempt_at=NOW()+INTERVAL '30 seconds',
		attempts=attempts+1,failure_code='cache_invalidation_failed' WHERE request_id=$1 AND api_key_id=$2 AND state='applied'`, key.RequestID, key.APIKeyID)
	return err
}
