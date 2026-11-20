package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

const oauth429QueueLimit = 10000

func oauth429PolicyAccount(ctx context.Context, tx *sql.Tx, id int64) (service.OAuthAutoConfig, *service.Account, error) {
	cfg := service.DefaultOAuthAutoConfig()
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1 FOR SHARE`, service.SettingKeyOAuthAutoConfig).Scan(&raw); err != nil {
		return cfg, nil, err
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, nil, err
	}
	if !cfg.DegradationAction.Enabled || !cfg.DegradationAction.Independent429.Enabled {
		return cfg, nil, nil
	}
	var credentials, extra []byte
	a := &service.Account{ID: id}
	err := tx.QueryRowContext(ctx, `SELECT name,platform,type,status,schedulable,concurrency,credentials,COALESCE(extra,'{}') FROM accounts
 WHERE id=$1 AND deleted_at IS NULL AND parent_account_id IS NULL FOR UPDATE`, id).Scan(&a.Name, &a.Platform, &a.Type, &a.Status, &a.Schedulable, &a.Concurrency, &credentials, &extra)
	if err != nil {
		return cfg, nil, err
	}
	if err = json.Unmarshal(credentials, &a.Credentials); err != nil {
		return cfg, nil, err
	}
	if err = json.Unmarshal(extra, &a.Extra); err != nil {
		return cfg, nil, err
	}
	return cfg, a, nil
}
func oauth429Eligible(c service.OAuthAutoConfig, a *service.Account, model string) bool {
	return c.Revision != "" && c.DegradationAction.Enabled && c.DegradationAction.UsesUpstream429() && c.DegradationAction.Independent429.Enabled && c.DegradationAction.Validate() == nil && a != nil && a.Type == service.AccountTypeOAuth && a.Schedulable && a.Status == service.StatusActive && a.Concurrency > 0 && !a.IsOAuth429ModelBlocked(model) && a.GetMappedModel(model) == model && a.IsModelSupported(model)
}
func oauth429Save(ctx context.Context, tx *sql.Tx, task *service.OAuth429Task) error {
	raw, err := json.Marshal(task)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE oauth_429_tasks SET state=$3,data=$4,updated_at=clock_timestamp() WHERE account_id=$1 AND model=$2`, task.AccountID, task.Model, task.State, string(raw))
	return err
}
func oauth429Load(ctx context.Context, tx *sql.Tx, id int64, model string) (*service.OAuth429Task, error) {
	var data []byte
	var state string
	err := tx.QueryRowContext(ctx, `SELECT data,state FROM oauth_429_tasks WHERE account_id=$1 AND model=$2 FOR UPDATE`, id, model).Scan(&data, &state)
	if err != nil {
		return nil, err
	}
	var task service.OAuth429Task
	if err = json.Unmarshal(data, &task); err != nil {
		return nil, err
	}
	task.State = state
	return &task, nil
}
func (r *accountRepository) oauth429DB() (*sql.DB, error) {
	db, ok := r.sql.(*sql.DB)
	if !ok {
		return nil, errors.New("oauth429 requires transactional database")
	}
	return db, nil
}

