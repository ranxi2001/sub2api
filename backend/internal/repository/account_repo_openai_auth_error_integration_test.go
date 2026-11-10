//go:build integration

package repository

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/accounttimer"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSetErrorPreservesOpenAIOAuthSchedulingIntent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		platform string
		kind     string
		before   bool
		want     bool
	}{
		{name: "enabled OpenAI OAuth", platform: service.PlatformOpenAI, kind: service.AccountTypeOAuth, before: true, want: true},
		{name: "paused OpenAI OAuth", platform: service.PlatformOpenAI, kind: service.AccountTypeOAuth},
		{name: "OpenAI API Key keeps legacy behavior", platform: service.PlatformOpenAI, kind: service.AccountTypeAPIKey, before: true},
		{name: "Anthropic OAuth keeps legacy behavior", platform: service.PlatformAnthropic, kind: service.AccountTypeOAuth, before: true},
		{name: "Gemini OAuth keeps legacy behavior", platform: service.PlatformGemini, kind: service.AccountTypeOAuth, before: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tx := testEntTx(t)
			cache := &schedulerCacheRecorder{}
			repo := newAccountRepositoryWithSQL(tx.Client(), tx, cache)
			a := mustCreateAccount(t, tx.Client(), &service.Account{
				Name: "auth-error-scheduling", Platform: tc.platform, Type: tc.kind,
			})
			require.NoError(t, tx.Client().Account.UpdateOneID(a.ID).SetSchedulable(tc.before).Exec(ctx))
			require.NoError(t, repo.SetError(ctx, a.ID, "401 authentication failed"))
			current, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, service.StatusError, current.Status)
			require.Equal(t, "401 authentication failed", current.ErrorMessage)
			require.Equal(t, tc.want, current.Schedulable)
			require.False(t, current.IsSchedulable())
			require.Len(t, cache.setAccounts, 1)
			require.Equal(t, service.StatusError, cache.setAccounts[0].Status)
			require.Equal(t, tc.want, cache.setAccounts[0].Schedulable)
			require.False(t, cache.setAccounts[0].IsSchedulable(), "the immediate scheduler snapshot must block authentication failures")
			var outboxCount int
			require.NoError(t, scanSingleRow(ctx, tx,
				"SELECT COUNT(*) FROM scheduler_outbox WHERE event_type=$1 AND account_id=$2",
				[]any{service.SchedulerOutboxEventAccountChanged, a.ID}, &outboxCount))
			require.Equal(t, 1, outboxCount)
		})
	}
}

func TestOpenAIOAuthErrorAccountEditPreservesCurrentSchedulingIntent(t *testing.T) {
	for _, before := range []bool{true, false} {
		name := "enabled"
		if !before {
			name = "paused"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			tx := testEntTx(t)
			ctx = dbent.NewTxContext(ctx, tx)
			repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
			a := mustCreateAccount(t, tx.Client(), &service.Account{
				Name: "auth-error-edit", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			})
			require.NoError(t, repo.SetSchedulable(ctx, a.ID, before))
			require.NoError(t, repo.SetError(ctx, a.ID, "401 authentication failed"))
			edit, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			edit.Name = "edited-name"
			edit.Schedulable = !before // An ordinary settings edit can contain a stale switch.
			require.NoError(t, repo.Update(ctx, edit))
			current, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, "edited-name", current.Name)
			require.Equal(t, service.StatusError, current.Status)
			require.Equal(t, "401 authentication failed", current.ErrorMessage)
			require.Equal(t, before, current.Schedulable)
			require.False(t, current.IsSchedulable())
		})
	}
}

func TestOpenAIOAuthStaleSettingsEditCannotClearConcurrentAuthError(t *testing.T) {
	for _, before := range []bool{true, false} {
		name := "enabled"
		if !before {
			name = "paused"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			tx := testEntTx(t)
			ctx = dbent.NewTxContext(ctx, tx)
			repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
			a := mustCreateAccount(t, tx.Client(), &service.Account{
				Name: "stale-auth-edit", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			})
			require.NoError(t, repo.SetSchedulable(ctx, a.ID, before))
			edit, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, service.StatusActive, edit.Status)
			// Fix the real interleaving: the form read finishes before the auth
			// failure, but its stale save obtains the row lock afterwards.
			require.NoError(t, repo.SetError(ctx, a.ID, "401 authentication failed"))
			edit.Name = "unrelated-settings-change"
			editCtx := service.WithPreservedAccountStatus(accounttimer.WithPreservedSchedulable(ctx))
			require.NoError(t, repo.UpdateWithAccountBillingSettings(editCtx, edit, nil, nil, nil))
			current, err := repo.GetByID(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, edit.Name, current.Name)
			require.Equal(t, service.StatusError, current.Status)
			require.Equal(t, "401 authentication failed", current.ErrorMessage)
			require.Equal(t, before, current.Schedulable)
			require.False(t, current.IsSchedulable())
			require.Equal(t, current.Status, edit.Status, "the returned account must reflect the saved state")
			require.Equal(t, current.ErrorMessage, edit.ErrorMessage)
		})
	}
}

func TestOpenAIOAuthStaleErrorEditCannotUndoCredentialRecovery(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	ctx = dbent.NewTxContext(ctx, tx)
	repo := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	old := map[string]any{"access_token": "old", "refresh_token": "old-rt"}
	a := mustCreateAccount(t, tx.Client(), &service.Account{
		Name: "stale-recovery-edit", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Credentials: old,
	})
	require.NoError(t, repo.SetError(ctx, a.ID, "401 authentication failed"))
	edit, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	taskID := mustCreateOpenAIReauthCallbackTask(t, tx, a.ID)
	applied, err := repo.ApplyOpenAIOAuthReauth(ctx, taskID, "worker", a.ID, old,
		map[string]any{"access_token": "new", "refresh_token": "new-rt"}, nil)
	require.NoError(t, err)
	require.True(t, applied)
	// Settings updates do not own the authentication status even when their
	// original snapshot was already an error. Keep credentials current here;
	// this regression targets the status/error-message write boundary.
	latest, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	edit.Credentials = latest.Credentials
	edit.Name = "edited-after-recovery"
	editCtx := service.WithPreservedAccountStatus(accounttimer.WithPreservedSchedulable(ctx))
	require.NoError(t, repo.Update(editCtx, edit))
	current, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, edit.Name, current.Name)
	require.Equal(t, service.StatusActive, current.Status)
	require.Empty(t, current.ErrorMessage)
	require.True(t, current.IsSchedulable())
	// An explicit administrator status change retains the existing write path.
	current.Status = service.StatusDisabled
	require.NoError(t, repo.Update(ctx, current))
	current, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusDisabled, current.Status)
	require.False(t, current.IsSchedulable())
}
