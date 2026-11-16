//go:build integration

package repository

import (
	"os"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestTOTPRecoveryInsertNeverOverwritesExistingOrConcurrentConfig(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		name := "existing"
		if concurrent {
			name = "concurrent-insert"
		}
		t.Run(name, func(t *testing.T) {
			ctx, repo, cfg := totpRotationFixture(t)
			// Even a destination with the same zero version must be preserved. Recovery
			// is insert-only, not a timestamp-dependent variant of ordinary editing.
			_, err := integrationDB.ExecContext(ctx, `DELETE FROM openai_oauth_reauth_configs WHERE account_id=$1`, cfg.AccountID)
			require.NoError(t, err)
			loser := *cfg
			loser.LoginEmail = "loser@example.test"
			loser.PasswordCiphertext = "enc:loser-password"
			loser.TOTPSecretCiphertext = "enc:loser-pair"
			loser.UpdatedAt = time.Time{}
			tx, err := integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			var winnerPID int
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&winnerPID))
			_, err = tx.ExecContext(ctx, `INSERT INTO openai_oauth_reauth_configs(account_id,login_email,credential_mode,engine,proxy_source,password_ciphertext,totp_secret_ciphertext,otp_url_ciphertext,updated_at)
   VALUES($1,'winner@example.test','password_totp','local_worker','account','enc:winner-password','enc:winner-pair','enc:untouched',$2)`, cfg.AccountID, time.Time{})
			require.NoError(t, err)
			done := make(chan error, 1)
			if concurrent {
				go func() { done <- repo.InsertTOTPRecoveryConfig(ctx, &loser) }()
				require.Eventually(t, func() bool {
					var blocked bool
					e := integrationDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, winnerPID).Scan(&blocked)
					return e == nil && blocked
				}, 5*time.Second, 10*time.Millisecond, "recovery insert must encounter the uncommitted winner")
				require.NoError(t, tx.Commit())
			} else {
				require.NoError(t, tx.Commit())
				done <- repo.InsertTOTPRecoveryConfig(ctx, &loser)
			}
			require.Equal(t, 409, infraerrors.Code(<-done), "recovery loser must conflict")
			winner, err := repo.GetConfig(ctx, cfg.AccountID)
			require.NoError(t, err)
			require.Equal(t, &service.OpenAIOAuthReauthStoredConfig{AccountID: cfg.AccountID, LoginEmail: "winner@example.test", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "account", PasswordCiphertext: "enc:winner-password", TOTPSecretCiphertext: "enc:winner-pair", OTPURLCiphertext: "enc:untouched", UpdatedAt: time.Time{}}, winner)
			winner.LoginEmail = "normal-save@example.test"
			require.NoError(t, repo.UpsertConfig(ctx, winner), "ordinary explicit configuration edits retain their existing CAS semantics")
			reloaded, err := repo.GetConfig(ctx, cfg.AccountID)
			require.NoError(t, err)
			require.Equal(t, "normal-save@example.test", reloaded.LoginEmail)
			require.Equal(t, "enc:winner-pair", reloaded.TOTPSecretCiphertext)
		})
	}
}

func TestTOTPBindingMigrationCanReloadWithoutReplacingSnapshot(t *testing.T) {
	ctx, repo, cfg := totpRotationFixture(t)
	r, err := repo.CreateTOTPRotation(ctx, cfg.AccountID, "snapshot", cfg.UpdatedAt)
	require.NoError(t, err)
	migrationPath := os.Getenv("ISSUE881_MIGRATION_TEST_FILE")
	if migrationPath == "" {
		migrationPath = "../../migrations/382_openai_totp_binding.sql"
	}
	migration, err := os.ReadFile(migrationPath)
	require.NoError(t, err)
	for range 2 {
		_, err = integrationDB.ExecContext(ctx, string(migration))
		require.NoError(t, err, "migration reload must be idempotent")
	}
	saved, err := NewOpenAIOAuthReauthRepository(integrationDB).(*openAIOAuthReauthRepository).GetTOTPRotationByTask(ctx, r.TaskID)
	require.NoError(t, err)
	require.Equal(t, cfg.LoginEmail, saved.LoginEmail)
	require.Equal(t, cfg.TOTPSecretCiphertext, saved.PreviousCiphertext)
	require.False(t, saved.BindingVerified)
}
