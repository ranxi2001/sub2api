package service

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// QualityActionEnableBPS 是质量规则的第三种处置：判定降智（或用量达标）后给账号开启 Excel/BPS，
// 而不是摘分组或停调度。开启时写入的 BPS 选项来自规则里的 BPS 配置。
const QualityActionEnableBPS = "enable_bps"

// QualityBPSPolicy 描述「何时开」和「开成什么样」。
// 触发条件：FailureThreshold = 连续判定降智 N 轮（0 = 不按次数）；UsagePercent = 5h 或 7d
// 窗口用量 ≥ X%（0 = 不按用量）。两者都配时默认满足任一即开，RequireAll 表示两个都满足才开。
// 关闭条件（规则开了 auto_restore 才生效）：PassThreshold = 连续满血 N 轮才关（0 按 1 处理）；
// HoldOnUsage = 按「满足任一」开启时，用量仍在 UsagePercent 以上就先不关。
// 其余字段与账号编辑页的 BPS 选项一一对应。
type QualityBPSPolicy struct {
	FailureThreshold        int      `json:"failure_threshold"`
	UsagePercent            float64  `json:"usage_percent"`
	RequireAll              bool     `json:"require_all"`
	PassThreshold           int      `json:"pass_threshold"`
	HoldOnUsage             bool     `json:"hold_on_usage"`
	AllModels               bool     `json:"all_models"`
	Models                  []string `json:"models"`
	OmitUnsupportedTools    bool     `json:"omit_unsupported_tools"`
	WebSearch               bool     `json:"web_search"`
	WebSearchAuto           bool     `json:"web_search_auto"`
	IgnoreEncryptedContent  bool     `json:"ignore_encrypted_content"`
	AutoDisableOn403        bool     `json:"auto_disable_on_403"`
	AutoRecoverOn403        bool     `json:"auto_recover_on_403"`
	RecoveryIntervalMinutes *int     `json:"recovery_interval_minutes,omitempty"`
	AutoMoveOn403           bool     `json:"auto_move_on_403"`
	TargetGroupID           int64    `json:"target_group_id"`
	SessionProxy            bool     `json:"session_proxy"`
	ProxySource             string   `json:"proxy_source"`
	CacheCreationAsInput    bool     `json:"cache_creation_as_input"`
}

// QualityBPSManagedKeys 是规则开 BPS 时会改写、恢复时会还原的账号 Extra 键。
// 403 标记不在其中：开启时清掉「曾因 403 自动关闭」记录由调用方单独处理。
var QualityBPSManagedKeys = []string{
	"openai_excel_bps",
	"openai_excel_bps_models",
	ExcelBPSOmitUnsupportedToolsKey,
	ExcelBPSIgnoreEncryptedContentKey,
	ExcelBPSWebSearchKey,
	ExcelBPSWebSearchAutoKey,
	"openai_excel_bps_auto_disable_on_403",
	ExcelBPSAutoRecoverOn403Key,
	ExcelBPS403RecoveryIntervalMinutesKey,
	ExcelBPSAutoMoveOn403Key,
	ExcelBPS403TargetGroupIDKey,
	"openai_excel_bps_mihomo",
	ExcelBPSProxySourceKey,
	"openai_excel_bps_cache_creation_as_input",
}

func validateQualityBPSPolicy(b *QualityBPSPolicy) error {
	if b == nil {
		return fmt.Errorf("BPS settings are required for the enable_bps action")
	}
	if b.RecoveryIntervalMinutes != nil {
		if _, ok := excelBPS403RecoveryMinutes(*b.RecoveryIntervalMinutes); !ok {
			return fmt.Errorf("BPS recovery interval must be an integer between 1 and 10080 minutes")
		}
	}
	if b.FailureThreshold < 0 || b.FailureThreshold > 100 {
		return fmt.Errorf("degraded count must be 0–100")
	}
	if math.IsNaN(b.UsagePercent) || b.UsagePercent < 0 || b.UsagePercent > 100 {
		return fmt.Errorf("usage percent must be 0–100")
	}
	if b.FailureThreshold == 0 && b.UsagePercent == 0 {
		return fmt.Errorf("set a degraded count or a usage percent to trigger BPS")
	}
	if b.PassThreshold == 0 {
		b.PassThreshold = 1
	}
	if b.PassThreshold < 1 || b.PassThreshold > 100 {
		return fmt.Errorf("healthy count before turning BPS off must be 1–100")
	}
	if b.AllModels {
		b.Models = nil
	} else {
		models := make([]string, 0, len(b.Models))
		seen := make(map[string]bool, len(b.Models))
		for _, model := range b.Models {
			model = strings.TrimSpace(model)
			if model == "" || seen[model] {
				continue
			}
			if len(model) > 100 {
				return fmt.Errorf("BPS model names must be at most 100 bytes")
			}
			seen[model] = true
			models = append(models, model)
		}
		if len(models) == 0 || len(models) > 50 {
			return fmt.Errorf("select 1–50 BPS models, or all models")
		}
		b.Models = models
	}
	if b.AutoMoveOn403 {
		if b.TargetGroupID < 0 {
			return fmt.Errorf("BPS 403 group action requires a target group, or 0 to leave all groups")
		}
	} else {
		b.TargetGroupID = 0
	}
	if b.SessionProxy {
		if b.ProxySource == "" {
			b.ProxySource = ExcelBPSProxySourceMihomo
		}
		if b.ProxySource != ExcelBPSProxySourceMihomo && b.ProxySource != ExcelBPSProxySourceIPPool {
			return fmt.Errorf("invalid BPS proxy source")
		}
	} else {
		b.ProxySource = ""
	}
	return nil
}

