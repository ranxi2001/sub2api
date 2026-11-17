package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"strings"
)

// ClearModelRateLimit 删除 model_rate_limits 中指定的单个 scope。
// 与 ClearModelRateLimits 的全量清除不同，这里只解封一个模型窗口，
// 不会把其他模型或并发新写入的限流一起抹掉。
func (r *accountRepository) ClearModelRateLimit(ctx context.Context, id int64, scope string) error {
	if strings.TrimSpace(scope) == "" {
		return nil
	}
	if scoped := r.accountTransactionScope(ctx); scoped != r {
		return scoped.ClearModelRateLimit(ctx, id, scope)
	}
	client := clientFromContext(ctx, r.client)
	result, err := client.ExecContext(
		ctx,
		`UPDATE accounts SET
			extra = jsonb_set(
				COALESCE(extra, '{}'::jsonb),
				'{model_rate_limits}'::text[],
				COALESCE(extra->'model_rate_limits', '{}'::jsonb) - $1::text,
				true
			),
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL`,
		strings.TrimSpace(scope),
		id,
	)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrAccountNotFound
	}
	if err := enqueueSchedulerOutbox(ctx, r.sql, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
		logger.LegacyPrintf("repository.account", "[SchedulerOutbox] enqueue clear model rate limit scope failed: account=%d err=%v", id, err)
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return nil
}
