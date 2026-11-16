package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type rotationTestRepo struct {
	*reauthTestRepo
	rotation   *OpenAITOTPRotation
	candidate  string
	finishCode string
}

func (r *rotationTestRepo) GetTOTPRotation(context.Context, int64) (*OpenAITOTPRotation, error) {
	return r.rotation, nil
}
func (r *rotationTestRepo) CreateTOTPRotation(context.Context, int64, string, time.Time) (*OpenAITOTPRotation, error) {
	return r.rotation, nil
}
func (r *rotationTestRepo) ClaimTOTPRotation(context.Context, string) (*OpenAITOTPRotation, error) {
	return r.rotation, nil
}
func (r *rotationTestRepo) TransitionTOTPRotation(_ context.Context, _ int64, _ string, _ string, _ string, candidate string) error {
	r.candidate = candidate
	return nil
}
func (r *rotationTestRepo) FinishTOTPRotation(_ context.Context, _ int64, _ string, _ bool, code string) error {
	r.finishCode = code
	return nil
}
func (r *rotationTestRepo) RetryTOTPRotation(context.Context, int64, int64, string) error { return nil }
func (r *rotationTestRepo) RecoverTOTPCandidate(_ context.Context, _ int64, candidate string) error {
	r.candidate = candidate
	return nil
}

