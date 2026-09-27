package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
)

const (
	SettingKeyExcelBPSImageMode         = "excel_bps_image_mode"
	ExcelBPSImageModeRelay              = "relay"
	ExcelBPSImageModeNative             = "native"
	SettingKeyExcelBPSImageRelayEnabled = "excel_bps_image_relay_enabled"
	SettingKeyExcelBPSImageBaseURL      = "excel_bps_image_base_url"
	SettingKeyExcelBPSImageBodyLimitMiB = "excel_bps_image_body_limit_mib"
	SettingKeyExcelBPSImageBudgetMiB    = "excel_bps_image_budget_mib"
	SettingKeyExcelBPSImageMaxRequests  = "excel_bps_image_max_requests"
	SettingKeyExcelBPSImageMaxImageMiB  = "excel_bps_image_max_image_mib"
	SettingKeyExcelBPSImageMaxImages    = "excel_bps_image_max_images"
	// Read-only fallback for the custom v2.8.14 setting; retain it for rollback.
	SettingKeyExcelBPSImageMaxImagesPerRequest = "excel_bps_image_max_images_per_request"
	SettingKeyExcelBPSImageMaxTotalMiB         = "excel_bps_image_max_total_mib"
	SettingKeyExcelBPSImageStorageMiB          = "excel_bps_image_storage_mib"
	SettingKeyExcelBPSImageStorageEntries      = "excel_bps_image_storage_entries"
	SettingKeyExcelBPSImageTTLMinutes          = "excel_bps_image_ttl_minutes"

	DefaultExcelBPSImageBodyLimitMiB = 64
	DefaultExcelBPSImageBudgetMiB    = 1024
	DefaultExcelBPSImageMaxRequests  = 128
)

type ExcelBPSImageRelaySettings struct {
	Mode         string
	Enabled      bool
	BaseURL      string
	BodyLimitMiB int
	BudgetMiB    int
	MaxRequests  int
	Limits       basispoints.ImageRelayLimits
}

func normalizeExcelBPSImageRelaySettings(enabled bool, baseURL, mode string) (ExcelBPSImageRelaySettings, error) {
	if mode == "" {
		mode = ExcelBPSImageModeRelay
	}
	if mode != ExcelBPSImageModeRelay && mode != ExcelBPSImageModeNative {
		return ExcelBPSImageRelaySettings{}, infraerrors.BadRequest("INVALID_EXCEL_BPS_IMAGE_MODE", "Image mode must be relay or native")
	}
	baseURL = strings.TrimSpace(baseURL)
	if (enabled && mode == ExcelBPSImageModeRelay) || baseURL != "" {
		if err := basispoints.ValidateImageRelayOrigin(baseURL); err != nil {
			return ExcelBPSImageRelaySettings{}, infraerrors.BadRequest("INVALID_EXCEL_BPS_IMAGE_BASE_URL", err.Error())
		}
	}
	return ExcelBPSImageRelaySettings{
		Mode: mode, Enabled: enabled, BaseURL: strings.TrimRight(baseURL, "/"),
		BodyLimitMiB: DefaultExcelBPSImageBodyLimitMiB,
		BudgetMiB:    DefaultExcelBPSImageBudgetMiB,
		MaxRequests:  DefaultExcelBPSImageMaxRequests,
		Limits:       basispoints.DefaultImageRelayLimits(),
	}, nil
}

func validateExcelBPSImageCapacity(bodyLimitMiB, budgetMiB, maxRequests int) error {
	if bodyLimitMiB < 1 || bodyLimitMiB > 128 {
		return infraerrors.BadRequest("INVALID_EXCEL_BPS_IMAGE_CAPACITY", "Image request body limit must be 1-128 MiB")
	}
	if budgetMiB < 512 || budgetMiB > 2048 || budgetMiB < bodyLimitMiB*8 {
		return infraerrors.BadRequest("INVALID_EXCEL_BPS_IMAGE_CAPACITY", "Image request budget must be 512-2048 MiB and at least eight times the body limit")
	}
	if maxRequests < 1 || maxRequests > 512 {
		return infraerrors.BadRequest("INVALID_EXCEL_BPS_IMAGE_CAPACITY", "Image concurrent requests must be 1-512")
	}
	return nil
}

func parseExcelBPSImageCapacity(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid image relay capacity setting: %w", err)
	}
	return parsed, nil
}

