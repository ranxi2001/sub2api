package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lib/pq"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Aliases are versioned in measurement_schema. Missing historical ingress is
// unknown; upstream_endpoint is deliberately never used to infer ingress.
const priorityLifecycleEndpointSQL = "CASE rtrim(COALESCE(inbound_endpoint,''),'/') WHEN '/responses' THEN '/v1/responses' WHEN '/v1/responses' THEN '/v1/responses' WHEN '/chat/completions' THEN '/v1/chat/completions' WHEN '/v1/chat/completions' THEN '/v1/chat/completions' WHEN '/messages' THEN '/v1/messages' WHEN '/v1/messages' THEN '/v1/messages' ELSE 'unknown' END"

var _ service.PriorityLifecycleRepository = (*usageLogRepository)(nil)

// One short primary transaction fences worker ownership, publishes statistics
// and immutable shadow evidence, and acknowledges the reliable outbox together.
// The rolling compensation query includes new accounts and ready-empty scopes;
// it does not depend on any instance's last candidate pool.
func (r *usageLogRepository) AdvancePriorityLifecycle(ctx context.Context, cfg service.PrioritySchedulingConfig) (result error) {
	if r.db == nil {
		return errors.New("lifecycle worker requires primary database")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	var attempted []string
	defer func() {
		_ = tx.Rollback()
		if result != nil {
			report, end := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer end()
			for _, identity := range attempted {
				_, _ = r.db.ExecContext(report, "UPDATE priority_lifecycle_scopes SET last_error='refresh_failed',refresh_state='ERROR' WHERE identity=$1", identity)
			}
		}
	}()
	var locked bool
	if err = tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock(6142026)").Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	var now time.Time
	if err = tx.QueryRowContext(ctx, "SELECT transaction_timestamp()").Scan(&now); err != nil {
		return err
	}
	measurement, spec, revision := service.PriorityLifecycleVersions(cfg)
	// Every explicitly configured model/group has an empty-ready population,
	// even before any traffic; unknown historical ingress stays separate.
	for _, model := range cfg.Models {
		groups := cfg.GroupIDs
		if len(groups) == 0 {
			groups = []int64{0}
		}
		for _, id := range groups {
			for _, endpoint := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
				for _, kind := range []service.RequestType{service.RequestTypeSync, service.RequestTypeStream, service.RequestTypeWSV2} {
					scope := service.PriorityLifecycleScope{Model: model, Endpoint: endpoint, RequestType: kind}
					if id != 0 {
						value := id
						scope.GroupID = &value
					}
					if _, err = tx.ExecContext(ctx, "INSERT INTO priority_lifecycle_scopes(identity) VALUES($1) ON CONFLICT DO NOTHING", scope.Identity()); err != nil {
						return err
					}
				}
			}
		}
	}
	// Includes quiet scopes forever. Fair oldest-first rotation is persistent;
	// hot/new scopes cannot monopolize progress or block a >32-scope universe.
	rows, err := tx.QueryContext(ctx, "SELECT identity,source_revision FROM priority_lifecycle_scopes ORDER BY last_processed_at NULLS FIRST,identity::text LIMIT 8")
	if err != nil {
		return err
	}
	type work struct {
		scope  service.PriorityLifecycleScope
		source int64
	}
	batch := make([]work, 0, 8)
	for rows.Next() {
		var raw []byte
		var w work
		if err = rows.Scan(&raw, &w.source); err != nil {
			_ = rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &w.scope); err != nil {
			_ = rows.Close()
			return err
		}
		w.scope.MeasurementSchema, w.scope.StatisticsSpec = measurement, spec
		batch = append(batch, w)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	scoped := &usageLogRepository{sql: tx}
	for _, w := range batch {
		scope := w.scope
		attempted = append(attempted, scope.Identity())
		pending, err := tx.QueryContext(ctx, "SELECT sequence FROM priority_lifecycle_outbox o WHERE processed_at IS NULL AND (scope_identity=$1 OR (scope_identity IS NULL AND EXISTS(SELECT 1 FROM usage_logs u WHERE u.id=o.usage_log_id AND priority_lifecycle_scope_identity(u.group_id,COALESCE(NULLIF(u.requested_model,''),u.model),u.inbound_endpoint,u.request_type)=$1))) ORDER BY sequence", scope.Identity())
		if err != nil {
			return err
		}
		sequences := make([]int64, 0)
		var watermark int64
		for pending.Next() {
			var seq int64
			if err = pending.Scan(&seq); err != nil {
				_ = pending.Close()
				return err
			}
			sequences = append(sequences, seq)
			if seq > watermark {
				watermark = seq
			}
		}
		err = pending.Err()
		_ = pending.Close()
		if err != nil {
			return err
		}
		signals := make(map[int64]service.PrioritySchedulingSignal)
		after := int64(0)
		for {
			accounts, err := tx.QueryContext(ctx, "SELECT id FROM accounts WHERE platform='openai' AND deleted_at IS NULL AND id>$1 ORDER BY id LIMIT 500", after)
			if err != nil {
				return err
			}
			ids := make([]int64, 0, 500)
			for accounts.Next() {
				var id int64
				if err = accounts.Scan(&id); err != nil {
					_ = accounts.Close()
					return err
				}
				ids = append(ids, id)
			}
			err = accounts.Err()
			_ = accounts.Close()
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			kind, endpoint := scope.RequestType, scope.Endpoint
			page, err := scoped.ReadPrioritySchedulingSignals(ctx, service.PrioritySchedulingQuery{AccountIDs: ids, Model: scope.Model, GroupID: scope.GroupID, InboundEndpoint: &endpoint, RequestType: &kind, HalfOpen: true, LatencyPercentile: cfg.LatencyPercentile, LatencyMeasurement: cfg.LatencyMeasurement, TargetTTFTMs: cfg.TargetTTFTMs, UsageSince: now.Add(-time.Duration(cfg.WindowMinutes) * time.Minute), UsageUntil: now, QualitySince: now.Add(-time.Duration(cfg.QualityMaxAgeHours) * time.Hour)})
			if err != nil {
				return err
			}
			for _, id := range ids {
				value, ok := page[id]
				if !ok {
					return errors.New("incomplete lifecycle account page")
				}
				signals[id] = value
			}
			after = ids[len(ids)-1]
		}
		stats := service.PriorityLifecycleStatistics{Scope: scope, PolicyRevision: revision, SourceRevision: w.source, Watermark: watermark, WindowStart: now.Add(-time.Duration(cfg.WindowMinutes) * time.Minute), WindowEnd: now, ObservedAt: now, CollectedThrough: now, PublishedAt: time.Now().UTC(), SourceVisibility: "primary_repeatable_read_visible_commits; allocation_sequence_not_commit_order", RefreshState: "IDLE", Signals: signals}
		raw, err := json.Marshal(stats)
		if err != nil {
			return err
		}
		// Generation CAS and the single primary fencing owner reject older
		// source revisions. Watermark is diagnostic, never a permanent cursor.
		err = tx.QueryRowContext(ctx, "INSERT INTO priority_lifecycle_statistics(scope_key,generation,watermark,observed_at,snapshot,source_revision) VALUES($1,1,$2,$3,$4,$5) ON CONFLICT(scope_key) DO UPDATE SET generation=priority_lifecycle_statistics.generation+1,watermark=GREATEST(priority_lifecycle_statistics.watermark,EXCLUDED.watermark),observed_at=EXCLUDED.observed_at,snapshot=EXCLUDED.snapshot,source_revision=EXCLUDED.source_revision WHERE priority_lifecycle_statistics.source_revision<=EXCLUDED.source_revision RETURNING generation", scope.Key(), watermark, now, string(raw), w.source).Scan(&stats.Generation)
		if err != nil {
			return err
		}
		if err = publishPriorityLifecyclePolicies(ctx, tx, scope, cfg, signals, revision, now); err != nil {
			return err
		}
		if len(sequences) > 0 {
			if _, err = tx.ExecContext(ctx, "UPDATE priority_lifecycle_outbox SET processed_at=clock_timestamp() WHERE sequence=ANY($1) AND processed_at IS NULL", pq.Array(sequences)); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE priority_lifecycle_scopes SET published_revision=$2,last_collected_at=$3,last_processed_at=clock_timestamp(),last_error='',refresh_state='IDLE' WHERE identity=$1 AND source_revision=$2", scope.Identity(), w.source, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *usageLogRepository) RegisterPriorityLifecycleScope(ctx context.Context, scope service.PriorityLifecycleScope) error {
	_, err := r.sql.ExecContext(ctx, "INSERT INTO priority_lifecycle_scopes(identity) VALUES($1) ON CONFLICT DO NOTHING", scope.Identity())
	return err
}

func publishPriorityLifecyclePolicies(ctx context.Context, tx *sql.Tx, scope service.PriorityLifecycleScope, cfg service.PrioritySchedulingConfig, signals map[int64]service.PrioritySchedulingSignal, revision string, now time.Time) error {
	for id, signal := range signals {
		if signal.Samples < cfg.MinSamples || signal.SelectedTTFTMs == nil || *signal.SelectedTTFTMs <= float64(cfg.TargetTTFTMs) || signal.LatestBadLatencyAt == nil {
			continue
		}
		evidence := fmt.Sprintf("legacy-shadow/%s/%d/%d", scope.MeasurementSchema, id, signal.LatestBadLatencyAt.UnixNano())
		policy := service.PriorityLifecyclePolicy{AccountID: id, State: "PENALIZED_SHADOW", EvidenceID: evidence, EvidenceAt: *signal.LatestBadLatencyAt, ExpiresAt: signal.LatestBadLatencyAt.Add(time.Duration(cfg.LatencyPenaltyMinutes) * time.Minute), PolicyRevision: revision, ObservedValue: signal.SelectedTTFTMs, Threshold: cfg.TargetTTFTMs, Percentile: cfg.LatencyPercentile, Mode: cfg.LatencyPenaltyMode, Rule: "legacy_shadow_v1; v2 trigger and renewal pending"}
		raw, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "INSERT INTO priority_lifecycle_policy(scope_key,account_id,evidence_id,evidence_at,policy_revision,expires_at,state) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(scope_key,account_id) DO UPDATE SET evidence_id=EXCLUDED.evidence_id,evidence_at=EXCLUDED.evidence_at,policy_revision=EXCLUDED.policy_revision,expires_at=EXCLUDED.expires_at,state=EXCLUDED.state WHERE priority_lifecycle_policy.evidence_at<EXCLUDED.evidence_at", scope.PolicyKey(), id, evidence, policy.EvidenceAt, revision, policy.ExpiresAt, string(raw))
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed > 0 {
			if _, err = tx.ExecContext(ctx, "INSERT INTO priority_lifecycle_evidence(scope_key,account_id,evidence_id,evidence) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", scope.PolicyKey(), id, evidence, string(raw)); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO priority_lifecycle_policy_events(scope_key,account_id,evidence_id,kind,event_at,event) VALUES($1,$2,$3,'OBSERVED_SHADOW',$4,$5) ON CONFLICT DO NOTHING", scope.PolicyKey(), id, evidence, policy.EvidenceAt, string(raw)); err != nil {
				return err
			}
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT account_id,state,evaluated_revision FROM priority_lifecycle_policy WHERE scope_key=$1 FOR UPDATE", scope.PolicyKey())
	if err != nil {
		return err
	}
	type evaluation struct {
		id       int64
		policy   service.PriorityLifecyclePolicy
		revision string
	}
	values := make([]evaluation, 0)
	for rows.Next() {
		var value evaluation
		var raw []byte
		if err = rows.Scan(&value.id, &raw, &value.revision); err != nil {
			_ = rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &value.policy); err != nil {
			_ = rows.Close()
			return err
		}
		values = append(values, value)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, value := range values {
		p := value.policy
		if value.revision != "" && value.revision != revision {
			raw, err := json.Marshal(p)
			if err != nil {
				return err
			}
			// Configuration reevaluation is a distinct event. It records the
			// new revision in kind and never rewrites the original evidence.
			if _, err = tx.ExecContext(ctx, "INSERT INTO priority_lifecycle_policy_events(scope_key,account_id,evidence_id,kind,event_at,event) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING", scope.PolicyKey(), value.id, p.EvidenceID, "CONFIG_REEVALUATED:"+revision, now, string(raw)); err != nil {
				return err
			}
		}
		if !now.Before(p.ExpiresAt) && p.State == "PENALIZED_SHADOW" {
			p.State = "RECOVERY_SHADOW"
			raw, err := json.Marshal(p)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO priority_lifecycle_policy_events(scope_key,account_id,evidence_id,kind,event_at,event) VALUES($1,$2,$3,'EXPIRED',$4,$5) ON CONFLICT DO NOTHING", scope.PolicyKey(), value.id, p.EvidenceID, p.ExpiresAt, string(raw)); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE priority_lifecycle_policy SET state=$3 WHERE scope_key=$1 AND account_id=$2 AND evidence_id=$4", scope.PolicyKey(), value.id, string(raw), p.EvidenceID); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE priority_lifecycle_policy SET evaluated_revision=$3,evaluated_at=$4 WHERE scope_key=$1 AND account_id=$2", scope.PolicyKey(), value.id, revision, now); err != nil {
			return err
		}
	}
	return nil
}

func (r *usageLogRepository) ReadPriorityLifecycle(ctx context.Context, scope service.PriorityLifecycleScope) (*service.PriorityLifecycleStatistics, []service.PriorityLifecyclePolicy, error) {
	if r.db != nil {
		tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			return nil, nil, err
		}
		defer func() { _ = tx.Rollback() }()
		read := &usageLogRepository{sql: tx}
		stats, policies, err := read.ReadPriorityLifecycle(ctx, scope)
		if err != nil {
			return nil, nil, err
		}
		return stats, policies, tx.Commit()
	}

	rows, err := r.sql.QueryContext(ctx, "SELECT generation,watermark,observed_at,snapshot FROM priority_lifecycle_statistics WHERE scope_key=$1", scope.Key())
	if err != nil {
		return nil, nil, err
	}
	var stats *service.PriorityLifecycleStatistics
	if rows.Next() {
		var value service.PriorityLifecycleStatistics
		var raw []byte
		var generation, watermark int64
		var observed time.Time
		if err = rows.Scan(&generation, &watermark, &observed, &raw); err == nil {
			err = json.Unmarshal(raw, &value)
		}
		if err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		value.Generation, value.Watermark, value.ObservedAt = generation, watermark, observed
		stats = &value
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, nil, err
	}
	if stats != nil {
		status, err := r.sql.QueryContext(ctx, "SELECT source_revision,published_revision,last_error,refresh_state FROM priority_lifecycle_scopes WHERE identity=$1", scope.Identity())
		if err != nil {
			return nil, nil, err
		}
		if status.Next() {
			var source, published int64
			if err = status.Scan(&source, &published, &stats.LastError, &stats.RefreshState); err != nil {
				_ = status.Close()
				return nil, nil, err
			}
			if source > published {
				stats.RefreshState = "QUEUED"
			}
		}
		err = status.Err()
		_ = status.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	rows, err = r.sql.QueryContext(ctx, "SELECT state FROM priority_lifecycle_policy WHERE scope_key=$1 ORDER BY account_id", scope.PolicyKey())
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	policies := make([]service.PriorityLifecyclePolicy, 0)
	for rows.Next() {
		var raw []byte
		var value service.PriorityLifecyclePolicy
		if err = rows.Scan(&raw); err != nil {
			return nil, nil, err
		}
		if err = json.Unmarshal(raw, &value); err != nil {
			return nil, nil, err
		}
		policies = append(policies, value)
	}
	return stats, policies, rows.Err()
}

func (r *usageLogRepository) RecordPriorityLifecycleDecision(ctx context.Context, d service.PriorityLifecycleDecision) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = r.sql.ExecContext(ctx, "INSERT INTO priority_lifecycle_decisions(decision_id,scope_key,record) VALUES($1,$2,$3) ON CONFLICT(decision_id) DO NOTHING", d.ID, d.Scope.PolicyKey(), string(raw))
	return err
}

func (r *usageLogRepository) ReadPriorityLifecycleDecisions(ctx context.Context, scope service.PriorityLifecycleScope, after int64) ([]service.PriorityLifecycleDecision, error) {
	rows, err := r.sql.QueryContext(ctx, "SELECT sequence,record FROM priority_lifecycle_decisions WHERE scope_key=$1 AND sequence>$2 ORDER BY sequence LIMIT 100", scope.PolicyKey(), after)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.PriorityLifecycleDecision, 0)
	for rows.Next() {
		var raw []byte
		var d service.PriorityLifecycleDecision
		var sequence int64
		if err = rows.Scan(&sequence, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &d); err != nil {
			return nil, err
		}
		d.Sequence = sequence
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *usageLogRepository) ReadPriorityLifecycleEvents(ctx context.Context, scope service.PriorityLifecycleScope, after int64) ([]service.PriorityLifecycleEvent, error) {
	rows, err := r.sql.QueryContext(ctx, "SELECT sequence,kind,event_at,recorded_at,event FROM priority_lifecycle_policy_events WHERE scope_key=$1 AND sequence>$2 ORDER BY sequence LIMIT 100", scope.PolicyKey(), after)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.PriorityLifecycleEvent, 0)
	for rows.Next() {
		var raw []byte
		var d service.PriorityLifecycleEvent
		if err = rows.Scan(&d.Sequence, &d.Kind, &d.EventAt, &d.RecordedAt, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &d.Policy); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
