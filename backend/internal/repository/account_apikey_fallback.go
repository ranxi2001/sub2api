package repository

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// EnableOpenAIAPIKeyFallback changes only the scheduling switch. Both account
// eligibility and the saved config revision/selection are checked in this write.
// Error state, credentials, cooldowns and scheduled tasks remain intact.
func (r *accountRepository) EnableOpenAIAPIKeyFallback(ctx context.Context, id int64, revision string) (bool, error) {
	result, err := r.sql.ExecContext(ctx, `UPDATE accounts SET schedulable=TRUE,updated_at=NOW()
  WHERE id=$1 AND deleted_at IS NULL AND platform='openai' AND type='apikey'
   AND status='active' AND parent_account_id IS NULL AND schedulable=FALSE
   AND (expires_at IS NULL OR expires_at>NOW())
   AND EXISTS (SELECT 1 FROM settings WHERE key=$2 AND value::jsonb->>'revision'=$3
    AND value::jsonb->'openai_apikey_fallback'->>'enabled'='true'
    AND value::jsonb->'openai_apikey_fallback'->'account_ids' @> jsonb_build_array($1::bigint))`,
		id, service.SettingKeyOAuthAutoConfig, revision)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return false, err
	}
	if err := enqueueSchedulerOutbox(ctx, r.sql, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		logger.LegacyPrintf("repository.account", "[SchedulerOutbox] enqueue API Key fallback change failed: account=%d", id)
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return true, nil
}
