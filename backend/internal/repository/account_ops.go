package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

type accountOpsRepository struct{ db *sql.DB }

func accountOpsEventGroupID(e *service.AccountOpsEvent) string {
	if e != nil && e.GroupID != "" {
		return e.GroupID
	}
	if e == nil {
		return ""
	}
	return "account:" + strconv.FormatInt(e.AccountID, 10)
}

func NewAccountOpsRepository(db *sql.DB) service.AccountOpsRepository {
	return &accountOpsRepository{db: db}
}
func (r *accountOpsRepository) Record(ctx context.Context, e service.AccountOpsEvent) error {
	details, err := json.Marshal(e.Details)
	if err != nil {
		return err
	}
	groupID := accountOpsEventGroupID(&e)
	_, err = r.db.ExecContext(ctx, `INSERT INTO account_ops_alerts(account_id,group_id,group_name,kind,account_name,signal,http_status,details,identity)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9) ON CONFLICT(group_id,kind) DO UPDATE SET
 account_id=EXCLUDED.account_id,group_name=EXCLUDED.group_name,account_name=EXCLUDED.account_name,signal=EXCLUDED.signal,http_status=EXCLUDED.http_status,details=EXCLUDED.details,identity=CASE WHEN account_ops_alerts.state IN ('sending','pending') OR (account_ops_alerts.state='failed' AND (account_ops_alerts.attempts<3 OR account_ops_alerts.next_send_at>NOW())) THEN account_ops_alerts.identity ELSE EXCLUDED.identity END,last_seen=NOW(),occurrences=account_ops_alerts.occurrences+1,
 state=CASE WHEN account_ops_alerts.state IN ('sent','suppressed','failed','resolved') AND account_ops_alerts.next_send_at<=NOW() AND (account_ops_alerts.state<>'failed' OR account_ops_alerts.attempts>=3) THEN 'pending' ELSE account_ops_alerts.state END,
 deliveries=CASE WHEN account_ops_alerts.state IN ('sent','suppressed','failed','resolved') AND account_ops_alerts.next_send_at<=NOW() AND (account_ops_alerts.state<>'failed' OR account_ops_alerts.attempts>=3) THEN '{}'::jsonb ELSE account_ops_alerts.deliveries END,
 attempts=CASE WHEN account_ops_alerts.state IN ('sent','suppressed','failed','resolved') AND account_ops_alerts.next_send_at<=NOW() AND (account_ops_alerts.state<>'failed' OR account_ops_alerts.attempts>=3) THEN 0 ELSE account_ops_alerts.attempts END`, e.AccountID, groupID, e.GroupName, e.Kind, e.AccountName, e.Signal, e.HTTPStatus, string(details), e.Identity)
	return err
}

const accountOpsColumns = `account_id,group_id,group_name,kind,account_name,signal,http_status,first_seen,last_seen,occurrences,state,last_sent_at,next_send_at,attempts,lease,details,deliveries,identity`

