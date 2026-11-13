//go:build integration

package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/qualityqueue"
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
