package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

var _ service.AIOpsRepository = (*accountRepository)(nil)

type aiOpsBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func (r *accountRepository) aiOpsTx(ctx context.Context) (*sql.Tx, error) {
	db, ok := r.sql.(aiOpsBeginner)
	if !ok {
		return nil, service.ErrAIOpsUnavailable
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL statement_timeout='4s'; SET LOCAL lock_timeout='1s'; SET LOCAL work_mem='4MB'`); err == nil {
		_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(662662)`)
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func readAIOpsPolicy(ctx context.Context, db sqlExecutor) (service.AIOpsPolicy, error) {
	p := service.DefaultAIOpsPolicy()
	var raw []byte
	var revision int64
	err := scanSingleRow(ctx, db, `SELECT revision,config FROM ai_ops_policy WHERE id=1`, nil, &revision, &raw)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return p, service.ErrAIOpsUnavailable
	}
	p.Revision = revision
	return p, nil
}

func (r *accountRepository) AIOpsPolicy(ctx context.Context) (service.AIOpsPolicy, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return readAIOpsPolicy(ctx, r.sql)
}

func (r *accountRepository) SaveAIOpsPolicy(ctx context.Context, p service.AIOpsPolicy, expectedRev, actorID int64) (service.AIOpsPolicy, error) {
	if err := service.ValidateAIOpsPolicy(p); err != nil {
		return p, err
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	tx, err := r.aiOpsTx(ctx)
	if err != nil {
		return p, err
	}
	defer func() { _ = tx.Rollback() }()
	if p.Mode != "disabled" {
		unsupported, e := aiOpsRegionalUnsupported(ctx, tx)
		if e != nil {
			return p, e
		}
		if unsupported {
			return p, service.ErrAIOpsBlocked
		}
	}
	old, err := readAIOpsPolicy(ctx, tx)
	if err != nil {
		return p, err
	}
	if old.Revision != expectedRev {
		return p, service.ErrAIOpsConflict
	}
	// Scope changes with outstanding ownership need an explicit rollback first.
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_ops_controls WHERE owner_action_id IS NOT NULL`).Scan(&active); err != nil {
		return p, err
	}
	if active > 0 && (old.GroupID != p.GroupID || !slices.Equal(old.Accounts, p.Accounts)) {
		return p, service.ErrAIOpsBlocked
	}
	p.Revision = old.Revision + 1
	raw, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE ai_ops_policy SET revision=$1,config=$2,actor_id=$3,updated_at=NOW() WHERE id=1 AND revision=$4`, p.Revision, string(raw), actorID, expectedRev)
	if err != nil {
		return p, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return p, service.ErrAIOpsConflict
	}
	return p, tx.Commit()
}

