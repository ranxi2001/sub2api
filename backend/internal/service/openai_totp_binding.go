package service

import (
	"context"
	"encoding/json"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Secret and provenance share one authenticated ciphertext and one database write.
// Legacy plaintext-inside-ciphertext records remain readable as unbound secrets.
type openAITOTPBinding struct {
	Version             int    `json:"v"`
	Secret              string `json:"secret"`
	FactorID            string `json:"factor_id,omitempty"`
	EnrollmentSessionID string `json:"enrollment_session_id,omitempty"`
	RecoveryTaskID      int64  `json:"recovery_task_id,omitempty"`
}

func validTOTPFactorID(id string) bool {
	if len(id) < 1 || len(id) > 256 {
		return false
	}
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}
func (s *OpenAIOAuthReauthService) decryptTOTP(ciphertext string) (openAITOTPBinding, error) {
	raw, err := s.decryptReauthSecret(ciphertext, "TOTP recovery record cannot be decrypted")
	if err != nil {
		return openAITOTPBinding{}, err
	}
	b := openAITOTPBinding{Secret: raw}
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		if json.Unmarshal([]byte(raw), &b) != nil || b.Version != 1 || b.Secret == "" || (b.FactorID != "" && !validTOTPFactorID(b.FactorID)) {
			return openAITOTPBinding{}, infraerrors.BadRequest("TOTP_BINDING_INVALID", "Invalid saved TOTP binding")
		}
	}
	return b, nil
}
func (s *OpenAIOAuthReauthService) encryptTOTP(b openAITOTPBinding) (string, error) {
	if err := s.ensureDurableEncryption(); err != nil {
		return "", err
	}
	secret, err := normalizeRotationSecret(b.Secret)
	if err != nil {
		return "", err
	}
	if b.FactorID != "" && !validTOTPFactorID(b.FactorID) || len(b.EnrollmentSessionID) > 512 {
		return "", infraerrors.BadRequest("TOTP_BINDING_INVALID", "Invalid TOTP provenance")
	}
	b.Version, b.Secret = 1, secret
	raw, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	ciphertext, err := s.encryptor.Encrypt(string(raw))
	if err != nil {
		return "", infraerrors.InternalServer("TOTP_ENCRYPT_FAILED", "Could not preserve TOTP provenance")
	}
	return ciphertext, nil
}

type openAITOTPBindingRepository interface {
	InsertTOTPRecoveryConfig(context.Context, *OpenAIOAuthReauthStoredConfig) error
	GetTOTPRotationByTask(context.Context, int64) (*OpenAITOTPRotation, error)
	BindTOTPRotation(context.Context, int64, string, string, string) error
	RestoreTOTPProvenance(context.Context, int64, string, string) error
}

func (s *OpenAIOAuthReauthService) bindingRepo() (openAITOTPBindingRepository, error) {
	repo, err := s.rotationRepo()
	if err != nil {
		return nil, err
	}
	b, ok := repo.(openAITOTPBindingRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("TOTP_UNAVAILABLE", "TOTP binding storage is unavailable")
	}
	return b, nil
}
func (s *OpenAIOAuthReauthService) bindTOTPRotation(ctx context.Context, task int64, worker, secret, factor string) error {
	repo, err := s.bindingRepo()
	if err != nil {
		return err
	}
	if !validTOTPFactorID(factor) {
		return infraerrors.BadRequest("TOTP_BINDING_INVALID", "Verified factor ID is required")
	}
	r, err := repo.GetTOTPRotationByTask(ctx, task)
	if err != nil || r == nil || r.WorkerID != worker || r.State != "verifying" {
		return rotationError()
	}
	ciphertext := r.CandidateCiphertext
	if r.Action == "verify_old" {
		ciphertext = r.PreviousCiphertext
	}
	b, err := s.decryptTOTP(ciphertext)
	if err != nil {
		return err
	}
	secret, err = normalizeRotationSecret(secret)
	if err != nil {
		return err
	}
	old, err := normalizeRotationSecret(b.Secret)
	if err != nil || secret != old || (b.FactorID != "" && b.FactorID != factor) {
		return rotationError()
	}
	b.FactorID = factor
	updated, err := s.encryptTOTP(b)
	if err != nil {
		return err
	}
	if err = repo.BindTOTPRotation(ctx, task, worker, ciphertext, updated); err != nil {
		return rotationError()
	}
	return nil
}