// QualityBPSExtra 返回规则开启 BPS 时写入账号 Extra 的值。nil 表示删除该键
// （全部模型 = 不限定模型范围；未开启 403 转组 = 不留目标分组）。
func QualityBPSExtra(b *QualityBPSPolicy) map[string]any {
	interval := DefaultExcelBPS403RecoveryIntervalMinutes
	if b.RecoveryIntervalMinutes != nil {
		interval = *b.RecoveryIntervalMinutes
	}
	extra := map[string]any{
		ExcelBPSWebSearchKey:                       b.WebSearch,
		ExcelBPSWebSearchAutoKey:                   b.WebSearch && b.WebSearchAuto,
		ExcelBPS403RecoveryIntervalMinutesKey:      interval,
		"openai_excel_bps":                         true,
		"openai_excel_bps_models":                  nil,
		ExcelBPSOmitUnsupportedToolsKey:            b.OmitUnsupportedTools,
		ExcelBPSIgnoreEncryptedContentKey:          b.IgnoreEncryptedContent,
		"openai_excel_bps_auto_disable_on_403":     b.AutoDisableOn403,
		ExcelBPSAutoRecoverOn403Key:                b.AutoDisableOn403 && b.AutoRecoverOn403,
		ExcelBPSAutoMoveOn403Key:                   b.AutoMoveOn403,
		ExcelBPS403TargetGroupIDKey:                nil,
		"openai_excel_bps_mihomo":                  b.SessionProxy,
		ExcelBPSProxySourceKey:                     ExcelBPSProxySourceMihomo,
		"openai_excel_bps_cache_creation_as_input": b.CacheCreationAsInput,
	}
	if !b.AllModels {
		extra["openai_excel_bps_models"] = append([]string(nil), b.Models...)
	}
	if b.AutoMoveOn403 {
		extra[ExcelBPS403TargetGroupIDKey] = b.TargetGroupID
	}
	if b.SessionProxy && b.ProxySource != "" {
		extra[ExcelBPSProxySourceKey] = b.ProxySource
	}
	return extra
}

// QualityBPSEligible 与账号编辑页一致：只有普通 ChatGPT OAuth 母账号能开 BPS。
func QualityBPSEligible(account *Account) bool {
	return account != nil && account.Platform == PlatformOpenAI && account.Type == AccountTypeOAuth &&
		!account.IsShadow() && !account.IsOpenAIAgentIdentity() && !account.IsOpenAIPersonalAccessToken() &&
		!strings.EqualFold(strings.TrimSpace(account.GetCredential("plan_type")), "free")
}

// QualityUsagePercent 取 5h / 7d 两个窗口里较高的已用百分比；窗口已到重置时间按 0 算。
// ok=false 表示账号还没有任何用量快照。
func QualityUsagePercent(extra map[string]any, now time.Time) (float64, bool) {
	window5h, window7d := openAICanonicalQuotaWindows(extra, now)
	used, ok := 0.0, false
	for _, window := range []openAICanonicalQuotaWindow{window5h, window7d} {
		if !window.hasUsed {
			continue
		}
		ok = true
		if !window.reset && window.usedPercent > used {
			used = window.usedPercent
		}
	}
	return used, ok
}

// QualityBPSTrigger 判断本轮是否该开 BPS。streak 是含本轮在内的连续降智轮数。
// 返回 "degraded" / "usage" 表示命中的原因，空串表示不开。
func QualityBPSTrigger(b *QualityBPSPolicy, streak int, usage float64, hasUsage bool) string {
	if b == nil {
		return ""
	}
	degraded := b.FailureThreshold > 0 && streak >= b.FailureThreshold
	overUsage := b.UsagePercent > 0 && hasUsage && usage >= b.UsagePercent
	if b.RequireAll && b.FailureThreshold > 0 && b.UsagePercent > 0 {
		if degraded && overUsage {
			return "degraded"
		}
		return ""
	}
	switch {
	case degraded:
		return "degraded"
	case overUsage:
		return "usage"
	}
	return ""
}

// QualityBPSPassThreshold 是连续满血几轮后关闭 BPS。规则已不再是「开 BPS」时（policy 为 nil）按 1 轮。
func QualityBPSPassThreshold(b *QualityBPSPolicy) int {
	if b == nil || b.PassThreshold < 1 {
		return 1
	}
	return b.PassThreshold
}

// QualityBPSHoldForUsage 为 true 时，即便探针已恢复满血也先不关 BPS：
// 规则勾选了 HoldOnUsage，且按「满足任一」开启、用量仍在阈值以上（开启条件还成立）。
func QualityBPSHoldForUsage(b *QualityBPSPolicy, usage float64, hasUsage bool) bool {
	if b == nil || !b.HoldOnUsage || b.UsagePercent <= 0 || !hasUsage {
		return false
	}
	if b.RequireAll && b.FailureThreshold > 0 {
		return false
	}
	return usage >= b.UsagePercent
}
