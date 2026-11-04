package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

type autoCreditRepository struct{ db *sql.DB }

func NewAutoCreditRepository(db *sql.DB) service.AutoCreditRepository {
	return &autoCreditRepository{db: db}
}

func advanceAutoCreditAfterDebit(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, result *service.UsageBillingApplyResult) error {
	balance, status, err := lockAutoCreditUser(ctx, tx, cmd.UserID)
	if err != nil {
		return err
	}
	var epoch int64
	err = tx.QueryRowContext(ctx, `UPDATE auto_credit_config SET debit_epoch=debit_epoch+1,trigger_identity=$2 WHERE user_id=$1 RETURNING debit_epoch`, cmd.UserID, fmt.Sprintf("usage:%d:%s", cmd.APIKeyID, cmd.RequestID)).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	credit, err := evaluateAutoCredit(ctx, tx, cmd.UserID, balance, status)
	if err != nil {
		return err
	}
	if credit.Issued {
		// Legacy notification/cache DTO only. All ledger arithmetic stayed in SQL NUMERIC.
		n, err := strconv.ParseFloat(credit.Balance, 64)
		if err != nil {
			return err
		}
		result.NewBalance = &n
		result.AutoCreditIssued = true
	}
	return nil
}

// The users row is the sole lock ordering root. No process-local mutex.
func lockAutoCreditUser(ctx context.Context, tx *sql.Tx, uid int64) (string, string, error) {
	var balance, status string
	err := tx.QueryRowContext(ctx, `SELECT balance::text,status FROM users WHERE id=$1 AND deleted_at IS NULL FOR NO KEY UPDATE`, uid).Scan(&balance, &status)
	if errors.Is(err, sql.ErrNoRows) {
		err = service.ErrUserNotFound
	}
	return balance, status, err
}

type creditQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readAutoCredit(ctx context.Context, q creditQuerier, uid int64, status string, cursor ...int64) (*service.AutoCreditState, error) {
	if status != "all" && status != "pending" && status != "settled" {
		return nil, service.ErrAutoCreditInvalid
	}
	before := int64(0)
	if len(cursor) > 0 {
		before = cursor[0]
	}
	if before < 0 {
		return nil, service.ErrAutoCreditInvalid
	}
	s := &service.AutoCreditState{Entries: []service.AutoCreditEntry{}}
	err := q.QueryRowContext(ctx, `SELECT enabled,threshold::text,amount::text,cap::text,version FROM auto_credit_config WHERE user_id=$1`, uid).Scan(&s.Config.Enabled, &s.Config.Threshold, &s.Config.Amount, &s.Config.Cap, &s.Config.Version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount),0)::numeric(20,8)::text FROM auto_credit_ledger WHERE user_id=$1 AND status='pending'`, uid).Scan(&s.Outstanding); err != nil {
		return nil, err
	}
	if s.Config.Enabled && s.Config.Amount != nil && s.Config.Cap != nil {
		s.CapBlocked = decimal.RequireFromString(s.Outstanding).Add(decimal.RequireFromString(*s.Config.Amount)).GreaterThan(decimal.RequireFromString(*s.Config.Cap))
	}
	rows, err := q.QueryContext(ctx, `SELECT id,user_id,epoch,source,trigger_identity,amount::text,balance_before::text,balance_after::text,note,status,created_at,settled_at,settled_by FROM auto_credit_ledger WHERE user_id=$1 AND ($2='all' OR status=$2) AND ($3::bigint=0 OR id<$3) ORDER BY id DESC LIMIT 51`, uid, status, before)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var e service.AutoCreditEntry
		if err = rows.Scan(&e.ID, &e.UserID, &e.Epoch, &e.Source, &e.Trigger, &e.Amount, &e.BalanceBefore, &e.BalanceAfter, &e.Note, &e.Status, &e.CreatedAt, &e.SettledAt, &e.SettledBy); err != nil {
			return nil, err
		}
		s.Entries = append(s.Entries, e)
	}
	if len(s.Entries) > 50 {
		s.HasMore = true
		s.Entries = s.Entries[:50]
		s.NextBeforeID = s.Entries[len(s.Entries)-1].ID
	}
	return s, rows.Err()
}
func (r *autoCreditRepository) Get(ctx context.Context, uid int64, status string) (*service.AutoCreditState, error) {
	return r.GetPage(ctx, uid, status, 0)
}
func (r *autoCreditRepository) GetPage(ctx context.Context, uid int64, status string, before int64) (*service.AutoCreditState, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND deleted_at IS NULL)`, uid).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, service.ErrUserNotFound
	}
	s, err := readAutoCredit(ctx, tx, uid, status, before)
	if err != nil {
		return nil, err
	}
	return s, tx.Commit()
}

var creditMoneyPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,6})(\.[0-9]{1,8})?$`)

func patchCreditMoney(raw json.RawMessage, current *string) (*string, error) {
	if len(raw) == 0 {
		return current, nil
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var v string
	if json.Unmarshal(raw, &v) != nil || !creditMoneyPattern.MatchString(v) {
		return nil, service.ErrAutoCreditInvalid
	}
	d, err := decimal.NewFromString(v)
	if err != nil || d.GreaterThan(decimal.NewFromInt(1000000)) || !d.IsPositive() {
		return nil, service.ErrAutoCreditInvalid
	}
	v = d.StringFixed(8)
	return &v, nil
}
func (r *autoCreditRepository) Save(ctx context.Context, uid, actor int64, p service.AutoCreditPatch) (*service.AutoCreditState, error) {
	if actor <= 0 || p.Version < 0 {
		return nil, service.ErrAutoCreditInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, _, err = lockAutoCreditUser(ctx, tx, uid); err != nil {
		return nil, err
	}
	s, err := readAutoCredit(ctx, tx, uid, "all")
	if err != nil {
		return nil, err
	}
	c := s.Config
	if p.Version != c.Version {
		return nil, service.ErrAutoCreditConflict
	}
	if p.Enabled != nil {
		c.Enabled = *p.Enabled
	}
	if c.Threshold, err = patchCreditMoney(p.Threshold, c.Threshold); err != nil {
		return nil, err
	}
	if c.Amount, err = patchCreditMoney(p.Amount, c.Amount); err != nil {
		return nil, err
	}
	if c.Cap, err = patchCreditMoney(p.Cap, c.Cap); err != nil {
		return nil, err
	}
	if c.Enabled && (c.Threshold == nil || c.Amount == nil || c.Cap == nil || decimal.RequireFromString(*c.Amount).GreaterThan(decimal.RequireFromString(*c.Cap))) {
		return nil, service.ErrAutoCreditInvalid
	}
	c.Version++
	_, err = tx.ExecContext(ctx, `INSERT INTO auto_credit_config(user_id,enabled,threshold,amount,cap,version) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(user_id) DO UPDATE SET enabled=$2,threshold=$3,amount=$4,cap=$5,version=$6`, uid, c.Enabled, c.Threshold, c.Amount, c.Cap, c.Version)
	if err != nil {
		return nil, err
	}
	detail, err := json.Marshal(struct {
		Before service.AutoCreditConfig `json:"before"`
		After  service.AutoCreditConfig `json:"after"`
	}{s.Config, c})
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO auto_credit_audit(user_id,actor_id,action,detail) VALUES($1,$2,'configure',$3::jsonb)`, uid, actor, string(detail))
	if err != nil {
		return nil, err
	}
	state, err := readAutoCredit(ctx, tx, uid, "all")
	if err != nil {
		return nil, err
	}
	return state, tx.Commit()
}

