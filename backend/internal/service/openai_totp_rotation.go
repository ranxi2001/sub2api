package service

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// OpenAITOTPRotation is safe to serialize. Ciphertexts and worker ownership never leave the service.
type OpenAITOTPRotation struct {
	TaskID              int64     `json:"task_id"`
	AccountID           int64     `json:"account_id"`
	State               string    `json:"state"`
	Action              string    `json:"action"`
	ErrorCode           string    `json:"error_code,omitempty"`
	UpdatedAt           time.Time `json:"updated_at"`
	HasCandidate        bool      `json:"has_candidate"`
	WorkerID            string    `json:"-"`
	CandidateCiphertext string    `json:"-"`
	ConfigUpdatedAt     time.Time `json:"-"`
}

type OpenAITOTPRotationRepository interface {
	RecoverTOTPCandidate(context.Context, int64, string) error
	GetTOTPRotation(context.Context, int64) (*OpenAITOTPRotation, error)
	CreateTOTPRotation(context.Context, int64, string, time.Time) (*OpenAITOTPRotation, error)
	ClaimTOTPRotation(context.Context, string) (*OpenAITOTPRotation, error)
	TransitionTOTPRotation(context.Context, int64, string, string, string, string) error
	FinishTOTPRotation(context.Context, int64, string, bool, string) error
	RetryTOTPRotation(context.Context, int64, int64, string) error
}

