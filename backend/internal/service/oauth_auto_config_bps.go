package service

import (
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// ExcelBPSDefaults is a reusable form template. It never enables accounts by itself.
type ExcelBPSDefaults struct {
	WSSSEAcceleration       bool     `json:"ws_sse_acceleration"`
	AutoEnableOnDegradation bool     `json:"auto_enable_on_degradation"`
	AllModels               bool     `json:"all_models"`
	Models                  []string `json:"models"`
	OmitUnsupportedTools    bool     `json:"omit_unsupported_tools"`
	WebSearch               bool     `json:"web_search"`
	WebSearchAuto           bool     `json:"web_search_auto"`
	IgnoreEncryptedContent  bool     `json:"ignore_encrypted_content"`
	AutoDisableOn403        bool     `json:"auto_disable_on_403"`
	AutoRecoverOn403        bool     `json:"auto_recover_on_403"`
	RecoveryIntervalMinutes int      `json:"recovery_interval_minutes"`
	AutoMoveOn403           bool     `json:"auto_move_on_403"`
	TargetGroupID           int64    `json:"target_group_id"`
	SessionProxy            bool     `json:"session_proxy"`
	ProxySource             string   `json:"proxy_source"`
	CacheCreationAsInput    bool     `json:"cache_creation_as_input"`
}

func DefaultExcelBPSDefaults() ExcelBPSDefaults {
	return ExcelBPSDefaults{
		TargetGroupID:          -1,
		Models:                 []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra"},
		IgnoreEncryptedContent: true, AutoDisableOn403: true, CacheCreationAsInput: true,
		RecoveryIntervalMinutes: DefaultExcelBPS403RecoveryIntervalMinutes, ProxySource: "mihomo",
	}
}

func validateExcelBPSDefaults(b ExcelBPSDefaults) error {
	bad := func(message string) error { return infraerrors.BadRequest("AUTO_CONFIG_BPS_INVALID", message) }
	if len(b.Models) > 100 {
		return bad("at most 100 BPS models allowed")
	}
	for _, model := range b.Models {
		if strings.TrimSpace(model) == "" || len(model) > 200 {
			return bad("invalid BPS model name")
		}
	}
	if !b.AllModels && len(b.Models) == 0 {
		return bad("select at least one BPS model")
	}
	if b.RecoveryIntervalMinutes < 1 || b.RecoveryIntervalMinutes > MaxExcelBPS403RecoveryIntervalMinutes {
		return bad("BPS recovery interval must be between 1 and 10080 minutes")
	}
	if b.ProxySource != "mihomo" && b.ProxySource != "ip_pool" {
		return bad("invalid BPS proxy source")
	}
	if b.AutoRecoverOn403 && !b.AutoDisableOn403 {
		return bad("BPS recovery requires automatic disabling on 403")
	}
	if b.AutoMoveOn403 && b.TargetGroupID < 0 {
		return bad("select a BPS 403 target group")
	}
	return nil
}

func (b ExcelBPSDefaults) extra() map[string]any {
	extra := map[string]any{
		ExcelBPSWebSearchKey:                        b.WebSearch,
		ExcelBPSWebSearchAutoKey:                    b.WebSearch && b.WebSearchAuto,
		"openai_excel_bps":                          true,
		"openai_excel_bps_omit_unsupported_tools":   b.OmitUnsupportedTools,
		"openai_excel_bps_ignore_encrypted_content": b.IgnoreEncryptedContent,
		"openai_excel_bps_auto_disable_on_403":      b.AutoDisableOn403,
		"openai_excel_bps_auto_recover_on_403":      b.AutoDisableOn403 && b.AutoRecoverOn403,
		ExcelBPS403RecoveryIntervalMinutesKey:       b.RecoveryIntervalMinutes,
		"openai_excel_bps_auto_move_on_403":         b.AutoMoveOn403,
		"openai_excel_bps_mihomo":                   b.SessionProxy,
		"openai_excel_bps_cache_creation_as_input":  b.CacheCreationAsInput,
	}
	if !b.AllModels {
		models := make([]string, 0, len(b.Models))
		seen := map[string]bool{}
		for _, model := range b.Models {
			model = strings.TrimSpace(model)
			if model != "" && !seen[model] {
				models = append(models, model)
				seen[model] = true
			}
		}
		extra["openai_excel_bps_models"] = models
	}
	if b.SessionProxy {
		extra["openai_excel_bps_proxy_source"] = b.ProxySource
	}
	if b.AutoMoveOn403 {
		extra["openai_excel_bps_403_target_group_id"] = b.TargetGroupID
	}
	return extra
}