func (r *accountRepository) ObserveOAuth429(ctx context.Context, id int64, model, request string, status int) (bool, error) {
	if id <= 0 || strings.TrimSpace(model) == "" || len(model) > 256 || len(request) != 64 || status < 200 || status > 599 {
		return false, nil
	}
	db, err := r.oauth429DB()
	if err != nil {
		return false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	cfg, a, err := oauth429PolicyAccount(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !oauth429Eligible(cfg, a, model) {
		return false, nil
	}
	// Serializes only the bounded global capacity/budget reservation. No network
	// or test execution occurs under this lock.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(4299281)`); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	task, err := oauth429Load(ctx, tx, id, model)
	if errors.Is(err, sql.ErrNoRows) {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM oauth_429_tasks`).Scan(&count); err != nil {
			return false, err
		}
		if count >= oauth429QueueLimit {
			return false, nil
		}
		task = &service.OAuth429Task{AccountID: id, Model: model, Revision: cfg.Revision, Armed: true, State: "observing"}
		if _, err = tx.ExecContext(ctx, `INSERT INTO oauth_429_tasks(account_id,model,state,data) VALUES($1,$2,'observing','{}')`, id, model); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	if task.Revision != cfg.Revision {
		task.Revision = cfg.Revision
		task.State = "observing"
		task.Armed = true
		task.Samples = nil
		task.Runs = nil
	}
	if task.State == "pending" || task.State == "running" {
		return false, nil
	}
	samples := make([]service.OAuth429Sample, 0, 200)
	duplicate := false
	for _, v := range task.Samples {
		if v.At.Before(now.Add(-time.Hour)) {
			continue
		}
		samples = append(samples, v)
		if v.Request == request {
			duplicate = true
		}
	}
	if !duplicate && len(samples) < 200 {
		samples = append(samples, service.OAuth429Sample{Request: request, Status: status, At: now})
	}
	task.Samples = samples
	failures, limited := 0, 0
	for _, v := range samples {
		if v.Status >= 400 {
			failures++
		}
		if v.Status == 429 {
			limited++
		}
	}
	pressure := len(samples) >= cfg.DegradationAction.Independent429.MinSamples && failures > 0 && limited > 0 && 100*float64(failures)/float64(len(samples)) >= cfg.DegradationAction.HTTPErrorThreshold() && 100*float64(limited)/float64(failures) >= cfg.DegradationAction.HTTP429ShareThreshold()
	// A completed episode stays latched until a measured non-pressure window.
	// Losing samples alone is not a new episode and cannot cause infinite probes.
	if !pressure && len(samples) >= cfg.DegradationAction.Independent429.MinSamples {
		task.Armed = true
	}
	queued := false
	if pressure && task.Armed && !duplicate {
		var reserved int
		if _, err = tx.ExecContext(ctx, `DELETE FROM oauth_429_daily_budget WHERE day < (clock_timestamp() AT TIME ZONE 'UTC')::date-7`); err != nil {
			return false, err
		}
		err = tx.QueryRowContext(ctx, `INSERT INTO oauth_429_daily_budget(day,reserved) VALUES((clock_timestamp() AT TIME ZONE 'UTC')::date,2)
 ON CONFLICT(day) DO UPDATE SET reserved=oauth_429_daily_budget.reserved+2 WHERE oauth_429_daily_budget.reserved+2<=$1 RETURNING reserved`, cfg.DegradationAction.Independent429.DailyTestBudget).Scan(&reserved)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if err == nil {
			task.TriggerID = uuid.NewString()
			task.Generation++
			task.TriggeredAt = now
			task.ExpiresAt = now.Add(10 * time.Minute)
			task.Lease = ""
			task.LeaseUntil = time.Time{}
			task.Runs = nil
			task.State = "pending"
			task.Armed = false
			queued = true
		}
	}
	if err = oauth429Save(ctx, tx, task); err != nil {
		return false, err
	}
	return queued, tx.Commit()
}

func (r *accountRepository) ClaimOAuth429(ctx context.Context) (*service.OAuth429Task, error) {
	db, err := r.oauth429DB()
	if err != nil {
		return nil, err
	}
	// Select without taking task locks; all mutations follow settings/account/task
	// order, also used by cancellation triggers and account administration.
	rows, err := db.QueryContext(ctx, `SELECT account_id,model FROM oauth_429_tasks WHERE state IN ('pending','running') ORDER BY updated_at LIMIT 100`)
	if err != nil {
		return nil, err
	}
	type key struct {
		id    int64
		model string
	}
	var keys []key
	for rows.Next() {
		var k key
		if err = rows.Scan(&k.id, &k.model); err != nil {
			_ = rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		task, err := r.claimOAuth429(ctx, k.id, k.model)
		if err != nil {
			return nil, err
		}
		if task != nil {
			return task, nil
		}
	}
	return nil, nil
}
func (r *accountRepository) claimOAuth429(ctx context.Context, id int64, model string) (*service.OAuth429Task, error) {
	db, err := r.oauth429DB()
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	cfg, a, err := oauth429PolicyAccount(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	task, err := oauth429Load(ctx, tx, id, model)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if task.State != "pending" && task.State != "running" {
		return nil, nil
	}
	now := time.Now().UTC()
	if !oauth429Eligible(cfg, a, model) || task.Revision != cfg.Revision || !now.Before(task.ExpiresAt) {
		task.State = "cancelled"
		if err = oauth429Save(ctx, tx, task); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	if task.State == "running" && now.Before(task.LeaseUntil) {
		return nil, nil
	}
	for i := range task.Runs {
		if task.Runs[i].Verdict == "started" {
			task.Runs[i].Verdict = "inconclusive"
			task.Runs[i].FinishedAt = now
			task.Runs[i].ResultID = uuid.NewString()
		}
	}
	task.Lease = uuid.NewString()
	task.LeaseUntil = now.Add(4 * time.Minute)
	task.State = "running"
	if err = oauth429Save(ctx, tx, task); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return task, nil
}

func (r *accountRepository) mutateOAuth429(ctx context.Context, claimed *service.OAuth429Task, fn func(*sql.Tx, service.OAuthAutoConfig, *service.Account, *service.OAuth429Task) error) error {
	db, err := r.oauth429DB()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	cfg, a, err := oauth429PolicyAccount(ctx, tx, claimed.AccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	task, err := oauth429Load(ctx, tx, claimed.AccountID, claimed.Model)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if task.State != "running" || task.Lease != claimed.Lease || task.TriggerID != claimed.TriggerID || task.Generation != claimed.Generation || !now.Before(task.LeaseUntil) {
		return nil
	}
	if !oauth429Eligible(cfg, a, task.Model) || task.Revision != cfg.Revision || !now.Before(task.ExpiresAt) {
		task.State = "cancelled"
	} else if err = fn(tx, cfg, a, task); err != nil {
		return err
	}
	if err = oauth429Save(ctx, tx, task); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *accountRepository) StartOAuth429Run(ctx context.Context, task *service.OAuth429Task) (*service.OAuth429Run, error) {
	var started *service.OAuth429Run
	err := r.mutateOAuth429(ctx, task, func(_ *sql.Tx, _ service.OAuthAutoConfig, _ *service.Account, current *service.OAuth429Task) error {
		if len(current.Runs) >= 2 {
			return nil
		}
		for _, run := range current.Runs {
			if run.Verdict == "started" {
				return nil
			}
		}
		started = &service.OAuth429Run{ID: uuid.NewString(), StartedAt: time.Now().UTC(), Verdict: "started"}
		current.Runs = append(current.Runs, *started)
		return nil
	})
	return started, err
}
func (r *accountRepository) FinishOAuth429Run(ctx context.Context, task *service.OAuth429Task, result service.OAuth429Run) error {
	return r.mutateOAuth429(ctx, task, func(_ *sql.Tx, _ service.OAuthAutoConfig, _ *service.Account, current *service.OAuth429Task) error {
		if result.ResultID == "" || result.FinishedAt.Before(current.TriggeredAt) || result.FinishedAt.After(current.ExpiresAt) {
			return nil
		}
		if result.Verdict != "correct" && result.Verdict != "incorrect" && result.Verdict != "inconclusive" {
			return nil
		}
		for i, run := range current.Runs {
			if run.ID == result.ID && run.Verdict == "started" && run.StartedAt.Equal(result.StartedAt) {
				current.Runs[i] = result
			}
		}
		return nil
	})
}
func (r *accountRepository) CompleteOAuth429(ctx context.Context, task *service.OAuth429Task) error {
	changed := false
	err := r.mutateOAuth429(ctx, task, func(tx *sql.Tx, cfg service.OAuthAutoConfig, a *service.Account, current *service.OAuth429Task) error {
		if len(current.Runs) != 2 {
			return nil
		}
		for _, run := range current.Runs {
			if run.Verdict == "started" {
				return nil
			}
		}
		current.State = "complete"
		audit := *current
		audit.Samples = nil
		audit.Lease = ""
		audit.LeaseUntil = time.Time{}
		if err := insertAutoConfigEvent(ctx, tx, service.AutoConfigEvent{AccountID: a.ID, AccountName: a.Name, Platform: a.Platform, Kind: service.AutoConfigEventOAuth429, Details: service.AutoConfigEventDetails{OAuth429: &audit, Concurrency: a.Concurrency, PreviousConcurrency: a.Concurrency}}); err != nil {
			return err
		}
		one, two := current.Runs[0], current.Runs[1]
		if one.Verdict != "incorrect" || two.Verdict != "incorrect" || one.ID == two.ID || one.ResultID == "" || one.ResultID == two.ResultID || one.StartedAt.Before(current.TriggeredAt) || two.StartedAt.Before(one.FinishedAt) {
			return nil
		}
		// Reuse only the selected model-restriction action, never account-wide
		// concurrency/group actions. Models intentionally retained by the old policy
		// stay allowed. HTTP confirmation does not expand that action contract.
		if !cfg.DegradationAction.RestrictsModels() {
			return nil
		}
		for _, allowed := range cfg.DegradationAction.Models {
			if allowed == current.Model {
				return nil
			}
		}
		blocks, _ := a.Extra[service.OAuth429ModelBlocksKey].(map[string]any)
		if blocks == nil {
			blocks = map[string]any{}
		}
		blocks[current.Model] = map[string]any{"trigger_id": current.TriggerID, "generation": current.Generation, "revision": current.Revision, "runs": current.Runs, "applied_at": time.Now().UTC()}
		if a.Extra == nil {
			a.Extra = map[string]any{}
		}
		a.Extra[service.OAuth429ModelBlocksKey] = blocks
		raw, err := json.Marshal(a.Extra)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET extra=$2,updated_at=GREATEST(clock_timestamp(),updated_at+INTERVAL '1 microsecond') WHERE id=$1`, a.ID, string(raw)); err != nil {
			return err
		}
		if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &a.ID, nil, nil); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err == nil && changed {
		r.syncSchedulerAccountSnapshotDetached(ctx, task.AccountID)
	}
	return err
}
