package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// Excel grants are encrypted independently of the primary Codex credentials.
// The repository finalizes the shared login task in the same transaction.
type OpenAIExcelOAuthRepository interface {
	CreateExcelTask(context.Context, int64, string) (*OpenAIOAuthReauthTaskRecord, error)
	GetLatestExcelTask(context.Context, int64) (*OpenAIOAuthReauthTaskRecord, error)
	PrepareMissingExcelRoutes(context.Context) error
	GetExcelCredentials(context.Context, int64) (string, error)
	ApplyExcelCredentials(context.Context, *OpenAIOAuthReauthTaskRecord, map[string]any, string, ...ExcelBPSCredentialState) (bool, error)
	ReplaceExcelCredentials(context.Context, int64, string, string, ...ExcelBPSCredentialState) (bool, error)
	DeleteExcelCredentials(context.Context, int64, string, ...ExcelBPSCredentialState) error
	ListMissingExcelAuthorizations(context.Context, int) ([]int64, error)
}

func reauthTaskProfile(record *OpenAIOAuthReauthTaskRecord) string {
	if record.OAuthProfile == "siwc" {
		return "siwc"
	}
	if record.OAuthProfile == "excel" {
		return "excel"
	}
	return "codex"
}

// Claims are consistency checks on the authenticated worker / TLS token
// exchange result, not a replacement for token signature verification.
func excelOAuthTokenInfo(credentials map[string]any) (*OpenAITokenInfo, error) {
	return decodeExcelOAuthTokenInfo(credentials, false)
}

func decodeExcelOAuthTokenInfo(credentials map[string]any, allowExpired bool) (*OpenAITokenInfo, error) {
	if reauthMapString(credentials, "client_id") != openai.ExcelClientID || reauthMapString(credentials, "refresh_token") == "" {
		return nil, errors.New("excel OAuth client or refresh token is missing")
	}
	access := reauthMapString(credentials, "access_token")
	parts := strings.Split(access, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid Excel access token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid Excel access token")
	}
	var client struct {
		ClientID string `json:"client_id"`
		AZP      string `json:"azp"`
	}
	if json.Unmarshal(payload, &client) != nil || firstNonEmptyReauth(client.ClientID, client.AZP) != openai.ExcelClientID {
		return nil, errors.New("excel access token client mismatch")
	}
	at, err := openai.DecodeIDToken(access)
	if err != nil || at.Exp <= 0 || (!allowExpired && at.Exp <= time.Now().Unix()) || at.OpenAIAuth == nil || at.OpenAIAuth.ChatGPTAccountID == "" {
		return nil, errors.New("excel access token identity or expiry is missing")
	}
	id, err := openai.DecodeIDToken(reauthMapString(credentials, "id_token"))
	if err != nil {
		return nil, errors.New("invalid Excel identity token")
	}
	audienceOK := false
	for _, aud := range id.Aud {
		audienceOK = audienceOK || aud == openai.ExcelClientID
	}
	if !audienceOK {
		return nil, errors.New("excel identity token client mismatch")
	}
	if workspace := reauthMapString(credentials, "chatgpt_account_id"); workspace != "" && workspace != at.OpenAIAuth.ChatGPTAccountID {
		return nil, errors.New("excel workspace mismatch")
	}
	return &OpenAITokenInfo{AccessToken: access, RefreshToken: reauthMapString(credentials, "refresh_token"),
		IDToken: reauthMapString(credentials, "id_token"), ClientID: openai.ExcelClientID, ExpiresAt: at.Exp,
		Email: id.Email, ChatGPTAccountID: at.OpenAIAuth.ChatGPTAccountID,
		ChatGPTUserID: firstNonEmptyReauth(at.OpenAIAuth.ChatGPTUserID, at.OpenAIAuth.UserID, id.GetUserInfo().ChatGPTUserID)}, nil
}

