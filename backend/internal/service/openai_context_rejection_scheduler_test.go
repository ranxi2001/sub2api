//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type contextRejectionHealthSettings struct {
	*openAIAPIKeyHealthSettingRepo
}

func (*contextRejectionHealthSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func TestReportOpenAIAccountScheduleResult_ContextRejectionSkipsFeedback(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"canonical", errGatewayContextRejection},
		{"wrapped", fmt.Errorf("forward: %w", errGatewayContextRejection)},
		// The request sentinel must win before even otherwise eligible health feedback.
		{"joined_provider", errors.Join(errGatewayContextRejection, &UpstreamFailoverError{StatusCode: http.StatusBadGateway})},
	} {
		for _, existingFailure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing_failure_%t", tc.name, existingFailure), func(t *testing.T) {
				cfg := DefaultPrioritySchedulingConfig()
				cfg.Enabled = true
				svc := priorityGateway(cfg, &priorityReaderStub{})
				healthSettings := &contextRejectionHealthSettings{&openAIAPIKeyHealthSettingRepo{value: `{"enabled":true,"window_minutes":1,"failure_threshold":3,"cooldown_minutes":5}`}}
				cache := &openAIAPIKeyHealthCacheStub{tripped: true}
				repo := &openAIAPIKeyHealthAccountRepo{}
				blocker := &openAIAPIKeyHealthRuntimeBlocker{}
				svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, cache)
				svc.rateLimitService.SetSettingService(NewSettingService(healthSettings, &config.Config{}))
				svc.rateLimitService.SetOpenAIAPIKeyHealthCache(cache)
				svc.rateLimitService.SetAccountRuntimeBlocker(blocker)
				now := time.Now()
				svc.openaiAccountStats = newOpenAIAccountRuntimeStats()
				svc.openaiAccountStats.now = func() time.Time { return now }
				account := openAIHealthPoolAccount()
				ttft := 450
				if existingFailure {
					svc.openaiAccountStats.reportAt(account.ID, false, &ttft, now)
					svc.initOpenAIAccountScheduler()
				}
				beforeRate, beforeTTFT, beforeKnown := svc.openaiAccountStats.snapshot(account.ID)
				beforeSize := svc.openaiAccountStats.size()
				svc.openaiOAuth429RetryStartedAt.Store(account.ID, now)
				svc.openaiModelTransient = newOpenAIAccountModelTransientState(128)
				svc.openaiModelTransient.recordFailure(account.ID, "gpt-6-astra", now)
				svc.openaiModelTransient.recordFailure(account.ID, "gpt-6-astra", now)
				require.True(t, svc.openaiModelTransient.isBlocked(account.ID, "gpt-6-astra", now))
				require.False(t, svc.rateLimitService.openAIIPUnauthorized.observe(account.ID, "prior", now))

				localTTFT := 1
				require.False(t, svc.ReportOpenAIAccountScheduleResult(account, "gpt-6-astra", false, &localTTFT, tc.err))

				rate, latency, known := svc.openaiAccountStats.snapshot(account.ID)
				require.Equal(t, beforeRate, rate, "context rejection must neither penalize nor heal an account")
				require.Equal(t, beforeTTFT, latency)
				require.Equal(t, beforeKnown, known)
				require.Equal(t, beforeSize, svc.openaiAccountStats.size(), "a first rejection must not create an error sample")
				if !existingFailure {
					require.Nil(t, svc.openaiScheduler, "a first rejection must not initialize the scheduler")
				}
				require.Zero(t, healthSettings.getCalls)
				require.Zero(t, cache.recordCalls)
				require.Zero(t, cache.setCalls)
				require.Zero(t, repo.setCalls)
				require.Zero(t, blocker.calls)
				require.Nil(t, account.TempUnschedulableUntil)
				_, retryPending := svc.openaiOAuth429RetryStartedAt.Load(account.ID)
				require.True(t, retryPending, "a rejected request is not successful recovery")
				require.True(t, svc.openaiModelTransient.isBlocked(account.ID, "gpt-6-astra", now))
				require.True(t, svc.rateLimitService.openAIIPUnauthorized.observe(account.ID, "next", now), "rejection must not run success health reset")
			})
		}
	}
}

