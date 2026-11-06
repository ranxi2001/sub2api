package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.UpstreamModelMappingSyncRepository = (*accountRepository)(nil)

// ClaimAutomaticUpstreamModelSync is one SQL compare-and-set, shared by all
// application instances. It records failed attempts too and does not change
// scheduling, quotas, credentials, or the account revision.
func (r *accountRepository) ClaimAutomaticUpstreamModelSync(ctx context.Context, accountID int64, now, next time.Time) (bool, error) {
	if r == nil || r.sql == nil {
		return false, errors.New("account repository SQL executor is not configured")
	}
	result, err := r.sql.ExecContext(ctx, `
		UPDATE accounts
		SET extra = jsonb_set(COALESCE(extra, '{}'::jsonb), '{upstream_model_sync}',
			jsonb_build_object('last_attempt_unix', $2::bigint, 'next_attempt_unix', $3::bigint), true)
		WHERE id = $1 AND deleted_at IS NULL AND status = 'active' AND schedulable = TRUE
			AND CASE WHEN jsonb_typeof(extra->'upstream_model_sync'->'next_attempt_unix') = 'number'
				THEN (extra->'upstream_model_sync'->>'next_attempt_unix')::numeric
				ELSE 0 END <= $2::bigint
	`, accountID, now.Unix(), next.Unix())
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

// UpdateUpstreamModelMappingIfUnchanged updates only model_mapping and commits
// its scheduler invalidation atomically. Credential/proxy changes after model
// discovery cause a harmless skip, never a stale full-credential replacement.
func (r *accountRepository) UpdateUpstreamModelMappingIfUnchanged(ctx context.Context, expected *service.Account, mapping map[string]string) (bool, error) {
	if r == nil || r.sql == nil || expected == nil {
		return false, errors.New("account repository or expected account is not configured")
	}
	if len(mapping) == 0 {
		return false, errors.New("an empty upstream model mapping cannot be applied")
	}
	expectedJSON, err := json.Marshal(normalizeJSONMap(expected.Credentials))
	if err != nil {
		return false, err
	}
	mappingJSON, err := json.Marshal(mapping)
	if err != nil {
		return false, err
	}
	result, err := r.sql.ExecContext(ctx, `
		WITH updated AS (
		UPDATE accounts AS a
		SET credentials = jsonb_set(COALESCE(a.credentials, '{}'::jsonb), '{model_mapping}', $2::jsonb, true),
			updated_at = NOW()
		WHERE a.id = $1 AND a.deleted_at IS NULL
			AND a.platform = $3 AND a.type = $4
			AND a.credentials = $5::jsonb
			AND a.proxy_id IS NOT DISTINCT FROM $6
		RETURNING a.id
		)
		INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
		SELECT $7, updated.id, NULL, NULL FROM updated
	`, expected.ID, string(mappingJSON), expected.Platform, expected.Type,
		string(expectedJSON), expected.ProxyID, service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, expected.ID)
	return true, nil
}