func (r *accountRepository) HaltAIOps(ctx context.Context, actorID int64) (service.AIOpsPolicy, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	tx, err := r.aiOpsTx(ctx)
	if err != nil {
		return service.AIOpsPolicy{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := readAIOpsPolicy(ctx, tx)
	if err != nil {
		return p, err
	}
	p.Halted = true
	p.Revision++
	raw, _ := json.Marshal(p)
	_, err = tx.ExecContext(ctx, `UPDATE ai_ops_policy SET revision=$1,config=$2,actor_id=$3,updated_at=NOW() WHERE id=1`, p.Revision, string(raw), actorID)
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

type aiOpsAccount struct {
	id, revision             int64
	concurrency              int
	loadFactor               *float64
	healthy, managed, paused bool
	controlRevision          int64
	owner                    string
	groups                   []int64
	providerFingerprint      string
	quotaKnown               bool
}

// Fixed projection only: configuration revision, independent ownership, and
// scheduler eligibility. No credential blob, identity or request text leaves PG.
const aiOpsAccountsSQL = `SELECT a.id,a.ai_ops_revision,a.concurrency,a.load_factor,
 (a.status='active' AND a.schedulable AND (a.expires_at IS NULL OR a.expires_at>NOW())
 AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at<=NOW())
 AND (a.overload_until IS NULL OR a.overload_until<=NOW())
 AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until<=NOW())),
 (COALESCE(a.extra,'{}') ? 'auto_config_concurrency'
 OR COALESCE(a.extra,'{}') ? 'quality_model_isolation' OR COALESCE(a.extra,'{}') ? 'openai_two_fa_admission'
 OR a.parent_account_id IS NOT NULL
 OR EXISTS(SELECT 1 FROM scheduled_test_plans q WHERE q.account_id=a.id AND q.pelican_config->'quality' IS NOT NULL)
 OR EXISTS(SELECT 1 FROM account_schedulable_timers t WHERE t.account_id=a.id AND t.status='pending')
 OR EXISTS(SELECT 1 FROM account_performance_monitors m WHERE m.account_id=a.id AND (m.paused OR m.config->>'enabled'='true'))),
 COALESCE(c.ai_paused,false),COALESCE(c.revision,0),COALESCE(c.owner_action_id::text,''),
 COALESCE((SELECT array_agg(ag.group_id ORDER BY ag.group_id) FROM account_groups ag JOIN groups g ON g.id=ag.group_id WHERE ag.account_id=a.id AND g.deleted_at IS NULL),'{}'),
 md5(COALESCE(a.credentials->>'chatgpt_account_id',a.credentials->>'organization_id',a.credentials->>'base_url','')||'|'||COALESCE(a.platform,'')||'|'||COALESCE(a.type,'')),
 COALESCE((a.platform='openai' AND a.type='oauth' AND a.parent_account_id IS NULL
 AND COALESCE(a.extra->>'codex_primary_used_percent',a.extra->>'codex_5h_used_percent','') ~ '^[0-9]+(\.[0-9]+)?$'
 AND CASE WHEN COALESCE(a.extra->>'codex_primary_used_percent',a.extra->>'codex_5h_used_percent','') ~ '^[0-9]+(\.[0-9]+)?$'
 THEN COALESCE(a.extra->>'codex_primary_used_percent',a.extra->>'codex_5h_used_percent')::numeric < 100 ELSE false END
 AND COALESCE(a.extra->>'codex_7d_used_percent',a.extra->>'codex_secondary_used_percent','') ~ '^[0-9]+(\.[0-9]+)?$'
 AND CASE WHEN COALESCE(a.extra->>'codex_7d_used_percent',a.extra->>'codex_secondary_used_percent','') ~ '^[0-9]+(\.[0-9]+)?$'
 THEN COALESCE(a.extra->>'codex_7d_used_percent',a.extra->>'codex_secondary_used_percent')::numeric < 100 ELSE false END
 AND (a.extra->>'codex_usage_updated_at') ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'
 AND CASE WHEN (a.extra->>'codex_usage_updated_at') ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T' THEN (a.extra->>'codex_usage_updated_at')::timestamptz>NOW()-interval '10 minutes' AND (a.extra->>'codex_usage_updated_at')::timestamptz<=NOW() ELSE false END),false)
 FROM accounts a LEFT JOIN ai_ops_controls c ON c.account_id=a.id WHERE a.id=ANY($1) AND a.deleted_at IS NULL ORDER BY a.id FOR UPDATE OF a`

func readAIOpsAccounts(ctx context.Context, tx *sql.Tx, ids []int64) (map[int64]aiOpsAccount, error) {
	rows, err := tx.QueryContext(ctx, aiOpsAccountsSQL, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]aiOpsAccount{}
	for rows.Next() {
		var a aiOpsAccount
		var lf sql.NullFloat64
		if err = rows.Scan(&a.id, &a.revision, &a.concurrency, &lf, &a.healthy, &a.managed, &a.paused, &a.controlRevision, &a.owner, pq.Array(&a.groups), &a.providerFingerprint, &a.quotaKnown); err != nil {
			return nil, err
		}
		if lf.Valid {
			v := lf.Float64
			a.loadFactor = &v
		}
		out[a.id] = a
	}
	return out, rows.Err()
}

// Final failed provider requests and successes are separate populations. The
// dedup key prevents retries/log duplication becoming fictitious sample size;
// recovered failover rows, business rejections, 429/529 and client cancels are
// deliberately excluded from automatic fault evidence.
// A final failure wins over a conflicting completed usage trace for the same
// request: partial billable output must not add a second, successful sample.
var aiOpsEvidenceSQL = `WITH success_candidates AS (
 SELECT u.request_id,u.api_key_id,u.model,COALESCE(u.reasoning_effort,'') AS effort,
 t.detail->'protocol_decision'->>'upstream' AS protocol,
 CASE WHEN u.input_tokens+u.cache_read_tokens<4000 THEN 'small' WHEN u.input_tokens+u.cache_read_tokens<32000 THEN 'medium' ELSE 'large' END AS input_bucket,
 CASE WHEN u.cache_read_tokens>0 THEN 'cached' ELSE 'uncached' END AS cache_bucket,
 CASE WHEN u.output_tokens<1000 THEN 'small' WHEN u.output_tokens<8000 THEN 'medium' ELSE 'large' END AS output_bucket
 FROM usage_logs u JOIN LATERAL (
 SELECT (array_agg(detail))[1] AS detail FROM (SELECT detail FROM request_timing_details WHERE usage_log_id=u.id LIMIT 2) bounded HAVING COUNT(*)=1
 ) t ON true
 WHERE u.account_id=$1 AND u.created_at>=$2 AND u.created_at<$3
 AND t.detail->>'version'='1' AND t.detail->>'outcome'='success' AND t.detail->>'status'='200'
 AND t.detail->>'body_complete'='true' AND t.detail->>'canceled'='false' AND t.detail->>'truncated'='false'
 AND t.detail->>'client_disconnect'='false' AND t.detail->>'downstream_error'='false'
 AND (jsonb_path_query_first_tz(jsonb_build_object('at',regexp_replace(regexp_replace(t.detail->>'started_at','Z$','+00:00'),'(\.[0-9]{6})[0-9]+([+-])','\1\2')),'$.at.datetime()','{}',true)#>>'{}')::timestamptz >= $2
 AND (jsonb_path_query_first_tz(jsonb_build_object('at',regexp_replace(regexp_replace(t.detail->>'started_at','Z$','+00:00'),'(\.[0-9]{6})[0-9]+([+-])','\1\2')),'$.at.datetime()','{}',true)#>>'{}')::timestamptz < $3
 AND (NOT u.stream OR t.detail->>'terminal'='completed')
 AND COALESCE(u.image_count,0)=0 AND COALESCE(u.image_output_tokens,0)=0 AND COALESCE(u.billing_mode,'token') NOT IN ('image','video')
), failures AS (
 SELECT DISTINCT ON (COALESCE(NULLIF(request_id,''),'error:'||id::text))
 request_id,client_request_id,api_key_id,model FROM ops_error_logs o WHERE account_id=$1 AND created_at>=$2 AND created_at<$3
 AND NOT is_count_tokens AND NOT ` + opsBusinessLimitedSQL("o") + ` AND ` + opsEffectiveOwnerSQL("o") + `='provider'
 AND NOT ` + opsExplicitRequestRejectionSQL("o", "400, 404, 413, 422, 500, 502, 503", append(append([]string{}, service.OpsContextLimitRejectionNeedles...), service.OpsModelCapabilityRejectionNeedles...)) + `
 AND (status_code>=400 OR error_type='transport_error') AND error_type<>'client_canceled'
 AND COALESCE(NULLIF(upstream_status_code,0),status_code) NOT IN (429,529)
 AND (COALESCE(NULLIF(upstream_status_code,0),status_code)>=500 OR error_phase IN ('connect','transport') OR error_type='transport_error')
 ORDER BY COALESCE(NULLIF(request_id,''),'error:'||id::text),created_at DESC,id DESC
), successes AS (
 SELECT s.* FROM success_candidates s
 WHERE NOT EXISTS (SELECT 1 FROM failures f WHERE f.api_key_id=s.api_key_id AND (
   (f.request_id<>'' AND (f.request_id=s.request_id OR 'local:'||f.request_id=s.request_id))
   OR (f.client_request_id<>'' AND 'client:'||f.client_request_id=s.request_id)))
)
 SELECT (SELECT COUNT(*) FROM successes),(SELECT COUNT(*) FROM failures),
 (SELECT COUNT(*) FROM successes WHERE model<>'' AND effort<>'' AND protocol IN ('http','ws')),
 (SELECT COUNT(DISTINCT model||'|'||effort||'|'||protocol||'|'||input_bucket||'|'||cache_bucket||'|'||output_bucket) FROM successes),
 (SELECT COUNT(*) FROM failures WHERE model IS NULL OR model='' OR model NOT IN (SELECT model FROM successes)),
 (SELECT MIN(model) FROM successes),(SELECT MIN(effort) FROM successes),(SELECT MIN(protocol) FROM successes),
 (SELECT MIN(input_bucket) FROM successes),(SELECT MIN(cache_bucket) FROM successes),(SELECT MIN(output_bucket) FROM successes) `

func captureAIOpsEvidence(ctx context.Context, tx *sql.Tx, id int64, p service.AIOpsPolicy, now time.Time, accounts map[int64]aiOpsAccount) (service.AIOpsEvidenceSnapshot, error) {
	window := time.Duration(p.ObservationSeconds) * time.Second
	e := service.AIOpsEvidenceSnapshot{ObservedAt: now, WindowStart: now.Add(-window), WindowEnd: now, Inflight: -1}
	var successes, failures, known, cohorts, unknownFailures int
	var model, effort, protocol, inputBucket, cacheBucket, outputBucket sql.NullString
	if err := tx.QueryRowContext(ctx, aiOpsEvidenceSQL, id, e.WindowStart, e.WindowEnd).Scan(&successes, &failures, &known, &cohorts, &unknownFailures, &model, &effort, &protocol, &inputBucket, &cacheBucket, &outputBucket); err != nil {
		return e, err
	}
	e.Samples = successes + failures
	if e.Samples > 0 {
		e.ErrorRate = float64(failures) / float64(e.Samples)
	}
	// Changes require a single observed model/effort cohort. Errors without
	// successes cannot establish cohort coverage; latency alone never drives it.
	e.ModelCoverageComplete = successes > 0 && known == successes && cohorts == 1 && unknownFailures == 0
	if e.ModelCoverageComplete {
		e.Model = model.String
		e.Effort = effort.String
		e.Protocol = protocol.String
		e.InputBucket = inputBucket.String
		e.CacheBucket = cacheBucket.String
		e.OutputBucket = outputBucket.String
	}
	if !accounts[id].quotaKnown {
		e.ModelCoverageComplete = false
	}
	for peerID, a := range accounts {
		if a.healthy && a.quotaKnown && !a.managed && !a.paused && a.concurrency > 0 {
			supported, supportErr := aiOpsModelSupported(ctx, tx, peerID, p.GroupID, model.String)
			if supportErr != nil {
				return e, supportErr
			}
			if !supported {
				if peerID == id {
					e.ModelCoverageComplete = false
				}
				continue
			}
			var compatible bool
			err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_logs peer WHERE peer.account_id=$1 AND peer.created_at>=$3 AND peer.created_at<$4 AND (peer.model,COALESCE(peer.reasoning_effort,'')) IN (SELECT model,COALESCE(reasoning_effort,'') FROM usage_logs WHERE account_id=$2 AND created_at>=$3 AND created_at<$4) AND COALESCE(peer.reasoning_effort,'')<>'')`, peerID, id, e.WindowStart, e.WindowEnd).Scan(&compatible)
			if err != nil {
				return e, err
			}
			if compatible {
				e.EligibleAccounts++
				e.TotalConcurrency += a.concurrency
				e.EligibleAccountIDs = append(e.EligibleAccountIDs, peerID)
			}
		}
	}
	return e, nil
}

func aiOpsModelSupported(ctx context.Context, tx *sql.Tx, id, groupID int64, model string) (bool, error) {
	if model == "" {
		return false, nil
	}
	a := service.Account{ID: id}
	var credentials, extra []byte
	if err := tx.QueryRowContext(ctx, `SELECT platform,type,COALESCE(credentials,'{}'),COALESCE(extra,'{}') FROM accounts WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&a.Platform, &a.Type, &credentials, &extra); err != nil {
		return false, err
	}
	if json.Unmarshal(credentials, &a.Credentials) != nil || json.Unmarshal(extra, &a.Extra) != nil {
		return false, service.ErrAIOpsUnavailable
	}
	// The recovery runner supports native primary OAuth only. Never pause an
	// otherwise healthy transport that our explicit recovery probe cannot use.
	if !a.IsOpenAIOAuth() || a.IsShadow() || a.IsExcelBPSEnabled() || a.IsSIWCEnabled() || a.IsExcelOAuth() || a.HasManagedOpenAIConnections() {
		return false, nil
	}
	var allowed []byte
	var groupAllowlist []byte
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(ag.allowed_models,'[]'),COALESCE(g.model_allowlist,'{}') FROM account_groups ag JOIN groups g ON g.id=ag.group_id WHERE ag.account_id=$1 AND ag.group_id=$2 AND g.deleted_at IS NULL AND g.status='active'`, id, groupID).Scan(&allowed, &groupAllowlist); err != nil {
		return false, err
	}
	var models []string
	var groupModels service.GroupModelAllowlist
	if json.Unmarshal(allowed, &models) != nil || json.Unmarshal(groupAllowlist, &groupModels) != nil {
		return false, service.ErrAIOpsUnavailable
	}
	a.AccountGroups = []service.AccountGroup{{AccountID: id, GroupID: groupID, AllowedModels: models}}
	return groupModels.Allows(model) && a.IsModelSupportedInGroup(&groupID, model), nil
}

func aiOpsRegionalUnsupported(ctx context.Context, tx *sql.Tx) (bool, error) {
	var unsupported bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM settings WHERE key='serverless_routing_v1' AND value::jsonb->>'enabled'='true')`).Scan(&unsupported)
	return unsupported, err
}

func aiOpsLoadFactorConsumed(ctx context.Context, tx *sql.Tx, action string) (bool, error) {
	if action != "set_load_factor" {
		return true, nil
	}
	var priorityEnabled bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM settings WHERE key='priority_scheduling_v1' AND value::jsonb->>'enabled'='true')`).Scan(&priorityEnabled)
	// Priority scheduling uses actual inflight/C rather than load_factor for
	// capacity ranking. Do not approve a parameter the active path may ignore.
	return !priorityEnabled, err
}

func aiOpsScopeIDs(p service.AIOpsPolicy) []int64 {
	ids := make([]int64, 0, len(p.Accounts))
	for _, a := range p.Accounts {
		ids = append(ids, a.AccountID)
	}
	slices.Sort(ids)
	return ids
}
func aiOpsDomain(p service.AIOpsPolicy, id int64) string {
	for _, a := range p.Accounts {
		if a.AccountID == id {
			return a.CapacityDomain
		}
	}
	return ""
}
func aiOpsDomainIDs(p service.AIOpsPolicy, id int64) []int64 {
	domain := aiOpsDomain(p, id)
	ids := []int64{}
	if domain == "" {
		return ids
	}
	for _, a := range p.Accounts {
		if a.CapacityDomain == domain {
			ids = append(ids, a.AccountID)
		}
	}
	slices.Sort(ids)
	return ids
}
func aiOpsFloat(v float64) *float64 { return &v }

func aiOpsReasons(p service.AIOpsPolicy, in service.AIOpsPreviewInput, a aiOpsAccount, e service.AIOpsEvidenceSnapshot, accounts map[int64]aiOpsAccount, now time.Time) []string {
	reasons := []string{}
	if service.ValidateAIOpsPolicy(p) != nil || p.Mode == "disabled" || p.Halted {
		reasons = append(reasons, "policy_disabled")
	}
	if a.id == 0 || aiOpsDomain(p, in.AccountID) == "" {
		return append(reasons, "unknown_scope")
	}
	if !slices.Equal(a.groups, []int64{p.GroupID}) {
		reasons = append(reasons, "shared_group_scope")
	}
	if a.managed {
		reasons = append(reasons, "managed_by_other_controller")
	}
	if !a.quotaKnown {
		reasons = append(reasons, "quota_or_protocol_unknown")
	}
	for peerID, peer := range accounts {
		if peerID != a.id && peer.providerFingerprint != "" && peer.providerFingerprint == a.providerFingerprint && aiOpsDomain(p, peerID) != aiOpsDomain(p, a.id) {
			reasons = append(reasons, "provider_domain_mismatch")
			break
		}
	}
	if !a.healthy {
		reasons = append(reasons, "stronger_restriction")
	}
	if a.paused && in.Action != "resume_account" {
		reasons = append(reasons, "ai_pause_already_owned")
	}
	if in.EvidenceObservedAt.IsZero() || in.EvidenceObservedAt.After(now.Add(5*time.Second)) || now.Sub(in.EvidenceObservedAt) > time.Duration(p.EvidenceMaxAgeSeconds)*time.Second {
		reasons = append(reasons, "stale_evidence")
	}
	if !e.ProbePassed && (e.Samples < p.MinSamples || !e.ModelCoverageComplete) {
		reasons = append(reasons, "insufficient_model_evidence")
	}
	if in.Action == "set_concurrency" || in.Action == "set_load_factor" {
		before := float64(a.concurrency)
		if in.Action == "set_load_factor" {
			if a.loadFactor == nil {
				reasons = append(reasons, "unknown_load_factor")
			} else {
				before = *a.loadFactor
			}
		}
		if in.ProposedValue == nil || math.IsNaN(*in.ProposedValue) || math.IsInf(*in.ProposedValue, 0) || *in.ProposedValue <= 0 || *in.ProposedValue > 10000 || math.Trunc(*in.ProposedValue) != *in.ProposedValue || before <= 0 {
			reasons = append(reasons, "invalid_value")
		} else if math.Abs(*in.ProposedValue-before)*100 > before*p.MaxStepPercent || *in.ProposedValue == before {
			reasons = append(reasons, "step_budget")
		}
		if in.Action == "set_concurrency" && in.ProposedValue != nil && (*in.ProposedValue < float64(p.MinConcurrency) || *in.ProposedValue > float64(p.MaxConcurrency)) {
			reasons = append(reasons, "concurrency_bounds")
		}
	} else if (in.Action != "pause_account" && in.Action != "resume_account") || in.ProposedValue != nil {
		reasons = append(reasons, "invalid_action")
	}
	if in.Action == "pause_account" && a.paused {
		reasons = append(reasons, "already_paused")
	}
	if in.Action == "pause_account" && e.Protocol != "http" {
		reasons = append(reasons, "recovery_protocol_unsupported")
	}
	if in.Action == "resume_account" && !a.paused {
		reasons = append(reasons, "not_ai_paused")
	}
	if in.Action == "resume_account" && !e.ProbePassed {
		reasons = append(reasons, "recovery_probe_required")
	}
	// Budget the whole configured capacity domain as unavailable while deciding
	// pause/shrink, so a correlated upstream is never counted as its own fallback.
	if in.Action == "pause_account" || in.Action == "set_concurrency" {
		healthy, total := 0, 0
		for id, peer := range accounts {
			if slices.Contains(e.EligibleAccountIDs, id) && peer.healthy && peer.quotaKnown && !peer.managed && !peer.paused && peer.concurrency > 0 && slices.Equal(peer.groups, []int64{p.GroupID}) {
				capacity := peer.concurrency
				if id == a.id {
					if in.Action == "pause_account" {
						continue
					}
					if in.ProposedValue != nil {
						capacity = int(*in.ProposedValue)
					}
				}
				if id != a.id && aiOpsDomain(p, id) == aiOpsDomain(p, a.id) {
					continue
				}
				healthy++
				total += capacity
			}
		}
		if healthy < p.MinHealthyAccounts || total < p.MinTotalConcurrency {
			reasons = append(reasons, "capacity_floor")
		}
	}
	return reasons
}

func (r *accountRepository) PreviewAIOps(ctx context.Context, in service.AIOpsPreviewInput, actorID int64) (service.AIOpsProposal, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	tx, err := r.aiOpsTx(ctx)
	if err != nil {
		return service.AIOpsProposal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	proposal, err := previewAIOps(ctx, tx, in, actorID, time.Now().UTC())
	if err != nil {
		return proposal, err
	}
	return proposal, tx.Commit()
}

func previewAIOps(ctx context.Context, tx *sql.Tx, in service.AIOpsPreviewInput, actorID int64, now time.Time) (service.AIOpsProposal, error) {
	if in.RunID == "" || len(in.RunID) > 100 || in.ProposalIndex < 0 || in.ProposalIndex > 20 || in.AccountID <= 0 {
		return service.AIOpsProposal{}, service.ErrAIOpsBlocked
	}
	if consumed, err := aiOpsLoadFactorConsumed(ctx, tx, in.Action); err != nil {
		return service.AIOpsProposal{}, err
	} else if !consumed {
		return service.AIOpsProposal{}, service.ErrAIOpsBlocked
	}
	var existing []byte
	err := tx.QueryRowContext(ctx, `SELECT data FROM ai_ops_proposals WHERE run_id=$1 AND proposal_index=$2`, in.RunID, in.ProposalIndex).Scan(&existing)
	if err == nil {
		var p service.AIOpsProposal
		if json.Unmarshal(existing, &p) != nil {
			return p, service.ErrAIOpsUnavailable
		}
		return p, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return service.AIOpsProposal{}, err
	}
	p, err := readAIOpsPolicy(ctx, tx)
	if err != nil {
		return service.AIOpsProposal{}, err
	}
	if unsupported, e := aiOpsRegionalUnsupported(ctx, tx); e != nil {
		return service.AIOpsProposal{}, e
	} else if unsupported {
		return service.AIOpsProposal{}, service.ErrAIOpsBlocked
	}
	accounts, err := readAIOpsAccounts(ctx, tx, aiOpsScopeIDs(p))
	if err != nil {
		return service.AIOpsProposal{}, err
	}
	a := accounts[in.AccountID]
	e := service.AIOpsEvidenceSnapshot{ObservedAt: now, WindowStart: now, WindowEnd: now}
	if p.ObservationSeconds >= 60 && a.id > 0 {
		e, err = captureAIOpsEvidence(ctx, tx, in.AccountID, p, now, accounts)
		if err != nil {
			return service.AIOpsProposal{}, err
		}
	}
	if in.Action == "resume_account" {
		owned, scopeErr := aiOpsOwnedScope(ctx, tx, a)
		if scopeErr != nil {
			return service.AIOpsProposal{}, scopeErr
		}
		e.Model = owned.Model
		e.Effort = owned.Effort
		e.Protocol = owned.Protocol
		e.ProbePassed, err = freshAIOpsProbe(ctx, tx, a, p, now)
		if err != nil {
			return service.AIOpsProposal{}, err
		}
	}
	proposal := service.AIOpsProposal{ID: uuid.NewString(), RunID: in.RunID, ProposalIndex: in.ProposalIndex, Action: in.Action, AccountID: in.AccountID,
		ProposedValue: in.ProposedValue, PolicyRevision: p.Revision, TargetRevision: a.revision, CreatedAt: now, ExpiresAt: now.Add(time.Duration(max(30, p.EvidenceMaxAgeSeconds)) * time.Second),
		Status: "ready", BlockedReasons: aiOpsReasons(p, in, a, e, accounts, now), Evidence: e, ImpactGroupIDs: a.groups}
	switch in.Action {
	case "set_concurrency":
		proposal.BeforeValue = aiOpsFloat(float64(a.concurrency))
	case "set_load_factor":
		proposal.BeforeValue = a.loadFactor
	case "pause_account", "resume_account":
		v := 0.0
		if a.paused {
			v = 1
		}
		proposal.BeforeValue = &v
	}
	if len(proposal.BlockedReasons) > 0 {
		proposal.Status = "blocked"
	}
	raw, _ := json.Marshal(proposal)
	digest := sha256.Sum256(raw)
	proposal.Digest = hex.EncodeToString(digest[:])
	raw, _ = json.Marshal(proposal)
	_, err = tx.ExecContext(ctx, `INSERT INTO ai_ops_proposals(id,run_id,proposal_index,account_id,digest,policy_revision,target_revision,status,data,created_at,expires_at,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, proposal.ID, proposal.RunID, proposal.ProposalIndex, proposal.AccountID, proposal.Digest, proposal.PolicyRevision, proposal.TargetRevision, proposal.Status, string(raw), proposal.CreatedAt, proposal.ExpiresAt, actorID)
	return proposal, err
}

func readAIOpsProposal(ctx context.Context, tx *sql.Tx, id string) (service.AIOpsProposal, error) {
	var raw []byte
	var p service.AIOpsProposal
	err := tx.QueryRowContext(ctx, `SELECT data FROM ai_ops_proposals WHERE id=$1 FOR UPDATE`, id).Scan(&raw)
	if err != nil {
		return p, err
	}
	if json.Unmarshal(raw, &p) != nil {
		return p, service.ErrAIOpsUnavailable
	}
	return p, nil
}
func readAIOpsAction(ctx context.Context, db sqlExecutor, id string) (service.AIOpsAction, error) {
	var raw []byte
	var a service.AIOpsAction
	err := scanSingleRow(ctx, db, `SELECT data FROM ai_ops_actions WHERE id=$1`, []any{id}, &raw)
	if err != nil {
		return a, err
	}
	if json.Unmarshal(raw, &a) != nil {
		return a, service.ErrAIOpsUnavailable
	}
	return a, nil
}
func saveAIOpsAction(ctx context.Context, tx *sql.Tx, a service.AIOpsAction) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE ai_ops_actions SET status=$2,data=$3,updated_at=$4 WHERE id=$1`, a.ID, a.Status, string(raw), a.UpdatedAt)
	return err
}

// Cancellation uses the same global transaction and proposal row lock as apply.
// It only revokes a frozen proposal; an existing action requires its own rollback.
func (r *accountRepository) CancelAIOpsProposal(ctx context.Context, id, digest string, actorID int64) (service.AIOpsProposal, error) {
	if _, err := uuid.Parse(id); err != nil {
		return service.AIOpsProposal{}, service.ErrAIOpsBlocked
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	tx, err := r.aiOpsTx(ctx)
	if err != nil {
		return service.AIOpsProposal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	proposal, err := readAIOpsProposal(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return proposal, service.ErrAIOpsConflict
	}
	if err != nil {
		return proposal, err
	}
	if digest == "" || proposal.Digest != digest {
		return proposal, service.ErrAIOpsConflict
	}
	if proposal.Status == "cancelled" {
		return proposal, tx.Commit()
	}
	if proposal.Status != "ready" && proposal.Status != "blocked" {
		return proposal, service.ErrAIOpsConflict
	}
	var applied bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_ops_actions WHERE proposal_id=$1)`, id).Scan(&applied); err != nil {
		return proposal, err
	}
	if applied {
		return proposal, service.ErrAIOpsConflict
	}
	now := time.Now().UTC()
	proposal.Status, proposal.CancelledAt, proposal.CancelledBy = "cancelled", &now, actorID
	raw, err := json.Marshal(proposal)
	if err != nil {
		return proposal, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ai_ops_proposals SET status='cancelled',data=$2 WHERE id=$1`, id, string(raw)); err != nil {
		return proposal, err
	}
	return proposal, tx.Commit()
}

func (r *accountRepository) ApplyAIOps(ctx context.Context, id, digest string, actorID int64, automatic bool) (service.AIOpsAction, error) {
	if _, err := uuid.Parse(id); err != nil {
		return service.AIOpsAction{}, service.ErrAIOpsBlocked
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	tx, err := r.aiOpsTx(ctx)
	if err != nil {
		return service.AIOpsAction{}, err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := applyAIOps(ctx, tx, id, digest, actorID, automatic, time.Now().UTC())
	if err != nil {
		return a, err
	}
	if err = tx.Commit(); err != nil {
		return a, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, a.AccountID)
	return a, nil
}

func applyAIOps(ctx context.Context, tx *sql.Tx, id, digest string, actorID int64, automatic bool, now time.Time) (service.AIOpsAction, error) {
	proposal, err := readAIOpsProposal(ctx, tx, id)
	if err != nil {
		return service.AIOpsAction{}, err
	}
	if proposal.Digest != digest || digest == "" {
		return service.AIOpsAction{}, service.ErrAIOpsConflict
	}
	var existing []byte
	err = tx.QueryRowContext(ctx, `SELECT data FROM ai_ops_actions WHERE proposal_id=$1`, id).Scan(&existing)
	if err == nil {
		var a service.AIOpsAction
		if json.Unmarshal(existing, &a) != nil {
			return a, service.ErrAIOpsUnavailable
		}
		return a, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return service.AIOpsAction{}, err
	}
	p, err := readAIOpsPolicy(ctx, tx)
	if err != nil {
		return service.AIOpsAction{}, err
	}
	if unsupported, e := aiOpsRegionalUnsupported(ctx, tx); e != nil {
		return service.AIOpsAction{}, e
	} else if unsupported {
		return service.AIOpsAction{}, service.ErrAIOpsBlocked
	}
	if proposal.Status != "ready" || !now.Before(proposal.ExpiresAt) || p.Revision != proposal.PolicyRevision {
		return service.AIOpsAction{}, service.ErrAIOpsConflict
	}
	if consumed, err := aiOpsLoadFactorConsumed(ctx, tx, proposal.Action); err != nil {
		return service.AIOpsAction{}, err
	} else if !consumed {
		return service.AIOpsAction{}, service.ErrAIOpsBlocked
	}
	if automatic && (p.Mode != "bounded_auto" || !slices.Contains(p.AutoActions, proposal.Action) || proposal.Action == "set_load_factor") {
		return service.AIOpsAction{}, service.ErrAIOpsBlocked
	}
	accounts, err := readAIOpsAccounts(ctx, tx, aiOpsScopeIDs(p))
	if err != nil {
		return service.AIOpsAction{}, err
	}
	target := accounts[proposal.AccountID]
	if target.revision != proposal.TargetRevision {
		return service.AIOpsAction{}, service.ErrAIOpsConflict
	}
	e, err := captureAIOpsEvidence(ctx, tx, target.id, p, now, accounts)
	if err != nil {
		return service.AIOpsAction{}, err
	}
	if proposal.Action == "resume_account" {
		e.ProbePassed, err = freshAIOpsProbe(ctx, tx, target, p, now)
		if err != nil {
			return service.AIOpsAction{}, err
		}
	} else if !service.ComparableAIOpsEvidence(proposal.Evidence, e) {
		return service.AIOpsAction{}, service.ErrAIOpsConflict
	}
	in := service.AIOpsPreviewInput{Action: proposal.Action, AccountID: target.id, ProposedValue: proposal.ProposedValue, EvidenceObservedAt: proposal.CreatedAt}
	if len(aiOpsReasons(p, in, target, e, accounts, now)) > 0 {
		return service.AIOpsAction{}, service.ErrAIOpsBlocked
	}
	if automatic && proposal.Action != "resume_account" && (e.ErrorRate < p.ErrorRateThreshold || (proposal.Action == "set_concurrency" && proposal.ProposedValue != nil && *proposal.ProposedValue >= float64(target.concurrency))) {
		return service.AIOpsAction{}, service.ErrAIOpsBlocked
	}
	if proposal.Action == "resume_account" {
		if !e.ProbePassed || target.owner == "" {
			return service.AIOpsAction{}, service.ErrAIOpsBlocked
		}
	}
	var active, daily int
	var last sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE status IN ('propagation_pending','observing') OR (status='inconclusive' AND data->>'reason_code'='propagation_unconfirmed')),COUNT(*) FILTER(WHERE created_at>=date_trunc('day',$1::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),MAX(created_at) FILTER(WHERE account_id=ANY($2)) FROM ai_ops_actions`, now, pq.Array(aiOpsDomainIDs(p, target.id))).Scan(&active, &daily, &last)
	if err != nil {
		return service.AIOpsAction{}, err
	}
	if active > 0 || daily >= p.DailyActionLimit || (last.Valid && now.Sub(last.Time) < time.Duration(p.CooldownSeconds)*time.Second) {
		return service.AIOpsAction{}, service.ErrAIOpsBlocked
	}
	a := service.AIOpsAction{ID: uuid.NewString(), ProposalID: proposal.ID, RunID: proposal.RunID, Action: proposal.Action, AccountID: target.id,
		BeforeValue: proposal.BeforeValue, AfterValue: proposal.ProposedValue, Status: "propagation_pending", CreatedAt: now, UpdatedAt: now,
		ObserveUntil: now.Add(time.Duration(p.ObservationSeconds) * time.Second), OwnerRevision: target.controlRevision + 1, TargetRevision: target.revision + 1, Auto: automatic}
	if a.Action == "pause_account" {
		a.AfterValue = aiOpsFloat(1)
	}
	if a.Action == "resume_account" {
		a.AfterValue = aiOpsFloat(0)
	}
	raw, _ := json.Marshal(a)
	_, err = tx.ExecContext(ctx, `INSERT INTO ai_ops_actions(id,proposal_id,account_id,status,data,created_at,updated_at,actor_id) VALUES($1,$2,$3,$4,$5,$6,$6,$7)`, a.ID, a.ProposalID, a.AccountID, a.Status, string(raw), now, actorID)
	if err != nil {
		return a, err
	}
	var revision int64
	switch a.Action {
	case "set_concurrency":
		err = tx.QueryRowContext(ctx, `UPDATE accounts SET concurrency=$2,updated_at=NOW() WHERE id=$1 AND ai_ops_revision=$3 AND deleted_at IS NULL RETURNING ai_ops_revision`, target.id, int(*a.AfterValue), target.revision).Scan(&revision)
	case "set_load_factor":
		err = tx.QueryRowContext(ctx, `UPDATE accounts SET load_factor=$2,updated_at=NOW() WHERE id=$1 AND ai_ops_revision=$3 AND deleted_at IS NULL RETURNING ai_ops_revision`, target.id, int(*a.AfterValue), target.revision).Scan(&revision)
	case "pause_account", "resume_account":
		err = tx.QueryRowContext(ctx, `UPDATE accounts SET ai_ops_revision=ai_ops_revision+1 WHERE id=$1 AND ai_ops_revision=$2 AND deleted_at IS NULL RETURNING ai_ops_revision`, target.id, target.revision).Scan(&revision)
	default:
		return a, service.ErrAIOpsBlocked
	}
	if err != nil {
		return a, err
	}
	a.TargetRevision = revision
	paused := target.paused
	if a.Action == "pause_account" {
		paused = true
	}
	if a.Action == "resume_account" {
		paused = false
	}
	controlResult, controlErr := tx.ExecContext(ctx, `INSERT INTO ai_ops_controls(account_id,ai_paused,revision,owner_action_id,updated_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(account_id) DO UPDATE SET ai_paused=EXCLUDED.ai_paused,revision=EXCLUDED.revision,owner_action_id=EXCLUDED.owner_action_id,updated_at=EXCLUDED.updated_at WHERE ai_ops_controls.revision=$6`, target.id, paused, a.OwnerRevision, a.ID, now, target.controlRevision)
	if controlErr != nil {
		return a, controlErr
	}
	controlN, _ := controlResult.RowsAffected()
	if controlN != 1 {
		return a, service.ErrAIOpsConflict
	}
	if err = saveAIOpsAction(ctx, tx, a); err != nil {
		return a, err
	}
	proposal.Status = "applied"
	raw, _ = json.Marshal(proposal)
	_, err = tx.ExecContext(ctx, `UPDATE ai_ops_proposals SET status='applied',data=$2 WHERE id=$1`, proposal.ID, string(raw))
	if err != nil {
		return a, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO scheduler_outbox(event_type,account_id,payload) VALUES('account_changed',$1,$2)`, target.id, `{"ai_ops":true}`)
	return a, err
}

func (r *accountRepository) RollbackAIOps(ctx context.Context, id string, expectedRevision, actorID int64) (service.AIOpsAction, error) {
	if _, err := uuid.Parse(id); err != nil {
		return service.AIOpsAction{}, service.ErrAIOpsBlocked
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	tx, err := r.aiOpsTx(ctx)
	if err != nil {
		return service.AIOpsAction{}, err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := readAIOpsAction(ctx, tx, id)
	if err != nil {
		return a, err
	}
	if a.Status == "rolled_back" {
		return a, nil
	}
	if a.OwnerRevision != expectedRevision {
		return a, service.ErrAIOpsConflict
	}
	a, err = rollbackAIOps(ctx, tx, a, time.Now().UTC(), "operator_rollback")
	if err != nil {
		return a, err
	}
	if err = tx.Commit(); err != nil {
		return a, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, a.AccountID)
	return a, nil
}

func rollbackAIOps(ctx context.Context, tx *sql.Tx, a service.AIOpsAction, now time.Time, reason string) (service.AIOpsAction, error) {
	p, err := readAIOpsPolicy(ctx, tx)
	if err != nil {
		return a, err
	}
	accounts, err := readAIOpsAccounts(ctx, tx, aiOpsScopeIDs(p))
	if err != nil {
		return a, err
	}
	target := accounts[a.AccountID]
	if target.id == 0 || target.revision != a.TargetRevision || target.controlRevision != a.OwnerRevision || target.owner != a.ID || target.managed {
		p.Halted = true
		p.Revision++
		raw, _ := json.Marshal(p)
		if _, err = tx.ExecContext(ctx, `UPDATE ai_ops_policy SET revision=$1,config=$2,updated_at=NOW() WHERE id=1`, p.Revision, string(raw)); err != nil {
			return a, err
		}
		a.Status = "conflict"
		a.ReasonCode = "ownership_changed"
		a.UpdatedAt = now
		return a, saveAIOpsAction(ctx, tx, a)
	}
	if a.Action == "pause_account" {
		passed, e := freshAIOpsProbe(ctx, tx, target, p, now)
		if e != nil {
			return a, e
		}
		if !passed || !target.healthy {
			return haltAIOpsRollback(ctx, tx, p, a, now, "rollback_requires_fresh_probe")
		}
	}
	// Rollback is a new transition: restoring an old higher load or a previous
	// pause still has to fit today's capacity and controller restrictions.
	e, err := captureAIOpsEvidence(ctx, tx, target.id, p, now, accounts)
	if err != nil {
		return a, err
	}
	in := service.AIOpsPreviewInput{AccountID: target.id, ProposedValue: a.BeforeValue, EvidenceObservedAt: now, Action: a.Action}
	if a.Action == "pause_account" {
		in.Action = "resume_account"
		in.ProposedValue = nil
		e.ProbePassed = true
	}
	if a.Action == "resume_account" {
		in.Action = "pause_account"
		in.ProposedValue = nil
	}
	checkPolicy := p
	checkPolicy.Halted = false
	if checkPolicy.Mode == "disabled" {
		checkPolicy.Mode = "approve_each"
	}
	reasons := aiOpsReasons(checkPolicy, in, target, e, accounts, now)
	reasons = slices.DeleteFunc(reasons, func(reason string) bool { return reason == "step_budget" })
	if len(reasons) > 0 {
		return haltAIOpsRollback(ctx, tx, p, a, now, "rollback_capacity_or_evidence_blocked")
	}
	var revision int64
	switch a.Action {
	case "set_concurrency":
		if a.BeforeValue == nil {
			return a, service.ErrAIOpsBlocked
		}
		err = tx.QueryRowContext(ctx, `UPDATE accounts SET concurrency=$2,updated_at=NOW() WHERE id=$1 AND ai_ops_revision=$3 RETURNING ai_ops_revision`, target.id, int(*a.BeforeValue), target.revision).Scan(&revision)
	case "set_load_factor":
		if a.BeforeValue == nil {
			return a, service.ErrAIOpsBlocked
		}
		err = tx.QueryRowContext(ctx, `UPDATE accounts SET load_factor=$2,updated_at=NOW() WHERE id=$1 AND ai_ops_revision=$3 RETURNING ai_ops_revision`, target.id, int(*a.BeforeValue), target.revision).Scan(&revision)
	case "pause_account", "resume_account":
		err = tx.QueryRowContext(ctx, `UPDATE accounts SET ai_ops_revision=ai_ops_revision+1 WHERE id=$1 AND ai_ops_revision=$2 RETURNING ai_ops_revision`, target.id, target.revision).Scan(&revision)
	default:
		return a, service.ErrAIOpsBlocked
	}
	if err != nil {
		return a, err
	}
	paused := target.paused
	if a.Action == "pause_account" || a.Action == "resume_account" {
		paused = a.BeforeValue != nil && *a.BeforeValue == 1
	}
	res, err := tx.ExecContext(ctx, `UPDATE ai_ops_controls SET ai_paused=$2,revision=revision+1,owner_action_id=CASE WHEN $2 THEN $5::uuid ELSE NULL END,updated_at=$3 WHERE account_id=$1 AND revision=$4 AND owner_action_id=$5`, target.id, paused, now, a.OwnerRevision, a.ID)
	if err != nil {
		return a, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return a, service.ErrAIOpsConflict
	}
	a.Status = "rolled_back"
	a.ReasonCode = reason
	a.UpdatedAt = now
	a.TargetRevision = revision
	if paused {
		a.OwnerRevision = target.controlRevision + 1
	}
	if err = saveAIOpsAction(ctx, tx, a); err != nil {
		return a, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO scheduler_outbox(event_type,account_id,payload) VALUES('account_changed',$1,$2)`, target.id, `{"ai_ops_rollback":true}`)
	return a, err
}

