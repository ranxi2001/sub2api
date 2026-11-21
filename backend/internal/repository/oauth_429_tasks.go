package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
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
	err := tx.QueryRowContext(ctx, `SELECT name,platform,type,status,schedulable,concurrency,expires_at,auto_pause_on_expired,credentials,COALESCE(extra,'{}') FROM accounts
 WHERE id=$1 AND deleted_at IS NULL AND parent_account_id IS NULL FOR UPDATE`, id).Scan(&a.Name, &a.Platform, &a.Type, &a.Status, &a.Schedulable, &a.Concurrency, &a.ExpiresAt, &a.AutoPauseOnExpired, &credentials, &extra)
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
func oauth429EligibleRequest(c service.OAuthAutoConfig, a *service.Account, request, target, endpoint, compactModel string) bool {
	if c.Revision == "" || !c.DegradationAction.Enabled || !c.DegradationAction.UsesUpstream429() ||
		!c.DegradationAction.Independent429.Enabled || c.DegradationAction.Validate() != nil {
		return false
	}
	if a == nil || a.Type != service.AccountTypeOAuth || !a.Schedulable || a.Status != service.StatusActive || a.Concurrency <= 0 {
		return false
	}
	if a.AutoPauseOnExpired && a.ExpiresAt != nil && !time.Now().Before(*a.ExpiresAt) {
		return false
	}
	return service.OAuth429RequestModelSupported(a, request, endpoint, compactModel) &&
		service.ResolveOAuth429TargetModel(a, request, endpoint, compactModel) == target
}