func scanAccountOps(row scannable) (*service.AccountOpsEvent, error) {
	var e service.AccountOpsEvent
	var details, deliveries []byte
	err := row.Scan(&e.AccountID, &e.GroupID, &e.GroupName, &e.Kind, &e.AccountName, &e.Signal, &e.HTTPStatus, &e.FirstSeen, &e.LastSeen, &e.Occurrences, &e.State, &e.LastSentAt, &e.NextSendAt, &e.Attempts, &e.Lease, &details, &deliveries, &e.Identity)
	if err == nil {
		if err = json.Unmarshal(details, &e.Details); err == nil {
			err = json.Unmarshal(deliveries, &e.Deliveries)
		}
	}
	return &e, err
}
func (r *accountOpsRepository) Claim(ctx context.Context) (*service.AccountOpsEvent, error) {
	if e, err := r.claimTransition(ctx); err != nil || e != nil {
		return e, err
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE account_ops_alerts SET state='failed',lease='',lease_until=NULL,next_send_at=NOW()+INTERVAL '1 hour' WHERE state='sending' AND lease_until<NOW() AND attempts>=3`); err != nil {
		return nil, err
	}
	e, err := scanAccountOps(r.db.QueryRowContext(ctx, `UPDATE account_ops_alerts SET state='sending',attempts=attempts+1,lease=$1,lease_until=NOW()+INTERVAL '2 minutes'
 WHERE (group_id,kind)=(SELECT group_id,kind FROM account_ops_alerts WHERE kind IN ('balance_low','weekly_quota') AND
 ((state IN ('pending','failed') AND attempts<3 AND next_send_at<=NOW()) OR (state='sending' AND lease_until<NOW()))
 ORDER BY next_send_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING `+accountOpsColumns, uuid.NewString()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}
func (r *accountOpsRepository) Complete(ctx context.Context, e *service.AccountOpsEvent, state string, delay time.Duration) error {
	if e.Phase != "" {
		return r.completeTransition(ctx, e, state, delay)
	}
	_, err := r.db.ExecContext(ctx, `UPDATE account_ops_alerts SET state=$4,lease='',lease_until=NULL,next_send_at=CASE WHEN kind IN ('balance_threshold','quota_threshold') AND $4 IN ('suppressed','resolved') THEN CASE WHEN GREATEST(last_sent_at,(SELECT MAX((x->>'last_sent_at')::timestamptz) FROM jsonb_each(deliveries) j(k,x))) IS NULL THEN NOW() ELSE GREATEST(next_send_at,GREATEST(last_sent_at,(SELECT MAX((x->>'last_sent_at')::timestamptz) FROM jsonb_each(deliveries) j(k,x)))+($5*INTERVAL '1 second')) END ELSE NOW()+($5*INTERVAL '1 second') END,last_sent_at=CASE WHEN $4='sent' THEN NOW() ELSE last_sent_at END
 WHERE group_id=$1 AND kind=$2 AND lease=$3 AND state='sending' AND lease_until>NOW()`, accountOpsEventGroupID(e), e.Kind, e.Lease, state, int64(delay/time.Second))
	return err
}

type opsConfigExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (r *accountOpsRepository) SuppressDisabled(ctx context.Context, c service.AccountOpsConfig) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = r.suppressDisabled(ctx, tx, c); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *accountOpsRepository) suppressDisabled(ctx context.Context, exec opsConfigExecutor, c service.AccountOpsConfig) error {
	if err := r.suppressTransitions(ctx, exec, c); err != nil {
		return err
	}
	balance, _ := json.Marshal(c.BalanceThresholds)
	quota, _ := json.Marshal(c.QuotaThresholds)
	_, err := exec.ExecContext(ctx, `UPDATE account_ops_alerts SET state='suppressed',lease='',lease_until=NULL,next_send_at=CASE WHEN kind IN ('balance_threshold','quota_threshold') THEN CASE WHEN GREATEST(last_sent_at,(SELECT MAX((x->>'last_sent_at')::timestamptz) FROM jsonb_each(deliveries) j(k,x))) IS NULL OR (state IN ('pending','failed','sending') AND GREATEST(last_sent_at,(SELECT MAX((x->>'last_sent_at')::timestamptz) FROM jsonb_each(deliveries) j(k,x)))+($6*INTERVAL '1 second')<=NOW()) THEN NOW() ELSE GREATEST(next_send_at,GREATEST(last_sent_at,(SELECT MAX((x->>'last_sent_at')::timestamptz) FROM jsonb_each(deliveries) j(k,x)))+($6*INTERVAL '1 second')) END ELSE next_send_at END
 WHERE state IN ('pending','failed','sending') AND (NOT $1 OR (kind='balance_low' AND NOT $2) OR (kind='weekly_quota' AND NOT $3)
 OR (kind='balance_threshold' AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(NULLIF($4::jsonb,'null'::jsonb),'[]'::jsonb)) x WHERE (x->>'account_id')::bigint=account_ops_alerts.account_id AND (x->>'enabled')::boolean))
 OR (kind='quota_threshold' AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(NULLIF($5::jsonb,'null'::jsonb),'[]'::jsonb)) x WHERE (x->>'account_id')::bigint=account_ops_alerts.account_id AND (x->>'enabled')::boolean)))`, c.Enabled, c.BalanceLow, c.WeeklyQuota, string(balance), string(quota), int64(c.CooldownMinutes)*60)
	return err
}

func (r *accountOpsRepository) List(ctx context.Context, offset, limit int) ([]service.AccountOpsEvent, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+opsTransitionColumns+` FROM (SELECT 'legacy:'||account_id||':'||kind AS id,''::text AS episode_id,''::text AS phase,NULL::boolean AS notification_enabled,`+accountOpsColumns+`,''::text AS criteria FROM account_ops_alerts UNION ALL SELECT `+opsTransitionColumns+` FROM account_ops_threshold_events) history ORDER BY last_seen DESC,id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]service.AccountOpsEvent, 0)
	for rows.Next() {
		e, err := scanOpsTransition(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *e)
	}
	return items, rows.Err()
}

