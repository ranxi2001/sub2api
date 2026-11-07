//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func totpRotationFixture(t *testing.T) (context.Context, *openAIOAuthReauthRepository, *service.OpenAIOAuthReauthStoredConfig) {
	t.Helper()
	ctx := context.Background()
	account := mustCreateAccount(t, testEntClient(t), &service.Account{Name: "totp-test", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"email": "owner@example.test"}})
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, account.ID)
		require.NoError(t, err)
	})
	repo := NewOpenAIOAuthReauthRepository(integrationDB).(*openAIOAuthReauthRepository)
	require.NoError(t, repo.UpsertConfig(ctx, &service.OpenAIOAuthReauthStoredConfig{AccountID: account.ID, LoginEmail: "owner@example.test", CredentialMode: "password_totp", Engine: "local_worker", ProxySource: "account", PasswordCiphertext: "enc:password", TOTPSecretCiphertext: "enc:old"}))
	cfg, err := repo.GetConfig(ctx, account.ID)
	require.NoError(t, err)
	return ctx, repo, cfg
}

func TestTOTPRotationOn429ConcurrentCreationAndCooldown(t *testing.T) {
	ctx, repo, cfg := totpRotationFixture(t)
	type outcome struct {
		task *service.OpenAITOTPRotation
		err  error
	}
	const workers = 6
	start, results := make(chan struct{}), make(chan outcome, workers)
	for range workers {
		go func() {
			<-start
			task, err := repo.CreateTOTPRotationOn429(ctx, cfg.AccountID, "snapshot", cfg.UpdatedAt)
			results <- outcome{task, err}
		}()
	}
	close(start)
	var created *service.OpenAITOTPRotation
	count := 0
	for range workers {
		result := <-results
		require.NoError(t, result.err)
		if result.task != nil {
			count++
			created = result.task
		}
	}
	require.Equal(t, 1, count, "concurrent API replicas may create only one task")
	claimed, err := repo.ClaimTOTPRotation(ctx, "worker")
	require.NoError(t, err)
	require.Equal(t, created.TaskID, claimed.TaskID)
	require.NoError(t, repo.FinishTOTPRotation(ctx, claimed.TaskID, "worker", false, "login_failed"))

	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_totp_rotations SET updated_at=NOW()-INTERVAL '30 minutes' WHERE task_id=$1`, created.TaskID)
	require.NoError(t, err)
	task, err := repo.CreateTOTPRotationOn429(ctx, cfg.AccountID, "snapshot", cfg.UpdatedAt)
	require.NoError(t, err)
	require.Nil(t, task, "a failed task also consumes the hourly automatic budget")
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_totp_rotations SET updated_at=NOW()-INTERVAL '61 minutes' WHERE task_id=$1`, created.TaskID)
	require.NoError(t, err)
	task, err = repo.CreateTOTPRotationOn429(ctx, cfg.AccountID, "snapshot", cfg.UpdatedAt)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.NotEqual(t, created.TaskID, task.TaskID)
}

func TestTOTPRotationOn429DoesNotReplaceUnresolvedTask(t *testing.T) {
	ctx, repo, cfg := totpRotationFixture(t)
	manual, err := repo.CreateTOTPRotation(ctx, cfg.AccountID, "snapshot", cfg.UpdatedAt)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_totp_rotations SET state='uncertain',candidate_ciphertext='enc:candidate',updated_at=NOW()-INTERVAL '2 hours' WHERE task_id=$1`, manual.TaskID)
	require.NoError(t, err)
	task, err := repo.CreateTOTPRotationOn429(ctx, cfg.AccountID, "snapshot", cfg.UpdatedAt)
	require.NoError(t, err)
	require.Nil(t, task, "uncertain tasks block automatic replacement even after cooldown")
	latest, err := repo.GetTOTPRotation(ctx, cfg.AccountID)
	require.NoError(t, err)
	require.Equal(t, manual.TaskID, latest.TaskID)
	require.Equal(t, "enc:candidate", latest.CandidateCiphertext)
}

func TestTOTPRotationOn429KeepsManualRotationAndReauthExclusion(t *testing.T) {
	ctx, repo, cfg := totpRotationFixture(t)
	automatic, err := repo.CreateTOTPRotationOn429(ctx, cfg.AccountID, "snapshot", cfg.UpdatedAt)
	require.NoError(t, err)
	_, err = repo.ClaimTOTPRotation(ctx, "worker")
	require.NoError(t, err)
	require.NoError(t, repo.FinishTOTPRotation(ctx, automatic.TaskID, "worker", false, "login_failed"))
	manual, err := repo.CreateTOTPRotation(ctx, cfg.AccountID, "snapshot", cfg.UpdatedAt)
	require.NoError(t, err, "manual rotation keeps its existing behavior during automatic cooldown")
	_, err = repo.ClaimTOTPRotation(ctx, "worker")
	require.NoError(t, err)
	require.NoError(t, repo.FinishTOTPRotation(ctx, manual.TaskID, "worker", false, "login_failed"))
	_, err = repo.CreateTOTPRotationOn429(ctx, cfg.AccountID, "snapshot", cfg.UpdatedAt)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_totp_rotations SET updated_at=NOW()-INTERVAL '2 hours' WHERE account_id=$1`, cfg.AccountID)
	require.NoError(t, err)
	_, err = repo.CreateTask(ctx, cfg.AccountID, "snapshot")
	require.NoError(t, err)
	_, err = repo.CreateTOTPRotationOn429(ctx, cfg.AccountID, "snapshot", cfg.UpdatedAt)
	require.Error(t, err, "an active re-login still excludes automatic rotation")
}