func haltAIOpsRollback(ctx context.Context, tx *sql.Tx, p service.AIOpsPolicy, a service.AIOpsAction, now time.Time, reason string) (service.AIOpsAction, error) {
	p.Halted = true
	p.Revision++
	raw, _ := json.Marshal(p)
	if _, err := tx.ExecContext(ctx, `UPDATE ai_ops_policy SET revision=$1,config=$2,updated_at=NOW() WHERE id=1`, p.Revision, string(raw)); err != nil {
		return a, err
	}
	a.Status = "inconclusive"
	a.ReasonCode = reason
	a.UpdatedAt = now
	return a, saveAIOpsAction(ctx, tx, a)
}

func (r *accountRepository) ListAIOpsActions(ctx context.Context) ([]service.AIOpsAction, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := r.sql.QueryContext(ctx, `SELECT data FROM ai_ops_actions ORDER BY created_at DESC,id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.AIOpsAction{}
	for rows.Next() {
		var raw []byte
		var a service.AIOpsAction
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &a) != nil {
			return nil, service.ErrAIOpsUnavailable
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *accountRepository) AIOpsRuntime(ctx context.Context, ids []int64) (map[int64]service.AIOpsRuntime, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out := map[int64]service.AIOpsRuntime{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT a.id,a.concurrency,a.load_factor,a.ai_ops_revision,COALESCE(c.ai_paused,false) FROM accounts a LEFT JOIN ai_ops_controls c ON c.account_id=a.id WHERE a.id=ANY($1) AND a.deleted_at IS NULL`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var v service.AIOpsRuntime
		var lf sql.NullInt64
		if err = rows.Scan(&id, &v.Concurrency, &lf, &v.Revision, &v.Paused); err != nil {
			return nil, err
		}
		if lf.Valid {
			x := int(lf.Int64)
			v.LoadFactor = &x
		}
		out[id] = v
	}
	return out, rows.Err()
}

