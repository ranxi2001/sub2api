package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/google/uuid"
)

// SetGroupAntiDistillationService installs the shared group policy at startup.
func (s *OpenAIGatewayService) SetGroupAntiDistillationService(protection *GroupAntiDistillationService) {
	s.groupAntiDistillation = protection
}

// ObserveGroupAntiDistillationUsage runs independently of asynchronous billing:
// a saturated usage worker pool must not silently skip protection evidence.
// Call once after a successful HTTP request or websocket turn, never on retry.
func (s *OpenAIGatewayService) ObserveGroupAntiDistillationUsage(ctx context.Context, key *APIKey, account *Account, result *OpenAIForwardResult, startedAt time.Time) {
	if s == nil || s.groupAntiDistillation == nil || key == nil || account == nil || account.Platform != PlatformOpenAI || result == nil {
		return
	}
	if !openAIGroupAntiDistillationUsageEligible(result) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if startedAt.IsZero() {
		startedAt, _ = ctx.Value(ctxkey.RequestStartTime).(time.Time)
	}
	// Internal callers without ingress timing use the measured request duration.
	if startedAt.IsZero() {
		startedAt = time.Now().Add(-result.Duration)
	}
	sampleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	sampleCtx = WithGroupAntiDistillationRequestStart(sampleCtx, startedAt)
	// Never trust caller-controlled request IDs for sample deduplication.
	if result.antiDistillationRequestID == "" {
		result.antiDistillationRequestID = uuid.NewString()
	}
	s.groupAntiDistillation.Observe(sampleCtx, key, result.antiDistillationRequestID, result.Usage.CacheReadInputTokens)
}

func openAIGroupAntiDistillationUsageEligible(result *OpenAIForwardResult) bool {
	return result != nil && result.Usage.CacheReadKnown && !result.Usage.cacheReadMediaObserved && !result.UsageUnavailable && !result.ClientDisconnect &&
		result.SucceededForScheduling() && result.Usage.InputTokens > 0 && result.Usage.CacheReadInputTokens >= 0 &&
		!result.ImageEndpoint && result.ImageCount == 0 && result.VideoCount == 0 && result.AudioUsage == nil &&
		result.Usage.ImageInputTokens == 0 && result.Usage.ImageOutputTokens == 0
}
