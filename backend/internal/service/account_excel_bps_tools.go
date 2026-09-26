package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"go.uber.org/zap"
)

const ExcelBPSOmitUnsupportedToolsKey = "openai_excel_bps_omit_unsupported_tools"

// IsExcelBPSOmitUnsupportedToolsEnabled opts into the bridge's hosted-tool
// omission and capability notice. Forced selections still fail validation.
func (a *Account) IsExcelBPSOmitUnsupportedToolsEnabled() bool {
	return a.IsExcelBPSEnabled() && a.Extra[ExcelBPSOmitUnsupportedToolsKey] == true
}

func (a *Account) excelBPSNativeFallbackReason(body []byte) string {
	if a.IsExcelBPSOmitUnsupportedToolsEnabled() {
		return ""
	}
	return basispoints.NativeFallbackReason(body)
}

func recordExcelBPSNativeFallback(ctx context.Context, account *Account, reason string) {
	// The context logger carries the request ID. Never log the request body,
	// tool arguments, account credentials or session identity here.
	logger.FromContext(ctx).Info("excel_bps.native_fallback",
		zap.Int64("account_id", account.ID),
		zap.String("policy", "native_fallback"),
		zap.String("reason", reason),
		zap.String("upstream_endpoint", openAIResponsesUpstreamEndpoint))
}
