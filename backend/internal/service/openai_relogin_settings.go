package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

const openAIReloginSettingsKey = "openai_relogin_service_config"
const legacyTokenGuardSettingsKey = "account_token_guard_config_v1"

type OpenAIReloginConfig struct {
	ReloginEndpoint string            `json:"relogin_endpoint"`
	ReloginHeaders  map[string]string `json:"relogin_headers"`
}

func defaultOpenAIReloginConfig() OpenAIReloginConfig {
	return OpenAIReloginConfig{
		ReloginEndpoint: "https://session.ameng2027.xyz/api/v1/relogin",
		ReloginHeaders:  map[string]string{"X-Session-Studio-Relogin": "1", "X-Session-Studio-Client": "{{uuid}}"},
	}
}

func loadOpenAIReloginConfig(ctx context.Context, settings AccountTokenGuardV2Settings) (OpenAIReloginConfig, error) {
	cfg := defaultOpenAIReloginConfig()
	if settings == nil {
		return cfg, nil
	}
	raw, err := settings.GetValue(ctx, openAIReloginSettingsKey)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return cfg, err
	}
	if strings.TrimSpace(raw) == "" {
		raw, err = settings.GetValue(ctx, legacyTokenGuardSettingsKey)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			return cfg, err
		}
	}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func (s *OpenAIReloginService) GetConfig(ctx context.Context) (OpenAIReloginConfig, error) {
	return loadOpenAIReloginConfig(ctx, s.settings)
}

func (s *OpenAIReloginService) SaveConfig(ctx context.Context, cfg OpenAIReloginConfig) (OpenAIReloginConfig, error) {
	cfg.ReloginEndpoint = strings.TrimSpace(cfg.ReloginEndpoint)
	endpoint, err := url.Parse(cfg.ReloginEndpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Opaque != "" {
		return cfg, errors.New("invalid HTTPS relogin endpoint")
	}
	if err := validateGuardHeaders(cfg.ReloginHeaders, "relogin_headers"); err != nil {
		return cfg, err
	}
	for key := range cfg.ReloginHeaders {
		switch strings.ToLower(key) {
		case "host", "content-length", "transfer-encoding", "connection", "x-openai-reauth-worker-token":
			return cfg, errors.New("reserved relogin header")
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	if err := s.settings.Set(ctx, openAIReloginSettingsKey, string(raw)); err != nil {
		return cfg, err
	}
	return cfg, nil
}
