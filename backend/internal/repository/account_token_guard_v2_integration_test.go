//go:build integration

package repository

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountTokenGuardV2RepositoryLeasesAndReauth(t *testing.T) {
	ctx := context.Background()
	account := mustCreateAccount(t, testEntClient(t), &service.Account{
		Name: "credential-guard-v2-test", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "test-old-token"},
	})
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, account.ID)
		require.NoError(t, err)
	})
	guard := NewAccountTokenGuardV2Repository(integrationDB)
	require.NoError(t, guard.UpsertAccount(ctx, account.ID, false, false))
	claimed, err := guard.ClaimDue(ctx, "instance-a", time.Minute, 10, time.Now())
	require.NoError(t, err)
	require.Empty(t, claimed, "paused accounts must not be scheduled")
	require.NoError(t, guard.UpsertAccount(ctx, account.ID, true, true))
	claimed, err = guard.ClaimDue(ctx, "instance-a", time.Minute, 10, time.Now())
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	other, err := guard.ClaimAccount(ctx, account.ID, "instance-b", time.Minute)
	require.NoError(t, err)
	require.Nil(t, other, "a second instance cannot claim a live lease")
	now := time.Now().Truncate(time.Microsecond)
	completion := service.AccountTokenGuardV2ProbeCompletion{
		ProbeState: service.AccountTokenGuardV2ProbeOK, LastProbeAt: now, NextProbeAt: now.Add(time.Hour),
	}
	require.ErrorIs(t, guard.CompleteProbe(ctx, account.ID, "instance-b", completion), sql.ErrNoRows)
	require.NoError(t, guard.CompleteProbe(ctx, account.ID, "instance-a", completion))
	stored, err := guard.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Empty(t, stored.LeaseOwner)
	require.Nil(t, stored.LeaseUntil)
	require.Equal(t, service.AccountTokenGuardV2ProbeOK, stored.ProbeState)
	claimed, err = guard.ClaimDue(ctx, "instance-b", time.Minute, 10, time.Now())
	require.NoError(t, err)
	require.Empty(t, claimed)
	require.NoError(t, guard.RescheduleEnabled(ctx))
	claimed, err = guard.ClaimDue(ctx, "instance-b", time.Minute, 10, time.Now())
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	_, err = integrationDB.ExecContext(ctx, `UPDATE account_token_guard_v2_accounts SET lease_until = NOW() - INTERVAL '1 second' WHERE account_id = $1`, account.ID)
	require.NoError(t, err)
	other, err = guard.ClaimAccount(ctx, account.ID, "instance-c", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, other, "expired leases must be recoverable")
	require.ErrorIs(t, guard.CompleteProbe(ctx, account.ID, "instance-b", completion), sql.ErrNoRows)

	reauth := NewOpenAIOAuthReauthRepository(integrationDB)
	require.NoError(t, reauth.UpsertConfig(ctx, &service.OpenAIOAuthReauthStoredConfig{
		AccountID: account.ID, LoginEmail: "guard@example.com", CredentialMode: service.OpenAIOAuthReauthModePasswordTOTP,
		ProxySource: service.OpenAIOAuthReauthProxySourceAccount, PasswordCiphertext: "synthetic-ciphertext",
	}))
	config, err := reauth.GetConfig(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, "synthetic-ciphertext", config.PasswordCiphertext)
	require.Empty(t, config.OTPURLCiphertext)
	task, err := reauth.CreateTask(ctx, account.ID, "synthetic-hash")
	require.NoError(t, err)
	_, err = reauth.CreateTask(ctx, account.ID, "another-hash")
	require.Equal(t, http.StatusConflict, infraerrors.Code(err))
	running, err := reauth.ClaimNextTask(ctx, "worker-a", time.Minute)
	require.NoError(t, err)
	require.Equal(t, task.ID, running.ID)
	duplicate, err := reauth.ClaimNextTask(ctx, "worker-b", time.Minute)
	require.NoError(t, err)
	require.Nil(t, duplicate)
	require.Error(t, reauth.UpdateStage(ctx, task.ID, "worker-b", service.OpenAIOAuthReauthStageStarting))
	require.NoError(t, reauth.UpdateStage(ctx, task.ID, "worker-a", service.OpenAIOAuthReauthStagePasswordSubmitted))
	_, started, err := reauth.BeginDirectCallback(ctx, task.ID, "worker-a")
	require.NoError(t, err)
	require.True(t, started)
	accountRepo := NewAccountRepository(testEntClient(t), integrationDB, nil).(service.OpenAIOAuthReauthCredentialUpdater)
	updated := map[string]any{"access_token": "test-new-token"}
	applied, err := accountRepo.ApplyOpenAIOAuthReauth(ctx, task.ID, "worker-a", account.ID, map[string]any{"access_token": "stale-token"}, updated, nil)
	require.NoError(t, err)
	require.False(t, applied)
	applied, err = accountRepo.ApplyOpenAIOAuthReauth(ctx, task.ID, "worker-a", account.ID, account.Credentials, updated, nil)
	require.NoError(t, err)
	require.True(t, applied)
	done, err := reauth.GetTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIOAuthReauthStatusSucceeded, done.Status)
}