// Read current settings for each request so saves take effect immediately,
// including on other instances sharing the settings database.
func (s *SettingService) GetExcelBPSImageRelaySettings(ctx context.Context) (ExcelBPSImageRelaySettings, error) {
	if s == nil || s.settingRepo == nil {
		return ExcelBPSImageRelaySettings{}, nil
	}
	dbCtx, cancel := context.WithTimeout(ctx, gatewayForwardingDBTimeout)
	defer cancel()
	values, err := s.settingRepo.GetMultiple(dbCtx, []string{
		SettingKeyExcelBPSImageMode, SettingKeyExcelBPSImageRelayEnabled, SettingKeyExcelBPSImageBaseURL,
		SettingKeyExcelBPSImageBodyLimitMiB, SettingKeyExcelBPSImageBudgetMiB, SettingKeyExcelBPSImageMaxRequests,
		SettingKeyExcelBPSImageMaxImageMiB, SettingKeyExcelBPSImageMaxImages, SettingKeyExcelBPSImageMaxTotalMiB, SettingKeyExcelBPSImageStorageMiB, SettingKeyExcelBPSImageStorageEntries, SettingKeyExcelBPSImageTTLMinutes,
		SettingKeyExcelBPSImageMaxImagesPerRequest,
	})
	if err != nil {
		return ExcelBPSImageRelaySettings{}, infraerrors.ServiceUnavailable("EXCEL_BPS_IMAGE_SETTINGS_UNAVAILABLE", "Excel BPS image settings are unavailable")
	}
	settings, err := normalizeExcelBPSImageRelaySettings(values[SettingKeyExcelBPSImageRelayEnabled] == "true", values[SettingKeyExcelBPSImageBaseURL], values[SettingKeyExcelBPSImageMode])
	if err != nil {
		return ExcelBPSImageRelaySettings{}, err
	}
	settings.BodyLimitMiB, err = parseExcelBPSImageCapacity(values[SettingKeyExcelBPSImageBodyLimitMiB], DefaultExcelBPSImageBodyLimitMiB)
	if err == nil {
		settings.BudgetMiB, err = parseExcelBPSImageCapacity(values[SettingKeyExcelBPSImageBudgetMiB], DefaultExcelBPSImageBudgetMiB)
	}
	if err == nil {
		settings.MaxRequests, err = parseExcelBPSImageCapacity(values[SettingKeyExcelBPSImageMaxRequests], DefaultExcelBPSImageMaxRequests)
	}
	if err != nil || validateExcelBPSImageCapacity(settings.BodyLimitMiB, settings.BudgetMiB, settings.MaxRequests) != nil {
		return ExcelBPSImageRelaySettings{}, infraerrors.ServiceUnavailable("EXCEL_BPS_IMAGE_SETTINGS_UNAVAILABLE", "Excel BPS image settings are unavailable")
	}
	settings.Limits, err = parseExcelBPSImageLimits(values)
	if err != nil {
		return ExcelBPSImageRelaySettings{}, infraerrors.ServiceUnavailable("EXCEL_BPS_IMAGE_SETTINGS_UNAVAILABLE", "Excel BPS image limits are unavailable")
	}
	return settings, nil
}

func parseExcelBPSImageLimits(values map[string]string) (basispoints.ImageRelayLimits, error) {
	limits := basispoints.DefaultImageRelayLimits()
	var err error
	limits.MaxImageMiB, err = parseExcelBPSImageCapacity(values[SettingKeyExcelBPSImageMaxImageMiB], limits.MaxImageMiB)
	if err != nil {
		return limits, err
	}
	imageCount := values[SettingKeyExcelBPSImageMaxImages]
	if imageCount == "" {
		imageCount = values[SettingKeyExcelBPSImageMaxImagesPerRequest]
	}
	limits.MaxImages, err = parseExcelBPSImageCapacity(imageCount, limits.MaxImages)
	if err != nil {
		return limits, err
	}
	limits.MaxTotalMiB, err = parseExcelBPSImageCapacity(values[SettingKeyExcelBPSImageMaxTotalMiB], limits.MaxTotalMiB)
	if err != nil {
		return limits, err
	}
	limits.StorageMiB, err = parseExcelBPSImageCapacity(values[SettingKeyExcelBPSImageStorageMiB], limits.StorageMiB)
	if err != nil {
		return limits, err
	}
	limits.StorageEntries, err = parseExcelBPSImageCapacity(values[SettingKeyExcelBPSImageStorageEntries], limits.StorageEntries)
	if err != nil {
		return limits, err
	}
	limits.TTLMinutes, err = parseExcelBPSImageCapacity(values[SettingKeyExcelBPSImageTTLMinutes], limits.TTLMinutes)
	if err != nil {
		return limits, err
	}
	return limits, limits.Validate()
}

func (s *SystemSettings) imageRelayLimits() basispoints.ImageRelayLimits {
	return basispoints.ImageRelayLimits{
		MaxImageMiB:    s.ExcelBPSImageMaxImageMiB,
		MaxImages:      s.ExcelBPSImageMaxImages,
		MaxTotalMiB:    s.ExcelBPSImageMaxTotalMiB,
		StorageMiB:     s.ExcelBPSImageStorageMiB,
		StorageEntries: s.ExcelBPSImageStorageEntries,
		TTLMinutes:     s.ExcelBPSImageTTLMinutes,
	}
}