func (s *OpenAIOAuthReauthService) applyExcelReauthCredentials(ctx context.Context, record *OpenAIOAuthReauthTaskRecord, account *Account, credentials map[string]any) (*OpenAIOAuthReauthTask, error) {
	info, err := excelOAuthTokenInfo(credentials)
	if err == nil {
		err = validateReauthToken(account, info)
	}
	if err != nil {
		return s.failCallback(ctx, record.ID, "Excel OAuth identity does not match the account", err)
	}
	repo, ok := s.repo.(OpenAIExcelOAuthRepository)
	if !ok {
		return s.failCallback(ctx, record.ID, "Excel credential storage is unavailable", errors.New("missing repository"))
	}
	raw, err := json.Marshal(s.oauth.BuildAccountCredentials(info))
	if err != nil {
		return s.failCallback(ctx, record.ID, "failed to encode Excel credentials", err)
	}
	ciphertext, err := s.encryptor.Encrypt(string(raw))
	if err != nil {
		return s.failCallback(ctx, record.ID, "failed to encrypt Excel credentials", err)
	}
	changed, err := repo.ApplyExcelCredentials(ctx, record, account.Credentials, ciphertext, ExcelBPSGrantState(info.ExpiresAt))
	if err != nil || !changed {
		return s.failCallback(ctx, record.ID, "account changed while Excel authorization was running", errors.New("excel credential write failed"))
	}
	final, err := s.repo.GetTask(ctx, record.ID)
	if err != nil {
		return nil, err
	}
	return safeReauthTask(final), nil
}

// Runs with the existing credential worker lifecycle. Includes existing enabled
// accounts and quality/bulk switches; one task per account and a bounded retry.
func (s *OpenAIOAuthReauthService) QueueMissingExcelAuthorizations(ctx context.Context) error {
	repo, ok := s.repo.(OpenAIExcelOAuthRepository)
	if !ok {
		return nil
	}
	if s.settings != nil {
		value, err := s.settings.GetValue(ctx, SettingKeyExcelBPSEnabled)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			return err
		}
		if value == "false" {
			return nil
		}
	}
	if err := repo.PrepareMissingExcelRoutes(ctx); err != nil {
		return err
	}
	ids, err := repo.ListMissingExcelAuthorizations(ctx, 50)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A concurrent login/config edit is handled by task uniqueness and the CAS.
		if _, err := s.CreateTaskForProfile(ctx, id, "excel"); err != nil {
			slog.Warn("openai_excel_authorization_queue_failed", "account_id", id, "reason", infraerrors.Reason(err))
			continue
		}
	}
	return nil
}

var excelOAuthRefreshLocks sync.Map

// Stored inside the encrypted grant. A resource-server 401 rejects the access
// token, not necessarily the refresh token; refresh it on the next token read.
const excelAccessTokenRejectedKey = "_access_token_rejected"

