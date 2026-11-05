package service

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// CheckGroupAntiDistillation uses the effective customer billing group and
// never changes the upstream account state or routes to a more expensive group.
func (s *APIKeyService) CheckGroupAntiDistillation(ctx context.Context, key *APIKey) (time.Duration, error) {
	if s == nil || s.groupAntiDistillation == nil || key == nil || key.Group == nil || key.Group.Platform != PlatformOpenAI {
		return 0, nil
	}
	return s.groupAntiDistillation.Check(ctx, key.UserID, key.Group.ID)
}

// WithGroupAntiDistillationAdmission preserves the existing fresh auth check,
// then rechecks the pause before queue admission and each WebSocket turn.
func WithGroupAntiDistillationAdmission(ctx context.Context, check func(context.Context) (time.Duration, error)) context.Context {
	previous := apiKeyQueueAuthRevalidatorFromContext(ctx)
	if previous == nil || check == nil {
		return ctx
	}
	return WithAPIKeyQueueAuthRevalidator(ctx, func(ctx context.Context) (int, error) {
		limit, err := previous(ctx)
		if err != nil {
			return limit, err
		}
		remaining, err := check(ctx)
		// Cache outages fail open; the ingress check logs the failure. Never
		// turn unavailable statistics into a customer suspension.
		if err == nil && remaining > 0 {
			return 0, NewAPIKeyQueueAuthRejected(infraerrors.New(429, "group_anti_distillation_paused", "零缓存保护已暂停此分组的新请求，请在暂停结束后重试"))
		}
		return limit, nil
	})
}