func TestAccountTokenGuardV2ManagedWorkerOnlyClaimsPasswordTasks(t *testing.T) {
	ctx := context.Background()
	repo := NewOpenAIOAuthReauthRepository(integrationDB).(*openAIOAuthReauthRepository)
	var tasks []*service.OpenAIOAuthReauthTaskRecord
	for _, mode := range []string{service.OpenAIOAuthReauthModeEmailOTPURL, service.OpenAIOAuthReauthModePasswordTOTP} {
		account := mustCreateAccount(t, testEntClient(t), &service.Account{
			Name: "mixed-reauth-" + mode, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Credentials: map[string]any{"access_token": "synthetic"},
		})
		t.Cleanup(func() {
			_, err := integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id=$1", account.ID)
			require.NoError(t, err)
		})
		require.NoError(t, repo.UpsertConfig(ctx, &service.OpenAIOAuthReauthStoredConfig{
			AccountID: account.ID, LoginEmail: "synthetic@example.com", CredentialMode: mode,
			ProxySource: service.OpenAIOAuthReauthProxySourceAccount, PasswordCiphertext: "synthetic", OTPURLCiphertext: "synthetic",
		}))
		task, err := repo.CreateTask(ctx, account.ID, "synthetic-hash")
		require.NoError(t, err)
		tasks = append(tasks, task)
	}
	password, err := repo.ClaimNextPasswordTask(ctx, "managed-test", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, password)
	require.Equal(t, tasks[1].ID, password.ID)
	email, err := repo.GetTask(ctx, tasks[0].ID)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIOAuthReauthStatusQueued, email.Status)
	require.Empty(t, email.WorkerID)
	external, err := repo.ClaimNextTask(ctx, "external-test", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, external)
	require.Equal(t, tasks[0].ID, external.ID)
}

func TestAccountTokenGuardV2PruneDeletedAccounts(t *testing.T) {
	ctx := context.Background()
	guard := NewAccountTokenGuardV2Repository(integrationDB)
	for _, tc := range []struct {
		name    string
		deleted bool
		enabled bool
	}{
		{"deleted-enabled", true, true},
		{"deleted-paused", true, false},
		{"live-auth-failure", false, true},
		{"live-paused", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := mustCreateAccount(t, testEntClient(t), &service.Account{
				Name: tc.name, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
				Credentials: map[string]any{"access_token": "synthetic"},
			})
			t.Cleanup(func() {
				_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, account.ID)
				require.NoError(t, err)
			})
			require.NoError(t, guard.UpsertAccount(ctx, account.ID, tc.enabled, true))
			// Even repeated authentication failures and missing login configs must
			// not remove live accounts; future schedules must not delay cleanup.
			_, err := integrationDB.ExecContext(ctx, `UPDATE account_token_guard_v2_accounts
				SET probe_state = 'auth', fail_streak = 50, next_probe_at = NOW() + INTERVAL '1 day'
				WHERE account_id = $1`, account.ID)
			require.NoError(t, err)
			if tc.deleted {
				_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET deleted_at = NOW() WHERE id = $1`, account.ID)
				require.NoError(t, err)
			}
			removed, err := guard.PruneDeletedAccounts(ctx)
			require.NoError(t, err)
			record, err := guard.GetAccount(ctx, account.ID)
			require.NoError(t, err)
			if tc.deleted {
				require.EqualValues(t, 1, removed)
				require.Nil(t, record)
			} else {
				require.Zero(t, removed)
				require.NotNil(t, record)
				require.Equal(t, 50, record.FailStreak)
			}
			removed, err = guard.PruneDeletedAccounts(ctx)
			require.NoError(t, err)
			require.Zero(t, removed)
		})
	}
}
