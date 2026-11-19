package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OpenAIReloginService owns explicit 2FA imports and the shared remote login
// transport. It has no periodic inspection, account recovery or plaintext
// password enrollment.
type OpenAIReloginService struct {
	settings   SettingRepository
	httpClient *http.Client
	loginMu    sync.Mutex
	logins     map[string]*OpenAITwoFALoginJob
}

func NewOpenAIReloginService(settings SettingRepository) *OpenAIReloginService {
	return &OpenAIReloginService{settings: settings, httpClient: &http.Client{}}
}
func (s *OpenAIReloginService) Stop() { s.clearTwoFALogins() }

type OpenAIReloginCredentials struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	MFASecret string `json:"mfa_secret"`
}

type OpenAIReloginDiagnostic struct {
	Code        string     `json:"code,omitempty"`
	HTTPStatus  int        `json:"http_status,omitempty"`
	BytesRead   int64      `json:"bytes_read,omitempty"`
	HeadersAt   *time.Time `json:"headers_at,omitempty"`
	FirstByteAt *time.Time `json:"first_byte_at,omitempty"`
	ResultAt    *time.Time `json:"result_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
}

func validateGuardHTTPURL(raw, field string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New(field + " 不能为空")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New(field + " 需要是合法的 http/https 地址")
	}
	return nil
}

func (s *OpenAIReloginService) relogin(ctx context.Context, cfg OpenAIReloginConfig, entry OpenAIReloginCredentials) (map[string]any, error) {
	payload := map[string]any{
		"action":     "start",
		"email":      entry.Email,
		"auth_mode":  "password_2fa",
		"password":   entry.Password,
		"mfa_secret": entry.MFASecret,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("重登请求构造失败")
	}
	reqCtx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cfg.ReloginEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyGuardRequestHeaders(req, cfg.ReloginEndpoint, cfg.ReloginHeaders)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, errors.New(guardHTTPErrorCode(reqCtx, err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("重登接口返回 HTTP %d", resp.StatusCode)
	}
	result, _, err := readGuardResult(reqCtx, resp.Body, 8<<20, 60*time.Second)
	if err != nil {
		return nil, errors.New(guardReadErrorCode(err))
	}
	if credential, ok := result["credential"].(map[string]any); ok {
		if guardText(credential["access_token"]) == "" || guardText(credential["refresh_token"]) == "" || guardText(credential["id_token"]) == "" {
			return nil, errors.New("重登返回的凭据不完整")
		}
		// Re-login providers may return only tokens, leaving the stored plan_type
		// stale when credentials are merged. As in the normal OAuth flow, prefer
		// the new ID token's explicit plan (including Free) over provider metadata.
		// Missing/unparseable claims must not turn an unknown plan into Free.
		if claims, err := openai.ParseIDToken(guardText(credential["id_token"])); err == nil {
			if plan := strings.TrimSpace(claims.GetUserInfo().PlanType); plan != "" {
				credential["plan_type"] = plan
			}
		}
		return credential, nil
	}
	if errorValue, ok := result["error"].(map[string]any); ok {
		code := strings.ToLower(guardText(errorValue["code"]))
		if code == "" {
			code = "relogin_rejected"
		}
		return nil, errors.New(code)
	}
	return nil, errors.New("重登未返回凭据")
}

// applyGuardRequestHeaders 写入通用请求头，并叠加管理员配置的接口专属头部。
// 不同测活 / 重登服务的协议要求由配置提供，代码里不内置任何第三方标识。
func applyGuardRequestHeaders(req *http.Request, endpoint string, extra map[string]string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson, application/json")
	origin := guardOrigin(endpoint)
	if origin != "" {
		req.Header.Set("Origin", origin)
		req.Header.Set("Referer", origin+"/")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/131.0")
	for name, value := range extra {
		// 支持 {{uuid}} 占位符：每次请求生成新的随机值，便于需要客户端标识的接口。
		if strings.Contains(value, "{{uuid}}") {
			value = strings.ReplaceAll(value, "{{uuid}}", guardRandomID())
		}
		req.Header.Set(name, value)
	}
}

func validateGuardHeaders(in map[string]string, field string) error {
	for name, value := range in {
		if !validGuardHeaderName(name) {
			return fmt.Errorf("%s 包含非法请求头名称: %s", field, name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s 的请求头 %s 不能包含换行", field, name)
		}
	}
	return nil
}

func validGuardHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

func guardOrigin(raw string) string {
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return parsed.Scheme + "://" + parsed.Host
	}
	return raw
}

func guardRandomID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer[:4]) + "-" + hex.EncodeToString(buffer[4:6]) + "-" + hex.EncodeToString(buffer[6:8]) +
		"-" + hex.EncodeToString(buffer[8:10]) + "-" + hex.EncodeToString(buffer[10:])
}

type guardCountingReader struct {
	mu        sync.Mutex
	reader    io.Reader
	bytesRead int64
	firstByte *time.Time
}

func (r *guardCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.bytesRead += int64(n)
		if r.firstByte == nil {
			now := time.Now()
			r.firstByte = &now
		}
	}
	return n, err
}

type guardDecodeResult struct {
	event map[string]any
	err   error
}

// readGuardResult incrementally decodes ordinary JSON and NDJSON alike. It
// returns as soon as a terminal result object is complete; it does not wait for
// the upstream connection to send EOF.
func readGuardResult(ctx context.Context, body io.ReadCloser, maxBytes int64, idleTimeout time.Duration) (map[string]any, OpenAIReloginDiagnostic, error) {
	counter := &guardCountingReader{reader: io.LimitReader(body, maxBytes+1)}
	decoder := json.NewDecoder(bufio.NewReader(counter))
	diagnostic := OpenAIReloginDiagnostic{}
	for {
		resultCh := make(chan guardDecodeResult, 1)
		go func() {
			var event map[string]any
			err := decoder.Decode(&event)
			resultCh <- guardDecodeResult{event: event, err: err}
		}()
		timer := time.NewTimer(idleTimeout)
		var decoded guardDecodeResult
		select {
		case decoded = <-resultCh:
			if !timer.Stop() {
				<-timer.C
			}
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = body.Close()
			return nil, guardDiagnosticBytes(diagnostic, counter), ctx.Err()
		case <-timer.C:
			_ = body.Close()
			return nil, guardDiagnosticBytes(diagnostic, counter), errors.New("guard response idle timeout")
		}
		diagnostic = guardDiagnosticBytes(diagnostic, counter)
		if decoded.err != nil {
			if errors.Is(decoded.err, io.EOF) {
				return nil, diagnostic, errors.New("guard response missing result")
			}
			if counter.bytesRead > maxBytes {
				return nil, diagnostic, errors.New("guard response too large")
			}
			return nil, diagnostic, errors.New("guard response malformed")
		}
		if counter.bytesRead > maxBytes {
			return nil, diagnostic, errors.New("guard response too large")
		}
		if payload, ok := guardResultPayload(decoded.event); ok {
			now := time.Now()
			diagnostic.ResultAt = &now
			diagnostic.Code = "result"
			return payload, guardDiagnosticBytes(diagnostic, counter), nil
		}
	}
}

func guardResultPayload(event map[string]any) (map[string]any, bool) {
	if guardText(event["type"]) == "result" {
		payload, ok := event["payload"].(map[string]any)
		return payload, ok
	}
	// The relogin/probe endpoint may return one ordinary JSON object rather
	// than an event envelope. Heartbeats and progress events do not match this
	// shape and are therefore ignored.
	if status, hasStatus := event["status"]; hasStatus {
		switch strings.ToLower(guardText(status)) {
		case "active", "ok", "success", "succeeded", "failed", "error", "auth_failed":
			return event, true
		}
	}
	if _, hasCredential := event["credential"]; hasCredential {
		return event, true
	}
	if _, hasError := event["error"]; hasError {
		return event, true
	}
	return nil, false
}

func guardDiagnosticBytes(d OpenAIReloginDiagnostic, reader *guardCountingReader) OpenAIReloginDiagnostic {
	reader.mu.Lock()
	bytesRead, firstByte := reader.bytesRead, reader.firstByte
	reader.mu.Unlock()
	d.BytesRead = bytesRead
	if d.FirstByteAt == nil {
		d.FirstByteAt = firstByte
	}
	return d
}

func guardHTTPErrorCode(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return "request_canceled"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "request_timeout"
	}
	return "connection_error"
}

func guardReadErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "request_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "request_timeout"
	case strings.Contains(err.Error(), "idle timeout"):
		return "idle_timeout"
	case strings.Contains(err.Error(), "too large"):
		return "response_too_large"
	case strings.Contains(err.Error(), "malformed"):
		return "malformed_response"
	case strings.Contains(err.Error(), "missing result"):
		return "missing_result"
	default:
		return "response_read_error"
	}
}

func guardText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}
