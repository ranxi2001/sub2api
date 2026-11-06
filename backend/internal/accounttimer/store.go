package accounttimer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Task struct {
	AccountID   int64      `json:"account_id"`
	Action      string     `json:"action"`
	Version     int64      `json:"version"`
	ExecuteAt   time.Time  `json:"execute_at"`
	Timezone    string     `json:"timezone"`
	LocalDate   string     `json:"local_date"`
	LocalTime   string     `json:"local_time"`
	Offset      string     `json:"offset"`
	RequestedBy int64      `json:"requested_by"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	ActualAt    *time.Time `json:"actual_at"`
	Reason      string     `json:"reason"`
	Recurrence  string     `json:"recurrence"`
	DailyTime   string     `json:"daily_time"`
}
type Audit struct {
	Action     string    `json:"action"`
	Version    int64     `json:"version"`
	ActorID    int64     `json:"actor_id"`
	ExecuteAt  time.Time `json:"execute_at"`
	Timezone   string    `json:"timezone"`
	ActualAt   time.Time `json:"actual_at"`
	Result     string    `json:"result"`
	Reason     string    `json:"reason"`
	Recurrence string    `json:"recurrence"`
	DailyTime  string    `json:"daily_time"`
}
type Snapshot struct {
	Tasks            []Task    `json:"tasks"`
	History          []Audit   `json:"history"`
	ServerNow        time.Time `json:"server_now"`
	PollSeconds      int       `json:"poll_seconds"`
	OpenGraceSeconds int       `json:"open_grace_seconds"`
	MaxDelayMinutes  int64     `json:"max_delay_minutes"`
}
type SaveRequest struct {
	Input
	Action          string `json:"action"`
	ExpectedVersion int64  `json:"expected_version"`
	// Required for fixed times: prove that the caller confirmed the server preview.
	ConfirmedAt *time.Time `json:"confirmed_at"`
}

type DailyWindowRequest struct {
	OpenTime             string `json:"open_time"`
	CloseTime            string `json:"close_time"`
	Timezone             string `json:"timezone"`
	ExpectedOpenVersion  int64  `json:"expected_open_version"`
	ExpectedCloseVersion int64  `json:"expected_close_version"`
}

// DailyWindowRepository is optional so existing one-shot consumers and mocks
// remain source compatible with Repository.
type DailyWindowRepository interface {
	SaveDailyWindow(context.Context, int64, int64, DailyWindowRequest) (Snapshot, error)
}
type Repository interface {
	Preview(Input) (Preview, error)
	List(context.Context, int64) (Snapshot, error)
	Save(context.Context, int64, int64, SaveRequest) (Task, error)
	Cancel(context.Context, int64, int64, string, int64) error
	RunDue(context.Context) error
}
type Store struct {
	DB          *sql.DB
	Now         func() time.Time
	Policy      Policy
	Enqueue     func(context.Context, *sql.Tx, int64) error
	AfterCommit func(context.Context, int64)
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s *Store) policy() Policy {
	if s.Policy.MaxDelay <= 0 {
		return DefaultPolicy()
	}
	return s.Policy
}
func (s *Store) Preview(in Input) (Preview, error) { return Resolve(in, s.now(), s.policy()) }

const taskColumns = "account_id, action, version, execute_at, timezone, requested_by, status, attempts, actual_at, reason, recurrence, daily_time"

func fillLocalTime(t *Task) {
	if loc, err := time.LoadLocation(t.Timezone); err == nil {
		local := t.ExecuteAt.In(loc)
		t.LocalDate = local.Format("2006-01-02")
		t.LocalTime = local.Format("15:04")
		t.Offset = local.Format("-07:00")
	}
}
func actionValid(a string) bool { return a == "open" || a == "close" }

type scanner interface{ Scan(...any) error }

func scanTask(row scanner) (Task, error) {
	var t Task
	err := row.Scan(&t.AccountID, &t.Action, &t.Version, &t.ExecuteAt, &t.Timezone, &t.RequestedBy, &t.Status, &t.Attempts, &t.ActualAt, &t.Reason, &t.Recurrence, &t.DailyTime)
	if err == nil {
		t.ExecuteAt = t.ExecuteAt.UTC()
		fillLocalTime(&t)
		if t.ActualAt != nil {
			utc := t.ActualAt.UTC()
			t.ActualAt = &utc
		}
	}
	return t, err
}

type accountState struct {
	status  string
	expires *time.Time
	deleted *time.Time
}

func lockAccount(ctx context.Context, tx *sql.Tx, id int64, skip bool) (accountState, error) {
	var a accountState
	suffix := " FOR UPDATE"
	if skip {
		suffix += " SKIP LOCKED"
	}
	err := tx.QueryRowContext(ctx, "SELECT status, expires_at, deleted_at FROM accounts WHERE id = $1"+suffix, id).Scan(&a.status, &a.expires, &a.deleted)
	return a, err
}
func audit(ctx context.Context, tx *sql.Tx, t Task, actor int64, now time.Time, result, reason string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO account_schedulable_timer_audit
 (account_id,action,version,actor_id,execute_at,timezone,actual_at,result,reason,recurrence,daily_time) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, t.AccountID, t.Action, t.Version, actor, t.ExecuteAt, t.Timezone, now, result, reason, recurrenceOf(t), t.DailyTime)
	return err
}

func recurrenceOf(t Task) string {
	if t.Recurrence == "daily" {
		return "daily"
	}
	return "once"
}
func (s *Store) Save(ctx context.Context, id, actor int64, req SaveRequest) (Task, error) {
	var zero Task
	if id <= 0 || actor <= 0 || !actionValid(req.Action) || req.ExpectedVersion < 0 {
		return zero, invalid("request")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := lockAccount(ctx, tx, id, false)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && a.deleted != nil) {
		return zero, invalid("account_deleted")
	}
	if err != nil {
		return zero, err
	}
	t, err := s.saveLocked(ctx, tx, id, actor, req, s.now(), false)
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return t, nil
}

// saveLocked requires the account lock and leaves commit/rollback to its caller.
func (s *Store) saveLocked(ctx context.Context, tx *sql.Tx, id, actor int64, req SaveRequest, now time.Time, window bool) (Task, error) {
	var zero Task
	preview, err := Resolve(req.Input, now, s.policy())
	if err != nil {
		return zero, err
	}
	if req.Mode == "absolute" && (req.Date == "" || req.ConfirmedAt == nil || !req.ConfirmedAt.Equal(preview.ExecuteAt)) {
		return zero, invalid("confirmation")
	}
	var version int64
	err = tx.QueryRowContext(ctx, "SELECT version FROM account_schedulable_timers WHERE account_id=$1 AND action=$2 FOR UPDATE", id, req.Action).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	if version != req.ExpectedVersion {
		return zero, invalid("version")
	}
	recurrence, dailyTime := "once", ""
	if req.Mode == "daily" {
		recurrence, dailyTime = "daily", req.Time
	}
	var conflict bool
	// A recurring pair must share one timezone. This avoids a seasonal overlap
	// when zones change UTC offsets on different dates. Check future one-shot
	// conflicts too, rather than checking only the next daily occurrence.
	if !window {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM account_schedulable_timers
 WHERE account_id=$1 AND action<>$2 AND status='pending' AND (
 execute_at=$3 OR
 ($4='daily' AND recurrence='daily' AND (timezone<>$5 OR daily_time=$6)) OR
 ($4='daily' AND recurrence='once' AND execute_at>$7
   AND date_trunc('minute',execute_at)=execute_at AND to_char(execute_at AT TIME ZONE $5,'HH24:MI')=$6) OR
 ($4='once' AND recurrence='daily' AND date_trunc('minute',$3::timestamptz)=$3
   AND to_char($3::timestamptz AT TIME ZONE timezone,'HH24:MI')=daily_time)))`, id, req.Action, preview.ExecuteAt, recurrence, req.Timezone, dailyTime, now).Scan(&conflict)
		if err != nil {
			return zero, err
		}
	}
	if conflict {
		return zero, invalid("conflict")
	}
	t := Task{AccountID: id, Action: req.Action, Version: version + 1, ExecuteAt: preview.ExecuteAt, Timezone: req.Timezone, RequestedBy: actor, Status: "pending", Recurrence: recurrence, DailyTime: dailyTime}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_schedulable_timers
 (account_id,action,version,execute_at,timezone,requested_by,status,attempts,next_attempt_at,actual_at,reason,recurrence,daily_time)
 VALUES ($1,$2,$3,$4,$5,$6,'pending',0,$4,NULL,'',$7,$8)
 ON CONFLICT (account_id,action) DO UPDATE SET version=EXCLUDED.version,execute_at=EXCLUDED.execute_at,
 timezone=EXCLUDED.timezone,requested_by=EXCLUDED.requested_by,status='pending',attempts=0,next_attempt_at=EXCLUDED.execute_at,actual_at=NULL,reason='',
 recurrence=EXCLUDED.recurrence,daily_time=EXCLUDED.daily_time`, id, t.Action, t.Version, t.ExecuteAt, t.Timezone, actor, recurrence, dailyTime)
	if err != nil {
		return zero, err
	}
	fillLocalTime(&t)
	result := "scheduled"
	if version > 0 {
		result = "replaced"
	}
	if err = audit(ctx, tx, t, actor, now, result, ""); err != nil {
		return zero, err
	}
	return t, nil
}

// SaveDailyWindow changes both actions atomically. Replacing a window does not
// immediately change schedulable; the first effect occurs at its next event.
func (s *Store) SaveDailyWindow(ctx context.Context, id, actor int64, req DailyWindowRequest) (Snapshot, error) {
	var zero Snapshot
	if id <= 0 || actor <= 0 || req.ExpectedOpenVersion < 0 || req.ExpectedCloseVersion < 0 {
		return zero, invalid("request")
	}
	now := s.now()
	openReq := SaveRequest{Input: Input{Mode: "daily", Time: req.OpenTime, Timezone: req.Timezone}, Action: "open", ExpectedVersion: req.ExpectedOpenVersion}
	closeReq := SaveRequest{Input: Input{Mode: "daily", Time: req.CloseTime, Timezone: req.Timezone}, Action: "close", ExpectedVersion: req.ExpectedCloseVersion}
	for _, input := range []Input{openReq.Input, closeReq.Input} {
		if _, err := Resolve(input, now, s.policy()); err != nil {
			return zero, err
		}
	}
	if req.OpenTime == req.CloseTime {
		return zero, invalid("conflict")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := lockAccount(ctx, tx, id, false)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && a.deleted != nil) {
		return zero, invalid("account_deleted")
	}
	if err != nil {
		return zero, err
	}
	// Check both optimistic versions before touching either action. All writers
	// hold this same account lock, so neither version can change during the pair.
	for _, action := range []SaveRequest{openReq, closeReq} {
		var version int64
		err = tx.QueryRowContext(ctx, "SELECT version FROM account_schedulable_timers WHERE account_id=$1 AND action=$2 FOR UPDATE", id, action.Action).Scan(&version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return zero, err
		}
		if version != action.ExpectedVersion {
			return zero, invalid("version")
		}
	}
	// The account lock may have waited across a wall-clock boundary. Resolve the
	// actual persisted occurrences from receipt under that lock, not preflight.
	now = s.now()
	// Release the old pending uniqueness slots together. This intermediate state
	// is never visible outside the transaction, including when the times swap.
	if _, err = tx.ExecContext(ctx, "UPDATE account_schedulable_timers SET status='cancelled' WHERE account_id=$1 AND status='pending'", id); err != nil {
		return zero, err
	}
	for _, action := range []SaveRequest{openReq, closeReq} {
		if _, err = s.saveLocked(ctx, tx, id, actor, action, now, true); err != nil {
			return zero, err
		}
	}
	snapshot, err := s.list(ctx, tx, id)
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return snapshot, nil
}
func (s *Store) Cancel(ctx context.Context, id, actor int64, action string, version int64) error {
	if id <= 0 || actor <= 0 || !actionValid(action) || version <= 0 {
		return invalid("request")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = lockAccount(ctx, tx, id, false)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	t, err := scanTask(tx.QueryRowContext(ctx, "SELECT "+taskColumns+" FROM account_schedulable_timers WHERE account_id=$1 AND action=$2 FOR UPDATE", id, action))
	if errors.Is(err, sql.ErrNoRows) {
		return invalid("version")
	}
	if err != nil {
		return err
	}
	if t.Version != version || t.Status != "pending" {
		return invalid("version")
	}
	now := s.now()
	_, err = tx.ExecContext(ctx, `UPDATE account_schedulable_timers SET status='cancelled',version=version+1,actual_at=$3,reason='admin_cancelled' WHERE account_id=$1 AND action=$2`, id, action, now)
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, t, actor, now, "cancelled", "admin_cancelled"); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) List(ctx context.Context, id int64) (Snapshot, error) {
	return s.list(ctx, s.DB, id)
}

type taskQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) list(ctx context.Context, q taskQuerier, id int64) (Snapshot, error) {
	p := s.policy()
	out := Snapshot{Tasks: []Task{}, History: []Audit{}, ServerNow: s.now(), PollSeconds: int(PollInterval / time.Second), OpenGraceSeconds: int(p.OpenGrace / time.Second), MaxDelayMinutes: int64(p.MaxDelay / time.Minute)}
	rows, err := q.QueryContext(ctx, "SELECT "+taskColumns+" FROM account_schedulable_timers WHERE account_id=$1 ORDER BY action", id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		t, e := scanTask(rows)
		if e != nil {
			_ = rows.Close()
			return out, e
		}
		out.Tasks = append(out.Tasks, t)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = q.QueryContext(ctx, `SELECT action,version,actor_id,execute_at,timezone,actual_at,result,reason,recurrence,daily_time FROM account_schedulable_timer_audit WHERE account_id=$1 ORDER BY id DESC LIMIT 20`, id)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var a Audit
		if err = rows.Scan(&a.Action, &a.Version, &a.ActorID, &a.ExecuteAt, &a.Timezone, &a.ActualAt, &a.Result, &a.Reason, &a.Recurrence, &a.DailyTime); err != nil {
			return out, err
		}
		a.ExecuteAt = a.ExecuteAt.UTC()
		a.ActualAt = a.ActualAt.UTC()
		out.History = append(out.History, a)
	}
	return out, rows.Err()
}
func (s *Store) RunDue(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+taskColumns+" FROM account_schedulable_timers WHERE status='pending' AND next_attempt_at <= $1 ORDER BY next_attempt_at,account_id,action LIMIT 50", s.now())
	if err != nil {
		return err
	}
	tasks := []Task{}
	for rows.Next() {
		t, e := scanTask(rows)
		if e != nil {
			_ = rows.Close()
			return e
		}
		tasks = append(tasks, t)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range tasks {
		if e := s.execute(ctx, t); e != nil {
			errs = append(errs, e)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(errs...)
}
func (s *Store) execute(ctx context.Context, candidate Task) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := lockAccount(ctx, tx, candidate.AccountID, true)
	if errors.Is(err, sql.ErrNoRows) {
		// SKIP LOCKED must not mistake a busy account for a deleted one.
		var exists bool
		if e := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1)", candidate.AccountID).Scan(&exists); e != nil {
			return e
		}
		if exists {
			return nil
		}
		deleted := s.now()
		a.deleted = &deleted
	} else if err != nil {
		return err
	}
	now := s.now()
	t, err := scanTask(tx.QueryRowContext(ctx, "SELECT "+taskColumns+` FROM account_schedulable_timers WHERE account_id=$1 AND action=$2 AND next_attempt_at <= $3 AND NOT EXISTS (
 SELECT 1 FROM account_schedulable_timers earlier WHERE earlier.account_id=account_schedulable_timers.account_id
 AND earlier.status='pending' AND earlier.execute_at < account_schedulable_timers.execute_at) FOR UPDATE`, candidate.AccountID, candidate.Action, now))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if t.Version != candidate.Version || t.Status != "pending" || t.ExecuteAt.After(now) {
		return nil
	}
	reason := Decision(t.Action == "open", t.ExecuteAt, now, a.status, a.expires, a.deleted != nil, s.policy())
	result := "executed"
	var next time.Time
	if t.Recurrence == "daily" && reason != "account_deleted" {
		loc, zoneErr := time.LoadLocation(t.Timezone)
		if zoneErr == nil {
			next, zoneErr = nextDaily(t.DailyTime, loc, now)
		}
		if zoneErr != nil {
			// Malformed persisted state cannot enable an account or spin forever.
			reason = "recurrence_invalid"
			result = "failed"
		}
	}
	if reason != "" {
		if result != "failed" {
			result = "skipped"
		}
	} else {
		// Do not touch status, expiry, credentials, quotas, cooldown or quality fields.
		_, err = tx.ExecContext(ctx, "UPDATE accounts SET schedulable=$1, updated_at=$2 WHERE id=$3 AND deleted_at IS NULL", t.Action == "open", now, t.AccountID)
		if err == nil && s.Enqueue != nil {
			err = s.Enqueue(ctx, tx, t.AccountID)
		}
		if err != nil {
			_ = tx.Rollback()
			return s.retry(ctx, t)
		}
	}
	if !next.IsZero() {
		// Audit this occurrence using its original version, while atomically
		// fencing old workers and making the next local date durable.
		_, err = tx.ExecContext(ctx, `UPDATE account_schedulable_timers
 SET status='pending',version=version+1,execute_at=$3,next_attempt_at=$3,attempts=0,actual_at=$4,reason=$5
 WHERE account_id=$1 AND action=$2 AND version=$6`, t.AccountID, t.Action, next, now, reason, t.Version)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE account_schedulable_timers SET status=$3,actual_at=$4,reason=$5 WHERE account_id=$1 AND action=$2 AND version=$6`, t.AccountID, t.Action, result, now, reason, t.Version)
	}
	if err == nil {
		err = audit(ctx, tx, t, t.RequestedBy, now, result, reason)
	}
	if err != nil {
		_ = tx.Rollback()
		return s.retry(ctx, t)
	}
	if err = tx.Commit(); err != nil {
		return err
	} // Ambiguous commit: reread pending state next poll, never replay blindly.
	if result == "executed" && s.AfterCommit != nil {
		s.AfterCommit(ctx, t.AccountID)
	}
	return nil
}
func (s *Store) retry(ctx context.Context, t Task) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = lockAccount(ctx, tx, t.AccountID, false)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := s.now()
	attempts := t.Attempts + 1
	status := "pending"
	if attempts >= 3 {
		status = "failed"
	}
	result, err := tx.ExecContext(ctx, `UPDATE account_schedulable_timers SET attempts=$4,status=$5,next_attempt_at=$6,actual_at=$7,reason='execution_error'
 WHERE account_id=$1 AND action=$2 AND version=$3 AND status='pending' AND attempts=$8`, t.AccountID, t.Action, t.Version, attempts, status, now.Add(time.Duration(1<<uint(attempts-1))*PollInterval), now, t.Attempts)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	if err = audit(ctx, tx, t, t.RequestedBy, now, status, "execution_error"); err != nil {
		return err
	}
	return tx.Commit()
}

