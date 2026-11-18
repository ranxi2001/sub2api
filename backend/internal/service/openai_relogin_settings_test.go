package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type reloginSettingsFixture struct {
	SettingRepository
	values  map[string]string
	failNew bool
}

func (r *reloginSettingsFixture) GetValue(_ context.Context, key string) (string, error) {
	if r.failNew && key == openAIReloginSettingsKey {
		return "", errors.New("storage unavailable")
	}
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}
func (r *reloginSettingsFixture) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func TestReloginSettingsExtractOnlyProviderConfigAndPreferNewModule(t *testing.T) {
	legacy := `{"enabled":true,"relogin_endpoint":"https://legacy.example/relogin","relogin_headers":{"X-Provider":"fixture"},"relogin_accounts":[{"email":"fixture@example.test","password":"legacy-password","mfa_secret":"legacy-mfa"}]}`
	repo := &reloginSettingsFixture{values: map[string]string{legacyTokenGuardSettingsKey: legacy}}
	svc := NewOpenAIReloginService(repo)
	cfg, err := svc.GetConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "https://legacy.example/relogin", cfg.ReloginEndpoint)
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "legacy-password")
	require.NotContains(t, string(raw), "legacy-mfa")
	cfg.ReloginEndpoint = "https://new.example/relogin"
	_, err = svc.SaveConfig(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, legacy, repo.values[legacyTokenGuardSettingsKey])
	require.NotContains(t, repo.values[openAIReloginSettingsKey], "relogin_accounts")
	reloaded, err := loadOpenAIReloginConfig(context.Background(), repo)
	require.NoError(t, err)
	require.Equal(t, cfg, reloaded)
	reauth := &OpenAIOAuthReauthService{settings: repo}
	endpoint, headers, err := reauth.sessionStudioConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, cfg.ReloginEndpoint, endpoint)
	require.Equal(t, cfg.ReloginHeaders, headers)
	repo.failNew = true
	_, err = svc.GetConfig(context.Background())
	require.Error(t, err, "storage errors must not silently fall back to legacy configuration")
}

func TestReloginSettingsRejectInvalidProviderBeforeWriting(t *testing.T) {
	repo := &reloginSettingsFixture{values: map[string]string{}}
	svc := NewOpenAIReloginService(repo)
	for _, cfg := range []OpenAIReloginConfig{
		{ReloginEndpoint: "http://login.example"},
		{ReloginEndpoint: "https://user:secret@login.example"},
		{ReloginEndpoint: "https://login.example?secret=value"},
		{ReloginEndpoint: "https://login.example", ReloginHeaders: map[string]string{"Host": "other.example"}},
		{ReloginEndpoint: "https://login.example", ReloginHeaders: map[string]string{"X-Key": "value\r\nInjected: value"}},
	} {
		_, err := svc.SaveConfig(context.Background(), cfg)
		require.Error(t, err)
	}
	require.Empty(t, repo.values)
}