// Include legacy timed-out records so an upgrade cannot silently release an
// unresolved reservation created by an earlier controller version.
func aiOpsPropagationUnresolved(a service.AIOpsAction) bool {
	return a.Status == "propagation_pending" || (a.Status == "inconclusive" && a.ReasonCode == "propagation_unconfirmed")
}

func (r *accountRepository) ReconcileAIOps(ctx context.Context) error {
	if err := r.expireAIOpsProbes(ctx); err != nil {
		return err
	}
	items, err := r.ListAIOpsActions(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		eligible := aiOpsPropagationUnresolved(item) || item.Status == "observing" || (item.Action == "pause_account" && item.Status == "inconclusive" && item.ReasonCode != "pause_expired_requires_fresh_probe")
		if !eligible {
			continue
		}
		if err = r.reconcileAIOpsAction(ctx, item.ID); err != nil && !errors.Is(err, service.ErrAIOpsBlocked) {
			return err
		}
	}
	return nil
}

func (r *accountRepository) reconcileAIOpsAction(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	tx, err := r.aiOpsTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := readAIOpsAction(ctx, tx, id)
	if err != nil {
		return err
	}
	eligible := aiOpsPropagationUnresolved(a) || a.Status == "observing" || (a.Action == "pause_account" && a.Status == "inconclusive")
	if !eligible {
		return nil
	}
	now := time.Now().UTC()
	p, err := readAIOpsPolicy(ctx, tx)
	if err != nil {
		return err
	}
	accounts, err := readAIOpsAccounts(ctx, tx, aiOpsScopeIDs(p))
	if err != nil {
		return err
	}
	target := accounts[a.AccountID]
	a.UpdatedAt = now
	if target.id == 0 || target.revision != a.TargetRevision || target.owner != a.ID || target.controlRevision != a.OwnerRevision {
		a.Status = "conflict"
		a.ReasonCode = "ownership_changed"
	} else if a.Action == "pause_account" && now.Sub(a.CreatedAt) >= time.Duration(max(60, p.MaxPauseSeconds))*time.Second {
		a.Status = "inconclusive"
		a.ReasonCode = "pause_expired_requires_fresh_probe"
		p.Halted = true
		p.Revision++
		raw, _ := json.Marshal(p)
		if _, err = tx.ExecContext(ctx, `UPDATE ai_ops_policy SET revision=$1,config=$2,updated_at=NOW() WHERE id=1`, p.Revision, string(raw)); err != nil {
			return err
		}
	} else if aiOpsPropagationUnresolved(a) {
		propagated := a.Action == "pause_account" || a.Action == "resume_account"
		if !propagated && r.schedulerCache != nil {
			cacheCtx, done := context.WithTimeout(ctx, time.Second)
			cached, e := r.schedulerCache.GetAccount(cacheCtx, a.AccountID)
			done()
			if e == nil && cached != nil && a.AfterValue != nil {
				if a.Action == "set_concurrency" {
					propagated = cached.Concurrency == int(*a.AfterValue)
				} else {
					propagated = cached.LoadFactor != nil && *cached.LoadFactor == int(*a.AfterValue)
				}
			}
		}
		if propagated {
			a.Status = "observing"
			a.ReasonCode = ""
			a.ObservationStartedAt = now
			a.ObserveUntil = now.Add(time.Duration(p.ObservationSeconds) * time.Second)
		} else if !now.Before(a.ObserveUntil) {
			// Keep reserving capacity until propagation is confirmed or an
			// explicit rollback/ownership change resolves this action. Clearing
			// the policy halt alone must not release this reservation.
			a.Status = "propagation_pending"
			a.ReasonCode = "propagation_unconfirmed"
			if !p.Halted {
				p.Halted = true
				p.Revision++
				raw, _ := json.Marshal(p)
				if _, err = tx.ExecContext(ctx, `UPDATE ai_ops_policy SET revision=$1,config=$2,updated_at=NOW() WHERE id=1`, p.Revision, string(raw)); err != nil {
					return err
				}
			}
		}
	} else if !now.Before(a.ObserveUntil) {
		e, eErr := captureAIOpsEvidence(ctx, tx, a.AccountID, p, now, accounts)
		if eErr != nil {
			return eErr
		}
		proposal, eErr := readAIOpsProposal(ctx, tx, a.ProposalID)
		if eErr != nil {
			return eErr
		}
		if a.Action == "pause_account" {
			a.Status = "inconclusive"
			a.ReasonCode = "paused_requires_fresh_probe"
		} else if a.ObservationStartedAt.IsZero() || e.WindowStart.Before(a.ObservationStartedAt) || e.Samples < p.MinSamples || !e.ModelCoverageComplete {
			a.Status = "inconclusive"
			a.ReasonCode = "insufficient_post_change_evidence"
		} else if !service.ComparableAIOpsEvidence(proposal.Evidence, e) {
			a.Status = "inconclusive"
			a.ReasonCode = "post_change_cohort_changed"
		} else if e.ErrorRate > proposal.Evidence.ErrorRate+p.RollbackErrorRateDelta {
			a, eErr = rollbackAIOps(ctx, tx, a, now, "error_rate_regressed")
			if eErr != nil {
				return eErr
			}
		} else {
			a.Status = "succeeded"
			a.ReasonCode = "post_change_verified"
		}
	}
	if err = saveAIOpsAction(ctx, tx, a); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *accountRepository) AIOpsAutoTick(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	tx, err := r.aiOpsTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := readAIOpsPolicy(ctx, tx)
	if err != nil {
		return err
	}
	if p.Mode != "bounded_auto" || p.Halted || service.ValidateAIOpsPolicy(p) != nil {
		return nil
	}
	now := time.Now().UTC()
	accounts, err := readAIOpsAccounts(ctx, tx, aiOpsScopeIDs(p))
	if err != nil {
		return err
	}
	for _, scope := range p.Accounts {
		a := accounts[scope.AccountID]
		if a.id == 0 || a.managed || !a.healthy {
			continue
		}
		if a.paused {
			if !slices.Contains(p.AutoActions, "resume_account") {
				continue
			}
			passed, e := freshAIOpsProbe(ctx, tx, a, p, now)
			if e != nil {
				return e
			}
			if !passed {
				continue
			}
			in := service.AIOpsPreviewInput{RunID: "auto:" + uuid.NewString(), Action: "resume_account", AccountID: a.id, EvidenceObservedAt: now}
			proposal, e := previewAIOps(ctx, tx, in, 0, now)
			if e != nil {
				return e
			}
			if proposal.Status != "ready" {
				continue
			}
			if _, e = applyAIOps(ctx, tx, proposal.ID, proposal.Digest, 0, true, now); e != nil {
				return e
			}
			return tx.Commit()
		}
		e, err := captureAIOpsEvidence(ctx, tx, a.id, p, now, accounts)
		if err != nil {
			return err
		}
		if e.Samples < p.MinSamples || !e.ModelCoverageComplete || e.ErrorRate < p.ErrorRateThreshold {
			continue
		}
		in := service.AIOpsPreviewInput{RunID: "auto:" + uuid.NewString(), Action: "set_concurrency", AccountID: a.id, EvidenceObservedAt: now}
		value := math.Ceil(float64(a.concurrency) * (1 - p.MaxStepPercent/100))
		in.ProposedValue = &value
		if !slices.Contains(p.AutoActions, in.Action) || value >= float64(a.concurrency) || value < float64(p.MinConcurrency) {
			if !slices.Contains(p.AutoActions, "pause_account") {
				continue
			}
			in.Action = "pause_account"
			in.ProposedValue = nil
		}
		proposal, err := previewAIOps(ctx, tx, in, 0, now)
		if err != nil {
			return err
		}
		if proposal.Status != "ready" {
			continue
		}
		if _, err = applyAIOps(ctx, tx, proposal.ID, proposal.Digest, 0, true, now); err != nil {
			return err
		}
		return tx.Commit()
	}
	return nil
}