type manualKey struct{}

func WithManualIntent(ctx context.Context, actor int64) context.Context {
	return context.WithValue(ctx, manualKey{}, actor)
}
func ManualActor(ctx context.Context) int64 { v, _ := ctx.Value(manualKey{}).(int64); return v }

type Executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// CancelPending must run AFTER locking the account and INSIDE the account update
// transaction. Only explicit administrator schedulable edits set this context.
func CancelPending(ctx context.Context, exec Executor, id int64, now time.Time) error {
	actor := ManualActor(ctx)
	if actor <= 0 {
		return nil
	}
	if scope, ok := ctx.Value(manualAccountKey{}).(int64); ok && scope != id {
		return nil
	}
	_, err := exec.ExecContext(ctx, `WITH cancelled AS (
 UPDATE account_schedulable_timers SET status='cancelled',version=version+1,actual_at=$3,reason='manual_override'
 WHERE account_id=$1 AND status='pending' RETURNING *)
 INSERT INTO account_schedulable_timer_audit (account_id,action,version,actor_id,execute_at,timezone,actual_at,result,reason,recurrence,daily_time)
 SELECT account_id,action,version-1,$2,execute_at,timezone,$3,'cancelled','manual_override',recurrence,daily_time FROM cancelled`, id, actor, now)
	return err
}

