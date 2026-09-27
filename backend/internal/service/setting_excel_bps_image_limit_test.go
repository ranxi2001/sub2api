package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestExcelBPSImageCountPersistsAndChangesExistingRelay(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	ctx := context.Background()
	repo := &excelBPSImageSettingsRepo{values: map[string]string{
		SettingKeyExcelBPSImageRelayEnabled: "true", SettingKeyExcelBPSImageBaseURL: "https://images.example",
	}}
	settings := NewSettingService(repo, &config.Config{})
	gateway := &OpenAIGatewayService{settingService: settings}
	t.Cleanup(func() { require.NoError(t, gateway.CloseExcelBPSImages()) })
	relay, err := gateway.excelBPSImageRelay(ctx)
	require.NoError(t, err)
	runtime, err := settings.GetExcelBPSImageRelaySettings(ctx)
	require.NoError(t, err)
	require.Equal(t, 20, runtime.Limits.MaxImages)
	imageBytes, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRusAAAAASUVORK5CYII=")
	require.NoError(t, err)
	parts := make([]any, 21)
	for i := range parts {
		parts[i] = map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes)}
	}
	raw, err := json.Marshal(map[string]any{"input": []any{map[string]any{"role": "user", "content": parts}}})
	require.NoError(t, err)
	_, err = relay.Rewrite(raw, "test")
	require.ErrorContains(t, err, "at most 20")
	require.NoError(t, settings.UpdateSettings(ctx, &SystemSettings{ExcelBPSImageRelayEnabled: true, ExcelBPSImageBaseURL: "https://images.example", ExcelBPSImageMaxImages: 64}))
	saved, err := settings.GetAllSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, 64, saved.ExcelBPSImageMaxImages)
	require.Equal(t, "64", repo.values[SettingKeyExcelBPSImageMaxImages])
	updated, err := gateway.excelBPSImageRelay(ctx)
	require.NoError(t, err)
	require.Same(t, relay, updated)
	_, err = updated.Rewrite(raw, "test")
	require.NoError(t, err)
	for _, invalid := range []int{-1, 4097} {
		err = settings.UpdateSettings(ctx, &SystemSettings{ExcelBPSImageRelayEnabled: true, ExcelBPSImageBaseURL: "https://images.example", ExcelBPSImageMaxImages: invalid})
		require.Error(t, err)
		require.Equal(t, "64", repo.values[SettingKeyExcelBPSImageMaxImages])
	}
}

func TestExcelBPSLegacyImageCountPreserved(t *testing.T) {
	ctx := context.Background()
	repo := &excelBPSImageSettingsRepo{values: map[string]string{
		SettingKeyExcelBPSImageMaxImagesPerRequest: "512",
	}}
	settings := NewSettingService(repo, &config.Config{})
	runtime, err := settings.GetExcelBPSImageRelaySettings(ctx)
	require.NoError(t, err)
	require.Equal(t, 512, runtime.Limits.MaxImages)
	saved, err := settings.GetAllSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, 512, saved.ExcelBPSImageMaxImages)
	repo.values[SettingKeyExcelBPSImageMaxImages] = "64"
	runtime, err = settings.GetExcelBPSImageRelaySettings(ctx)
	require.NoError(t, err)
	require.Equal(t, 64, runtime.Limits.MaxImages)
	require.Equal(t, "512", repo.values[SettingKeyExcelBPSImageMaxImagesPerRequest])
}
