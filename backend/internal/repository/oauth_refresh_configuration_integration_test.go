//go:build integration

package repository

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// Only the provider is synthetic. RefreshIfNeeded, credential persistence,
// PostgreSQL row locking, scheduler publication and admin writes are real.
type blockedConfigurationRefreshExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (*blockedConfigurationRefreshExecutor) CanRefresh(*service.Account) bool {
	return true
}

func (*blockedConfigurationRefreshExecutor) NeedsRefresh(*service.Account, time.Duration) bool {
	return true
}

func (*blockedConfigurationRefreshExecutor) CacheKey(*service.Account) string {
	return "synthetic-refresh-configuration"
}

func (e *blockedConfigurationRefreshExecutor) Refresh(ctx context.Context, account *service.Account) (map[string]any, error) {
	close(e.started)
	select {
	case <-e.release:
		return service.MergeCredentials(account.Credentials, map[string]any{
			"access_token": "synthetic-rotated-access", "refresh_token": "synthetic-rotated-refresh",
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		}), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestOAuthRefreshPreservesConcurrentModelConfigurationPostgres(t *testing.T) {
	for _, tc := range []struct {
		name          string
		removeMapping bool
		reauthorize   bool
	}{
		{name: "manual_mapping_edit"},
		{name: "manual_mapping_removal", removeMapping: true},
		{name: "reauthorization_wins", reauthorize: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			account := mustCreateAccount(t, testEntClient(t), &service.Account{
				Name: "refresh-config-interleaving", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Credentials: map[string]any{
					"access_token": "synthetic-old-access", "refresh_token": "synthetic-old-refresh",
					"expires_at":            time.Now().Add(-time.Hour).Format(time.RFC3339),
					"model_mapping":         map[string]any{"gpt-6-astra": "gpt-6-astra"},
					"compact_model_mapping": map[string]any{"old-compact": "old-compact"},
				},
			})
			t.Cleanup(func() {
				_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, account.ID)
				require.NoError(t, err)
			})
			cache := &schedulerCacheRecorder{}
			accounts := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, cache)
			executor := &blockedConfigurationRefreshExecutor{started: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			unblock := func() { release.Do(func() { close(executor.release) }) }
			t.Cleanup(unblock)
			type outcome struct {
				result *service.OAuthRefreshResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := service.NewOAuthRefreshAPI(accounts, nil).RefreshIfNeeded(ctx, account, executor, time.Minute)
				done <- outcome{result, err}
			}()
			select {
			case <-executor.started:
			case <-ctx.Done():
				t.Fatal("provider refresh did not start")
			}

			// Commit an actual account edit while the provider still holds the
			// old credentials. Removing a mapping must not resurrect it either.
			manual, err := accounts.GetByID(ctx, account.ID)
			require.NoError(t, err)
			delete(manual.Credentials, "compact_model_mapping")
			if tc.removeMapping {
				delete(manual.Credentials, "model_mapping")
			} else {
				manual.Credentials["model_mapping"] = map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"}
			}
			manual.Credentials["manual_request_setting"] = "latest"
			if tc.reauthorize {
				manual.Credentials["access_token"] = "synthetic-admin-access"
				manual.Credentials["refresh_token"] = "synthetic-admin-refresh"
			}
			require.NoError(t, accounts.Update(ctx, manual))
			require.NoError(t, accounts.SetSchedulable(ctx, account.ID, false))
			var before int
			require.NoError(t, integrationDB.QueryRowContext(ctx,
				`SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, account.ID).Scan(&before))
			unblock()
			var completed outcome
			select {
			case completed = <-done:
			case <-ctx.Done():
				t.Fatal("provider refresh did not complete")
			}
			require.NoError(t, completed.err)
			require.NotNil(t, completed.result)
			require.Equal(t, !tc.reauthorize, completed.result.Refreshed)
			stored, err := accounts.GetByID(ctx, account.ID)
			require.NoError(t, err)
			cached, err := cache.GetAccount(ctx, account.ID)
			require.NoError(t, err)
			for _, durable := range []*service.Account{stored, cached, completed.result.Account} {
				require.NotNil(t, durable)
				if tc.removeMapping {
					require.NotContains(t, durable.Credentials, "model_mapping")
				} else {
					require.Equal(t, map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"}, durable.Credentials["model_mapping"])
				}
				require.NotContains(t, durable.Credentials, "compact_model_mapping")
				require.Equal(t, "latest", durable.GetCredential("manual_request_setting"))
				require.False(t, durable.Schedulable)
				if tc.reauthorize {
					require.Equal(t, "synthetic-admin-access", durable.GetCredential("access_token"))
					require.Equal(t, "synthetic-admin-refresh", durable.GetCredential("refresh_token"))
				} else {
					require.Equal(t, "synthetic-rotated-access", durable.GetCredential("access_token"))
					require.Equal(t, "synthetic-rotated-refresh", durable.GetCredential("refresh_token"))
				}
			}
			var after int
			require.NoError(t, integrationDB.QueryRowContext(ctx,
				`SELECT count(*) FROM scheduler_outbox WHERE account_id=$1`, account.ID).Scan(&after))
			if tc.reauthorize {
				require.Nil(t, completed.result.NewCredentials)
				require.Equal(t, before, after, "stale refresh must not publish a scheduler invalidation")
			} else {
				require.Equal(t, before+1, after)
				require.Equal(t, stored.Credentials, completed.result.NewCredentials)
			}
		})
	}
}

type oauthRefreshFailOutboxExecutor struct {
	sqlExecutor
}

func (e *oauthRefreshFailOutboxExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	args = append([]any(nil), args...)
	args[len(args)-1] = nil // event_type is NOT NULL; PostgreSQL must roll back the token update too.
	return e.sqlExecutor.ExecContext(ctx, query, args...)
}

func TestOAuthRefreshCredentialsOutboxFailureRollsBackPostgres(t *testing.T) {
	ctx := context.Background()
	account := mustCreateAccount(t, testEntClient(t), &service.Account{
		Name: "refresh-config-outbox-failure", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "synthetic-old-access", "refresh_token": "synthetic-old-refresh"},
	})
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, account.ID)
		require.NoError(t, err)
	})
	accounts := newAccountRepositoryWithSQL(testEntClient(t), &oauthRefreshFailOutboxExecutor{integrationDB}, nil)
	applied, err := accounts.MergeOAuthRefreshCredentialsIfTokensUnchanged(ctx, account,
		map[string]any{"access_token": "synthetic-rotated-access", "refresh_token": "synthetic-rotated-refresh"})
	require.Error(t, err)
	require.False(t, applied)
	stored, err := accounts.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, "synthetic-old-access", stored.GetCredential("access_token"))
	require.Equal(t, "synthetic-old-refresh", stored.GetCredential("refresh_token"))
}
