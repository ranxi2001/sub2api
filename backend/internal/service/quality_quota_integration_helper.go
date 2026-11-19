//go:build integration

package service

import (
	"context"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/qualityqueue"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// Integration-only bridge to the actual cron execution and trigger worker.
// Only model discovery/execution are replaced; repository, lease, coverage and
// queue acknowledgement paths remain the production implementations.
func NewQuotaTriggerRunnerForTesting(scheduled *ScheduledTestService, accounts AccountRepository, queue qualityqueue.Queue, models []string,
	execute func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error),
) *ScheduledTestRunnerService {
	scheduled.qualityModels = func(context.Context, int64) ([]string, error) { return models, nil }
	return &ScheduledTestRunnerService{
		planRepo:       scheduled.planRepo,
		scheduledSvc:   scheduled,
		accountTestSvc: &AccountTestService{accountRepo: accounts},
		runPelican:     execute,
		qualityTrigger: &quality5xxTrigger{queue: queue},
	}
}

func (s *ScheduledTestRunnerService) RunQuotaScheduledPlanForTesting(ctx context.Context, plan *ScheduledTestPlan) bool {
	return s.runOnePlan(ctx, plan)
}

func (s *ScheduledTestRunnerService) StartQuotaTriggerWorkerForTesting() {
	s.startQualityTriggers()
}

// Drive one actual worker attempt synchronously, including the ACK boundary.
func (s *ScheduledTestRunnerService) RunQualityTriggerForTesting(ctx context.Context, entry qualityqueue.Signal) error {
	return s.runQualityTrigger(ctx, entry)
}

// Connectivity tests use the real background executor's synthetic account path.
func NewConnectivityRunnerForTesting(scheduled *ScheduledTestService, accounts AccountRepository) *ScheduledTestRunnerService {
	return &ScheduledTestRunnerService{
		planRepo:       scheduled.planRepo,
		scheduledSvc:   scheduled,
		accountTestSvc: &AccountTestService{accountRepo: accounts},
		rateLimitSvc:   NewRateLimitService(accounts, nil, nil, nil, nil),
	}
}

// Exercise the actual Grok transport/observer/background parser with an offline
// HTTPUpstream. No alternate execution or recovery implementation is installed.
func NewGrokConnectivityRunnerForTesting(scheduled *ScheduledTestService, accounts AccountRepository, upstream HTTPUpstream) (*ScheduledTestRunnerService, *AccountTestService) {
	runner := NewConnectivityRunnerForTesting(scheduled, accounts)
	runner.accountTestSvc.httpUpstream = upstream
	runner.accountTestSvc.grokTokenProvider = NewGrokTokenProvider(accounts, nil)
	runner.accountTestSvc.cfg = &config.Config{}
	return runner, runner.accountTestSvc
}

// Exercise the production adaptive policy at a logical instant after expiry.
func GrokRepeated429ResetForTesting(account *Account, now time.Time) (time.Time, bool) {
	retryAfter := 60
	return grokRateLimitResetAtForAccount(account, &xai.QuotaSnapshot{
		StatusCode: http.StatusTooManyRequests, RetryAfterSeconds: &retryAfter, UpdatedAt: now.Format(time.RFC3339Nano),
	}, now)
}
