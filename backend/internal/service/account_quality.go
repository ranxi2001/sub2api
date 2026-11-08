package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const QualityActionObserveOnly = "observe_only"
const QualityActionRemoveModel = "remove_models"
const QualityActionIsolateModels = "isolate_models"
const QualityModelIsolationKey = "quality_model_isolation"

// Quality cooldown deadlines schedule the next check, not permission to route
// traffic. Recognize existing records too so upgrades do not reopen failed models.
func IsQualityModelCooldown(entry any) bool {
	value, _ := entry.(map[string]any)
	reason, _ := value["reason"].(string)
	if !strings.HasPrefix(reason, "quality_rule:") {
		return false
	}
	owner := strings.TrimPrefix(reason, "quality_rule:")
	id, err := strconv.ParseInt(owner, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == owner
}

func (a *Account) hasQualityModelCooldown(model string) bool {
	entries, _ := a.Extra["model_rate_limits"].(map[string]any)
	return IsQualityModelCooldown(entries[model]) || IsQualityModelCooldown(entries[a.GetMappedModel(model)])
}

// QualityModels is the single source of truth for the models a policy manages.
// Composite isolation always follows the tested models; legacy cooldown rules
// retain their explicitly configured targets.
func QualityModels(plan *ScheduledTestPlan) []string {
	if plan.PelicanConfig.Quality.Action == QualityActionRemoveModel {
		return plan.PelicanConfig.Quality.RemoveModels
	}
	return qualityPlanModels(plan)
}

func (q *QualityPolicy) UsesModelResults() bool {
	return q.Action == QualityActionRemoveModel || q.Action == QualityActionIsolateModels
}

// QualityPolicy is opt-in. Legacy connectivity/HTML tests never modify membership.
type QualityPolicy struct {
	Admission            *OpenAITwoFAAdmissionConfig `json:"admission,omitempty"`
	TriggerOnUpstream5xx bool                        `json:"trigger_on_upstream_5xx"`
	Judge                *QualityJudgeConfig         `json:"judge,omitempty"`
	ExpectedAnswer       string                      `json:"expected_answer"`
	Action               string                      `json:"action"`
	RemoveGroupIDs       []int64                     `json:"remove_group_ids"`
	RemoveModels         []string                    `json:"remove_models,omitempty"`
	// RecoveryConcurrency is the temporary OAuth concurrency cap applied when
	// this rule quarantines an account. Zero keeps the safe default of five.
	RecoveryConcurrency int               `json:"recovery_concurrency,omitempty"`
	RetestRestMinutes   int               `json:"retest_rest_minutes,omitempty"`
	AutoRestore         bool              `json:"auto_restore"`
	BPS                 *QualityBPSPolicy `json:"bps,omitempty"`
}

func validateQualityPolicy(plan *ScheduledTestPlan) error {
	q := plan.PelicanConfig.Quality
	if q == nil {
		return nil
	}
	if q.RetestRestMinutes < 0 || q.RetestRestMinutes > 10080 {
		return fmt.Errorf("quality retest rest must be 0-10080 minutes")
	}
	if !q.UsesRetestRest() {
		q.RetestRestMinutes = 0
	}
	if q.Action == QualityActionOpenAITwoFAAdmission {
		if q.Admission == nil || !q.Admission.Enabled || q.Admission.Validate() != nil || plan.PelicanConfig.QuestionKind != "candy" || plan.PelicanConfig.ParallelCount != 1 || plan.ModelID != q.Admission.ModelID || plan.PelicanConfig.ReasoningEffort != q.Admission.ReasoningEffort || plan.PelicanConfig.TestChannel == "bps" || q.AutoRestore || plan.AutoRecover || plan.PelicanConfig.Prompt != CandyPrompt {
			return fmt.Errorf("invalid OpenAI 2FA admission plan")
		}
		return nil
	}
	q.Admission = nil
	if j := q.Judge; j != nil {
		if strings.TrimSpace(j.Prompt) == "" || len(j.Prompt) > 16000 {
			return fmt.Errorf("grading prompt must be 1-16000 bytes")
		}
		switch j.Kind {
		case "", "llm":
			if j.GroupID <= 0 || strings.TrimSpace(j.ModelID) == "" || len(j.ModelID) > 100 {
				return fmt.Errorf("judge group, model and grading prompt are required (100/16000 byte limits)")
			}
		case "jev":
			if j.GroupID != 0 || j.ModelID != "" {
				return fmt.Errorf("jev grading reuses the audit profile; group and model must be empty")
			}
		default:
			return fmt.Errorf("unsupported quality judge kind")
		}
	}
	if plan.AutoRecover {
		return fmt.Errorf("quality plans use auto_restore, not connectivity auto_recover")
	}
	if !isQualityAnswerQuestion(plan.PelicanConfig.QuestionKind) && plan.PelicanConfig.QuestionKind != OpenAICodexStateProbeQuestionKind {
		return fmt.Errorf("quality plans require a text answer question")
	}
	// 探针自带判定；随机题的参考答案由执行器生成，其他答题规则必须配置答案。
	if plan.PelicanConfig.QuestionKind != OpenAICodexStateProbeQuestionKind && plan.PelicanConfig.QuestionKind != QualityRandomQuestionKind {
		if strings.TrimSpace(q.ExpectedAnswer) == "" || len(q.ExpectedAnswer) > 4000 {
			return fmt.Errorf("expected answer must be 1–4000 bytes")
		}
	} else if len(q.ExpectedAnswer) > 4000 {
		return fmt.Errorf("expected answer must be 1–4000 bytes")
	}
	if q.Action != "remove_groups" && q.Action != "disable_scheduling" && q.Action != QualityActionRemoveModel && q.Action != QualityActionEnableBPS && q.Action != QualityActionObserveOnly && q.Action != QualityActionIsolateModels {
		return fmt.Errorf("invalid quality action")
	}
	if q.Action == QualityActionObserveOnly {
		q.AutoRestore = false
		q.RemoveGroupIDs = nil
		q.BPS = nil
	}
	if q.Action == QualityActionEnableBPS {
		// BPS 开启后糖果题会走 BPS 通道，判不出直连是否恢复；只有探针能绕开 BPS 继续探直连。
		if plan.PelicanConfig.QuestionKind != OpenAICodexStateProbeQuestionKind {
			return fmt.Errorf("the enable_bps action requires the state probe question")
		}
		if err := validateQualityBPSPolicy(q.BPS); err != nil {
			return err
		}
		q.RemoveGroupIDs = nil
	} else {
		q.BPS = nil
	}
	if q.Action == QualityActionRemoveModel {
		if len(q.RemoveModels) == 0 || len(q.RemoveModels) > 50 {
			return fmt.Errorf("select 1-50 models to cool down")
		}
		seen := map[string]bool{}
		for i, model := range q.RemoveModels {
			model = strings.TrimSpace(model)
			if model == "" || len(model) > 100 || strings.ContainsAny(model, "*\r\n\t") || seen[model] {
				return fmt.Errorf("invalid or duplicate cooldown model")
			}
			seen[model] = true
			q.RemoveModels[i] = model
		}
		q.RemoveGroupIDs = nil
	} else {
		q.RemoveModels = nil
	}
	if q.RecoveryConcurrency < 0 || q.RecoveryConcurrency > 10000 {
		return fmt.Errorf("recovery concurrency must be 0-10000")
	}
	if q.Action == QualityActionIsolateModels && !isQualityAnswerQuestion(plan.PelicanConfig.QuestionKind) {
		return fmt.Errorf("model isolation requires per-model answer tests")
	}
	if (q.Action == "remove_groups" || q.Action == QualityActionIsolateModels) && len(q.RemoveGroupIDs) == 0 {
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

// BPS has its own recovery interval; observation never quarantines accounts.
func (q *QualityPolicy) UsesRetestRest() bool {
	return q.Action == "remove_groups" || q.Action == "disable_scheduling" || q.UsesModelResults()
}

// One completed wrong answer quarantines; restoration requires every probe to pass.
// Transport errors alone are inconclusive, never evidence of degradation.
// Multi-model rules pass only when every selected model/sample passed.
func qualityPlanModelOutcomes(plan *ScheduledTestPlan, results []*ScheduledTestResult) map[string]string {
	targets := QualityModels(plan)
	if plan.PelicanConfig.Quality.Action != QualityActionRemoveModel || !isOpenAICodexStateProbePlan(plan.PelicanConfig) {
		return qualityModelOutcomes(results, targets)
	}
	// A state probe measures the account, not the individual probe model. Apply
	// its verdict to every configured cooldown target without running concurrent
	// state probes. Unknown/partial rounds cannot restore existing restrictions.
	outcome := "skipped"
	if qualityRoundHasResults(results) {
		outcome = qualityOutcome(results)
	}
	outcomes := make(map[string]string, len(targets))
	for _, model := range targets {
		outcomes[model] = outcome
	}
	return outcomes
}

func qualityModelOutcomes(results []*ScheduledTestResult, targets []string) map[string]string {
	outcomes := make(map[string]string, len(targets))
	for _, model := range targets {
		var samples []*ScheduledTestResult
		for _, result := range results {
			if result != nil && result.PelicanConfig != nil && result.PelicanConfig.ModelID == model {
				samples = append(samples, result)
			}
		}
		if len(samples) == 0 || !qualityRoundHasResults(samples) {
			outcomes[model] = "skipped"
		} else {
			// A partially skipped model did not complete every configured sample.
			// Do not let its successful sample release an existing restriction.
			outcomes[model] = qualityOutcome(samples)
		}
	}
	return outcomes
}

func qualityRoundHasResults(results []*ScheduledTestResult) bool {
	for _, result := range results {
		if result == nil || result.Status != "skipped" {
			return true
		}
	}
	return false
}

func qualityRoundOutcome(results []*ScheduledTestResult) string {
	if len(results) == 1 && results[0] != nil {
		r := results[0]
		if r.PelicanConfig != nil && r.PelicanConfig.Quality != nil && r.PelicanConfig.Quality.Action == QualityActionOpenAITwoFAAdmission {
			if r.Status == "success" && strings.TrimSpace(r.ResponseText) == "21" {
				return "passed"
			}
			if r.Status == "failed" && r.ErrorMessage == "answer_mismatch" && strings.TrimSpace(r.ResponseText) == "29" {
				return "answer_29"
			}
			return "inconclusive"
		}
	}
	effective := make([]*ScheduledTestResult, 0, len(results))
	for _, result := range results {
		if result == nil || result.Status != "skipped" {
			effective = append(effective, result)
		}
	}
	if len(effective) == 0 {
		return "inconclusive"
	}
	// Unknown grading or transport failure cannot become a wrong answer merely
	// because the rule tests multiple models. Recovery still requires all passes.
	return qualityOutcome(effective)
}

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
	// Results are persisted individually after all model requests have ended.
	// During those writes (or after a failed write), never label a partial round
	// as passing simply because its first saved sample passed.
	for _, item := range items {
		expected := item.TotalCount
		if cfg := item.PelicanConfig; cfg != nil && len(cfg.ModelIDs) > 1 {
			if planned := len(cfg.ModelIDs) * cfg.ParallelCount; planned > expected {
				expected = planned
			}
		}
		item.TotalCount = expected - item.SkippedCount
		if item.TotalCount <= 0 {
			item.TotalCount = 0
			item.Status = "skipped"
		} else if item.PassedCount == item.TotalCount {
			item.Status = "success"
		} else {
			item.Status = "failed"
		}
	}
	page := &QualityHistoryPage{Items: items}
	if len(items) > 100 {
		page.Items = items[:100]
		page.NextCursor = items[99].ID
	}
	return page, nil
}

// IsQualityModelIsolated shares account model mapping with the writer so all
// aliases of the same upstream model observe the same persistent isolation.
func (a *Account) IsQualityModelIsolated(model string) bool {
	if a == nil {
		return false
	}
	entries, _ := a.Extra[QualityModelIsolationKey].(map[string]any)
	return entries[model] != nil || entries[a.GetMappedModel(model)] != nil
}

// Only trusted internal account tests may probe through their own isolation.
// Normal scheduler selection and every later WebSocket turn share this gate.
func (a *Account) qualityModelBlocked(ctx context.Context, model string) bool {
	return ctx.Value(qualityProbeContextKey{}) != true && (a.IsQualityModelIsolated(model) || a.hasQualityModelCooldown(model))
}
