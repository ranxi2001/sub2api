package service

import (
	"context"
	"encoding/json"
	"maps"
	"math"
	"slices"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	ExcelBPSProxyModeKey     = "openai_excel_bps_proxy_mode"
	ExcelBPSProxyIDsKey      = "openai_excel_bps_proxy_ids"
	ExcelBPSProxyModeSession = "session"
	ExcelBPSProxyModeRequest = "request"
)

// ExcelBPSRequestProxyEnabled selects an explicit account-owned IP group. An
// invalid/empty stored group still selects this mode so runtime can fail closed.
func (a *Account) ExcelBPSRequestProxyEnabled() bool {
	return a != nil && a.IsExcelBPSEnabled() && a.OpenAIRequestProxyEnabled()
}

func (a *Account) ExcelBPSRequestProxyIDs() []int64 {
	return a.OpenAIRequestProxyIDs()
}

func parseExcelBPSProxyIDs(raw any) ([]int64, bool) {
	var values []any
	switch v := raw.(type) {
	case nil:
		return nil, true
	case []int64:
		for _, id := range v {
			values = append(values, id)
		}
	case []int:
		for _, id := range v {
			values = append(values, id)
		}
	case []any:
		values = v
	default:
		return nil, false
	}
	ids := make([]int64, 0, len(values))
	seen := make(map[int64]bool, len(values))
	for _, rawID := range values {
		var id int64
		switch value := rawID.(type) {
		case int:
			id = int64(value)
		case int64:
			id = value
		case float64:
			if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) || value > 9007199254740991 {
				return nil, false
			}
			id = int64(value)
		case json.Number:
			var err error
			id, err = value.Int64()
			if err != nil {
				return nil, false
			}
		default:
			return nil, false
		}
		if id <= 0 {
			return nil, false
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids, true
}

func hasExcelBPSProxyUpdates(extra map[string]any) bool {
	_, mode := extra[ExcelBPSProxyModeKey]
	_, ids := extra[ExcelBPSProxyIDsKey]
	_, enabled := extra["openai_excel_bps"]
	_, openAIMode := extra[OpenAIProxyModeKey]
	_, openAIIDs := extra[OpenAIProxyIDsKey]
	return mode || ids || enabled || openAIMode || openAIIDs
}

func preserveExcelBPSProxyExtra(existing, incoming map[string]any) map[string]any {
	for _, key := range []string{ExcelBPSProxyModeKey, ExcelBPSProxyIDsKey, OpenAIProxyModeKey, OpenAIProxyIDsKey} {
		if _, supplied := incoming[key]; !supplied {
			if value, ok := existing[key]; ok {
				if incoming == nil {
					incoming = make(map[string]any)
				}
				incoming[key] = value
			}
		}
	}
	return incoming
}

func (s *adminServiceImpl) validateExcelBPSProxySettings(ctx context.Context, account *Account) error {
	return s.validateExcelBPSProxySettingsChange(ctx, nil, account)
}

func (s *adminServiceImpl) validateExcelBPSProxySettingsChange(ctx context.Context, previous, account *Account) error {
	if err := s.validateOpenAIProxySettingsChange(ctx, previous, account); err != nil {
		return err
	}
	if _, explicit := account.Extra[OpenAIProxyModeKey]; explicit {
		return nil
	}
	mode, supplied := account.Extra[ExcelBPSProxyModeKey]
	if supplied && mode != ExcelBPSProxyModeSession && mode != ExcelBPSProxyModeRequest {
		return infraerrors.BadRequest("INVALID_BPS_PROXY_MODE", "BPS proxy mode must be session or request")
	}
	ids, valid := parseExcelBPSProxyIDs(account.Extra[ExcelBPSProxyIDsKey])
	if !valid {
		return infraerrors.BadRequest("INVALID_BPS_PROXY_IDS", "BPS proxy IDs must be positive integers")
	}
	if !supplied && len(ids) == 0 {
		return nil
	}
	if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth || account.IsShadow() || account.IsOpenAIAgentIdentity() || account.IsOpenAIPersonalAccessToken() {
		return infraerrors.BadRequest("INVALID_BPS_PROXY_ACCOUNT", "BPS request proxies require an OpenAI OAuth account")
	}
	if mode == ExcelBPSProxyModeRequest {
		if err := s.validateOpenAIRequestProxyConnection(ctx, previous, account); err != nil {
			return err
		}
	}
	if mode == ExcelBPSProxyModeRequest && len(ids) == 0 {
		return infraerrors.BadRequest("BPS_PROXY_GROUP_REQUIRED", "select at least one BPS proxy")
	}
	if len(ids) == 0 || mode != ExcelBPSProxyModeRequest {
		return nil
	}
	// Removing an exit must not prevent unrelated edits or turning the feature
	// off. Existing request groups remain explicit so forwarding still fails
	// closed; a changed group or newly activated feature must resolve every ID.
	if previous != nil && previous.Extra[ExcelBPSProxyModeKey] == mode &&
		slices.Equal(previous.ExcelBPSRequestProxyIDs(), ids) &&
		(previous.ExcelBPSRequestProxyEnabled() || !account.ExcelBPSRequestProxyEnabled()) {
		return nil
	}
	if s.proxyRepo == nil {
		return infraerrors.BadRequest("BPS_PROXY_NOT_FOUND", "selected BPS proxy not found")
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
			return infraerrors.BadRequest("BPS_PROXY_NOT_FOUND", "selected BPS proxy not found")
		}
	}
	account.Extra[ExcelBPSProxyIDsKey] = ids
	return nil
}

func (s *adminServiceImpl) validateExcelBPSProxyUpdates(ctx context.Context, account *Account, updates map[string]any) error {
	merged := *account
	merged.Extra = maps.Clone(account.Extra)
	if merged.Extra == nil {
		merged.Extra = make(map[string]any)
	}
	maps.Copy(merged.Extra, updates)
	return s.validateExcelBPSProxySettingsChange(ctx, account, &merged)
}