// Progress is durable before another channel is attempted. Expired or invalidated leases cannot overwrite outcomes.
func (r *accountOpsRepository) SaveDelivery(ctx context.Context, e *service.AccountOpsEvent, key string, out service.AccountOpsDelivery) (bool, error) {
	if e.Phase != "" {
		return r.saveTransitionDelivery(ctx, e, key, out)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return false, err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE account_ops_alerts SET deliveries=jsonb_set(deliveries,ARRAY[$4]::text[],COALESCE(deliveries->$4,'{}'::jsonb)||$5::jsonb,true) WHERE group_id=$1 AND kind=$2 AND lease=$3 AND state='sending' AND lease_until>NOW()`, accountOpsEventGroupID(e), e.Kind, e.Lease, key, string(raw))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (r *accountOpsRepository) ResolveThreshold(ctx context.Context, id int64, kind string, cooldown time.Duration) error {
	_, err := r.db.ExecContext(ctx, `UPDATE account_ops_alerts SET state='resolved',lease='',lease_until=NULL,next_send_at=CASE WHEN GREATEST(last_sent_at,(SELECT MAX((x->>'last_sent_at')::timestamptz) FROM jsonb_each(deliveries) j(k,x))) IS NULL OR (state IN ('pending','failed','sending') AND GREATEST(last_sent_at,(SELECT MAX((x->>'last_sent_at')::timestamptz) FROM jsonb_each(deliveries) j(k,x)))+($3*INTERVAL '1 second')<=NOW()) THEN NOW() ELSE GREATEST(next_send_at,GREATEST(last_sent_at,(SELECT MAX((x->>'last_sent_at')::timestamptz) FROM jsonb_each(deliveries) j(k,x)))+($3*INTERVAL '1 second')) END WHERE account_id=$1 AND kind=$2 AND state<>'resolved'`, id, kind, int64(cooldown/time.Second))
	return err
}

func (r *accountOpsRepository) DeliveryLeaseValid(ctx context.Context, e *service.AccountOpsEvent) (bool, error) {
	var valid bool
	if e.Phase != "" {
		err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_ops_threshold_events WHERE id=$1 AND lease=$2 AND state='sending' AND lease_until>NOW())`, e.ID, e.Lease).Scan(&valid)
		return valid, err
	}
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_ops_alerts WHERE group_id=$1 AND kind=$2 AND lease=$3 AND state='sending' AND lease_until>NOW())`, accountOpsEventGroupID(e), e.Kind, e.Lease).Scan(&valid)
	return valid, err
}

// ReserveRobotDelivery coordinates the provider ceiling across replicas using
// the existing queue. Private hash/timestamp fields are stripped by typed DTOs.
func (r *accountOpsRepository) ReserveRobotDelivery(ctx context.Context, e *service.AccountOpsEvent, key, provider, identity string) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, identity); err != nil {
		return false, err
	}
	var owned bool
	if e.Phase != "" {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_ops_threshold_events WHERE id=$1 AND lease=$2 AND state='sending' AND lease_until>clock_timestamp())`, e.ID, e.Lease).Scan(&owned)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_ops_alerts WHERE group_id=$1 AND kind=$2 AND lease=$3 AND state='sending' AND lease_until>clock_timestamp())`, accountOpsEventGroupID(e), e.Kind, e.Lease).Scan(&owned)
	}
	if err != nil || !owned {
		return false, err
	}
	if err = reserveOpsRobotSlot(ctx, tx, identity); err != nil {
		return false, err
	}
	var result sql.Result
	if e.Phase != "" {
		result, err = tx.ExecContext(ctx, `UPDATE account_ops_threshold_events SET deliveries=jsonb_set(deliveries,ARRAY[$3]::text[],COALESCE(deliveries->$3,'{}'::jsonb)||jsonb_build_object('provider',$4::text,'status','failed','attempts',$6::integer,'_robot_hash',$5::text,'_attempted_at',clock_timestamp()),true) WHERE id=$1 AND lease=$2 AND state='sending' AND lease_until>clock_timestamp()`, e.ID, e.Lease, key, provider, identity, e.Deliveries[key].Attempts+1)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE account_ops_alerts SET deliveries=jsonb_set(deliveries,ARRAY[$4]::text[],COALESCE(deliveries->$4,'{}'::jsonb)||jsonb_build_object('provider',$5::text,'status','failed','attempts',$7::integer,'_robot_hash',$6::text,'_attempted_at',clock_timestamp()),true) WHERE group_id=$1 AND kind=$2 AND lease=$3 AND state='sending' AND lease_until>clock_timestamp()`, accountOpsEventGroupID(e), e.Kind, e.Lease, key, provider, identity, e.Deliveries[key].Attempts+1)
	}
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// Saved-robot tests use the same advisory lock and timestamp as normal delivery,
// while leaving alert history untouched. Settings store only an opaque hash and time.
func (r *accountOpsRepository) ReserveRobotTest(ctx context.Context, identity string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, identity); err != nil {
		return err
	}
	if err = reserveOpsRobotSlot(ctx, tx, identity); err != nil {
		return err
	}
	return tx.Commit()
}
func reserveOpsRobotSlot(ctx context.Context, tx *sql.Tx, identity string) error {
	key := "account_ops_robot_slot:" + identity
	var last time.Time
	err := tx.QueryRowContext(ctx, `SELECT value::timestamptz FROM settings WHERE key=$1`, key).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		wait := time.Until(last.Add(3100 * time.Millisecond))
		if wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,clock_timestamp()::text,clock_timestamp()) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=EXCLUDED.updated_at`, key)
	return err
}

func (r *accountOpsRepository) SuppressThreshold(ctx context.Context, id int64, kind string, cooldown time.Duration) error {
	_, err := r.db.ExecContext(ctx, `UPDATE account_ops_alerts SET state='suppressed',lease='',lease_until=NULL,next_send_at=CASE WHEN GREATEST(last_sent_at,(SELECT MAX((x->>'last_sent_at')::timestamptz) FROM jsonb_each(deliveries) j(k,x))) IS NULL OR (state IN ('pending','failed','sending') AND GREATEST(last_sent_at,(SELECT MAX((x->>'last_sent_at')::timestamptz) FROM jsonb_each(deliveries) j(k,x)))+($3*INTERVAL '1 second')<=NOW()) THEN NOW() ELSE GREATEST(next_send_at,GREATEST(last_sent_at,(SELECT MAX((x->>'last_sent_at')::timestamptz) FROM jsonb_each(deliveries) j(k,x)))+($3*INTERVAL '1 second')) END WHERE account_id=$1 AND kind=$2 AND state IN ('pending','failed','sending')`, id, kind, int64(cooldown/time.Second))
	return err
}
