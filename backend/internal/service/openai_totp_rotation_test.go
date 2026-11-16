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
	wrapped := &rotationTestRepo{reauthTestRepo: repo, rotation: &OpenAITOTPRotation{TaskID: 7, AccountID: 42, State: "uncertain", WorkerID: "worker-private", CandidateCiphertext: "encrypted:GEZDGNBVGY3TQOJQ"}}
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
	require.Equal(t, "encrypted:GEZDGNBVGY3TQOJQ", repo.candidate)
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
