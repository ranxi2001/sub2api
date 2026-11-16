package service

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	OpenAISessionExportOperation                 = "chatgpt_session_export"
	OpenAISessionExportStatusQueued              = "queued"
	OpenAISessionExportStatusRunning             = "running"
	OpenAISessionExportStatusSucceeded           = "succeeded"
	OpenAISessionExportStatusFailed              = "failed"
	OpenAISessionExportStatusExpired             = "expired"
	OpenAISessionExportStageQueued               = "queued"
	OpenAISessionExportStageStarting             = "starting"
	OpenAISessionExportStageProtocolConnecting   = "protocol_connecting"
	OpenAISessionExportStagePasswordSubmitted    = "password_submitted"
	OpenAISessionExportStageMFASubmitted         = "mfa_submitted"
	OpenAISessionExportStageSessionRequest       = "session_request"
	OpenAISessionExportStageIdentityVerification = "identity_verification"
	OpenAISessionExportStageSucceeded            = "succeeded"
	OpenAISessionExportStageFailed               = "failed"
)

const (
	openAISessionExportResultTTL  = 10 * time.Minute
	openAISessionExportTaskTTL    = 5 * time.Minute
	openAISessionExportStaleAfter = 5 * time.Minute
	openAISessionExportMaxBody    = 1 << 20
)

