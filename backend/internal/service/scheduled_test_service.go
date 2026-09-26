package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

var scheduledTestCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ScheduledTestService provides CRUD operations for scheduled test plans and results.
type ScheduledTestService struct {
	planRepo   ScheduledTestPlanRepository
	resultRepo ScheduledTestResultRepository
	// showcase copies successful Pelican HTML results to the user gallery; nil disables it.
	showcase *PelicanShowcaseService
}

// NewScheduledTestService creates a new ScheduledTestService.
func NewScheduledTestService(
	planRepo ScheduledTestPlanRepository,
	resultRepo ScheduledTestResultRepository,
) *ScheduledTestService {
	return &ScheduledTestService{
		planRepo:   planRepo,
		resultRepo: resultRepo,
	}
}

// CreatePlan validates the cron expression, computes next_run_at, and persists the plan.
func (s *ScheduledTestService) CreatePlan(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	nextRun, err := nextPlanRun(plan, time.Now())
	if err != nil {
		return nil, fmt.Errorf("invalid test schedule: %w", err)
	}
	plan.NextRunAt = &nextRun

	if plan.MaxResults <= 0 {
		plan.MaxResults = 50
	}

	return s.planRepo.Create(ctx, plan)
}

// GetPlan retrieves a plan by ID.
func (s *ScheduledTestService) GetPlan(ctx context.Context, id int64) (*ScheduledTestPlan, error) {
	return s.planRepo.GetByID(ctx, id)
}

// ListPlansByAccount returns all plans for a given account.
func (s *ScheduledTestService) ListPlansByAccount(ctx context.Context, accountID int64) ([]*ScheduledTestPlan, error) {
	return s.planRepo.ListByAccountID(ctx, accountID)
}

// UpdatePlan validates cron and updates the plan.
func (s *ScheduledTestService) UpdatePlan(ctx context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	nextRun, err := nextPlanRun(plan, time.Now())
	if err != nil {
		return nil, fmt.Errorf("invalid test schedule: %w", err)
	}
	plan.NextRunAt = &nextRun

	return s.planRepo.Update(ctx, plan)
}

// DeletePlan removes a plan and its results (via CASCADE).
func (s *ScheduledTestService) DeletePlan(ctx context.Context, id int64) error {
	return s.planRepo.Delete(ctx, id)
}

// ListResults returns the most recent results for a plan.
func (s *ScheduledTestService) ListResults(ctx context.Context, planID int64, limit int, includeContent ...bool) ([]*ScheduledTestResult, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.resultRepo.ListByPlanID(ctx, planID, limit, includeContent...)
}

// SaveResult inserts a result and prunes old entries beyond maxResults.
func (s *ScheduledTestService) SaveResult(ctx context.Context, planID int64, maxResults int, result *ScheduledTestResult) error {
	result.PlanID = planID
	saved, err := s.resultRepo.Create(ctx, result)
	if err != nil {
		return err
	}
	s.showcase.PublishScheduledResult(ctx, saved)
	return s.resultRepo.PruneOldResults(ctx, planID, maxResults)
}

func computeNextRun(cronExpr string, from time.Time) (time.Time, error) {
	sched, err := scheduledTestCronParser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(from), nil
}

func nextPlanRun(plan *ScheduledTestPlan, now time.Time) (time.Time, error) {
	if cfg := plan.PelicanConfig; cfg != nil {
		// 探针题型不需要题目文本；其余题型题目必填。
		if isOpenAICodexStateProbePlan(cfg) {
			if len(cfg.Prompt) > 32000 || strings.TrimSpace(plan.ModelID) == "" || len(plan.ModelID) > 100 {
				return time.Time{}, fmt.Errorf("probe model is required (maximum 32000/100 bytes)")
			}
		} else if strings.TrimSpace(cfg.Prompt) == "" || len(cfg.Prompt) > 32000 || strings.TrimSpace(plan.ModelID) == "" || len(plan.ModelID) > 100 {
			return time.Time{}, fmt.Errorf("pelican prompt and model are required (maximum 32000/100 bytes)")
		}
		if err := validateQualityPolicy(plan); err != nil {
			return time.Time{}, err
		}
		if cfg.QuestionKind != "" && cfg.QuestionKind != "pelican" && cfg.QuestionKind != "candy" && cfg.QuestionKind != OpenAICodexStateProbeQuestionKind {
			return time.Time{}, fmt.Errorf("invalid question kind")
		}
		// 同一账号同一时刻只允许一次探针，并行只会互相挤掉，直接禁止。
		if isOpenAICodexStateProbePlan(cfg) && cfg.ParallelCount != 1 {
			return time.Time{}, fmt.Errorf("state probe does not support parallel runs")
		}
		if cfg.ParallelCount < 1 || cfg.ParallelCount > 8 {
			return time.Time{}, fmt.Errorf("parallel count must be 1–8")
		}
		if normalizePelicanReasoningEffort(cfg.ReasoningEffort) == "" {
			return time.Time{}, fmt.Errorf("invalid reasoning effort")
		}
		if plan.MaxResults == 0 {
			plan.MaxResults = 100
		}
		if plan.MaxResults < 1 || plan.MaxResults > 200 {
			return time.Time{}, fmt.Errorf("pelican history retention must be 1–200 results")
		}
		cfg.ModelID = plan.ModelID
	}
	return computeNextRun(plan.CronExpression, now)
}

func (s *ScheduledTestService) GetResult(ctx context.Context, planID, resultID int64) (*ScheduledTestResult, error) {
	return s.resultRepo.GetResult(ctx, planID, resultID)
}

func (s *ScheduledTestService) ListPelicanHistory(ctx context.Context, beforeID int64, limit int) (*PelicanHistoryPage, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	items, err := s.resultRepo.ListPelicanHistory(ctx, beforeID, limit+1)
	if err != nil {
		return nil, err
	}
	page := &PelicanHistoryPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = items[limit-1].ID
	}
	return page, nil
}
