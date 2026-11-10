package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// MergeOAuthRefreshCredentialsIfTokensUnchanged keeps configuration ownership
// independent from token refresh. PostgreSQL evaluates the delta against the
// attempted snapshot, then merges it into the latest row while holding its
// update lock. A concurrent model mapping edit can neither be reverted nor
// prevent a provider-issued token rotation from being saved.
func (r *accountRepository) MergeOAuthRefreshCredentialsIfTokensUnchanged(ctx context.Context, attempted *service.Account, credentials map[string]any) (bool, error) {
	if r == nil || r.sql == nil || attempted == nil {
		return false, errors.New("OAuth refresh persistence is not configured")
	}
	if scoped := r.accountTransactionScope(ctx); scoped != r {
		return scoped.MergeOAuthRefreshCredentialsIfTokensUnchanged(ctx, attempted, credentials)
	}
	if attempted.AuthConnectionID != "" || attempted.IsCredentialShadow() || attempted.IsGrokOAuth() {
		return false, errors.New("account requires a dedicated credential persistence path")
	}
	expectedJSON, err := json.Marshal(normalizeJSONMap(attempted.Credentials))
	if err != nil {
		return false, err
	}
	credentialsJSON, err := json.Marshal(normalizeJSONMap(credentials))
	if err != nil {
		return false, err
	}
	result, err := r.sql.ExecContext(ctx, `
		WITH delta AS (
			SELECT COALESCE(jsonb_object_agg(item.key, item.value), '{}'::jsonb) AS credentials
			FROM jsonb_each($1::jsonb) AS item
			WHERE item.value IS DISTINCT FROM ($2::jsonb -> item.key)
		), updated AS (
			UPDATE accounts AS a
			SET credentials = COALESCE(a.credentials, '{}'::jsonb) || delta.credentials,
				updated_at = NOW()
			FROM delta
			WHERE a.id = $3 AND a.deleted_at IS NULL
				AND a.platform = $4 AND a.type = $5
				AND (a.credentials -> 'access_token') IS NOT DISTINCT FROM ($2::jsonb -> 'access_token')
				AND (a.credentials -> 'refresh_token') IS NOT DISTINCT FROM ($2::jsonb -> 'refresh_token')
				AND (a.credentials -> 'id_token') IS NOT DISTINCT FROM ($2::jsonb -> 'id_token')
				AND (a.credentials -> '_token_version') IS NOT DISTINCT FROM ($2::jsonb -> '_token_version')
			RETURNING a.id
		)
		INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
		SELECT $6, updated.id, NULL, NULL FROM updated
	`, string(credentialsJSON), string(expectedJSON), attempted.ID, attempted.Platform, attempted.Type,
		service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, attempted.ID)
	return true, nil
}