func TestReportOpenAIAccountScheduleResult_ContextRejectionKeepsProviderFailures(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	svc := priorityGateway(cfg, &priorityReaderStub{})
	cache := &openAIAPIKeyHealthCacheStub{}
	svc.rateLimitService = NewRateLimitService(&openAIAPIKeyHealthAccountRepo{}, nil, &config.Config{}, nil, cache)
	svc.rateLimitService.SetSettingService(NewSettingService(&contextRejectionHealthSettings{&openAIAPIKeyHealthSettingRepo{value: `{"enabled":true,"window_minutes":1,"failure_threshold":3,"cooldown_minutes":5}`}}, &config.Config{}))
	svc.rateLimitService.SetOpenAIAPIKeyHealthCache(cache)
	now := time.Now()
	svc.openaiAccountStats = newOpenAIAccountRuntimeStats()
	svc.openaiAccountStats.now = func() time.Time { return now }
	account := openAIHealthPoolAccount()

	require.False(t, svc.ReportOpenAIAccountScheduleResult(account, "gpt-6-astra", false, nil, &UpstreamFailoverError{StatusCode: http.StatusBadGateway}))
	rate, _, _ := svc.openaiAccountStats.snapshot(account.ID)
	require.InDelta(t, 0.2, rate, 1e-12)
	require.Equal(t, 1, cache.recordCalls, "real provider failure must still reach the health counter")

	// Identical wording cannot impersonate the canonical error identity.
	require.False(t, svc.ReportOpenAIAccountScheduleResult(account, "gpt-6-astra", false, nil, errors.New(errGatewayContextRejection.Error())))
	rate, _, _ = svc.openaiAccountStats.snapshot(account.ID)
	require.InDelta(t, 0.36, rate, 1e-12)
}

func TestReportOpenAIAccountScheduleResult_ContextRejectionKeepsSuccessRecovery(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	svc := priorityGateway(cfg, &priorityReaderStub{})
	svc.rateLimitService = &RateLimitService{}
	now := time.Now()
	svc.openaiAccountStats = newOpenAIAccountRuntimeStats()
	svc.openaiAccountStats.now = func() time.Time { return now }
	account := openAIHealthPoolAccount()
	svc.openaiAccountStats.reportAt(account.ID, false, nil, now)
	svc.openaiOAuth429RetryStartedAt.Store(account.ID, now)
	svc.openaiModelTransient = newOpenAIAccountModelTransientState(128)
	svc.openaiModelTransient.recordFailure(account.ID, "gpt-6-astra", now)
	svc.openaiModelTransient.recordFailure(account.ID, "gpt-6-astra", now)
	require.True(t, svc.openaiModelTransient.isBlocked(account.ID, "gpt-6-astra", now))
	require.False(t, svc.rateLimitService.openAIIPUnauthorized.observe(account.ID, "prior", now))

	ttft := 450
	require.False(t, svc.ReportOpenAIAccountScheduleResult(account, "gpt-6-astra", true, &ttft, fmt.Errorf("prior: %w", errGatewayContextRejection)))

	rate, latency, known := svc.openaiAccountStats.snapshot(account.ID)
	require.InDelta(t, 0.16, rate, 1e-12)
	require.Equal(t, float64(ttft), latency)
	require.True(t, known)
	_, retryPending := svc.openaiOAuth429RetryStartedAt.Load(account.ID)
	require.False(t, retryPending)
	require.False(t, svc.openaiModelTransient.isBlocked(account.ID, "gpt-6-astra", now))
	require.False(t, svc.rateLimitService.openAIIPUnauthorized.observe(account.ID, "next", now), "success must still reset the health streak")
}
