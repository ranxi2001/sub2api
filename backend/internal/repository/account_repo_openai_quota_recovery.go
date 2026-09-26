package repository

import (
	"context"
	"time"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ClearOpenAIRateLimitIfObserved clears only the OAuth cooldown generation
// observed before a successful quota probe. Model limits, overload, temporary
// scheduling blocks and administrative state have independent recovery rules.
func (r *accountRepository) ClearOpenAIRateLimitIfObserved(ctx context.Context, id int64, limitedAt, resetAt time.Time) (bool, error) {
	updated, err := r.client.Account.Update().
		Where(
			dbaccount.IDEQ(id),
			dbaccount.DeletedAtIsNil(),
			dbaccount.PlatformEQ(service.PlatformOpenAI),
			dbaccount.TypeEQ(service.AccountTypeOAuth),
			dbaccount.ParentAccountIDIsNil(),
			dbaccount.RateLimitedAtEQ(limitedAt),
			dbaccount.RateLimitResetAtEQ(resetAt),
		).
		ClearRateLimitedAt().
		ClearRateLimitResetAt().
		Save(ctx)
	if err != nil {
		return false, err
	}
	if updated > 0 {
		if err := enqueueSchedulerOutbox(ctx, r.sql, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
			logger.LegacyPrintf("repository.account", "[SchedulerOutbox] enqueue OpenAI quota recovery failed: account=%d err=%v", id, err)
		}
	}
	r.syncSchedulerAccountSnapshot(ctx, id)
	return updated > 0, nil
}