// Both the refresh writer and 401 marker use the same local/distributed lock.
// A 401 never waits behind a refresh or modifies its snapshot: the refresh
// already owns recovery, and a failed attempt remains due for the next request.
func lockExcelCredentials(ctx context.Context, id int64, tokenCache OpenAITokenCache, wait bool) (func(), error) {
	key := fmt.Sprintf("openai:excel:account:%d", id)
	actual, _ := excelOAuthRefreshLocks.LoadOrStore(key, newContextMutex())
	mu, ok := actual.(*contextMutex)
	if !ok {
		return nil, errors.New("excel refresh lock is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if wait {
		if err := mu.Lock(ctx); err != nil {
			return nil, err
		}
	} else {
		select {
		case mu.token <- struct{}{}:
		default:
			return nil, nil // Refresh owns the grant; do not change its CAS snapshot.
		}
	}
	// Fail closed on distributed-lock errors: Excel refresh tokens rotate.
	if tokenCache != nil {
		acquired, err := tokenCache.AcquireRefreshLock(ctx, key, 60*time.Second)
		if err != nil || !acquired {
			mu.Unlock()
			if err == nil && !wait {
				return nil, nil
			}
			return nil, errors.New("excel credential refresh is busy or unavailable")
		}
		return func() {
			defer mu.Unlock()
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			_ = tokenCache.ReleaseRefreshLock(cleanup, key)
		}, nil
	}
	return mu.Unlock, nil
}

func (s *OpenAIOAuthReauthService) ExcelAccessToken(ctx context.Context, account *Account, tokenCache OpenAITokenCache) (string, error) {
	repo, ok := s.repo.(OpenAIExcelOAuthRepository)
	if !ok {
		return "", errors.New("excel credential storage is unavailable")
	}
	if account == nil {
		return "", errors.New("account is missing")
	}
	// Ready grants are read without a distributed lock so concurrent BPS
	// requests on multiple API replicas can use the same access token.
	_, cached, fresh, err := s.readExcelGrant(ctx, account)
	if err != nil {
		return "", err
	}
	claims, err := openai.DecodeIDToken(reauthMapString(cached, "access_token"))
	if err != nil {
		return "", errors.New("invalid saved Excel access token")
	}
	if cached[excelAccessTokenRejectedKey] != true && claims.Exp > time.Now().Add(openAITokenRefreshSkew).Unix() {
		return validatedExcelAccessToken(fresh, cached)
	}
	unlock, err := lockExcelCredentials(ctx, account.ID, tokenCache, true)
	if err != nil {
		return "", err
	}
	defer unlock()
	// Leave time for detached persistence (5s) and lock release (2s) within
	// the 60s lease, including the reread and the token endpoint call.
	lockedCtx, cancelLocked := context.WithTimeout(ctx, 50*time.Second)
	defer cancelLocked()
	ctx = lockedCtx
	ciphertext, credentials, fresh, err := s.readExcelGrant(ctx, account)
	if err != nil {
		return "", err
	}
	accessClaims, err := openai.DecodeIDToken(reauthMapString(credentials, "access_token"))
	if err != nil {
		return "", errors.New("invalid saved Excel access token")
	}
	if credentials[excelAccessTokenRejectedKey] == true || accessClaims.Exp <= time.Now().Add(openAITokenRefreshSkew).Unix() {
		oldInfo, err := decodeExcelOAuthTokenInfo(credentials, true)
		if err == nil {
			err = validateReauthToken(fresh, oldInfo)
		}
		if err != nil {
			return "", errors.New("saved Excel identity does not match the account")
		}
		var proxyURL string
		_, hasRequestRoute := openAIRequestProxyFromContext(ctx, fresh.ID)
		if fresh.OpenAIRequestProxyEnabled() || hasRequestRoute {
			proxyURL, err = resolveOpenAIMaintenanceProxy(ctx, fresh, s.oauth.proxyRepo)
		} else {
			proxyURL, err = s.reauthProxyURL(ctx, fresh.ProxyID)
		}
		if err != nil {
			return "", err
		}
		refreshCtx, stop := context.WithTimeout(ctx, 45*time.Second)
		response, err := s.oauth.oauthClient.RefreshTokenWithClientID(refreshCtx, reauthMapString(credentials, "refresh_token"), proxyURL, openai.ExcelClientID)
		stop()
		if err != nil || response == nil {
			if isInvalidGrantError(err) {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				if token, changed, recoverErr := s.recoverExcelRefreshRace(cleanup, account, ciphertext); changed || recoverErr != nil {
					return token, recoverErr
				}
				if err := repo.DeleteExcelCredentials(cleanup, account.ID, ciphertext); err != nil {
					return "", errors.New("failed to clear invalid Excel credentials; retry later")
				}
				// Reauthorization can replace the grant after our first reread.
				// Conditional deletion preserves it; recover it instead of lying
				// to this caller that a new login is required.
				if token, changed, recoverErr := s.recoverExcelRefreshRace(cleanup, account, ciphertext); changed || recoverErr != nil {
					return token, recoverErr
				}
				return "", errors.New("excel OAuth refresh token is invalid; automatic reauthorization is required")
			}
			return "", errors.New("excel OAuth refresh failed; credentials were retained, retry later")
		}
		credentials["access_token"] = response.AccessToken
		if response.RefreshToken != "" {
			credentials["refresh_token"] = response.RefreshToken
		}
		if response.IDToken != "" {
			credentials["id_token"] = response.IDToken
		}
		info, err := excelOAuthTokenInfo(credentials)
		if err == nil {
			err = validateReauthToken(fresh, info)
		}
		if err != nil {
			return "", errors.New("refreshed Excel identity does not match the account")
		}
		raw, err := json.Marshal(s.oauth.BuildAccountCredentials(info))
		if err != nil {
			return "", err
		}
		replacement, err := s.encryptor.Encrypt(string(raw))
		if err != nil {
			return "", errors.New("failed to encrypt refreshed Excel credentials")
		}
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		changed, err := repo.ReplaceExcelCredentials(persistCtx, account.ID, ciphertext, replacement, ExcelBPSGrantState(info.ExpiresAt))
		if err != nil {
			return "", errors.New("excel credentials could not be saved")
		}
		if !changed {
			if token, replaced, recoverErr := s.recoverExcelRefreshRace(persistCtx, account, ciphertext); replaced || recoverErr != nil {
				return token, recoverErr
			}
			return "", errors.New("excel credentials changed or could not be saved")
		}
	}
	return validatedExcelAccessToken(fresh, credentials)
}

// An invalid_grant may refer to an old RT consumed by another writer. Read
// current storage before classifying failure; never expose an unsaved token.
func (s *OpenAIOAuthReauthService) recoverExcelRefreshRace(ctx context.Context, account *Account, expected string) (string, bool, error) {
	repo, ok := s.repo.(OpenAIExcelOAuthRepository)
	if !ok {
		return "", false, errors.New("excel credential storage is unavailable")
	}
	current, err := repo.GetExcelCredentials(ctx, account.ID)
	if err != nil {
		return "", false, errors.New("failed to reread Excel credentials; retry later")
	}
	if current == "" || current == expected {
		return "", false, nil
	}
	_, credentials, fresh, err := s.readExcelGrant(ctx, account)
	if err != nil {
		return "", true, err
	}
	if credentials[excelAccessTokenRejectedKey] == true {
		return "", true, errors.New("excel credentials changed and still need refresh; retry later")
	}
	token, err := validatedExcelAccessToken(fresh, credentials)
	return token, true, err
}

func validatedExcelAccessToken(account *Account, credentials map[string]any) (string, error) {
	info, err := excelOAuthTokenInfo(credentials)
	if err == nil {
		err = validateReauthToken(account, info)
	}
	if err != nil {
		return "", errors.New("saved Excel identity does not match the account")
	}
	return info.AccessToken, nil
}

func (s *OpenAIOAuthReauthService) readExcelGrant(ctx context.Context, account *Account) (string, map[string]any, *Account, error) {
	repo, ok := s.repo.(OpenAIExcelOAuthRepository)
	if !ok {
		return "", nil, nil, errors.New("excel credential storage is unavailable")
	}
	ciphertext, err := repo.GetExcelCredentials(ctx, account.ID)
	if err != nil {
		return "", nil, nil, errors.New("failed to read Excel credentials")
	}
	if ciphertext == "" {
		if account.GetCredential("client_id") == openai.ExcelClientID {
			return "", nil, nil, errExcelLegacyCredentials
		}
		return "", nil, nil, s.missingExcelAuthorization(ctx, account.ID)
	}
	plain, err := s.encryptor.Decrypt(ciphertext)
	if err != nil {
		return "", nil, nil, errors.New("failed to decrypt Excel credentials")
	}
	var credentials map[string]any
	if json.Unmarshal([]byte(plain), &credentials) != nil {
		return "", nil, nil, errors.New("invalid saved Excel credentials")
	}
	fresh, err := s.accountFor(ctx, account.ID)
	if err != nil {
		return "", nil, nil, err
	}
	return ciphertext, credentials, fresh, nil
}

var errExcelLegacyCredentials = errors.New("legacy Excel credentials")

func (s *OpenAIGatewayService) getExcelBPSAccessToken(ctx context.Context, account *Account) (string, error) {
	if account != nil && account.AuthConnectionID != "" {
		token, _, err := s.GetAccessToken(ctx, account)
		return token, err
	}
	if s.excelOAuthReauth == nil {
		// Pure gateway embedders retain the pre-existing explicitly supplied path.
		token, _, err := s.GetAccessToken(ctx, account)
		return token, err
	}
	var cache OpenAITokenCache
	if s.openAITokenProvider != nil {
		cache = s.openAITokenProvider.tokenCache
	}
	token, err := s.excelOAuthReauth.ExcelAccessToken(ctx, account, cache)
	if errors.Is(err, errExcelLegacyCredentials) {
		token, _, err = s.GetAccessToken(ctx, account)
	}
	return token, err
}

// Reject only the access token actually used by this request. Preserve its RT
// for the normal refresh flow, and never overwrite a concurrently rotated grant.
func (s *OpenAIOAuthReauthService) invalidateExcelAccessToken(ctx context.Context, id int64, token string, cache OpenAITokenCache) error {
	repo, ok := s.repo.(OpenAIExcelOAuthRepository)
	if !ok || token == "" {
		return nil
	}
	unlock, err := lockExcelCredentials(ctx, id, cache, false)
	if err != nil {
		return err
	}
	if unlock == nil {
		return nil
	}
	defer unlock()
	ciphertext, err := repo.GetExcelCredentials(ctx, id)
	if err != nil || ciphertext == "" {
		return err
	}
	plain, err := s.encryptor.Decrypt(ciphertext)
	if err != nil {
		return errors.New("failed to decrypt Excel grant")
	}
	var credentials map[string]any
	if json.Unmarshal([]byte(plain), &credentials) != nil {
		return errors.New("invalid saved Excel grant")
	}
	if reauthMapString(credentials, "access_token") != token || credentials[excelAccessTokenRejectedKey] == true {
		return nil
	}
	credentials[excelAccessTokenRejectedKey] = true
	raw, err := json.Marshal(credentials)
	if err != nil {
		return errors.New("failed to encode rejected Excel access token")
	}
	replacement, err := s.encryptor.Encrypt(string(raw))
	if err != nil {
		return errors.New("failed to encrypt rejected Excel access token")
	}
	// Resource-server rejection describes the AT, not the RT. Keep diagnostics
	// at auth_failed until refresh determines whether this grant can recover.
	_, err = repo.ReplaceExcelCredentials(ctx, id, ciphertext, replacement, ExcelBPSGrantFailure(""))
	return err
}

// Missing grants are not necessarily queued. Read only the Excel task so a
// newer Codex login cannot hide an Excel failure. Never expose raw worker errors.
func (s *OpenAIOAuthReauthService) missingExcelAuthorization(ctx context.Context, id int64) error {
	repo, ok := s.repo.(OpenAIExcelOAuthRepository)
	if !ok {
		return infraerrors.ServiceUnavailable("OPENAI_EXCEL_AUTH_UNAVAILABLE", "Excel authorization status is unavailable")
	}
	task, err := repo.GetLatestExcelTask(ctx, id)
	if err != nil {
		return infraerrors.ServiceUnavailable("OPENAI_EXCEL_AUTH_UNAVAILABLE", "Excel authorization status is unavailable; retry later")
	}
	if task != nil {
		switch task.Status {
		case "queued", "running", "callback_processing":
			return infraerrors.ServiceUnavailable("OPENAI_EXCEL_AUTH_PENDING", "Excel authorization is in progress; see Credential Operations")
		case "failed":
			if strings.Contains(task.Error, "reason=security_check") || strings.Contains(task.Error, "reason=additional_verification") {
				return infraerrors.ServiceUnavailable("OPENAI_EXCEL_AUTH_VERIFICATION_REQUIRED", "Automatic Excel login is blocked by a security check; see Credential Operations for status")
			}
			return infraerrors.ServiceUnavailable("OPENAI_EXCEL_AUTH_FAILED", "Automatic Excel authorization failed and will retry after cooldown; see Credential Operations for status")
		}
	}
	config, err := s.repo.GetConfig(ctx, id)
	if err != nil {
		return infraerrors.ServiceUnavailable("OPENAI_EXCEL_AUTH_UNAVAILABLE", "Excel authorization configuration is unavailable; retry later")
	}
	if config == nil || config.CredentialMode != OpenAIOAuthReauthModePasswordTOTP || config.PasswordCiphertext == "" {
		return infraerrors.ServiceUnavailable("OPENAI_EXCEL_AUTH_CONFIG_REQUIRED", "Automatic Excel authorization needs a saved password login configuration in Credential Operations")
	}
	return infraerrors.ServiceUnavailable("OPENAI_EXCEL_AUTH_REQUIRED", "Excel authorization is waiting for the automatic worker; see Credential Operations for status")
}

// Only allowlisted application errors may cross the admin/model boundary.
func IsExcelAuthorizationError(err error) bool {
	switch infraerrors.Reason(err) {
	case "OPENAI_EXCEL_AUTH_PENDING", "OPENAI_EXCEL_AUTH_FAILED", "OPENAI_EXCEL_AUTH_VERIFICATION_REQUIRED", "OPENAI_EXCEL_AUTH_CONFIG_REQUIRED", "OPENAI_EXCEL_AUTH_REQUIRED", "OPENAI_EXCEL_AUTH_UNAVAILABLE":
		return true
	}
	return false
}

// Persist the requested switch, but keep native routing until the worker saves
// the independent grant. The worker queues after persistence, never before it.
func (s *adminServiceImpl) validateExcelBPSActivation(ctx context.Context, account *Account) error {
	if s.excelOAuthReauth == nil {
		return nil
	}
	if account.Extra["openai_excel_bps"] != true || account.IsExcelOAuth() {
		delete(account.Extra, ExcelBPSAuthorizationPendingKey)
		return nil
	}
	if account.ID == 0 {
		account.Extra[ExcelBPSAuthorizationPendingKey] = true
		return nil
	}
	_, credentials, _, err := s.excelOAuthReauth.readExcelGrant(ctx, account)
	if err == nil {
		info, validateErr := decodeExcelOAuthTokenInfo(credentials, true)
		if validateErr == nil {
			validateErr = validateReauthToken(account, info)
		}
		if validateErr == nil {
			delete(account.Extra, ExcelBPSAuthorizationPendingKey)
			return nil
		}
		err = validateErr
	}
	if IsExcelAuthorizationError(err) {
		account.Extra[ExcelBPSAuthorizationPendingKey] = true
		return nil
	}
	return infraerrors.ServiceUnavailable("OPENAI_EXCEL_AUTH_UNAVAILABLE", "Excel authorization is unavailable; review Credential Operations before enabling BPS")
}