func (r *accountRepository) expireAIOpsProbes(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	tx, err := r.aiOpsTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE ai_ops_probes SET status='unknown',data=data||jsonb_build_object('status','unknown','reason_code','probe_completion_unknown','finished_at',NOW()) WHERE status='reserved' AND expires_at<=NOW()`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func freshAIOpsProbe(ctx context.Context, tx *sql.Tx, a aiOpsAccount, p service.AIOpsPolicy, now time.Time) (bool, error) {
	scope, err := aiOpsOwnedScope(ctx, tx, a)
	if err != nil {
		return false, err
	}
	if !aiOpsRecoveryScope(scope) {
		return false, nil
	}
	var passed bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_ops_probes WHERE account_id=$1 AND status='passed' AND created_at>=$2 AND expires_at>$3 AND (data->>'target_revision')::bigint=$4 AND (data->>'owner_revision')::bigint=$5 AND data->>'model'=$6 AND data->>'effort'=$7 AND data->>'protocol'=$8)`, a.id, now.Add(-time.Duration(max(30, p.EvidenceMaxAgeSeconds))*time.Second), now, a.revision, a.controlRevision, scope.Model, scope.Effort, scope.Protocol).Scan(&passed)
	return passed, err
}

func aiOpsRecoveryScope(e service.AIOpsEvidenceSnapshot) bool {
	return e.Model != "" && len(e.Model) <= 100 && e.Protocol == "http" && slices.Contains([]string{"minimal", "low", "medium", "high", "xhigh", "max"}, e.Effort)
}

