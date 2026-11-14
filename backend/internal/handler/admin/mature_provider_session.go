// Adapted from mizaawa/zayuapi v0.2.219, a71aa9615ccb55a19973908c9f261b5ba25a48c6.
// LGPL v3; upstream authors retain their copyrights. See mature_provider_NOTICE.md.
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// A provider session signs in to a provider dashboard the way the balance
// probe does, then serves the key operations in provider_key_remote.go.

// providerSession is one signed-in dashboard connection to a provider.
type providerSession struct {
	record     providerRecord
	secret     string
	remote     providerRemote
	credential providerToken
	// rotated holds a renewed Sub2API token pair. Refresh tokens can be
	// single-use, so it must be persisted even when the operation fails.
	rotated string
}

func (s *providerSession) authenticate(ctx context.Context) error {
	password := s.record.AuthMode == "password"
	switch s.record.SiteType {
	case "sub2api":
		if password {
			return s.sub2apiLogin(ctx)
		}
		token, err := decodeProviderToken(s.secret)
		if err != nil {
			return errProviderStoredLogin
		}
		s.credential = token
		s.remote.token, s.remote.tokenType = token.AccessToken, token.TokenType
		if token.AccessToken == "" {
			return s.renew(ctx)
		}
		return nil
	case "newapi":
		return newAPISignIn(ctx, &s.remote, s.record, s.secret)
	default:
		return infraerrors.BadRequest("PROVIDER_TYPE_UNSUPPORTED", "Unsupported provider type")
	}
}

func (s *providerSession) sub2apiLogin(ctx context.Context) error {
	s.remote.token, s.remote.tokenType = "", ""
	login, err := s.remote.request(ctx, http.MethodPost, "/api/v1/auth/login", map[string]string{"email": s.record.Account, "password": s.secret})
	if err != nil {
		return err
	}
	token, _ := login["access_token"].(string)
	if token == "" {
		if required, _ := login["requires_2fa"].(bool); required {
			return errProviderTwoFactor
		}
		return errProviderResponse
	}
	tokenType, _ := login["token_type"].(string)
	if !validProviderTokenType(tokenType) {
		tokenType = "Bearer"
	}
	s.remote.token, s.remote.tokenType = token, tokenType
	return nil
}

func (s *providerSession) canRenew() bool {
	return s.record.SiteType == "sub2api" && (s.record.AuthMode == "password" || s.credential.RefreshToken != "")
}

func (s *providerSession) renew(ctx context.Context) error {
	if s.record.AuthMode == "password" {
		return s.sub2apiLogin(ctx)
	}
	if s.credential.RefreshToken == "" {
		return errProviderAuthentication
	}
	fresh, err := s.remote.request(ctx, http.MethodPost, "/api/v1/auth/refresh", map[string]string{"refresh_token": s.credential.RefreshToken})
	if err != nil {
		return err
	}
	access, _ := fresh["access_token"].(string)
	if access == "" {
		return errProviderResponse
	}
	refresh, _ := fresh["refresh_token"].(string)
	if refresh == "" {
		refresh = s.credential.RefreshToken
	}
	tokenType, _ := fresh["token_type"].(string)
	if tokenType == "" {
		tokenType = s.credential.TokenType
	}
	rotated, err := encodeProviderToken(access, refresh, tokenType)
	if err != nil {
		return err
	}
	s.credential = providerToken{AccessToken: access, RefreshToken: refresh, TokenType: tokenType}
	s.remote.token, s.remote.tokenType = access, tokenType
	s.rotated = rotated
	return nil
}

// call performs an authenticated dashboard request. A Sub2API session renews
// once after an authentication failure; writes pass retry=false because their
// first attempt may already have been committed upstream.
func (s *providerSession) call(ctx context.Context, method, path string, body any, header http.Header, retry bool) (any, map[string]any, error) {
	data, envelope, err := s.remote.requestData(ctx, method, path, body, header)
	if retry && errors.Is(err, errProviderAuthentication) && s.canRenew() {
		if renewErr := s.renew(ctx); renewErr != nil {
			return nil, nil, renewErr
		}
		return s.remote.requestData(ctx, method, path, body, header)
	}
	return data, envelope, err
}

// requestData performs one dashboard request and returns the envelope data
// without assuming its JSON shape. Numbers keep their exact text, so large
// IDs survive. Redirects are never followed (see providerHTTPClient).
func (r *providerRemote) requestData(ctx context.Context, method, path string, body any, header http.Header) (any, map[string]any, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.base+path, reader)
	if err != nil {
		return nil, nil, errProviderResponse
	}
	for name, values := range header {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r.authorize(req)
	res, err := r.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("provider request failed: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	operation := providerOperation(path)
	decoder := json.NewDecoder(io.LimitReader(res.Body, providerKeyResponseLimit))
	decoder.UseNumber()
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil || envelope == nil {
		switch {
		case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
			return nil, nil, errProviderAuthentication
		case res.StatusCode < 200 || res.StatusCode >= 300:
			return nil, nil, &providerRemoteError{operation: operation, status: res.StatusCode}
		default:
			return nil, nil, errProviderResponse
		}
	}
	message := providerMessage(envelope)
	if envelope["reason"] == "TURNSTILE_VERIFICATION_FAILED" || envelope["code"] == "TURNSTILE_VERIFICATION_FAILED" || strings.Contains(strings.ToLower(message), "turnstile") {
		return nil, nil, errProviderTurnstile
	}
	if err := newAPIAccessError(envelope); err != nil {
		return nil, nil, err
	}
	// A 403 with a message is an application refusal (for example a group
	// the user may not bind), not a failed login.
	if res.StatusCode == http.StatusUnauthorized || (res.StatusCode == http.StatusForbidden && message == "") {
		return nil, nil, errProviderAuthentication
	}
	failed := res.StatusCode < 200 || res.StatusCode >= 300
	if success, ok := envelope["success"].(bool); ok && !success {
		failed = true
	}
	if code, ok := number(envelope["code"]); ok && code != 0 {
		failed = true
	}
	if failed {
		if message == "" && (res.StatusCode < 200 || res.StatusCode >= 300) {
			return nil, nil, &providerRemoteError{operation: operation, status: res.StatusCode}
		}
		if message == "" {
			message = "request failed"
		}
		return nil, nil, &providerAPIError{status: res.StatusCode, message: message}
	}
	return envelope["data"], envelope, nil
}

func providerMessage(envelope map[string]any) string {
	for _, field := range []string{"message", "msg", "error"} {
		switch value := envelope[field].(type) {
		case string:
			if text := cleanProviderText(value, 200); text != "" {
				return text
			}
		case map[string]any:
			if text, _ := value["message"].(string); strings.TrimSpace(text) != "" {
				return cleanProviderText(text, 200)
			}
		}
	}
	return ""
}

func providerOperation(path string) string {
	if index := strings.IndexByte(path, '?'); index >= 0 {
		return path[:index]
	}
	return path
}
