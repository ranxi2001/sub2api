//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Model the interval after a deadline has passed but before its cancellation
// callback has run. No external state or real timers are needed for signaling.
type pendingDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c pendingDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }
func (pendingDeadlineContext) Done() <-chan struct{}         { return nil }
func (pendingDeadlineContext) Err() error                    { return nil }

type pendingDeadlineExecutor struct {
	pending bool
}

func (*pendingDeadlineExecutor) CacheKey(*Account) string                  { return "synthetic-deadline" }
func (*pendingDeadlineExecutor) CanRefresh(*Account) bool                  { return true }
func (*pendingDeadlineExecutor) NeedsRefresh(*Account, time.Duration) bool { return true }
func (e *pendingDeadlineExecutor) Refresh(ctx context.Context, _ *Account) (map[string]any, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		panic("synthetic attempt requires a deadline")
	}
	if remaining := time.Until(deadline); remaining > 0 {
		time.Sleep(remaining)
	}
	e.pending = ctx.Err() == nil && !time.Now().Before(deadline)
	return map[string]any{"access_token": "synthetic-only"}, nil
}

func TestRefreshRejectsResultWhenDeadlineSignalIsPending(t *testing.T) {
	t.Run("direct refresh with caller deadline", func(t *testing.T) {
		repo := &poolHealthAccountRepo{}
		executor := &pendingDeadlineExecutor{}
		svc := newPoolHealthService(repo, &poolHealthRefresher{}, config.TokenRefreshConfig{MaxRetries: 1})
		svc.attemptTimeoutOverride = time.Second
		ctx := pendingDeadlineContext{Context: context.Background(), deadline: time.Now().Add(10 * time.Millisecond)}
		account := grokPoolAccount(43)
		err := svc.refreshWithRetry(ctx, &account, executor, nil, time.Hour)
		require.True(t, executor.pending, "test must exercise a pending deadline signal")
		require.ErrorIs(t, err, context.DeadlineExceeded)
		_, updatedIDs, _, _ := repo.snapshot()
		require.Empty(t, updatedIDs)
	})

	t.Run("unified refresh", func(t *testing.T) {
		account := &Account{ID: 85, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive}
		repo := &refreshAPIAccountRepo{account: account}
		executor := &pendingDeadlineExecutor{}
		api := NewOAuthRefreshAPI(repo, nil)
		ctx := pendingDeadlineContext{Context: context.Background(), deadline: time.Now().Add(10 * time.Millisecond)}
		result, err := api.RefreshIfNeeded(ctx, account, executor, time.Hour)
		require.True(t, executor.pending, "test must exercise a pending deadline signal")
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Nil(t, result)
		require.Zero(t, repo.updateCredentialsCalls)
	})
}

func TestRefreshContextErrRecognizesPendingDeadlineAndCancellation(t *testing.T) {
	ctx := pendingDeadlineContext{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	require.ErrorIs(t, refreshContextErr(ctx), context.DeadlineExceeded)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, refreshContextErr(canceled), context.Canceled)
	require.NoError(t, refreshContextErr(context.Background()))
}
