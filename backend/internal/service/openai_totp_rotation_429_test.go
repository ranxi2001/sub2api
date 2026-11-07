package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type totp429TestRepo struct {
	*rotationTestRepo
	calls   int
	hash    string
	version time.Time
}

func (r *totp429TestRepo) CreateTOTPRotationOn429(_ context.Context, _ int64, hash string, version time.Time) (*OpenAITOTPRotation, error) {
	r.calls++
	r.hash, r.version = hash, version
	return r.rotation, nil
}

func newTOTP429TestService(t *testing.T) (*OpenAIOAuthReauthService, *totp429TestRepo, *Account) {
	t.Helper()
	svc, base := newRotationService(t)
	repo := &totp429TestRepo{rotationTestRepo: base}
	svc.repo = repo
	reader, ok := svc.accounts.(*reauthTestAccountReader)
	require.True(t, ok)
	account := reader.account
	account.Status = StatusActive
	account.Extra = map[string]any{OpenAITOTPRotateOn429ExtraKey: true}
	svc.totpWorkerLastSeen.Store(time.Now().UnixNano())
	repo.rotation.State, repo.rotation.Action = "queued", "rotate"
	return svc, repo, account
}

func TestTOTPRotationOn429UsesCredentialOperationsConfig(t *testing.T) {
	svc, repo, account := newTOTP429TestService(t)
	task, err := svc.CreateTOTPRotationOn429(t.Context(), account.ID)
	require.NoError(t, err)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, repo.config.UpdatedAt, repo.version)
	expectedHash, err := hashReauthCredentials(account.Credentials)
	require.NoError(t, err)
	require.Equal(t, expectedHash, repo.hash)
	// Recovery fields stay available internally but must never reach the API.
	status, err := json.Marshal(task)
	require.NoError(t, err)
	require.NotContains(t, string(status), "encrypted:")
	require.NotContains(t, string(status), "worker-private")
	claim, err := svc.ClaimTOTPRotation(t.Context(), "worker")
	require.NoError(t, err)
	require.Equal(t, "user@example.com", claim.LoginEmail)
	require.Equal(t, "secret-password", claim.Password)
	require.Equal(t, "JBSWY3DPEHPK3PXP", claim.TOTPSecret)
}

func TestTOTPRotationOn429RechecksOptInAndAccountStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Account)
	}{
		{"opt-out since scheduling", func(a *Account) { a.Extra[OpenAITOTPRotateOn429ExtraKey] = false }},
		{"legacy account", func(a *Account) { a.Extra = nil }},
		{"disabled account", func(a *Account) { a.Status = StatusDisabled }},
		{"independent connection", func(a *Account) { a.AuthConnectionID = "connection" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, account := newTOTP429TestService(t)
			tc.mutate(account)
			task, err := svc.CreateTOTPRotationOn429(t.Context(), account.ID)
			require.NoError(t, err)
			require.Nil(t, task)
			require.Zero(t, repo.calls)
		})
	}
}

func TestTOTPRotationOn429RequiresSavedCredentialsAndWorker(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*OpenAIOAuthReauthService, *totp429TestRepo)
	}{
		{"missing 2FA", func(_ *OpenAIOAuthReauthService, r *totp429TestRepo) { r.config.TOTPSecretCiphertext = "" }},
		{"missing password", func(_ *OpenAIOAuthReauthService, r *totp429TestRepo) { r.config.PasswordCiphertext = "" }},
		{"wrong identity", func(_ *OpenAIOAuthReauthService, r *totp429TestRepo) { r.config.LoginEmail = "other@example.test" }},
		{"encryption disabled", func(s *OpenAIOAuthReauthService, _ *totp429TestRepo) { s.encryptionKeyConfigured = false }},
		{"worker unavailable", func(s *OpenAIOAuthReauthService, _ *totp429TestRepo) { s.totpWorkerLastSeen.Store(0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, account := newTOTP429TestService(t)
			tc.mutate(svc, repo)
			task, err := svc.CreateTOTPRotationOn429(t.Context(), account.ID)
			require.Error(t, err)
			require.Nil(t, task)
			require.Zero(t, repo.calls)
		})
	}
}
