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
	SettingKeyExcelBPSImageLimitPolicy      = "excel_bps_image_limit_policy"
	SettingKeyExcelBPSImageWarningRemaining = "excel_bps_image_warning_remaining"
	SettingKeyExcelBPSImageCompactReserve   = "excel_bps_image_compact_reserve"

	SettingKeyExcelBPSImageMode           = "excel_bps_image_mode"
	ExcelBPSImageModeRelay                = "relay"
	ExcelBPSImageModeNative               = "native"
	SettingKeyExcelBPSImageRelayEnabled   = "excel_bps_image_relay_enabled"
	SettingKeyExcelBPSImageBaseURL        = "excel_bps_image_base_url"
	SettingKeyExcelBPSImageBodyLimitMiB   = "excel_bps_image_body_limit_mib"
	SettingKeyExcelBPSImageBudgetMiB      = "excel_bps_image_budget_mib"
	SettingKeyExcelBPSImageMaxRequests    = "excel_bps_image_max_requests"
	SettingKeyExcelBPSImageMaxImageMiB    = "excel_bps_image_max_image_mib"
	SettingKeyExcelBPSImageMaxImages      = "excel_bps_image_max_images"
	SettingKeyExcelBPSImageMaxTotalMiB    = "excel_bps_image_max_total_mib"
	SettingKeyExcelBPSImageStorageMiB     = "excel_bps_image_storage_mib"
	SettingKeyExcelBPSImageStorageEntries = "excel_bps_image_storage_entries"
	SettingKeyExcelBPSImageTTLMinutes     = "excel_bps_image_ttl_minutes"

	DefaultExcelBPSImageBodyLimitMiB = 64
	DefaultExcelBPSImageBudgetMiB    = 1024
	DefaultExcelBPSImageMaxRequests  = 128
)

type ExcelBPSImageRelaySettings struct {
	Mode             string
	Enabled          bool
	BaseURL          string
	BodyLimitMiB     int
	BudgetMiB        int
	MaxRequests      int
	Policy           string
	WarningRemaining int
	CompactReserve   int
	Limits           basispoints.ImageRelayLimits
}

func normalizeExcelBPSImageRelaySettings(enabled bool, baseURL, mode string) (ExcelBPSImageRelaySettings, error) {
	if mode == "" {
		mode = ExcelBPSImageModeNative
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
	if bodyLimitMiB < 1 || bodyLimitMiB > basispoints.MaxImageBodyMiB {
		return infraerrors.BadRequest("INVALID_EXCEL_BPS_IMAGE_CAPACITY", fmt.Sprintf("Image request body limit must be 1-%d MiB", basispoints.MaxImageBodyMiB))
	}
	if budgetMiB < basispoints.MinImageBudgetMiB || budgetMiB > basispoints.MaxImageBudgetMiB || budgetMiB < bodyLimitMiB*8 {
		return infraerrors.BadRequest("INVALID_EXCEL_BPS_IMAGE_CAPACITY", fmt.Sprintf("Image request budget must be %d-%d MiB and at least eight times the body limit", basispoints.MinImageBudgetMiB, basispoints.MaxImageBudgetMiB))
	}
	if maxRequests < 1 || maxRequests > basispoints.MaxImageRequests {
		return infraerrors.BadRequest("INVALID_EXCEL_BPS_IMAGE_CAPACITY", fmt.Sprintf("Image concurrent requests must be 1-%d", basispoints.MaxImageRequests))
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
		SettingKeyExcelBPSEnabled,
		SettingKeyExcelBPSImageLimitPolicy, SettingKeyExcelBPSImageWarningRemaining, SettingKeyExcelBPSImageCompactReserve,
		SettingKeyExcelBPSImageMode, SettingKeyExcelBPSImageRelayEnabled, SettingKeyExcelBPSImageBaseURL,
		SettingKeyExcelBPSImageBodyLimitMiB, SettingKeyExcelBPSImageBudgetMiB, SettingKeyExcelBPSImageMaxRequests,
		SettingKeyExcelBPSImageMaxImageMiB, SettingKeyExcelBPSImageMaxImages, SettingKeyExcelBPSImageMaxTotalMiB, SettingKeyExcelBPSImageStorageMiB, SettingKeyExcelBPSImageStorageEntries, SettingKeyExcelBPSImageTTLMinutes,
	})
	if err != nil {
		return ExcelBPSImageRelaySettings{}, infraerrors.ServiceUnavailable("EXCEL_BPS_IMAGE_SETTINGS_UNAVAILABLE", "Excel BPS image settings are unavailable")
	}
	enabled := values[SettingKeyExcelBPSImageRelayEnabled] == "" || values[SettingKeyExcelBPSImageRelayEnabled] == "true"
	if values[SettingKeyExcelBPSEnabled] == "false" {
		enabled = false
	}
	settings, err := normalizeExcelBPSImageRelaySettings(enabled, values[SettingKeyExcelBPSImageBaseURL], values[SettingKeyExcelBPSImageMode])
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
	settings.Policy = values[SettingKeyExcelBPSImageLimitPolicy]
	settings.WarningRemaining, err = parseExcelBPSImageCapacity(values[SettingKeyExcelBPSImageWarningRemaining], 8)
	if err == nil {
		settings.CompactReserve, err = parseExcelBPSImageCapacity(values[SettingKeyExcelBPSImageCompactReserve], 3)
	}
	if settings.Policy == "" {
		settings.Policy = "off"
	}
	if err == nil {
		err = validateExcelBPSImagePolicy(settings.Policy, settings.WarningRemaining, settings.CompactReserve, settings.Limits.MaxImages)
	}
	if err != nil {
		return ExcelBPSImageRelaySettings{}, infraerrors.ServiceUnavailable("EXCEL_BPS_IMAGE_SETTINGS_UNAVAILABLE", "Excel BPS image policy is invalid")
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
	limits.MaxImages, err = parseExcelBPSImageCapacity(values[SettingKeyExcelBPSImageMaxImages], limits.MaxImages)
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

func validateExcelBPSImagePolicy(policy string, warning, reserve, limit int) error {
	switch policy {
	case "off", "auto_compact", "warn":
	default:
		return fmt.Errorf("unknown image limit policy")
	}
	if warning < 1 || warning > basispoints.MaxRelayImages || reserve < 1 || reserve > basispoints.MaxRelayImages {
		return fmt.Errorf("image policy margins must be 1-%d", basispoints.MaxRelayImages)
	}
	if policy == "warn" && (reserve >= warning || warning >= limit) {
		return fmt.Errorf("image policy requires reserve < warning remaining < image limit")
	}
	return nil
}