// Task-addressed recovery works after a soft delete. Snapshot identity is checked
// explicitly; ordinary account APIs continue to reject deleted accounts.
func (s *OpenAIOAuthReauthService) ExportTOTPRecovery(ctx context.Context, task int64, email, source string, includePassword bool) (*OpenAITOTPExport, error) {
	repo, err := s.bindingRepo()
	if err != nil {
		return nil, err
	}
	r, err := repo.GetTOTPRotationByTask(ctx, task)
	if err != nil || r == nil || r.State != "uncertain" || r.LoginEmail == "" || !strings.EqualFold(strings.TrimSpace(email), r.LoginEmail) {
		return nil, rotationError()
	}
	ciphertext := r.CandidateCiphertext
	if source == "previous" {
		ciphertext = r.PreviousCiphertext
	} else if source != "candidate" {
		return nil, rotationError()
	}
	if ciphertext == "" {
		return nil, rotationError()
	}
	b, err := s.decryptTOTP(ciphertext)
	if err != nil {
		return nil, err
	}
	out, err := totpExport(r.LoginEmail, b, r.State, source)
	if err != nil {
		return nil, err
	}
	if includePassword {
		out.Password, err = s.decryptReauthSecret(r.PasswordCiphertext, "Recovery password cannot be decrypted")
	}
	return out, err
}

// Import config only into an explicitly selected, matching, unconfigured account.
// It neither activates a factor nor claims that a candidate has been verified.
func (s *OpenAIOAuthReauthService) ImportTOTPRecovery(ctx context.Context, id, task int64, email, source string) error {
	out, err := s.ExportTOTPRecovery(ctx, task, email, source, true)
	if err != nil {
		return err
	}
	account, err := s.accountFor(ctx, id)
	if err != nil {
		return err
	}
	actual, _ := account.Credentials["email"].(string)
	if actual == "" {
		actual, _ = account.Extra["email"].(string)
	}
	if actual == "" {
		actual, _ = account.Extra["email_address"].(string)
	}
	if !strings.EqualFold(strings.TrimSpace(actual), out.Email) {
		return infraerrors.BadRequest("TOTP_IDENTITY_MISMATCH", "Recovery identity must match the destination account")
	}
	cfg, err := s.repo.GetConfig(ctx, id)
	if err != nil {
		return err
	}
	if cfg != nil {
		return infraerrors.Conflict("TOTP_CONFIG_EXISTS", "Recovery import requires an unconfigured destination account")
	}
	if out.Password == "" {
		return infraerrors.BadRequest("TOTP_CONFIG_REQUIRED", "Recovery snapshot has no saved password")
	}
	password, err := s.encryptor.Encrypt(out.Password)
	if err != nil {
		return infraerrors.InternalServer("TOTP_ENCRYPT_FAILED", "Could not preserve recovery credentials")
	}
	ciphertext, err := s.encryptTOTP(openAITOTPBinding{Secret: out.Secret, FactorID: out.FactorID, RecoveryTaskID: task})
	if err != nil {
		return err
	}
	repo, err := s.bindingRepo()
	if err != nil {
		return err
	}
	return repo.InsertTOTPRecoveryConfig(ctx, &OpenAIOAuthReauthStoredConfig{AccountID: id, LoginEmail: out.Email, CredentialMode: OpenAIOAuthReauthModePasswordTOTP, Engine: OpenAIOAuthReauthEngineLocal, ProxySource: OpenAIOAuthReauthProxySourceAccount, PasswordCiphertext: password, TOTPSecretCiphertext: ciphertext})
}
