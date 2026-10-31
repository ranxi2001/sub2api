package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
func (r *usageLogRepository) AdvancePriorityLifecycle(ctx context.Context, cfg service.PrioritySchedulingConfig) error {
	if r.db == nil {
		return errors.New("lifecycle worker requires primary database")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var locked bool
	if err = tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock(6142026)").Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	now := time.Now().UTC()
	since := now.Add(-time.Duration(cfg.WindowMinutes) * time.Minute)
	var watermark int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM priority_lifecycle_outbox").Scan(&watermark); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("SELECT DISTINCT group_id,COALESCE(NULLIF(requested_model,''),model),%s,request_type FROM usage_logs WHERE created_at >= $1 AND created_at < $2 ORDER BY 2,3,4 LIMIT 33", priorityLifecycleEndpointSQL), since, now)
	if err != nil {
		return err
	}
	measurement, spec, revision := service.PriorityLifecycleVersions(cfg)
	scopes := make([]service.PriorityLifecycleScope, 0)
	for rows.Next() {
		var scope service.PriorityLifecycleScope
		if err = rows.Scan(&scope.GroupID, &scope.Model, &scope.Endpoint, &scope.RequestType); err != nil {
			_ = rows.Close()
			return err
		}
		scope.MeasurementSchema, scope.StatisticsSpec = measurement, spec
		scopes = append(scopes, scope)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	// A bounded worker must expose saturation instead of silently acknowledging
	// events for a truncated population. No new penalties are enforced.
	if len(scopes) > 32 {
		return errors.New("lifecycle scope budget exceeded")
	}
	scoped := &usageLogRepository{sql: tx}
	for _, scope := range scopes {
		accounts, err := tx.QueryContext(ctx, "SELECT id FROM accounts WHERE platform='openai' AND deleted_at IS NULL ORDER BY id LIMIT 2001")
		if err != nil {
			return err
		}
		ids := make([]int64, 0)
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
		if len(ids) > 2000 {
			return errors.New("lifecycle account budget exceeded")
		}
		requestType, endpoint := scope.RequestType, scope.Endpoint
		signals, err := scoped.ReadPrioritySchedulingSignals(ctx, service.PrioritySchedulingQuery{
			AccountIDs: ids, Model: scope.Model, GroupID: scope.GroupID, InboundEndpoint: &endpoint, RequestType: &requestType, HalfOpen: true,
			LatencyPercentile: cfg.LatencyPercentile, LatencyMeasurement: cfg.LatencyMeasurement, TargetTTFTMs: cfg.TargetTTFTMs,
			UsageSince: since, UsageUntil: now, QualitySince: now.Add(-time.Duration(cfg.QualityMaxAgeHours) * time.Hour),
		})
		if err != nil {
			return err
		}
		stats := service.PriorityLifecycleStatistics{Scope: scope, PolicyRevision: revision, Watermark: watermark, WindowStart: since, WindowEnd: now, ObservedAt: now, Signals: signals}
		raw, err := json.Marshal(stats)
		if err != nil {
			return err
		}
		// Serialized primary transaction is the fencing owner. Generation is
		// allocated by SQL, not a wall clock or per-instance counter.
		if err = tx.QueryRowContext(ctx, `INSERT INTO priority_lifecycle_statistics(scope_key,generation,watermark,observed_at,snapshot)
            VALUES($1,1,$2,$3,$4) ON CONFLICT(scope_key) DO UPDATE
            SET generation=priority_lifecycle_statistics.generation+1,watermark=EXCLUDED.watermark,observed_at=EXCLUDED.observed_at,snapshot=EXCLUDED.snapshot
            WHERE priority_lifecycle_statistics.watermark<=EXCLUDED.watermark RETURNING generation`, scope.Key(), watermark, now, string(raw)).Scan(&stats.Generation); err != nil {
			return err
		}
		for id, signal := range signals {
			// Preserve the deployed predicate as explicitly named shadow
			// evidence. Aggregate crossing/renewal remains unapproved. A single
			// new slow point must not silently implement a new v2 rule.
			if signal.Samples < cfg.MinSamples || signal.SelectedTTFTMs == nil || *signal.SelectedTTFTMs <= float64(cfg.TargetTTFTMs) || signal.LatestBadLatencyAt == nil {
				continue
			}
			evidence := fmt.Sprintf("legacy-shadow/%d/%d", id, signal.LatestBadLatencyAt.UnixNano())
			policy := service.PriorityLifecyclePolicy{AccountID: id, State: "PENALIZED_SHADOW", EvidenceID: evidence, EvidenceAt: *signal.LatestBadLatencyAt, ExpiresAt: signal.LatestBadLatencyAt.Add(time.Duration(cfg.LatencyPenaltyMinutes) * time.Minute), PolicyRevision: revision, ObservedValue: signal.SelectedTTFTMs, Threshold: cfg.TargetTTFTMs, Percentile: cfg.LatencyPercentile, Mode: cfg.LatencyPenaltyMode, Rule: "legacy_shadow_v1; v2 trigger and renewal pending"}
			raw, err = json.Marshal(policy)
			if err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO priority_lifecycle_policy(scope_key,account_id,evidence_id,evidence_at,policy_revision,expires_at,state)
                VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(scope_key,account_id) DO UPDATE
                SET evidence_id=EXCLUDED.evidence_id,evidence_at=EXCLUDED.evidence_at,policy_revision=EXCLUDED.policy_revision,expires_at=EXCLUDED.expires_at,state=EXCLUDED.state
                WHERE priority_lifecycle_policy.evidence_at<EXCLUDED.evidence_at`, scope.PolicyKey(), id, evidence, policy.EvidenceAt, revision, policy.ExpiresAt, string(raw))
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if changed > 0 {
				_, err = tx.ExecContext(ctx, "INSERT INTO priority_lifecycle_policy_events(scope_key,account_id,evidence_id,kind,event_at,event) VALUES($1,$2,$3,'OBSERVED_SHADOW',$4,$5) ON CONFLICT DO NOTHING", scope.PolicyKey(), id, evidence, policy.EvidenceAt, string(raw))
				if err != nil {
					return err
				}
			}
		}
	}
	// Timing updates arriving after this repeatable-read snapshot remain pending.
	if _, err = tx.ExecContext(ctx, "UPDATE priority_lifecycle_outbox SET processed_at=clock_timestamp() WHERE sequence <= $1 AND processed_at IS NULL", watermark); err != nil {
		return err
	}
	return tx.Commit()
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
