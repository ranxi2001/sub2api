package service

import (
	"context"
	"fmt"
	"strings"
)

// QualityPolicy is opt-in. Legacy connectivity/HTML tests never modify membership.
type QualityPolicy struct {
	Judge          *QualityJudgeConfig `json:"judge,omitempty"`
	ExpectedAnswer string              `json:"expected_answer"`
	Action         string              `json:"action"`
	RemoveGroupIDs []int64             `json:"remove_group_ids"`
	AutoRestore    bool                `json:"auto_restore"`
}

func validateQualityPolicy(plan *ScheduledTestPlan) error {
	q := plan.PelicanConfig.Quality
	if q == nil {
		return nil
	}
	if j := q.Judge; j != nil {
		if j.GroupID <= 0 || strings.TrimSpace(j.ModelID) == "" || len(j.ModelID) > 100 || strings.TrimSpace(j.Prompt) == "" || len(j.Prompt) > 16000 {
			return fmt.Errorf("judge group, model and grading prompt are required (100/16000 byte limits)")
		}
	}
	if plan.AutoRecover {
		return fmt.Errorf("quality plans use auto_restore, not connectivity auto_recover")
	}
	if plan.PelicanConfig.QuestionKind != "candy" && plan.PelicanConfig.QuestionKind != OpenAICodexStateProbeQuestionKind {
		return fmt.Errorf("quality plans require a text answer question")
	}
	// 探针题型自带满血/降智判定，不需要参考答案与判题模型。
	if plan.PelicanConfig.QuestionKind != OpenAICodexStateProbeQuestionKind {
		if strings.TrimSpace(q.ExpectedAnswer) == "" || len(q.ExpectedAnswer) > 4000 {
			return fmt.Errorf("expected answer must be 1–4000 bytes")
		}
	} else if len(q.ExpectedAnswer) > 4000 {
		return fmt.Errorf("expected answer must be 1–4000 bytes")
	}
	if q.Action != "remove_groups" && q.Action != "disable_scheduling" {
		return fmt.Errorf("invalid quality action")
	}
	if q.Action == "remove_groups" && len(q.RemoveGroupIDs) == 0 {
		return fmt.Errorf("select at least one group to remove")
	}
	if len(q.RemoveGroupIDs) > 100 {
		return fmt.Errorf("select at most 100 groups")
	}
	seen := map[int64]bool{}
	for _, id := range q.RemoveGroupIDs {
		if id <= 0 || seen[id] {
			return fmt.Errorf("invalid or duplicate group ID")
		}
		seen[id] = true
	}
	return nil
}

// One completed wrong answer quarantines; restoration requires every probe to pass.
// Transport errors alone are inconclusive, never evidence of degradation.
func qualityOutcome(results []*ScheduledTestResult) string {
	allPassed := len(results) > 0
	for _, r := range results {
		if r != nil && r.QualityJudgment != nil && r.QualityJudgment.Verdict == "incorrect" && r.Status == "failed" &&
			(r.ErrorMessage == "answer_mismatch" || r.ErrorMessage == openAICodexStateDegradedError) {
			return "failed"
		}
		if r == nil || r.Status != "success" || r.QualityJudgment == nil || r.QualityJudgment.Verdict != "correct" {
			allPassed = false
		}
	}
	if allPassed {
		return "passed"
	}
	return "inconclusive"
}

func (s *ScheduledTestService) ListQualityPlans(ctx context.Context) ([]*ScheduledTestPlan, error) {
	return s.planRepo.ListQualityPlans(ctx)
}
func (s *ScheduledTestService) TriggerQuality(ctx context.Context, id int64) error {
	return s.planRepo.TriggerQuality(ctx, id)
}

func (s *ScheduledTestService) ListQualityHistory(ctx context.Context, beforeID int64) (*QualityHistoryPage, error) {
	items, err := s.resultRepo.ListQualityHistory(ctx, beforeID, 101)
	if err != nil {
		return nil, err
	}
	page := &QualityHistoryPage{Items: items}
	if len(items) > 100 {
		page.Items = items[:100]
		page.NextCursor = items[99].ID
	}
	return page, nil
}