func newRotationService(t *testing.T) (*OpenAIOAuthReauthService, *rotationTestRepo) {
	t.Helper()
	svc, _, repo, _, _, _ := newReauthTestService("acct-1")
	repo.config = &OpenAIOAuthReauthStoredConfig{AccountID: 42, LoginEmail: "user@example.com", CredentialMode: "password_totp", ProxySource: "account", PasswordCiphertext: "encrypted:secret-password", TOTPSecretCiphertext: "encrypted:JBSWY3DPEHPK3PXP"}
	wrapped := &rotationTestRepo{reauthTestRepo: repo, rotation: &OpenAITOTPRotation{TaskID: 7, AccountID: 42, State: "uncertain", WorkerID: "worker-private", CandidateCiphertext: "encrypted:GEZDGNBVGY3TQOJQ", PreviousCiphertext: repo.config.TOTPSecretCiphertext, PasswordCiphertext: repo.config.PasswordCiphertext, LoginEmail: repo.config.LoginEmail}}
	svc.repo = wrapped
	return svc, wrapped
}
func TestTOTPRotationStatusNeverSerializesSecrets(t *testing.T) {
	svc, _ := newRotationService(t)
	r, err := svc.GetTOTPRotation(context.Background(), 42)
	require.NoError(t, err)
	raw, err := json.Marshal(r)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"has_candidate":true`)
	for _, secret := range []string{"encrypted:", "GEZDGNBVGY3TQOJQ", "worker-private", "config_updated_at"} {
		require.NotContains(t, string(raw), secret)
	}
}
func TestTOTPRotationExportSeparatesCurrentAndRecovery(t *testing.T) {
	svc, repo := newRotationService(t)
	ctx := context.Background()
	_, err := svc.ExportTOTP(ctx, 42, "current", false)
	require.Error(t, err)
	out, err := svc.ExportTOTP(ctx, 42, "candidate", false)
	require.NoError(t, err)
	require.Empty(t, out.Password)
	require.Equal(t, "GEZDGNBVGY3TQOJQ", out.Secret)
	require.Contains(t, out.OTPAuthURI, "issuer=OpenAI")
	old, err := svc.ExportTOTP(ctx, 42, "previous", true)
	require.NoError(t, err)
	require.Equal(t, "secret-password", old.Password)
	require.Equal(t, "JBSWY3DPEHPK3PXP", old.Secret)
	repo.rotation.State = "succeeded"
	_, err = svc.ExportTOTP(ctx, 42, "candidate", false)
	require.Error(t, err)
	_, err = svc.ExportTOTP(ctx, 42, "current", false)
	require.NoError(t, err)
}
func TestTOTPRotationCandidateEncryptedBeforeRepository(t *testing.T) {
	svc, repo := newRotationService(t)
	require.NoError(t, svc.TOTPRotationPhase(context.Background(), 7, "worker", "prepared", "GEZDGNBVGY3TQOJQ"))
	binding, err := svc.decryptTOTP(repo.candidate)
	require.NoError(t, err)
	require.Equal(t, "GEZDGNBVGY3TQOJQ", binding.Secret)
	repo.candidate = ""
	svc.encryptionKeyConfigured = false
	require.Error(t, svc.TOTPRotationPhase(context.Background(), 7, "worker", "prepared", "GEZDGNBVGY3TQOJQ"))
	require.Empty(t, repo.candidate)
}
func TestTOTPRotationRejectsIdentityMismatchAndInvalidSecret(t *testing.T) {
	svc, repo := newRotationService(t)
	repo.config.LoginEmail = "other@example.test"
	_, _, err := svc.rotationConfig(context.Background(), 42)
	require.Error(t, err)
	repo.config.LoginEmail = "user@example.com"
	require.Error(t, svc.TOTPRotationPhase(context.Background(), 7, "worker", "prepared", "invalid!"))
	require.Empty(t, repo.candidate)
	require.Error(t, svc.RetryTOTPRotation(context.Background(), 42, 7, "rotate"))
}

type rotationBrokenEncryptor struct{ reauthTestEncryptor }

func (rotationBrokenEncryptor) Encrypt(string) (string, error) {
	return "", errors.New("synthetic-encryption-error")
}
func TestTOTPRotationFailedEncryptionDoesNotAdvance(t *testing.T) {
	svc, repo := newRotationService(t)
	svc.encryptor = rotationBrokenEncryptor{}
	require.Error(t, svc.TOTPRotationPhase(context.Background(), 7, "worker", "prepared", "GEZDGNBVGY3TQOJQ"))
	require.Empty(t, repo.candidate)
}

func TestTOTPRotationRequiresCapableWorker(t *testing.T) {
	svc, repo := newRotationService(t)
	_, err := svc.CreateTOTPRotation(context.Background(), 42)
	require.ErrorContains(t, err, "local 2FA worker")
	svc.totpWorkerLastSeen.Store(time.Now().UnixNano())
	repo.rotation.State = "queued"
	_, err = svc.CreateTOTPRotation(context.Background(), 42)
	require.NoError(t, err)
}

func TestTOTPRotationFinishPreservesSafeMultiFactorDiagnostics(t *testing.T) {
	for _, code := range []string{"new_accepted_old_valid", "old_accepted_new_valid", "verification_failed"} {
		t.Run(code, func(t *testing.T) {
			svc, repo := newRotationService(t)
			require.NoError(t, svc.FinishTOTPRotation(context.Background(), 7, "worker", false, code))
			require.Equal(t, code, repo.finishCode)
		})
	}
	svc, repo := newRotationService(t)
	require.NoError(t, svc.FinishTOTPRotation(context.Background(), 7, "worker", false, "untrusted-secret-body"))
	require.Equal(t, "worker_failed", repo.finishCode)
}

func (r *rotationTestRepo) GetTOTPRotationByTask(context.Context, int64) (*OpenAITOTPRotation, error) {
	return r.rotation, nil
}
func (r *rotationTestRepo) BindTOTPRotation(_ context.Context, _ int64, _ string, expected, updated string) error {
	if r.rotation.Action == "verify_old" {
		if r.rotation.PreviousCiphertext != expected {
			return errors.New("CAS")
		}
		r.rotation.PreviousCiphertext = updated
	} else {
		if r.rotation.CandidateCiphertext != expected {
			return errors.New("CAS")
		}
		r.rotation.CandidateCiphertext = updated
	}
	r.rotation.BindingVerified = true
	return nil
}
func (r *rotationTestRepo) RestoreTOTPProvenance(_ context.Context, _ int64, expected, updated string) error {
	if r.rotation.CandidateCiphertext != expected {
		return errors.New("CAS")
	}
	r.rotation.CandidateCiphertext = updated
	return nil
}

func TestTOTPCandidateBindingPreservesSecretAndRejectsWrongFactor(t *testing.T) {
	svc, repo := newRotationService(t)
	ctx := context.Background()
	require.NoError(t, svc.TOTPRotationPhase(ctx, 7, "worker-private", "prepared", "GEZDGNBVGY3TQOJQ", "F2", "enrollment"))
	repo.rotation.CandidateCiphertext = repo.candidate
	repo.rotation.State = "verifying"
	require.Error(t, svc.TOTPRotationPhase(ctx, 7, "worker-private", "bound", "GEZDGNBVGY3TQOJQ", "F1"))
	require.False(t, repo.rotation.BindingVerified)
	require.NoError(t, svc.TOTPRotationPhase(ctx, 7, "worker-private", "bound", "GEZDGNBVGY3TQOJQ", "F2"))
	b, err := svc.decryptTOTP(repo.rotation.CandidateCiphertext)
	require.NoError(t, err)
	require.Equal(t, "F2", b.FactorID)
	require.Equal(t, "enrollment", b.EnrollmentSessionID)
	require.True(t, repo.rotation.BindingVerified)
	require.Error(t, svc.TOTPRotationPhase(ctx, 7, "stale-worker", "bound", b.Secret, "F2"))
}
func TestTOTPJournalRecoveryIsIdempotentAndCannotReplaceProvenance(t *testing.T) {
	svc, repo := newRotationService(t)
	ctx := context.Background()
	require.NoError(t, svc.RecoverTOTPCandidate(ctx, 7, "GEZDGNBVGY3TQOJQ", "F2", "session"))
	saved := repo.rotation.CandidateCiphertext
	require.NoError(t, svc.RecoverTOTPCandidate(ctx, 7, "GEZDGNBVGY3TQOJQ", "F2", "session"))
	require.Equal(t, saved, repo.rotation.CandidateCiphertext)
	for _, args := range [][3]string{{"JBSWY3DPEHPK3PXP", "F2", "session"}, {"GEZDGNBVGY3TQOJQ", "F1", "session"}, {"GEZDGNBVGY3TQOJQ", "F2", "different"}} {
		require.Error(t, svc.RecoverTOTPCandidate(ctx, 7, args[0], args[1], args[2]))
	}
	require.Equal(t, saved, repo.rotation.CandidateCiphertext)
}
func TestTOTPRecoveryExportAfterAccountDeletionUsesTaskSnapshot(t *testing.T) {
	svc, repo := newRotationService(t)
	ctx := context.Background()
	reader, ok := svc.accounts.(*reauthTestAccountReader)
	require.True(t, ok)
	reader.account = nil
	out, err := svc.ExportTOTPRecovery(ctx, 7, "USER@example.com", "candidate", true)
	require.NoError(t, err)
	require.Equal(t, "GEZDGNBVGY3TQOJQ", out.Secret)
	require.Equal(t, "secret-password", out.Password)
	_, err = svc.ExportTOTPRecovery(ctx, 7, "other@example.com", "candidate", true)
	require.Error(t, err)
	repo.rotation.State = "succeeded"
	_, err = svc.ExportTOTPRecovery(ctx, 7, "user@example.com", "candidate", true)
	require.Error(t, err)
}
func TestTOTPRecoveryImportBindsMatchingUnconfiguredAccountWithoutEnrollment(t *testing.T) {
	svc, repo := newRotationService(t)
	ctx := context.Background()
	require.NoError(t, svc.RecoverTOTPCandidate(ctx, 7, "GEZDGNBVGY3TQOJQ", "F2", "session"))
	repo.config = nil
	reader, ok := svc.accounts.(*reauthTestAccountReader)
	require.True(t, ok)
	reader.account.Credentials["email"] = "wrong@example.com"
	require.Error(t, svc.ImportTOTPRecovery(ctx, 42, 7, "user@example.com", "candidate"))
	reader.account.Credentials["email"] = "user@example.com"
	require.NoError(t, svc.ImportTOTPRecovery(ctx, 42, 7, "user@example.com", "candidate"))
	b, err := svc.decryptTOTP(repo.config.TOTPSecretCiphertext)
	require.NoError(t, err)
	require.Equal(t, "F2", b.FactorID)
	require.EqualValues(t, 7, b.RecoveryTaskID)
	require.Equal(t, "local_worker", repo.config.Engine)
	require.Equal(t, "uncertain", repo.rotation.State)
	require.Error(t, svc.ImportTOTPRecovery(ctx, 42, 7, "user@example.com", "candidate"))
	_, err = svc.CreateTOTPRotation(ctx, 42)
	require.ErrorContains(t, err, "allow login only")
}
func TestTOTPLegacyAndBoundClaimCompatibility(t *testing.T) {
	svc, repo := newRotationService(t)
	binding, err := svc.encryptTOTP(openAITOTPBinding{Secret: "JBSWY3DPEHPK3PXP", FactorID: "F1"})
	require.NoError(t, err)
	repo.rotation.PreviousCiphertext = binding
	claim, err := svc.ClaimTOTPRotation(context.Background(), "worker")
	require.NoError(t, err)
	require.Equal(t, "JBSWY3DPEHPK3PXP", claim.TOTPSecret)
	require.Equal(t, "F1", claim.TOTPFactorID)
	require.Empty(t, claim.CandidateFactorID)
}

func (r *rotationTestRepo) InsertTOTPRecoveryConfig(ctx context.Context, cfg *OpenAIOAuthReauthStoredConfig) error {
	if r.config != nil {
		return errors.New("destination exists")
	}
	return r.UpsertConfig(ctx, cfg)
}