func aiOpsOwnedScope(ctx context.Context, tx *sql.Tx, a aiOpsAccount) (service.AIOpsEvidenceSnapshot, error) {
	if a.owner == "" {
		return service.AIOpsEvidenceSnapshot{}, nil
	}
	owner, err := readAIOpsAction(ctx, tx, a.owner)
	if err != nil {
		return service.AIOpsEvidenceSnapshot{}, err
	}
	if owner.AccountID != a.id || owner.OwnerRevision != a.controlRevision || owner.TargetRevision != a.revision {
		return service.AIOpsEvidenceSnapshot{}, service.ErrAIOpsConflict
	}
	proposal, err := readAIOpsProposal(ctx, tx, owner.ProposalID)
	if err != nil {
		return service.AIOpsEvidenceSnapshot{}, err
	}
	return proposal.Evidence, nil
}

func (r *accountRepository) ReserveAIOpsProbe(ctx context.Context, accountID int64, model string, actorID int64) (service.AIOpsProbe, error) {
	probe := service.AIOpsProbe{}
	if accountID <= 0 || model == "" || len(model) > 100 || actorID <= 0 {
		return probe, service.ErrAIOpsBlocked
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	tx, err := r.aiOpsTx(ctx)
	if err != nil {
		return probe, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := readAIOpsPolicy(ctx, tx)
	if err != nil {
		return probe, err
	}
	// An explicit recovery probe remains possible while the emergency halt is
	// latched. It does not authorize a production mutation or clear the halt.
	if p.Mode == "disabled" || service.ValidateAIOpsPolicy(p) != nil || aiOpsDomain(p, accountID) == "" {
		return probe, service.ErrAIOpsBlocked
	}
	accounts, err := readAIOpsAccounts(ctx, tx, aiOpsScopeIDs(p))
	if err != nil {
		return probe, err
	}
	a := accounts[accountID]
	if a.id == 0 || !a.paused || a.owner == "" || a.managed || !a.healthy || !a.quotaKnown || !slices.Equal(a.groups, []int64{p.GroupID}) {
		return probe, service.ErrAIOpsBlocked
	}
	scope, err := aiOpsOwnedScope(ctx, tx, a)
	if err != nil {
		return probe, err
	}
	if !aiOpsRecoveryScope(scope) || scope.Model != model {
		return probe, service.ErrAIOpsBlocked
	}
	if supported, e := aiOpsModelSupported(ctx, tx, accountID, p.GroupID, model); e != nil {
		return probe, e
	} else if !supported {
		return probe, service.ErrAIOpsBlocked
	}
	now := time.Now().UTC()
	var daily int
	var last sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE created_at>=date_trunc('day',$1::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),MAX(created_at) FILTER(WHERE account_id=ANY($2)) FROM ai_ops_probes`, now, pq.Array(aiOpsDomainIDs(p, accountID))).Scan(&daily, &last)
	if err != nil {
		return probe, err
	}
	if daily >= min(3, p.DailyActionLimit) || (last.Valid && now.Sub(last.Time) < time.Duration(p.CooldownSeconds)*time.Second) {
		return probe, service.ErrAIOpsBlocked
	}
	probe = service.AIOpsProbe{ID: uuid.NewString(), AccountID: accountID, Model: model, Effort: scope.Effort, Protocol: scope.Protocol, Status: "reserved", CreatedAt: now, ExpiresAt: now.Add(time.Duration(p.EvidenceMaxAgeSeconds) * time.Second), OwnerRevision: a.controlRevision, TargetRevision: a.revision}
	raw, _ := json.Marshal(probe)
	_, err = tx.ExecContext(ctx, `INSERT INTO ai_ops_probes(id,account_id,status,data,created_at,expires_at,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, probe.ID, accountID, probe.Status, string(raw), now, probe.ExpiresAt, actorID)
	if err != nil {
		return probe, err
	}
	return probe, tx.Commit()
}

func (r *accountRepository) CompleteAIOpsProbe(ctx context.Context, id string, passed bool, actorID int64) (service.AIOpsProbe, error) {
	probe := service.AIOpsProbe{}
	if _, err := uuid.Parse(id); err != nil || actorID <= 0 {
		return probe, service.ErrAIOpsBlocked
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	tx, err := r.aiOpsTx(ctx)
	if err != nil {
		return probe, err
	}
	defer func() { _ = tx.Rollback() }()
	var raw []byte
	var creator int64
	err = tx.QueryRowContext(ctx, `SELECT data,actor_id FROM ai_ops_probes WHERE id=$1 FOR UPDATE`, id).Scan(&raw, &creator)
	if err != nil {
		return probe, err
	}
	if json.Unmarshal(raw, &probe) != nil {
		return probe, service.ErrAIOpsUnavailable
	}
	if probe.Status != "reserved" || creator != actorID {
		return probe, service.ErrAIOpsConflict
	}
	accounts, err := readAIOpsAccounts(ctx, tx, []int64{probe.AccountID})
	if err != nil {
		return probe, err
	}
	a := accounts[probe.AccountID]
	scope, scopeErr := aiOpsOwnedScope(ctx, tx, a)
	if scopeErr != nil {
		return probe, scopeErr
	}
	now := time.Now().UTC()
	probe.FinishedAt = &now
	probe.Status = "failed"
	probe.ReasonCode = "probe_failed"
	if a.id == 0 || a.revision != probe.TargetRevision || a.controlRevision != probe.OwnerRevision || !a.paused || a.managed || !a.healthy || !a.quotaKnown || !now.Before(probe.ExpiresAt) || !aiOpsRecoveryScope(scope) || scope.Model != probe.Model || scope.Effort != probe.Effort || scope.Protocol != probe.Protocol {
		probe.Status = "unknown"
		probe.ReasonCode = "stale_probe_ownership"
	} else if passed {
		probe.Status = "passed"
		probe.ReasonCode = "complete_terminal_verified"
	}
	raw, _ = json.Marshal(probe)
	_, err = tx.ExecContext(ctx, `UPDATE ai_ops_probes SET status=$2,data=$3 WHERE id=$1`, id, probe.Status, string(raw))
	if err != nil {
		return probe, err
	}
	return probe, tx.Commit()
}

func (r *accountRepository) ListAIOpsProbes(ctx context.Context) ([]service.AIOpsProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := r.sql.QueryContext(ctx, `SELECT data FROM ai_ops_probes ORDER BY created_at DESC,id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []service.AIOpsProbe{}
	for rows.Next() {
		var raw []byte
		var p service.AIOpsProbe
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &p) != nil {
			return nil, service.ErrAIOpsUnavailable
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