// The HTTP wire model may be reachable only through a public mapping alias.
// Persist that input name separately; the test transport still fences the actual
// target model and every alias of the blocked target observes the same action.
func oauth429RequestModel(a *service.Account, target, endpoint, compactModel string) string {
	if a == nil {
		return ""
	}
	if service.ResolveOAuth429TargetModel(a, target, endpoint, compactModel) == target && service.OAuth429RequestModelSupported(a, target, endpoint, compactModel) {
		return target
	}
	mapping := a.GetModelMapping()
	keys := make([]string, 0, len(mapping))
	for name := range mapping {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if !strings.Contains(name, "*") && service.ResolveOAuth429TargetModel(a, name, endpoint, compactModel) == target && service.OAuth429RequestModelSupported(a, name, endpoint, compactModel) {
			return name
		}
	}
	return ""
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

func (r *accountRepository) ObserveOAuth429(ctx context.Context, observation service.OAuth429Observation) (bool, error) {
	id, model := observation.AccountID, observation.Model
	request, status := observation.Request, observation.Status
	if id <= 0 || (strings.TrimSpace(model) == "" && strings.TrimSpace(observation.RequestModel) == "") || len(model) > 256 || len(observation.RequestModel) > 256 || len(request) != 64 || status < 200 || status > 599 {
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
	routing, err := r.oauth429RoutingPolicy(ctx, tx)
	if err != nil {
		return false, err
	}
	cfg, a, err := oauth429PolicyAccount(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if observation.RoutingRevision != "" && observation.RoutingRevision != routing.Revision {
		return false, nil
	}
	if strings.HasSuffix(observation.Endpoint, "/responses/compact") && (observation.RequestModel == "" || observation.RoutingRevision != routing.Revision || observation.CompactModel != routing.CompactModel) {
		return false, nil
	}
	requestModel := observation.RequestModel
	if requestModel != "" {
		if !service.OAuth429RequestModelSupported(a, requestModel, observation.Endpoint, routing.CompactModel) {
			return false, nil
		}
		target := service.ResolveOAuth429TargetModel(a, requestModel, observation.Endpoint, routing.CompactModel)
		if model != "" && model != target {
			return false, nil
		}
		model = target
	} else {
		requestModel = oauth429RequestModel(a, model, observation.Endpoint, routing.CompactModel)
	}
	if cfg.Revision != observation.Revision || requestModel == "" || !oauth429EligibleRequest(cfg, a, requestModel, model, observation.Endpoint, routing.CompactModel) {
		return false, nil
	}
	epoch, validSince, err := oauth429AccountEpoch(ctx, tx, id)
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	started := observation.StartedAt
	if started.IsZero() {
		started = observation.At
	}
	if started.Before(routing.ValidSince) || observation.At.Before(routing.ValidSince) || started.Before(validSince) || observation.At.Before(validSince) || started.After(observation.At) || observation.At.After(now) || observation.At.Before(now.Add(-time.Hour)) {
		return false, nil
	}
	task, err := oauth429Load(ctx, tx, id, model)
	if errors.Is(err, sql.ErrNoRows) {
		// Only new account/model pairs compete for capacity. Existing windows
		// serialize on their account row.
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(4299281)`); err != nil {
			return false, err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM oauth_429_tasks`).Scan(&count); err != nil {
			return false, err
		}
		if count >= oauth429QueueLimit {
			var freed int
			err = tx.QueryRowContext(ctx, `WITH victim AS MATERIALIZED (
 SELECT account_id, model FROM oauth_429_tasks
 WHERE state NOT IN ('pending','running') AND updated_at < clock_timestamp()-interval '1 hour'
 AND NOT EXISTS(SELECT 1 FROM oauth_429_request_dedup d WHERE d.account_id=oauth_429_tasks.account_id AND d.model=oauth_429_tasks.model AND d.expires_at>clock_timestamp())
 ORDER BY updated_at LIMIT 1 FOR UPDATE SKIP LOCKED)
 DELETE FROM oauth_429_tasks t USING victim v WHERE t.account_id=v.account_id AND t.model=v.model RETURNING 1`).Scan(&freed)
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
		}
		task = &service.OAuth429Task{AccountID: id, AccountEpoch: epoch, Model: model, Revision: cfg.Revision, RoutingRevision: routing.Revision, CompactModel: routing.CompactModel, Armed: true, State: "observing"}
		// A cold completed episode can be evicted after its samples expire.
		// Keep it latched until a new non-pressure window is observed.
		var previous int64
		err = tx.QueryRowContext(ctx, `SELECT (details->'oauth429'->>'generation')::bigint FROM account_auto_config_events
 WHERE account_id=$1 AND kind='oauth429_review_completed' AND details->'oauth429'->>'model'=$2
 AND details->'oauth429'->>'revision'=$3
 AND COALESCE(details->'oauth429'->>'routing_revision','')=$4 ORDER BY id DESC LIMIT 1`, id, model, cfg.Revision, routing.Revision).Scan(&previous)
		if err == nil {
			task.Generation = previous
			task.Armed = false
		} else if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO oauth_429_tasks(account_id,model,state,data) VALUES($1,$2,'observing','{}')`, id, model); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	if task.Revision != cfg.Revision || task.AccountEpoch != epoch || task.RoutingRevision != routing.Revision {
		task.AccountEpoch = epoch
		task.Revision = cfg.Revision
		task.State = "observing"
		task.Armed = true
		task.Samples = nil
		task.Runs = nil
	}
	task.RoutingRevision = routing.Revision
	task.CompactModel = routing.CompactModel
	fresh, err := oauth429RememberRequest(ctx, tx, id, model, request, observation.At)
	if err != nil {
		return false, err
	}
	if !fresh {
		return false, tx.Commit()
	}
	if task.State == "pending" || task.State == "running" {
		return false, tx.Commit()
	}
	samples := make([]service.OAuth429Sample, 0, 200)
	for _, v := range task.Samples {
		if !v.At.Before(now.Add(-time.Hour)) {
			samples = append(samples, v)
		}
	}
	samples = append(samples, observation.OAuth429Sample)
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].At.Before(samples[j].At) })
	if len(samples) > 200 {
		samples = samples[len(samples)-200:]
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
	if pressure && task.Armed {
		task.RequestModel = requestModel
		task.Endpoint = observation.Endpoint
		task.Reason = ""
		task.ModelBlocked = false
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
	rows, err := db.QueryContext(ctx, `SELECT account_id,model FROM oauth_429_tasks WHERE state='pending' OR (state='running' AND (data->>'lease_until')::timestamptz<=clock_timestamp()) ORDER BY updated_at LIMIT 100`)
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
	routing, err := r.oauth429RoutingPolicy(ctx, tx)
	if err != nil {
		return nil, err
	}
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
	epoch, _, err := oauth429AccountEpoch(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if task.RoutingRevision != routing.Revision || task.CompactModel != routing.CompactModel || task.AccountEpoch != epoch || !oauth429EligibleRequest(cfg, a, task.RequestModel, model, task.Endpoint, routing.CompactModel) || task.Revision != cfg.Revision || !now.Before(task.ExpiresAt) {
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
	routing, err := r.oauth429RoutingPolicy(ctx, tx)
	if err != nil {
		return err
	}
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
	epoch, _, err := oauth429AccountEpoch(ctx, tx, claimed.AccountID)
	if err != nil {
		return err
	}
	if task.RoutingRevision != routing.Revision || task.CompactModel != routing.CompactModel || task.AccountEpoch != epoch || task.AccountEpoch != claimed.AccountEpoch || !oauth429EligibleRequest(cfg, a, task.RequestModel, task.Model, task.Endpoint, routing.CompactModel) || task.Revision != cfg.Revision || !now.Before(task.ExpiresAt) {
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
		if !current.NeedsAnotherRun() {
			return nil
		}
		for _, run := range current.Runs {
			if run.Verdict == "started" {
				return nil
			}
		}
		current.LeaseUntil = time.Now().UTC().Add(4 * time.Minute)
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
		switch result.Verdict {
		case "correct", "incorrect", "inconclusive":
		case "rate_limited":
			if result.HTTPStatus != 429 {
				return nil
			}
		default:
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
		if len(current.Runs) < 2 || current.NeedsAnotherRun() {
			return nil
		}
		for _, run := range current.Runs {
			if run.Verdict == "started" {
				return nil
			}
		}
		current.State = "complete"
		reason := current.DecisionReason()
		if reason != "" {
			current.Reason = reason
		}
		current.ModelBlocked = reason != "" && cfg.DegradationAction.RestrictsModels() && !slices.Contains(cfg.DegradationAction.Models, current.Model)
		audit := *current
		audit.Samples = nil
		audit.Lease = ""
		audit.LeaseUntil = time.Time{}
		if err := insertAutoConfigEvent(ctx, tx, service.AutoConfigEvent{AccountID: a.ID, AccountName: a.Name, Platform: a.Platform, Kind: service.AutoConfigEventOAuth429, Details: service.AutoConfigEventDetails{OAuth429: &audit, Concurrency: a.Concurrency, PreviousConcurrency: a.Concurrency}}); err != nil {
			return err
		}
		if !current.ModelBlocked {
			return nil
		}
		blocks, _ := a.Extra[service.OAuth429ModelBlocksKey].(map[string]any)
		if blocks == nil {
			blocks = map[string]any{}
		}
		blocks[current.Model] = map[string]any{"trigger_id": current.TriggerID, "generation": current.Generation, "revision": current.Revision, "runs": current.Runs, "reason": current.Reason, "applied_at": time.Now().UTC()}
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

func (r *accountRepository) OAuth429Current(ctx context.Context, task *service.OAuth429Task) (bool, error) {
	db, err := r.oauth429DB()
	if err != nil {
		return false, err
	}
	var current bool
	var expectedModel any
	if expected := r.oauth429ExpectedRoutingModel(); expected != nil {
		expectedModel = *expected
	}
	err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM oauth_429_tasks t JOIN accounts a ON a.id=t.account_id
 JOIN oauth_429_account_epochs e ON e.account_id=a.id AND e.epoch=(t.data->>'account_epoch')::bigint
 JOIN settings s ON s.key=$1
 LEFT JOIN settings routing ON routing.key='smart_ops_oauth429_routing'
 WHERE COALESCE(routing.value::jsonb->>'revision','')=COALESCE(t.data->>'routing_revision','')
 AND ($6::text IS NULL OR routing.value::jsonb->>'compact_model'=$6)
 AND t.account_id=$2 AND t.model=$3 AND t.state='running'
 AND t.data->>'trigger_id'=$4 AND t.data->>'lease'=$5
 AND (t.data->>'lease_until')::timestamptz>clock_timestamp()
 AND (t.data->>'expires_at')::timestamptz>clock_timestamp()
 AND a.deleted_at IS NULL AND a.schedulable AND a.status='active'
 AND NOT(a.auto_pause_on_expired AND a.expires_at IS NOT NULL AND a.expires_at<=clock_timestamp())
 AND s.value::jsonb->>'revision'=t.data->>'revision'
 AND s.value::jsonb->'degradation_action'->>'enabled'='true'
 AND s.value::jsonb->'degradation_action'->'independent_429'->>'enabled'='true')`, service.SettingKeyOAuthAutoConfig, task.AccountID, task.Model, task.TriggerID, task.Lease, expectedModel).Scan(&current)
	return current, err
}
