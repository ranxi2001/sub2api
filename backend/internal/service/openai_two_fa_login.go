package service

import (
	"context"
	"errors"
	"log/slog"
	"net/mail"
	"strings"
	"time"
)

// Login jobs keep token results in bounded, short-lived memory. Successful logins
// return tokens; encrypted credential enrollment belongs to credential operations.
type OpenAITwoFALoginJob struct {
	ID         string         `json:"id"`
	Status     string         `json:"status"`
	Credential map[string]any `json:"credential,omitempty"`
	cancel     context.CancelFunc
	expiry     *time.Timer
}

func ValidateOpenAITwoFALogin(entry OpenAIReloginCredentials) error {
	email := strings.TrimSpace(entry.Email)
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 320 ||
		strings.TrimSpace(entry.Password) == "" || len(entry.Password) > 4096 ||
		strings.TrimSpace(entry.MFASecret) == "" || len(entry.MFASecret) > 4096 {
		return errors.New("请填写有效的邮箱、密码和 2FA 密钥")
	}
	return nil
}

func (s *OpenAIReloginService) StartTwoFALogin(ctx context.Context, entry OpenAIReloginCredentials) (*OpenAITwoFALoginJob, error) {
	if err := ValidateOpenAITwoFALogin(entry); err != nil {
		return nil, err
	}
	cfg, err := s.GetConfig(ctx)
	if err != nil {
		return nil, errors.New("无法读取凭证运营配置")
	}
	// Explicit login works while scheduled inspection/automatic relogin is off.
	if err := validateGuardHTTPURL(cfg.ReloginEndpoint, "relogin_endpoint"); err != nil {
		return nil, errors.New("请在凭证运营中配置有效的重登接口")
	}
	entry.Email = strings.ToLower(strings.TrimSpace(entry.Email))
	entry.MFASecret = strings.TrimSpace(entry.MFASecret)
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if len(s.logins) >= 32 {
		return nil, errors.New("登录任务过多，请稍后重试")
	}
	if s.logins == nil {
		s.logins = make(map[string]*OpenAITwoFALoginJob)
	}
	loginCtx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	job := &OpenAITwoFALoginJob{ID: guardRandomID(), Status: "running", cancel: cancel}
	s.logins[job.ID] = job
	job.expiry = time.AfterFunc(30*time.Minute, func() { s.DeleteTwoFALogin(job.ID) })
	go func() {
		defer cancel()
		credential, loginErr := s.relogin(loginCtx, cfg, entry)
		s.loginMu.Lock()
		defer s.loginMu.Unlock()
		if s.logins[job.ID] != job {
			return
		}
		if loginErr != nil {
			// Neither provider errors nor request data may be reflected to clients.
			slog.Warn("openai_two_fa_login_failed",
				"job_id", job.ID,
				"stage", "relogin",
				"reason", safeTwoFALoginError(loginErr, entry),
			)
			job.Status = "failed"
			return
		}
		job.Credential = twoFALoginCredential(credential, entry.Email)
		job.Status = "succeeded"
	}()
	return &OpenAITwoFALoginJob{ID: job.ID, Status: job.Status}, nil
}

// safeTwoFALoginError keeps asynchronous login diagnostics useful without
// allowing an upstream response or an accidentally wrapped secret to become a
// log entry. The relogin path already reduces network and provider failures to
// short error codes; this is a final redaction, length and control-character guard.
func safeTwoFALoginError(err error, entry OpenAIReloginCredentials) string {
	if err == nil {
		return "unknown"
	}
	reason := strings.TrimSpace(err.Error())
	if reason == "" {
		return "unknown"
	}
	for _, secret := range []string{entry.Email, entry.Password, entry.MFASecret} {
		if secret != "" {
			reason = strings.ReplaceAll(reason, secret, "[redacted]")
		}
	}
	reason = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, reason)
	if len(reason) > 160 {
		return reason[:160] + "..."
	}
	return reason
}

// Only token/session fields enter the existing Session importer. Discard any
// echoed password, MFA secret, or unexpected service configuration fields.
func twoFALoginCredential(in map[string]any, email string) map[string]any {
	out := map[string]any{"email": email}
	for _, key := range []string{"access_token", "refresh_token", "id_token", "expires_at", "expired", "account_id", "chatgpt_account_id", "chatgpt_user_id", "user_id", "client_id", "plan_type"} {
		switch value := in[key].(type) {
		case string:
			out[key] = value
		case float64:
			if key == "expires_at" || key == "expired" {
				out[key] = value
			}
		}
	}
	return out
}

func (s *OpenAIReloginService) TwoFALogin(id string) (*OpenAITwoFALoginJob, bool) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	job, ok := s.logins[id]
	if !ok {
		return nil, false
	}
	snapshot := &OpenAITwoFALoginJob{ID: job.ID, Status: job.Status}
	if job.Credential != nil {
		snapshot.Credential = make(map[string]any, len(job.Credential))
		for key, value := range job.Credential {
			snapshot.Credential[key] = value
		}
	}
	return snapshot, true
}

func (s *OpenAIReloginService) DeleteTwoFALogin(id string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if job, ok := s.logins[id]; ok {
		job.cancel()
		job.expiry.Stop()
		job.Credential = nil
		delete(s.logins, id)
	}
}

func (s *OpenAIReloginService) clearTwoFALogins() {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	for id, job := range s.logins {
		job.cancel()
		job.expiry.Stop()
		job.Credential = nil
		delete(s.logins, id)
	}
}