func TestTOTPRotationAtomicActivationAndExclusion(t *testing.T) {
	ctx, repo, cfg := totpRotationFixture(t)
	r, err := repo.CreateTOTPRotation(ctx, cfg.AccountID, "snapshot", cfg.UpdatedAt)
	require.NoError(t, err)
	latest, err := repo.GetLatestTask(ctx, cfg.AccountID)
	require.NoError(t, err)
	require.Nil(t, latest, "2FA reservations must not appear as OAuth re-login results")
	_, err = repo.CreateTask(ctx, cfg.AccountID, "snapshot")
	require.Error(t, err)
	// Legacy re-login workers cannot consume or expire the reserved task.
	old, err := repo.ClaimNextTask(ctx, "legacy", time.Nanosecond)
	require.NoError(t, err)
	require.Nil(t, old)
	claimed, err := repo.ClaimTOTPRotation(ctx, "worker")
	require.NoError(t, err)
	require.Equal(t, r.TaskID, claimed.TaskID)
	copy := *cfg
	copy.TOTPSecretCiphertext = "enc:stale-edit"
	require.Error(t, repo.UpsertConfig(ctx, &copy))
	require.Error(t, repo.FinishTOTPRotation(ctx, r.TaskID, "worker", true, ""), "cannot complete before durable candidate and verification")
	require.Error(t, repo.TransitionTOTPRotation(ctx, r.TaskID, "other", "running", "enrolling", ""))
	for _, step := range [][4]string{{"running", "enrolling", "", ""}, {"enrolling", "prepared", "enc:new", ""}, {"prepared", "activating", "", ""}, {"activating", "verifying", "", ""}} {
		require.NoError(t, repo.TransitionTOTPRotation(ctx, r.TaskID, "worker", step[0], step[1], step[2]))
	}
	// Even after the activation phase, current credentials remain untouched until verification.
	unchanged, err := repo.GetConfig(ctx, cfg.AccountID)
	require.NoError(t, err)
	require.Equal(t, "enc:old", unchanged.TOTPSecretCiphertext)
	// Force the effective-config write to fail after the rotation state UPDATE.
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE openai_oauth_reauth_configs ADD CONSTRAINT totp_test_write_failure CHECK(account_id <> %d OR totp_secret_ciphertext <> 'enc:new') NOT VALID`, cfg.AccountID))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `ALTER TABLE openai_oauth_reauth_configs DROP CONSTRAINT IF EXISTS totp_test_write_failure`)
	})
	require.Error(t, repo.FinishTOTPRotation(ctx, r.TaskID, "worker", true, ""))
	stillPending, err := repo.GetTOTPRotation(ctx, cfg.AccountID)
	require.NoError(t, err)
	require.Equal(t, "verifying", stillPending.State)
	require.Equal(t, "enc:new", stillPending.CandidateCiphertext)
	_, err = integrationDB.ExecContext(ctx, `ALTER TABLE openai_oauth_reauth_configs DROP CONSTRAINT totp_test_write_failure`)
	require.NoError(t, err)
	require.NoError(t, repo.FinishTOTPRotation(ctx, r.TaskID, "worker", true, ""))
	require.NoError(t, repo.FinishTOTPRotation(ctx, r.TaskID, "worker", true, ""), "success ACK may be retried safely")
	updated, err := repo.GetConfig(ctx, cfg.AccountID)
	require.NoError(t, err)
	require.Equal(t, "enc:new", updated.TOTPSecretCiphertext)
	require.Error(t, repo.UpsertConfig(ctx, &copy), "an edit read before rotation must not overwrite the new secret after completion")
	var previous, password, candidate string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT previous_ciphertext,password_ciphertext,candidate_ciphertext FROM openai_totp_rotations WHERE task_id=$1`, r.TaskID).Scan(&previous, &password, &candidate))
	require.Equal(t, []string{"enc:old", "enc:password", "enc:new"}, []string{previous, password, candidate})
	_, err = repo.CreateTask(ctx, cfg.AccountID, "next")
	require.NoError(t, err)
}