type OpenAISessionExportTask struct {
	ID         int64      `json:"id"`
	AccountID  int64      `json:"account_id"`
	Status     string     `json:"status"`
	Stage      string     `json:"stage"`
	ErrorCode  string     `json:"error_code,omitempty"`
	Attempt    int        `json:"attempt"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type OpenAISessionExportTaskRecord struct {
	OpenAISessionExportTask
	WorkerID                string
	ExpectedCredentialsHash string
	ClaimedAt               *time.Time
}

type OpenAISessionExportClaim struct {
	Operation      string `json:"operation"`
	TaskID         int64  `json:"task_id"`
	AccountID      int64  `json:"account_id"`
	LoginEmail     string `json:"login_email"`
	Password       string `json:"password"`
	TOTPSecret     string `json:"totp_secret"`
	TOTPFactorID   string `json:"totp_factor_id,omitempty"`
	ProxyURL       string `json:"proxy_url,omitempty"`
	ExpectedUserID string `json:"expected_user_id,omitempty"`
}

type OpenAISessionExportRepository interface {
	CreateSessionExportTask(context.Context, int64, string) (*OpenAISessionExportTaskRecord, error)
	GetSessionExportTask(context.Context, int64) (*OpenAISessionExportTaskRecord, error)
	ExpireActiveSessionExportTasks(context.Context) error
	ClaimSessionExportTask(context.Context, string, time.Duration) (*OpenAISessionExportTaskRecord, error)
	UpdateSessionExportStage(context.Context, int64, string, string) error
	FinishSessionExportTask(context.Context, int64, string, string, string) error
}

type sessionExportResult struct {
	body      json.RawMessage
	expiresAt time.Time
}

type sessionExportResultStore struct {
	mu      sync.Mutex
	results map[int64]sessionExportResult
}

func newSessionExportResultStore() *sessionExportResultStore {
	return &sessionExportResultStore{results: make(map[int64]sessionExportResult)}
}

func (s *sessionExportResultStore) put(taskID int64, body json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked(time.Now())
	s.results[taskID] = sessionExportResult{body: append(json.RawMessage(nil), body...), expiresAt: time.Now().Add(openAISessionExportResultTTL)}
}

func (s *sessionExportResultStore) consume(taskID int64) (json.RawMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked(time.Now())
	result, ok := s.results[taskID]
	if !ok {
		return nil, false
	}
	delete(s.results, taskID)
	return append(json.RawMessage(nil), result.body...), true
}

func (s *sessionExportResultStore) gcLocked(now time.Time) {
	for id, result := range s.results {
		if !result.expiresAt.After(now) {
			delete(s.results, id)
		}
	}
}

// The re-auth service owns the Worker authentication and credential decryptor;
// Session export uses a separate task repository and never updates account OAuth
// credentials.
func (s *OpenAIOAuthReauthService) SetSessionExportRepository(repo OpenAISessionExportRepository) {
	s.sessionExportRepo = repo
	if s.sessionExportResults == nil {
		s.sessionExportResults = newSessionExportResultStore()
	}
	// Results are process-local. Any task that was active before an API restart
	// can no longer produce a downloadable result and must not remain claimable.
	if repo != nil {
		_ = repo.ExpireActiveSessionExportTasks(context.Background())
	}
}

func (s *OpenAIOAuthReauthService) CreateSessionExportTask(ctx context.Context, accountID int64) (*OpenAISessionExportTask, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}
	if s.sessionExportRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPENAI_SESSION_EXPORT_UNAVAILABLE", "ChatGPT Session export is unavailable")
	}
	if err := s.ensureDurableEncryption(); err != nil {
		return nil, err
	}
	account, err := s.accountFor(ctx, accountID)
	if err != nil {
		return nil, err
	}
	config, err := s.repo.GetConfig(ctx, accountID)
	if err != nil || config == nil {
		return nil, infraerrors.BadRequest("OPENAI_SESSION_EXPORT_CONFIG_REQUIRED", "Save the account password and TOTP secret in Credential Operations first")
	}
	if strings.TrimSpace(config.CredentialMode) != OpenAIOAuthReauthModePasswordTOTP ||
		strings.TrimSpace(config.PasswordCiphertext) == "" || strings.TrimSpace(config.TOTPSecretCiphertext) == "" {
		return nil, infraerrors.BadRequest("OPENAI_SESSION_EXPORT_CONFIG_REQUIRED", "Save the account password and TOTP secret in Credential Operations first")
	}
	if err := s.checkWorkerMode(OpenAIOAuthReauthModePasswordTOTP); err != nil {
		return nil, err
	}
	hash, err := hashReauthCredentials(account.Credentials)
	if err != nil {
		return nil, infraerrors.InternalServer("OPENAI_SESSION_EXPORT_SNAPSHOT_FAILED", "Could not snapshot account credentials")
	}
	record, err := s.sessionExportRepo.CreateSessionExportTask(ctx, accountID, hash)
	if err != nil {
		if infraerrors.Code(err) == http.StatusConflict {
			return nil, err
		}
		return nil, infraerrors.InternalServer("OPENAI_SESSION_EXPORT_CREATE_FAILED", "Could not create Session export task")
	}
	return safeSessionExportTask(record), nil
}

func (s *OpenAIOAuthReauthService) GetSessionExportTask(ctx context.Context, accountID, taskID int64) (*OpenAISessionExportTask, error) {
	if s == nil || s.sessionExportRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPENAI_SESSION_EXPORT_UNAVAILABLE", "ChatGPT Session export is unavailable")
	}
	record, err := s.sessionExportRepo.GetSessionExportTask(ctx, taskID)
	if err != nil || record == nil || record.AccountID != accountID {
		return nil, infraerrors.NotFound("OPENAI_SESSION_EXPORT_TASK_NOT_FOUND", "Session export task was not found")
	}
	if (record.Status == OpenAISessionExportStatusRunning && time.Since(record.UpdatedAt) > openAISessionExportTaskTTL) ||
		(record.Status == OpenAISessionExportStatusQueued && time.Since(record.CreatedAt) > openAISessionExportTaskTTL) {
		_ = s.sessionExportRepo.FinishSessionExportTask(ctx, taskID, record.WorkerID, OpenAISessionExportStatusExpired, "worker_timeout")
		record.Status = OpenAISessionExportStatusExpired
		record.ErrorCode = "worker_timeout"
	}
	return safeSessionExportTask(record), nil
}

func (s *OpenAIOAuthReauthService) DownloadSessionExport(ctx context.Context, accountID, taskID int64) (json.RawMessage, error) {
	task, err := s.GetSessionExportTask(ctx, accountID, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != OpenAISessionExportStatusSucceeded {
		return nil, infraerrors.Conflict("OPENAI_SESSION_EXPORT_NOT_READY", "Session export is not ready")
	}
	if s.sessionExportResults == nil {
		return nil, infraerrors.New(http.StatusGone, "OPENAI_SESSION_EXPORT_EXPIRED", "Session export has expired or was already downloaded")
	}
	body, ok := s.sessionExportResults.consume(taskID)
	if !ok {
		return nil, infraerrors.New(http.StatusGone, "OPENAI_SESSION_EXPORT_EXPIRED", "Session export has expired or was already downloaded")
	}
	return body, nil
}

func (s *OpenAIOAuthReauthService) ClaimSessionExportTask(ctx context.Context, workerID string, capabilities []string) (*OpenAISessionExportClaim, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || len(workerID) > 128 {
		return nil, infraerrors.BadRequest("OPENAI_REAUTH_WORKER_ID_INVALID", "worker id is required")
	}
	s.MarkWorkerSeen()
	if s.sessionExportRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPENAI_SESSION_EXPORT_UNAVAILABLE", "ChatGPT Session export is unavailable")
	}
	for _, capability := range capabilities {
		if capability == OpenAISessionExportOperation {
			record, err := s.sessionExportRepo.ClaimSessionExportTask(ctx, workerID, openAISessionExportStaleAfter)
			if err != nil {
				return nil, infraerrors.InternalServer("OPENAI_SESSION_EXPORT_CLAIM_FAILED", "Could not claim Session export task")
			}
			if record == nil {
				return nil, nil
			}
			account, err := s.accountFor(ctx, record.AccountID)
			if err != nil {
				_ = s.sessionExportRepo.FinishSessionExportTask(ctx, record.ID, workerID, OpenAISessionExportStatusFailed, "account_invalid")
				return nil, nil
			}
			config, err := s.repo.GetConfig(ctx, record.AccountID)
			if err != nil || config == nil || config.CredentialMode != OpenAIOAuthReauthModePasswordTOTP {
				_ = s.sessionExportRepo.FinishSessionExportTask(ctx, record.ID, workerID, OpenAISessionExportStatusFailed, "config_required")
				return nil, nil
			}
			password, err := s.decryptReauthSecret(config.PasswordCiphertext, "login password cannot be decrypted")
			if err != nil || password == "" {
				_ = s.sessionExportRepo.FinishSessionExportTask(ctx, record.ID, workerID, OpenAISessionExportStatusFailed, "password_unavailable")
				return nil, nil
			}
			totpSecret, err := s.decryptTOTP(config.TOTPSecretCiphertext)
			if err != nil || totpSecret.Secret == "" {
				_ = s.sessionExportRepo.FinishSessionExportTask(ctx, record.ID, workerID, OpenAISessionExportStatusFailed, "totp_unavailable")
				return nil, nil
			}
			var proxyURL string
			if account.OpenAIRequestProxyEnabled() {
				// The worker receives one fixed exit for its whole login flow.
				// Explicit alternate sources cannot escape the selected account group.
				source, sourceErr := normalizeReauthProxySource(config.ProxySource, config.ProxyID)
				if sourceErr != nil || source != OpenAIOAuthReauthProxySourceAccount {
					_ = s.sessionExportRepo.FinishSessionExportTask(ctx, record.ID, workerID, OpenAISessionExportStatusFailed, "proxy_unavailable")
					return nil, nil
				}
				proxyURL, err = resolveOpenAIMaintenanceProxy(ctx, account, s.oauth.proxyRepo)
			} else {
				proxyURL, err = s.reauthProxyURL(ctx, account.ProxyID)
			}
			if err != nil {
				_ = s.sessionExportRepo.FinishSessionExportTask(ctx, record.ID, workerID, OpenAISessionExportStatusFailed, "proxy_unavailable")
				return nil, nil
			}
			expectedUserID := strings.TrimSpace(account.GetCredential("chatgpt_user_id"))
			s.workerLastSeen.Store(time.Now().UnixNano())
			return &OpenAISessionExportClaim{Operation: OpenAISessionExportOperation, TaskID: record.ID, AccountID: record.AccountID, LoginEmail: config.LoginEmail, Password: password, TOTPSecret: totpSecret.Secret, TOTPFactorID: totpSecret.FactorID, ProxyURL: proxyURL, ExpectedUserID: expectedUserID}, nil
		}
	}
	return nil, nil
}

func (s *OpenAIOAuthReauthService) UpdateSessionExportStage(ctx context.Context, taskID int64, workerID, stage string) error {
	if !validSessionExportStage(stage) {
		return infraerrors.BadRequest("OPENAI_SESSION_EXPORT_STAGE_INVALID", "Invalid Session export stage")
	}
	return s.sessionExportRepo.UpdateSessionExportStage(ctx, taskID, workerID, stage)
}

func (s *OpenAIOAuthReauthService) SubmitSessionExport(ctx context.Context, taskID int64, workerID string, body json.RawMessage) error {
	if len(body) == 0 || len(body) > openAISessionExportMaxBody {
		return s.failSessionExport(ctx, taskID, workerID, "invalid_response")
	}
	var session struct {
		AccessToken  string `json:"accessToken"`
		SessionToken string `json:"sessionToken"`
		Expires      string `json:"expires"`
		User         struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
	}
	if json.Unmarshal(body, &session) != nil || strings.TrimSpace(session.AccessToken) == "" || strings.TrimSpace(session.SessionToken) == "" {
		return s.failSessionExport(ctx, taskID, workerID, "invalid_response")
	}
	expires, err := time.Parse(time.RFC3339, session.Expires)
	if err != nil || !expires.After(time.Now()) {
		return s.failSessionExport(ctx, taskID, workerID, "session_expired")
	}
	record, err := s.sessionExportRepo.GetSessionExportTask(ctx, taskID)
	if err != nil || record == nil || record.WorkerID != workerID || record.Status != OpenAISessionExportStatusRunning {
		return infraerrors.Conflict("OPENAI_SESSION_EXPORT_TASK_NOT_ACTIVE", "Session export task is no longer active")
	}
	account, err := s.accountFor(ctx, record.AccountID)
	if err != nil {
		return err
	}
	hash, err := hashReauthCredentials(account.Credentials)
	if err != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(record.ExpectedCredentialsHash)) != 1 {
		return s.failSessionExport(ctx, taskID, workerID, "credentials_changed")
	}
	userID := strings.TrimSpace(account.GetCredential("chatgpt_user_id"))
	if userID != "" && session.User.ID != userID {
		return s.failSessionExport(ctx, taskID, workerID, "identity_mismatch")
	}
	if userID == "" {
		sessionEmail := strings.TrimSpace(session.User.Email)
		accountEmail := strings.TrimSpace(account.GetCredential("email"))
		if sessionEmail == "" || accountEmail == "" || !strings.EqualFold(sessionEmail, accountEmail) {
			return s.failSessionExport(ctx, taskID, workerID, "identity_mismatch")
		}
	}
	if s.sessionExportResults == nil {
		s.sessionExportResults = newSessionExportResultStore()
	}
	// Validate the upstream response above, then expose only the two credentials
	// consumed by the Session importer.
	compactBody, err := compactSessionExportPayload(session.AccessToken, session.SessionToken)
	if err != nil {
		return s.failSessionExport(ctx, taskID, workerID, "invalid_response")
	}
	s.sessionExportResults.put(taskID, compactBody)
	return s.sessionExportRepo.FinishSessionExportTask(ctx, taskID, workerID, OpenAISessionExportStatusSucceeded, "")
}

func compactSessionExportPayload(accessToken, sessionToken string) (json.RawMessage, error) {
	return json.Marshal(struct {
		AccessToken  string `json:"accessToken"`
		SessionToken string `json:"sessionToken"`
	}{
		AccessToken:  accessToken,
		SessionToken: sessionToken,
	})
}

func (s *OpenAIOAuthReauthService) FailSessionExport(ctx context.Context, taskID int64, workerID, code string) error {
	code = normalizeSessionExportErrorCode(code)
	return s.failSessionExport(ctx, taskID, workerID, code)
}

func (s *OpenAIOAuthReauthService) failSessionExport(ctx context.Context, taskID int64, workerID, code string) error {
	code = normalizeSessionExportErrorCode(code)
	return s.sessionExportRepo.FinishSessionExportTask(ctx, taskID, workerID, OpenAISessionExportStatusFailed, code)
}

func normalizeSessionExportErrorCode(code string) string {
	switch strings.TrimSpace(code) {
	case "password_rejected", "totp_rejected", "identity_mismatch", "upstream_rejected", "worker_timeout", "worker_offline", "credentials_required", "account_invalid", "config_required", "password_unavailable", "totp_unavailable", "proxy_unavailable", "credentials_changed", "invalid_response", "session_expired", "session_token_missing":
		return strings.TrimSpace(code)
	default:
		return "worker_failed"
	}
}

func safeSessionExportTask(record *OpenAISessionExportTaskRecord) *OpenAISessionExportTask {
	if record == nil {
		return nil
	}
	return &record.OpenAISessionExportTask
}

func validSessionExportStage(stage string) bool {
	switch stage {
	case OpenAISessionExportStageStarting, OpenAISessionExportStageProtocolConnecting, OpenAISessionExportStagePasswordSubmitted, OpenAISessionExportStageMFASubmitted, OpenAISessionExportStageSessionRequest, OpenAISessionExportStageIdentityVerification:
		return true
	default:
		return false
	}
}
