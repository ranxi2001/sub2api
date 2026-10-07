package service

import (
	"context"
	"strings"
)

type PrismBrowserRuntime struct {
	Enabled bool
	BaseURL string
	APIKey  string
}

// GetProtocolFeatureEnabled reads a database-backed protocol switch on every
// request so administrator changes apply without restarting gateway workers.
func (s *SettingService) GetProtocolFeatureEnabled(ctx context.Context, key string) (bool, error) {
	if s == nil || s.settingRepo == nil {
		return false, nil
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{key})
	if err != nil {
		return false, err
	}
	value, exists := values[key]
	if key == SettingKeyExcelBPSEnabled && (!exists || value == "") {
		return true, nil
	}
	return value == "true", nil
}

// GetPrismBrowserRuntime reads the administrator switch without requiring a
// process restart. Missing or unreadable settings fail closed.
func (s *SettingService) GetPrismBrowserRuntime(ctx context.Context) PrismBrowserRuntime {
	if s == nil || s.settingRepo == nil {
		return PrismBrowserRuntime{}
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyPrismBrowserEnabled, SettingKeyPrismBrowserBaseURL, SettingKeyPrismBrowserAPIKey})
	if err != nil {
		return PrismBrowserRuntime{}
	}
	baseURL := strings.TrimSpace(values[SettingKeyPrismBrowserBaseURL])
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8319/v1"
	}
	return PrismBrowserRuntime{Enabled: values[SettingKeyPrismBrowserEnabled] == "true", BaseURL: baseURL, APIKey: strings.TrimSpace(values[SettingKeyPrismBrowserAPIKey])}
}
