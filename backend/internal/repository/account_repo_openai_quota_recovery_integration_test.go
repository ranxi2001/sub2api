//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIQuotaRecoveryRepository(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	cache := &schedulerCacheRecorder{}
	repo.schedulerCache = cache
	group := mustCreateGroup(t, tx.Client(), &service.Group{Name: "issue123-single-account"})
	account := mustCreateAccount(t, tx.Client(), &service.Account{Name: "issue123-oauth", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Schedulable: true})
	mustBindAccountToGroup(t, tx.Client(), account.ID, group.ID, 1)
	require.NoError(t, repo.SetRateLimited(ctx, account.ID, time.Now().Add(4*24*time.Hour)))
	observed, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	candidates, err := repo.ListSchedulableByGroupID(ctx, group.ID)
	require.NoError(t, err)
	require.Empty(t, candidates)
	// Simulate consumption of the earlier event so dedup permits a new one.
	_, err = tx.ExecContext(ctx, "UPDATE scheduler_outbox SET dedup_key = NULL WHERE account_id = $1", account.ID)
	require.NoError(t, err)
	var before int
	require.NoError(t, scanSingleRow(ctx, repo.sql, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1", []any{account.ID}, &before))
	cleared, err := repo.ClearOpenAIRateLimitIfObserved(ctx, account.ID, *observed.RateLimitedAt, *observed.RateLimitResetAt)
	require.NoError(t, err)
	require.True(t, cleared)
	candidates, err = repo.ListSchedulableByGroupID(ctx, group.ID)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Nil(t, candidates[0].RateLimitResetAt)
	require.Nil(t, cache.accounts[account.ID].RateLimitResetAt)
	var after int
	require.NoError(t, scanSingleRow(ctx, repo.sql, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1", []any{account.ID}, &after))
	require.Equal(t, before+1, after)

	// A new 429 with the same deadline is still a different generation.
	require.NoError(t, repo.SetRateLimited(ctx, account.ID, *observed.RateLimitResetAt))
	cleared, err = repo.ClearOpenAIRateLimitIfObserved(ctx, account.ID, *observed.RateLimitedAt, *observed.RateLimitResetAt)
	require.NoError(t, err)
	require.False(t, cleared)
	require.NotNil(t, cache.accounts[account.ID].RateLimitResetAt)

	// A shorter reset after re-arming must survive too.
	require.NoError(t, repo.SetRateLimited(ctx, account.ID, time.Now().Add(time.Hour)))
	cleared, err = repo.ClearOpenAIRateLimitIfObserved(ctx, account.ID, *observed.RateLimitedAt, *observed.RateLimitResetAt)
	require.NoError(t, err)
	require.False(t, cleared)
	current, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)

	// Quota recovery must preserve independent overload, error, manual disable,
	// temporary unschedulability and model cooldown state.
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	_, err = tx.Client().Account.UpdateOneID(account.ID).
		SetOverloadUntil(until).SetTempUnschedulableUntil(until).
		SetTempUnschedulableReason("unrelated").SetStatus(service.StatusError).
		SetErrorMessage("unrelated error").SetSchedulable(false).
		SetExtra(map[string]any{"model_rate_limits": map[string]any{"gpt-test": "keep"}}).Save(ctx)
	require.NoError(t, err)
	cleared, err = repo.ClearOpenAIRateLimitIfObserved(ctx, account.ID, *current.RateLimitedAt, *current.RateLimitResetAt)
	require.NoError(t, err)
	require.True(t, cleared)
	current, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Nil(t, current.RateLimitResetAt)
	require.Equal(t, service.StatusError, current.Status)
	require.False(t, current.Schedulable)
	require.Equal(t, "unrelated error", current.ErrorMessage)
	require.Equal(t, "unrelated", current.TempUnschedulableReason)
	require.True(t, current.OverloadUntil.Equal(until))
	require.True(t, current.TempUnschedulableUntil.Equal(until))
	require.Contains(t, current.Extra, "model_rate_limits")
}

func TestOpenAIQuotaRecoveryRepositoryScope(t *testing.T) {
	for _, mode := range []string{"api key", "other platform", "shadow", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			tx := testEntTx(t)
			repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
			account := mustCreateAccount(t, tx.Client(), &service.Account{Name: "scope", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
			require.NoError(t, repo.SetRateLimited(ctx, account.ID, time.Now().Add(time.Hour)))
			observed, err := repo.GetByID(ctx, account.ID)
			require.NoError(t, err)
			update := tx.Client().Account.UpdateOneID(account.ID)
			switch mode {
			case "api key":
				update.SetType(service.AccountTypeAPIKey)
			case "other platform":
				update.SetPlatform(service.PlatformGrok)
			case "shadow":
				parent := mustCreateAccount(t, tx.Client(), &service.Account{Name: "parent", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth})
				update.SetParentAccountID(parent.ID).SetQuotaDimension("spark")
			case "deleted":
				update.SetDeletedAt(time.Now())
			}
			_, err = update.Save(ctx)
			require.NoError(t, err)
			cleared, err := repo.ClearOpenAIRateLimitIfObserved(ctx, account.ID, *observed.RateLimitedAt, *observed.RateLimitResetAt)
			require.NoError(t, err)
			require.False(t, cleared)
			var storedReset time.Time
			require.NoError(t, scanSingleRow(ctx, repo.sql, "SELECT rate_limit_reset_at FROM accounts WHERE id = $1", []any{account.ID}, &storedReset))
			require.True(t, storedReset.Equal(*observed.RateLimitResetAt))
		})
	}
}