func TestTOTPRotationExpirationPreservesCandidateAndFencesWorker(t *testing.T) {
	ctx, repo, cfg := totpRotationFixture(t)
	r, err := repo.CreateTOTPRotation(ctx, cfg.AccountID, "snapshot", cfg.UpdatedAt)
	require.NoError(t, err)
	_, err = repo.ClaimTOTPRotation(ctx, "old")
	require.NoError(t, err)
	require.NoError(t, repo.TransitionTOTPRotation(ctx, r.TaskID, "old", "running", "enrolling", ""))
	require.NoError(t, repo.TransitionTOTPRotation(ctx, r.TaskID, "old", "enrolling", "prepared", "enc:new"))
	require.NoError(t, repo.TransitionTOTPRotation(ctx, r.TaskID, "old", "prepared", "activating", ""))
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_totp_rotations SET updated_at=NOW()-INTERVAL '16 minutes' WHERE task_id=$1`, r.TaskID)
	require.NoError(t, err)
	expired, err := repo.GetTOTPRotation(ctx, cfg.AccountID)
	require.NoError(t, err)
	require.Equal(t, "uncertain", expired.State, "status must allow recovery while workers are offline")
	next, err := repo.ClaimTOTPRotation(ctx, "new")
	require.NoError(t, err)
	require.Nil(t, next, "expired rotations are never automatically replayed")
	saved, err := repo.GetTOTPRotation(ctx, cfg.AccountID)
	require.NoError(t, err)
	require.Equal(t, "uncertain", saved.State)
	require.Equal(t, "enc:new", saved.CandidateCiphertext)
	require.Error(t, repo.TransitionTOTPRotation(ctx, r.TaskID, "old", "activating", "verifying", ""))
	require.Error(t, repo.FinishTOTPRotation(ctx, r.TaskID, "old", true, ""))
	require.Error(t, repo.RecoverTOTPCandidate(ctx, r.TaskID, "enc:other"))
	require.NoError(t, repo.RetryTOTPRotation(ctx, cfg.AccountID, r.TaskID, "verify_new"))
	next, err = repo.ClaimTOTPRotation(ctx, "new")
	require.NoError(t, err)
	require.Equal(t, "verify_new", next.Action)
	require.Error(t, repo.TransitionTOTPRotation(ctx, r.TaskID, "new", "running", "enrolling", ""))
	require.NoError(t, repo.TransitionTOTPRotation(ctx, r.TaskID, "new", "running", "verifying", ""))
	require.NoError(t, repo.FinishTOTPRotation(ctx, r.TaskID, "new", true, ""))
}

func TestTOTPRotationLocalJournalRecoveryAndOriginalVerification(t *testing.T) {
	ctx, repo, cfg := totpRotationFixture(t)
	r, err := repo.CreateTOTPRotation(ctx, cfg.AccountID, "snapshot", cfg.UpdatedAt)
	require.NoError(t, err)
	_, err = repo.ClaimTOTPRotation(ctx, "worker")
	require.NoError(t, err)
	require.NoError(t, repo.TransitionTOTPRotation(ctx, r.TaskID, "worker", "running", "enrolling", ""))
	require.NoError(t, repo.FinishTOTPRotation(ctx, r.TaskID, "worker", false, "enrollment_failed"))
	require.NoError(t, repo.RecoverTOTPCandidate(ctx, r.TaskID, "enc:recovered"))
	require.NoError(t, repo.RetryTOTPRotation(ctx, cfg.AccountID, r.TaskID, "verify_old"))
	_, err = repo.ClaimTOTPRotation(ctx, "recovery")
	require.NoError(t, err)
	require.NoError(t, repo.TransitionTOTPRotation(ctx, r.TaskID, "recovery", "running", "verifying", ""))
	require.NoError(t, repo.FinishTOTPRotation(ctx, r.TaskID, "recovery", true, ""))
	current, err := repo.GetConfig(ctx, cfg.AccountID)
	require.NoError(t, err)
	require.Equal(t, "enc:old", current.TOTPSecretCiphertext)
	saved, err := repo.GetTOTPRotation(ctx, cfg.AccountID)
	require.NoError(t, err)
	require.Equal(t, "enc:recovered", saved.CandidateCiphertext)
}

func TestTOTPRotationConcurrentCreationHasSingleWinner(t *testing.T) {
	ctx, repo, cfg := totpRotationFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.CreateTOTPRotation(ctx, cfg.AccountID, "snapshot", cfg.UpdatedAt)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes)
}