func (s *OpenAIOAuthReauthService) rotationRepo() (OpenAITOTPRotationRepository, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}
	repo, ok := s.repo.(OpenAITOTPRotationRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("TOTP_UNAVAILABLE", "2FA rotation is unavailable")
	}
	return repo, nil
}
func rotationError() error {
	return infraerrors.Conflict("TOTP_STATE_CHANGED", "2FA task state changed; reload its status")
}
func safeRotation(r *OpenAITOTPRotation) *OpenAITOTPRotation {
	if r == nil {
		return nil
	}
	copy := *r
	copy.HasCandidate = copy.CandidateCiphertext != ""
	return &copy
}
func normalizeRotationSecret(raw string) (string, error) {
	value := strings.ToUpper(strings.Join(strings.Fields(raw), ""))
	value = strings.TrimRight(value, "=")
	if len(value) < 16 || len(value) > 128 {
		return "", infraerrors.BadRequest("TOTP_SECRET_INVALID", "Invalid TOTP secret")
	}
	if _, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(value); err != nil {
		return "", infraerrors.BadRequest("TOTP_SECRET_INVALID", "Invalid TOTP secret")
	}
	return value, nil
}
func (s *OpenAIOAuthReauthService) rotationConfig(ctx context.Context, id int64) (*Account, *OpenAIOAuthReauthStoredConfig, error) {
	if err := s.ensureDurableEncryption(); err != nil {
		return nil, nil, err
	}
	account, err := s.accountFor(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := s.repo.GetConfig(ctx, id)
	if err != nil {
		return nil, nil, infraerrors.InternalServer("TOTP_CONFIG_READ_FAILED", "Could not read login configuration")
	}
	if cfg == nil || cfg.CredentialMode != OpenAIOAuthReauthModePasswordTOTP || cfg.PasswordCiphertext == "" || cfg.TOTPSecretCiphertext == "" {
		return nil, nil, infraerrors.BadRequest("TOTP_CONFIG_REQUIRED", "Save the account password and current TOTP secret first")
	}
	// Only rotate a saved login identity that matches the managed account.
	email, _ := account.Credentials["email"].(string)
	if email == "" {
		email, _ = account.Extra["email"].(string)
	}
	if email == "" {
		email, _ = account.Extra["email_address"].(string)
	}
	if email == "" || !strings.EqualFold(strings.TrimSpace(email), cfg.LoginEmail) {
		return nil, nil, infraerrors.BadRequest("TOTP_IDENTITY_MISMATCH", "Saved login email must match the account email")
	}
	return account, cfg, nil
}
func (s *OpenAIOAuthReauthService) GetTOTPRotation(ctx context.Context, id int64) (*OpenAITOTPRotation, error) {
	repo, err := s.rotationRepo()
	if err != nil {
		return nil, err
	}
	if _, err = s.accountFor(ctx, id); err != nil {
		return nil, err
	}
	result, err := repo.GetTOTPRotation(ctx, id)
	if err != nil {
		return nil, infraerrors.InternalServer("TOTP_READ_FAILED", "Could not read 2FA task")
	}
	return safeRotation(result), nil
}
func (s *OpenAIOAuthReauthService) CreateTOTPRotation(ctx context.Context, id int64) (*OpenAITOTPRotation, error) {
	return s.createTOTPRotation(ctx, id, false)
}

// CreateTOTPRotationOn429 uses the same saved credentials and recovery protocol
// as manual rotation, with an additional durable cooldown for automatic tasks.
func (s *OpenAIOAuthReauthService) CreateTOTPRotationOn429(ctx context.Context, id int64) (*OpenAITOTPRotation, error) {
	return s.createTOTPRotation(ctx, id, true)
}

type openAITOTP429Repository interface {
	CreateTOTPRotationOn429(context.Context, int64, string, time.Time) (*OpenAITOTPRotation, error)
}

func (s *OpenAIOAuthReauthService) createTOTPRotation(ctx context.Context, id int64, automatic bool) (*OpenAITOTPRotation, error) {
	repo, err := s.rotationRepo()
	if err != nil {
		return nil, err
	}
	account, cfg, err := s.rotationConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	// Re-read the account instead of trusting the gateway's scheduling snapshot.
	if automatic && (!openAITOTPRotateOn429Enabled(account) || !account.IsActive()) {
		return nil, nil
	}
	if err = s.checkWorkerMode(OpenAIOAuthReauthModePasswordTOTP); err != nil {
		return nil, err
	}
	s.EnsureWorker()
	if last := s.totpWorkerLastSeen.Load(); last == 0 || time.Since(time.Unix(0, last)) > 15*time.Minute {
		return nil, infraerrors.ServiceUnavailable("TOTP_WORKER_UNAVAILABLE", "A local 2FA worker with persistent recovery storage must be online")
	}
	hash, err := hashReauthCredentials(account.Credentials)
	if err != nil {
		return nil, err
	}
	var result *OpenAITOTPRotation
	if automatic {
		autoRepo, ok := repo.(openAITOTP429Repository)
		if !ok {
			return nil, infraerrors.ServiceUnavailable("TOTP_UNAVAILABLE", "Automatic 2FA rotation is unavailable")
		}
		result, err = autoRepo.CreateTOTPRotationOn429(ctx, id, hash, cfg.UpdatedAt)
	} else {
		result, err = repo.CreateTOTPRotation(ctx, id, hash, cfg.UpdatedAt)
	}
	if err != nil {
		if infraerrors.Code(err) == 409 {
			return nil, err
		}
		return nil, infraerrors.InternalServer("TOTP_CREATE_FAILED", "Could not create 2FA task")
	}
	return safeRotation(result), nil
}

type OpenAITOTPRotationClaim struct {
	Operation  string `json:"operation"`
	TaskID     int64  `json:"task_id"`
	AccountID  int64  `json:"account_id"`
	Action     string `json:"action"`
	LoginEmail string `json:"login_email"`
	Password   string `json:"password"`
	TOTPSecret string `json:"totp_secret"`
	Candidate  string `json:"candidate_secret,omitempty"`
	ProxyURL   string `json:"proxy_url,omitempty"`
}

func (s *OpenAIOAuthReauthService) ClaimTOTPRotation(ctx context.Context, worker string) (*OpenAITOTPRotationClaim, error) {
	repo, err := s.rotationRepo()
	if err != nil {
		return nil, err
	}
	if worker == "" || len(worker) > 128 {
		return nil, infraerrors.BadRequest("TOTP_WORKER_INVALID", "Invalid worker ID")
	}
	s.workerLastSeen.Store(time.Now().UnixNano())
	s.totpWorkerLastSeen.Store(time.Now().UnixNano())
	record, err := repo.ClaimTOTPRotation(ctx, worker)
	if err != nil {
		return nil, infraerrors.InternalServer("TOTP_CLAIM_FAILED", "Could not claim 2FA task")
	}
	if record == nil {
		return nil, nil
	}
	s.releaseMihomoLease(-record.TaskID)
	account, cfg, err := s.rotationConfig(ctx, record.AccountID)
	if err == nil && !record.ConfigUpdatedAt.Equal(cfg.UpdatedAt) {
		err = errors.New("configuration changed")
	}
	claim := &OpenAITOTPRotationClaim{Operation: "totp_rotation", TaskID: record.TaskID, AccountID: record.AccountID, Action: record.Action}
	if err == nil {
		claim.LoginEmail = cfg.LoginEmail
		claim.Password, err = s.decryptReauthSecret(cfg.PasswordCiphertext, "Password cannot be decrypted")
	}
	if err == nil {
		claim.TOTPSecret, err = s.decryptReauthSecret(cfg.TOTPSecretCiphertext, "TOTP cannot be decrypted")
	}
	if err == nil && record.CandidateCiphertext != "" {
		claim.Candidate, err = s.decryptReauthSecret(record.CandidateCiphertext, "Candidate TOTP cannot be decrypted")
	}
	if err == nil {
		source, sourceErr := normalizeReauthProxySource(cfg.ProxySource, cfg.ProxyID)
		err = sourceErr
		if err == nil {
			switch source {
			case OpenAIOAuthReauthProxySourceAccount:
				claim.ProxyURL, err = s.reauthProxyURL(ctx, account.ProxyID)
			case OpenAIOAuthReauthProxySourceManagedProxy:
				claim.ProxyURL, err = s.reauthProxyURL(ctx, cfg.ProxyID)
			case OpenAIOAuthReauthProxySourceMihomo:
				if s.acquireMihomoProxy == nil {
					err = errors.New("proxy unavailable")
				} else {
					var release func()
					claim.ProxyURL, release, err = s.acquireMihomoProxy(ctx, fmt.Sprintf("openai-totp-task:%d", record.TaskID))
					if release != nil {
						if err != nil {
							release()
						} else {
							s.replaceMihomoLease(-record.TaskID, release)
						}
					}
				}
			}
		}
	}
	if err != nil {
		_ = repo.FinishTOTPRotation(ctx, record.TaskID, worker, false, "configuration_unavailable")
		s.releaseMihomoLease(-record.TaskID)
		return nil, infraerrors.BadRequest("TOTP_CONFIG_UNAVAILABLE", "2FA task login configuration is unavailable")
	}
	return claim, nil
}

// Phase is an acknowledged write barrier. Enrollment/activation must not run until it succeeds.
func (s *OpenAIOAuthReauthService) TOTPRotationPhase(ctx context.Context, task int64, worker, phase, secret string) error {
	repo, err := s.rotationRepo()
	if err != nil {
		return err
	}
	from := ""
	candidate := ""
	switch phase {
	case "enrolling":
		from = "running"
	case "prepared":
		from = "enrolling"
		if err = s.ensureDurableEncryption(); err != nil {
			return err
		}
		secret, err = normalizeRotationSecret(secret)
		if err != nil {
			return err
		}
		candidate, err = s.encryptor.Encrypt(secret)
		if err != nil {
			return infraerrors.InternalServer("TOTP_ENCRYPT_FAILED", "Could not preserve candidate TOTP")
		}
	case "activating":
		from = "prepared"
	case "verifying":
		from = "activating"
	case "verify_recovery":
		from = "running"
		phase = "verifying"
	default:
		return infraerrors.BadRequest("TOTP_PHASE_INVALID", "Invalid 2FA task phase")
	}
	if err = repo.TransitionTOTPRotation(ctx, task, worker, from, phase, candidate); err != nil {
		return rotationError()
	}
	return nil
}
func (s *OpenAIOAuthReauthService) FinishTOTPRotation(ctx context.Context, task int64, worker string, success bool, code string) error {
	repo, err := s.rotationRepo()
	if err != nil {
		return err
	}
	if success {
		code = ""
	} else {
		switch code {
		case "login_failed", "identity_mismatch", "enrollment_failed", "activation_unconfirmed", "verification_failed", "worker_failed":
		default:
			code = "worker_failed"
		}
	}
	if err = repo.FinishTOTPRotation(ctx, task, worker, success, code); err != nil {
		return rotationError()
	}
	s.releaseMihomoLease(-task)
	return nil
}
func (s *OpenAIOAuthReauthService) RetryTOTPRotation(ctx context.Context, id, task int64, action string) error {
	repo, err := s.rotationRepo()
	if err != nil {
		return err
	}
	if _, _, err = s.rotationConfig(ctx, id); err != nil {
		return err
	}
	if action != "verify_new" && action != "verify_old" {
		return infraerrors.BadRequest("TOTP_ACTION_INVALID", "Only verification can be retried")
	}
	if err = repo.RetryTOTPRotation(ctx, id, task, action); err != nil {
		return rotationError()
	}
	return nil
}

type OpenAITOTPExport struct {
	Email      string `json:"email"`
	Password   string `json:"password,omitempty"`
	Secret     string `json:"secret"`
	OTPAuthURI string `json:"otpauth_uri"`
	State      string `json:"state"`
	Source     string `json:"source"`
}

func (s *OpenAIOAuthReauthService) ExportTOTP(ctx context.Context, id int64, source string, includePassword bool) (*OpenAITOTPExport, error) {
	repo, err := s.rotationRepo()
	if err != nil {
		return nil, err
	}
	_, cfg, err := s.rotationConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	rotation, err := repo.GetTOTPRotation(ctx, id)
	if err != nil {
		return nil, infraerrors.InternalServer("TOTP_READ_FAILED", "Could not read 2FA task")
	}
	ciphertext := cfg.TOTPSecretCiphertext
	state := "saved"
	if rotation != nil {
		state = rotation.State
	}
	switch source {
	case "current":
		if rotation != nil && rotation.State != "succeeded" && rotation.State != "failed" {
			return nil, infraerrors.Conflict("TOTP_UNRESOLVED", "Resolve the 2FA task or export its recovery credentials")
		}
	case "candidate":
		if rotation == nil || rotation.State != "uncertain" || rotation.CandidateCiphertext == "" {
			return nil, rotationError()
		}
		ciphertext = rotation.CandidateCiphertext
	case "previous":
		if rotation == nil || rotation.State != "uncertain" {
			return nil, rotationError()
		}
	default:
		return nil, infraerrors.BadRequest("TOTP_EXPORT_INVALID", "Invalid export source")
	}
	secret, err := s.decryptReauthSecret(ciphertext, "TOTP cannot be decrypted")
	if err != nil {
		return nil, err
	}
	secret, err = normalizeRotationSecret(secret)
	if err != nil {
		return nil, err
	}
	result := &OpenAITOTPExport{Email: cfg.LoginEmail, Secret: secret, State: state, Source: source}
	q := url.Values{"secret": {secret}, "issuer": {"OpenAI"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	result.OTPAuthURI = "otpauth://totp/" + url.PathEscape("OpenAI:"+cfg.LoginEmail) + "?" + q.Encode()
	if includePassword {
		result.Password, err = s.decryptReauthSecret(cfg.PasswordCiphertext, "Password cannot be decrypted")
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *OpenAIOAuthReauthService) RecoverTOTPCandidate(ctx context.Context, task int64, secret string) error {
	repo, err := s.rotationRepo()
	if err != nil {
		return err
	}
	if err = s.ensureDurableEncryption(); err != nil {
		return err
	}
	secret, err = normalizeRotationSecret(secret)
	if err != nil {
		return err
	}
	candidate, err := s.encryptor.Encrypt(secret)
	if err != nil {
		return infraerrors.InternalServer("TOTP_ENCRYPT_FAILED", "Could not preserve candidate TOTP")
	}
	if err = repo.RecoverTOTPCandidate(ctx, task, candidate); err != nil {
		return rotationError()
	}
	return nil
}
