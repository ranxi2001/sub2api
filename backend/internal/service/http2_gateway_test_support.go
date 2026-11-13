//go:build unit

package service

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

// ObserveHTTP2SchedulerForTest wraps the real scheduler at its ReportResult
// boundary. It is compiled only for unit fixtures and remains with the tests
// during baseline overlays; no production behavior is supplied by this file.
func ObserveHTTP2SchedulerForTest(s *OpenAIGatewayService, observe func(int64, bool)) func() {
	previousSettings := openAIAdvancedSchedulerSettingCache.Load()
	openAIAdvancedSchedulerSettingCache.Store(&cachedOpenAIAdvancedSchedulerSetting{enabled: true, expiresAt: time.Now().Add(time.Hour).UnixNano()})
	s.initOpenAIAccountScheduler()
	original := s.openaiScheduler
	s.openaiScheduler = &http2SchedulerObserver{OpenAIAccountScheduler: original, observe: observe}
	return func() {
		s.openaiScheduler = original
		if previousSettings != nil {
			openAIAdvancedSchedulerSettingCache.Store(previousSettings)
		} else {
			openAIAdvancedSchedulerSettingCache.Store(&cachedOpenAIAdvancedSchedulerSetting{})
		}
	}
}

type http2SchedulerObserver struct {
	OpenAIAccountScheduler
	observe func(int64, bool)
}

func (s *http2SchedulerObserver) Select(ctx context.Context, request OpenAIAccountScheduleRequest) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	return s.OpenAIAccountScheduler.Select(ctx, request)
}
func (s *http2SchedulerObserver) ReportResult(id int64, success bool, ttft *int) {
	s.observe(id, success)
	s.OpenAIAccountScheduler.ReportResult(id, success, ttft)
}

// HTTP2SchedulerErrorRateForTest reads the real scheduler's post-report EWMA.
func HTTP2SchedulerErrorRateForTest(s *OpenAIGatewayService, id int64) float64 {
	rate, _, _ := s.openaiAccountStats.snapshot(id)
	return rate
}

// ForwardHTTP2BridgeTurnForTest exposes the production bridge to an upgraded
// socket fixture whose turn scopes and Ops middleware remain real.
func ForwardHTTP2BridgeTurnForTest(s *OpenAIGatewayService, c *gin.Context, account *Account, payload []byte, turn int, write func([]byte) error) error {
	_, err := s.proxyOpenAIWSHTTPBridgeTurn(c.Request.Context(), c, account, "synthetic", payload, len(payload), "gpt-5.1", "", "", "", "", turn, write)
	return err
}
