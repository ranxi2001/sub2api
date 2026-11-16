package service

import (
	"context"
	"slices"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	OpenAIProxyModeKey     = "openai_proxy_mode"
	OpenAIProxyIDsKey      = "openai_proxy_ids"
	OpenAIProxyModeSingle  = "single"
	OpenAIProxyModeRequest = "request"
)

func (a *Account) supportsOpenAIRequestProxies() bool {
	return a != nil && a.Platform == PlatformOpenAI && a.Type == AccountTypeOAuth &&
		!a.IsShadow() && !a.IsOpenAIAgentIdentity() && !a.IsOpenAIPersonalAccessToken()
}

// OpenAIProxyMode returns saved routing configuration. A legacy group remains
// saved when BPS is off, but OpenAIRequestProxyEnabled does not activate it.
func (a *Account) OpenAIProxyMode() string {
	if a == nil {
		return OpenAIProxyModeSingle
	}
	if raw, ok := a.Extra[OpenAIProxyModeKey]; ok {
		if mode, valid := raw.(string); valid {
			return mode
		}
		return "invalid"
	}
	if a.Extra[ExcelBPSProxyModeKey] == ExcelBPSProxyModeRequest {
		return OpenAIProxyModeRequest
	}
	return OpenAIProxyModeSingle
}

// OpenAIRequestProxyEnabled keeps invalid explicit configuration on the
// fail-closed route instead of silently reverting to a single/direct exit.
func (a *Account) OpenAIRequestProxyEnabled() bool {
	if !a.supportsOpenAIRequestProxies() {
		return false
	}
	if _, explicit := a.Extra[OpenAIProxyModeKey]; explicit {
		return a.OpenAIProxyMode() != OpenAIProxyModeSingle
	}
	return a.IsExcelBPSEnabled() && a.OpenAIProxyMode() == OpenAIProxyModeRequest
}

func (a *Account) OpenAIRequestProxyIDs() []int64 {
	if a == nil {
		return nil
	}
	key := ExcelBPSProxyIDsKey
	if _, explicit := a.Extra[OpenAIProxyModeKey]; explicit {
		if mode := a.OpenAIProxyMode(); mode != OpenAIProxyModeSingle && mode != OpenAIProxyModeRequest {
			return nil
		}
		key = OpenAIProxyIDsKey
	}
	ids, _ := parseExcelBPSProxyIDs(a.Extra[key])
	return ids
}

func (s *adminServiceImpl) validateOpenAIProxySettingsChange(ctx context.Context, previous, account *Account) error {
	rawMode, supplied := account.Extra[OpenAIProxyModeKey]
	if supplied && rawMode != OpenAIProxyModeSingle && rawMode != OpenAIProxyModeRequest {
		return infraerrors.BadRequest("INVALID_OPENAI_PROXY_MODE", "OpenAI proxy mode must be single or request")
	}
	ids, valid := parseExcelBPSProxyIDs(account.Extra[OpenAIProxyIDsKey])
	if !valid {
		return infraerrors.BadRequest("INVALID_OPENAI_PROXY_IDS", "OpenAI proxy IDs must be positive integers")
	}
	if !supplied && len(ids) == 0 {
		return nil
	}
	if !supplied {
		return infraerrors.BadRequest("INVALID_OPENAI_PROXY_MODE", "OpenAI proxy IDs require an explicit proxy mode")
	}
	if !account.supportsOpenAIRequestProxies() {
		return infraerrors.BadRequest("INVALID_OPENAI_PROXY_ACCOUNT", "request proxies require an OpenAI OAuth account")
	}
	if rawMode != OpenAIProxyModeRequest {
		return nil
	}
	if err := s.validateOpenAIRequestProxyConnection(ctx, previous, account); err != nil {
		return err
	}
	if len(ids) == 0 {
		return infraerrors.BadRequest("OPENAI_PROXY_GROUP_REQUIRED", "select at least one OpenAI proxy")
	}
	if previous != nil && previous.Extra[OpenAIProxyModeKey] == rawMode &&
		slices.Equal(previous.OpenAIRequestProxyIDs(), ids) && previous.OpenAIRequestProxyEnabled() {
		return nil
	}
	if s.proxyRepo == nil {
		return infraerrors.BadRequest("OPENAI_PROXY_NOT_FOUND", "selected OpenAI proxy not found or unavailable")
	}
	proxies, err := s.proxyRepo.ListByIDs(ctx, ids)
	if err != nil {
		return err
	}
	now := time.Now()
	found := make(map[int64]bool, len(proxies))
	for _, proxy := range proxies {
		found[proxy.ID] = proxy.IsActive() && !proxy.IsExpired(now)
	}
	for _, id := range ids {
		if !found[id] {
			return infraerrors.BadRequest("OPENAI_PROXY_NOT_FOUND", "selected OpenAI proxy not found or unavailable")
		}
	}
	account.Extra[OpenAIProxyIDsKey] = ids
	return nil
}

func (s *adminServiceImpl) validateOpenAIRequestProxyConnection(ctx context.Context, previous, account *Account) error {
	unsupported := func() error {
		return infraerrors.BadRequest("OPENAI_PROXY_CONNECTION_UNSUPPORTED", "request proxies cannot override a fixed or managed OpenAI auth connection")
	}
	if strings.TrimSpace(account.AuthConnectionID) != "" || account.HasManagedOpenAIConnections() ||
		previous != nil && (strings.TrimSpace(previous.AuthConnectionID) != "" || previous.HasManagedOpenAIConnections()) {
		return unsupported()
	}
	if account.ID > 0 && s.excelOAuthReauth != nil {
		if repo, ok := s.excelOAuthReauth.repo.(OpenAIAuthConnectionRepository); ok {
			managed, err := repo.AuthConnectionsManaged(ctx, account.ID)
			if err != nil {
				return infraerrors.ServiceUnavailable("OPENAI_PROXY_CONNECTION_CHECK_FAILED", "OpenAI authorization connection status is temporarily unavailable")
			}
			if managed {
				return unsupported()
			}
		}
	}
	return nil
}