// Manual atomically cancels both actions and applies the immediate switch.
func (s *Store) Manual(ctx context.Context, id, actor int64, open bool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if id <= 0 || actor <= 0 {
		return invalid("request")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := lockAccount(ctx, tx, id, false)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && a.deleted != nil) {
		return invalid("account_deleted")
	}
	if err != nil {
		return err
	}
	now := s.now()
	if err = CancelPending(WithManualIntent(ctx, actor), tx, id, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE accounts SET schedulable=$1, updated_at=$2 WHERE id=$3 AND deleted_at IS NULL", open, now, id)
	if err == nil && s.Enqueue != nil {
		err = s.Enqueue(ctx, tx, id)
	}
	if err != nil {
		return fmt.Errorf("manual schedulable transaction: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if s.AfterCommit != nil {
		s.AfterCommit(ctx, id)
	}
	return nil
}

type manualAccountKey struct{}

func WithManualAccount(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, manualAccountKey{}, id)
}
func WithoutManualIntent(ctx context.Context) context.Context { return WithManualIntent(ctx, 0) }

type preserveKey struct{}

func WithPreservedSchedulable(ctx context.Context) context.Context {
	return context.WithValue(ctx, preserveKey{}, true)
}
func PreserveSchedulable(ctx context.Context) bool { v, _ := ctx.Value(preserveKey{}).(bool); return v }
