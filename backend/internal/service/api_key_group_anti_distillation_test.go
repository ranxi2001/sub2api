package service

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestGroupAntiDistillationAdmissionRechecksWebSocketTurns(t *testing.T) {
	paused, authCalls, checks := false, 0, 0
	ctx := WithAPIKeyQueueAuthRevalidator(context.Background(), func(context.Context) (int, error) {
		authCalls++
		return 3, nil
	})
	ctx = WithGroupAntiDistillationAdmission(ctx, func(context.Context) (time.Duration, error) {
		checks++
		if paused {
			return time.Minute, nil
		}
		return 0, nil
	})
	revalidate := apiKeyQueueAuthRevalidatorFromContext(ctx)
	limit, err := revalidate(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, limit)
	paused = true
	_, err = revalidate(ctx)
	require.Error(t, err)
	require.Equal(t, 429, infraerrors.Code(err))
	require.Equal(t, 2, authCalls)
	require.Equal(t, 2, checks)
}

func TestGroupAntiDistillationRevalidationPreservesRevocationAndFailsOpen(t *testing.T) {
	authErr := infraerrors.Unauthorized("INVALID_API_KEY", "revoked")
	ctx := WithAPIKeyQueueAuthRevalidator(context.Background(), func(context.Context) (int, error) { return 0, authErr })
	ctx = WithGroupAntiDistillationAdmission(ctx, func(context.Context) (time.Duration, error) { t.Fatal("must authenticate first"); return 0, nil })
	_, err := apiKeyQueueAuthRevalidatorFromContext(ctx)(ctx)
	require.ErrorIs(t, err, authErr)
	ctx = WithAPIKeyQueueAuthRevalidator(context.Background(), func(context.Context) (int, error) { return 4, nil })
	ctx = WithGroupAntiDistillationAdmission(ctx, func(context.Context) (time.Duration, error) { return 0, context.DeadlineExceeded })
	limit, err := apiKeyQueueAuthRevalidatorFromContext(ctx)(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, limit)
}