// Caller must hold the users row lock. Shared with atomic usage settlement.
func evaluateAutoCredit(ctx context.Context, tx *sql.Tx, uid int64, balance, status string) (*service.AutoCreditResult, error) {
	result := &service.AutoCreditResult{Balance: balance}
	if status != "active" {
		return result, nil
	}
	var epoch int64
	var amount, trigger string
	err := tx.QueryRowContext(ctx, `SELECT c.debit_epoch,c.amount::text,c.trigger_identity FROM auto_credit_config c WHERE c.user_id=$1 AND c.enabled AND $2::numeric<c.threshold AND NOT EXISTS(SELECT 1 FROM auto_credit_ledger l WHERE l.user_id=c.user_id AND l.epoch=c.debit_epoch) AND COALESCE((SELECT SUM(l.amount) FROM auto_credit_ledger l WHERE l.user_id=c.user_id AND l.status='pending'),0)+c.amount<=c.cap`, uid, balance).Scan(&epoch, &amount, &trigger)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	var after string
	if err = tx.QueryRowContext(ctx, `UPDATE users SET balance=balance+$2::numeric,updated_at=NOW() WHERE id=$1 RETURNING balance::text`, uid, amount).Scan(&after); err != nil {
		return nil, err
	}
	var lid int64
	if err = tx.QueryRowContext(ctx, `INSERT INTO auto_credit_ledger(user_id,epoch,trigger_identity,amount,balance_before,balance_after) VALUES($1,$2,$3,$4::numeric,$5::numeric,$6::numeric) RETURNING id`, uid, epoch, trigger, amount, balance, after).Scan(&lid); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO auto_credit_audit(user_id,ledger_id,action,detail) VALUES($1,$2,'issue',jsonb_build_object('epoch',$3::bigint,'amount',$4::text,'balance_before',$5::text,'balance_after',$6::text))`, uid, lid, epoch, amount, balance, after); err != nil {
		return nil, err
	}
	result.Issued = true
	result.Balance = after
	if _, err = tx.ExecContext(ctx, `INSERT INTO auto_credit_cache_outbox(id,user_id) VALUES($1,$2)`, lid, uid); err != nil {
		return nil, err
	}
	// Existing worker delivers credential-free, cross-instance auth evictions.
	if _, err = tx.ExecContext(ctx, `INSERT INTO auth_cache_invalidation_outbox(cache_key) SELECT encode(sha256(convert_to(key,'UTF8')),'hex') FROM api_keys WHERE user_id=$1 AND deleted_at IS NULL AND key<>''`, uid); err != nil {
		return nil, err
	}
	return result, nil
}
func (r *autoCreditRepository) Evaluate(ctx context.Context, uid int64) (*service.AutoCreditResult, error) {
	return r.evaluate(ctx, uid, 0)
}
func (r *autoCreditRepository) EvaluateAdmission(ctx context.Context, uid, keyID int64) (*service.AutoCreditResult, error) {
	if keyID <= 0 {
		return nil, service.ErrAutoCreditInvalid
	}
	return r.evaluate(ctx, uid, keyID)
}
func (r *autoCreditRepository) evaluate(ctx context.Context, uid, keyID int64) (*service.AutoCreditResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}

	defer func() { _ = tx.Rollback() }()
	balance, status, err := lockAutoCreditUser(ctx, tx, uid)
	if err != nil {
		return nil, err
	}
	if keyID > 0 {
		var allowed bool
		err = tx.QueryRowContext(ctx, `SELECT status='active' AND (expires_at IS NULL OR expires_at>NOW()) AND (quota<=0 OR quota_used<quota) FROM api_keys WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL FOR UPDATE`, keyID, uid).Scan(&allowed)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrAPIKeyNotFound
		}
		if err != nil {
			return nil, err
		}
		if !allowed {
			return &service.AutoCreditResult{Balance: balance}, tx.Commit()
		}
	}
	result, err := evaluateAutoCredit(ctx, tx, uid, balance, status)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

func (r *autoCreditRepository) PendingInvalidations(ctx context.Context) ([]service.AutoCreditInvalidation, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,user_id,stage FROM auto_credit_cache_outbox WHERE available_at<=NOW() ORDER BY id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	events := []service.AutoCreditInvalidation{}
	for rows.Next() {
		var e service.AutoCreditInvalidation
		if err = rows.Scan(&e.ID, &e.UserID, &e.Stage); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
func (r *autoCreditRepository) AcknowledgeInvalidation(ctx context.Context, e service.AutoCreditInvalidation) error {
	if e.Stage == 0 {
		_, err := r.db.ExecContext(ctx, `UPDATE auto_credit_cache_outbox SET stage=1,available_at=NOW()+INTERVAL '5 seconds' WHERE id=$1 AND stage=0`, e.ID)
		return err
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM auto_credit_cache_outbox WHERE id=$1 AND stage=1`, e.ID)
	return err
}
func (r *autoCreditRepository) Settle(ctx context.Context, uid, lid, actor int64) (*service.AutoCreditState, error) {
	if actor <= 0 || lid <= 0 {
		return nil, service.ErrAutoCreditInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, _, err = lockAutoCreditUser(ctx, tx, uid); err != nil {
		return nil, err
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM auto_credit_ledger WHERE id=$1 AND user_id=$2 FOR UPDATE`, lid, uid).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrAutoCreditInvalid
	}
	if err != nil {
		return nil, err
	}
	if state == "pending" {
		if _, err = tx.ExecContext(ctx, `UPDATE auto_credit_ledger SET status='settled',settled_at=NOW(),settled_by=$2 WHERE id=$1`, lid, actor); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO auto_credit_audit(user_id,actor_id,ledger_id,action,detail) VALUES($1,$2,$3,'settle','{"full_settlement":true}')`, uid, actor, lid); err != nil {
			return nil, err
		}
	}
	s, err := readAutoCredit(ctx, tx, uid, "all")
	if err != nil {
		return nil, err
	}
	return s, tx.Commit()
}

func (r *autoCreditRepository) OutstandingByUsers(ctx context.Context, ids []int64) (map[int64]string, error) {
	result := map[int64]string{}
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT user_id,SUM(amount)::numeric(20,8)::text FROM auto_credit_ledger WHERE user_id=ANY($1::bigint[]) AND status='pending' GROUP BY user_id`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var uid int64
		var amount string
		if err = rows.Scan(&uid, &amount); err != nil {
			return nil, err
		}
		result[uid] = amount
	}
	return result, rows.Err()
}

func (r *autoCreditRepository) CanRecover(ctx context.Context, uid int64, minimum string) (bool, error) {
	var allowed bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users u LEFT JOIN auto_credit_config c ON c.user_id=u.id WHERE u.id=$1 AND u.status='active' AND u.deleted_at IS NULL AND ((u.balance>0 AND u.balance >= $2::numeric) OR (c.enabled AND u.balance+c.amount>0 AND u.balance+c.amount >= $2::numeric AND u.balance<c.threshold AND NOT EXISTS(SELECT 1 FROM auto_credit_ledger l WHERE l.user_id=u.id AND l.epoch=c.debit_epoch) AND COALESCE((SELECT SUM(l.amount) FROM auto_credit_ledger l WHERE l.user_id=u.id AND l.status='pending'),0)+c.amount<=c.cap)))`, uid, minimum).Scan(&allowed)
	return allowed, err
}
