package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

const CandyPrompt = `在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）
苹果味 桃子味 西瓜味
圆形 7 9 8
五角星形 7 6 4`

const PelicanDeliveryContract = "所有账号使用相同交付约定：直接返回独立 HTML，不使用 Markdown 代码块或外部依赖。只输出 HTML，不要解释。"

var pelicanHTMLPattern = regexp.MustCompile(`(?i)<(?:!doctype\s+html|html|svg)[\s>]`)

// Failures that describe the model's output rather than the account; group tests keep
// them as the group's answer instead of trying another account.
const (
	pelicanErrEmptyOutput  = "Model returned empty output"
	pelicanErrCaptureLimit = "Response exceeds 4 MiB capture limit"
	pelicanErrHistoryLimit = "Output exceeds 2 MiB history limit"
	pelicanErrMaxTokens    = "Model output hit max_tokens before finishing"
	pelicanErrRefused      = "Model refused the request"
)

func (s *AccountTestService) RunPelicanBackground(ctx context.Context, accountID int64, model string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
	ctx = context.WithValue(ctx, qualityProbeContextKey{}, true)
	// 探针题型不下发题目，直接走门票探针。
	if isOpenAICodexStateProbePlan(cfg) {
		return s.runOpenAICodexStateProbeScheduled(ctx, accountID, model, cfg)
	}
	// Recognize the exact built-in question in legacy HTML plans as well.
	if isBuiltinCandyPlan(cfg) {
		copy := *cfg
		copy.QuestionKind = "candy"
		cfg = &copy

	}
	started := time.Now()
	ctx = withPelicanTestOptions(ctx, pelicanTestOptions{
		qualityBatch: cfg.QualityBatch,
		testChannel:  cfg.TestChannel,
		observeOnly:  cfg.Quality != nil && cfg.Quality.Action == QualityActionObserveOnly,
	})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &pelicanRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	c, _ := gin.CreateTestContext(w)
	c.Request = (&http.Request{Header: make(http.Header)}).WithContext(ctx)
	err := s.TestPelicanAccountConnection(c, accountID, model, intelligenceTestPrompt(cfg), cfg.ReasoningEffort)
	output, message := parsePelicanOutput(w.Body.String())
	if w.overflow {
		output = ""
		message = pelicanErrCaptureLimit
	}
	if err != nil && message == "" {
		message = err.Error()
	}
	if message == "" {
		message = intelligenceTestOutputError(cfg, output)
	}
	// Bounded history storage; never persist a truncated animation as a success.
	if len(output) > 2<<20 {
		output = ""
		message = pelicanErrHistoryLimit
	}
	status := "success"
	if message != "" {
		status = "failed"
	}
	finished := time.Now()
	snapshot := *cfg
	snapshot.ModelID = model
	return &ScheduledTestResult{Status: status, ResponseText: output, ErrorMessage: message, LatencyMs: finished.Sub(started).Milliseconds(), StartedAt: started, FinishedAt: finished, PelicanConfig: &snapshot}, nil
}

func (s *ScheduledTestRunnerService) runPelicanPlan(ctx context.Context, plan *ScheduledTestPlan) bool {
	var triggeredAccount *Account
	if plan.TriggerSource == quality5xxSource && s.accountTestSvc != nil {
		var lookupErr error
		triggeredAccount, lookupErr = s.accountTestSvc.accountRepo.GetByID(ctx, plan.AccountID)
		if lookupErr != nil || triggeredAccount == nil {
			return false // Keep the queued signal; no completed run has taken place.
		}
		if triggeredAccount.TempUnschedulableUntil != nil && triggeredAccount.TempUnschedulableUntil.After(time.Now()) {
			return false // Let the native cooldown drain before probing the account again.
		}
	}
	now := time.Now()
	next, err := nextPlanRun(plan, now)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d invalid config: %v", plan.ID, err)
		return false
	}
	if plan.TriggerSource == quality5xxSource && plan.NextRunAt != nil && plan.NextRunAt.After(now) {
		next = *plan.NextRunAt
	}
	// Persisted lease prevents duplicate execution across ticks and server replicas.
	// It also recovers automatically after a process crash.
	until := now.Add(15 * time.Minute).Truncate(time.Microsecond)
	claimed, err := s.planRepo.ClaimPelican(ctx, plan, now, until, next)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d claim failed: %v", plan.ID, err)
	}
	if err != nil || !claimed {
		return false
	}
	// Result-only metadata must not mutate the stored rule or shared plan config.
	snapshot := *plan.PelicanConfig
	snapshot.QualityRestoreDiagnostic = nil
	plan.QualityRestoreDiagnostic = nil
	snapshot.TriggerSource = plan.TriggerSource
	if snapshot.TriggerSource == "" {
		snapshot.TriggerSource = "scheduled"
	}
	applyTriggeredQuality := true
	if triggeredAccount != nil {
		snapshot, applyTriggeredQuality = quality5xxTestConfig(triggeredAccount, plan.ModelID, snapshot)
	}
	plan.PelicanConfig = &snapshot
	// Legacy rules can target API-key accounts, or an account can change type
	// after a rule is saved. Advance the claimed schedule without running a
	// probe or recording a misleading inconclusive quality round.
	if isOpenAICodexStateProbePlan(plan.PelicanConfig) && s.accountTestSvc != nil {
		account, lookupErr := s.accountTestSvc.accountRepo.GetByID(ctx, plan.AccountID)
		ignoreBPS := plan.PelicanConfig.Quality != nil && (plan.PelicanConfig.Quality.Action == QualityActionEnableBPS || plan.PelicanConfig.BPSRecoveryPending)
		if lookupErr != nil || openAICodexStateProbeUnsupportedReason(account, plan.ModelID, ignoreBPS) != "" {
			logger.LegacyPrintf("service.scheduled_test_runner", "state probe plan=%d account=%d skipped: account unavailable or unsupported", plan.ID, plan.AccountID)
			finishCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			if finishErr := s.planRepo.FinishPelican(finishCtx, plan.ID, until, time.Now()); finishErr != nil {
				logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d finish failed: %v", plan.ID, finishErr)
			}
			return false
		}
	}
	// Fallback for queued legacy signals without an immediately applied episode.
	// A 5xx triggers protection; it is not itself a quality verdict.
	if plan.TriggerSource == quality5xxSource && plan.PelicanConfig.Quality != nil &&
		plan.PelicanConfig.Quality.Action == QualityActionRemoveModel && plan.Quality5xxEpisode == 0 && applyTriggeredQuality {
		preCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		preAction, preErr := s.planRepo.ApplyQualityOutcome(preCtx, plan, until, "pending")
		stop()
		if preErr != nil || preAction == "stale_run" || preAction == "account_deleted" {
			return false
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	models := []string{plan.ModelID}
	if len(plan.PelicanConfig.ModelIDs) > 0 {
		models = plan.PelicanConfig.ModelIDs
	}
	results := make([]*ScheduledTestResult, len(models)*plan.PelicanConfig.ParallelCount)
	var randomSamples []*PelicanTestConfig
	var randomQuestionErr error
	var sharedBatch *QualitySharedBatch
	if plan.PelicanConfig.QuestionKind == QualityRandomQuestionKind {
		candidate, _ := ctx.Value(qualitySharedBatchContextKey{}).(*QualitySharedBatch)
		if candidate == nil {
			candidate, randomQuestionErr = newQualitySharedBatch(now)
		}
		repo := s.sharedQualityBatchRepo()
		if repo == nil {
			randomQuestionErr = fmt.Errorf("quality shared batch storage unavailable")
		} else if randomQuestionErr == nil {
			sharedBatch, randomQuestionErr = repo.StartQualityBatch(runCtx, candidate, plan, until)
		}
		if randomQuestionErr == nil {
			randomSamples, randomQuestionErr = sharedQualitySamples(plan.PelicanConfig, sharedBatch)
		}
	}
	// Samples are interleaved by model so each model can start before repeats.
	// Bound upstream concurrency while waiting for every result, even failures.
	jobs := make(chan int, len(results))
	for i := range results {
		jobs <- i
	}
	close(jobs)
	var unsupported sync.Map
	var allowed []string
	var catalogErr error
	if plan.PelicanConfig.Quality != nil {
		allowed, _, catalogErr = s.scheduledSvc.eligibleQualityModels(runCtx, plan)
	}
	var wg sync.WaitGroup
	workers := len(results)
	if workers > 8 {
		workers = 8
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				samplePlan := *plan
				cfg := *plan.PelicanConfig
				samplePlan.ModelID = models[index%len(models)]
				cfg.ModelID = samplePlan.ModelID
				if randomSamples != nil {
					cfg = *randomSamples[index/len(models)]
					cfg.ModelID = samplePlan.ModelID
					if cfg.QualityBatch != nil {
						evidence := *cfg.QualityBatch
						cfg.QualityBatch = &evidence
					}
				}
				samplePlan.PelicanConfig = &cfg
				if randomQuestionErr != nil {
					results[index] = &ScheduledTestResult{Status: "failed", ErrorMessage: "random_question_generation_failed", StartedAt: time.Now(), FinishedAt: time.Now(), PelicanConfig: &cfg,
						QualityJudgment: &QualityJudgment{Verdict: "unknown", Reason: "random_question_generation_failed"}}
					continue
				}
				_, excluded := unsupported.Load(samplePlan.ModelID)
				if plan.PelicanConfig.Quality != nil && (catalogErr != nil || excluded || !containsString(allowed, samplePlan.ModelID)) {
					reason := "model_unavailable"
					if excluded {
						reason = "model_unsupported"
					}
					if catalogErr != nil {
						reason = "model_catalog_unavailable"
					}
					results[index] = &ScheduledTestResult{Status: "skipped", ErrorMessage: reason, StartedAt: time.Now(), FinishedAt: time.Now(), PelicanConfig: &cfg}
					continue
				}
				result := s.runPelicanSample(runCtx, &samplePlan)
				if plan.PelicanConfig.Quality != nil && qualityModelUnsupported(result.ErrorMessage) {
					unsupported.Store(samplePlan.ModelID, true)
					persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(runCtx), 3*time.Second)
					persistErr := s.scheduledSvc.rememberUnsupportedQualityModel(persistCtx, plan.AccountID, samplePlan.ModelID)
					persistCancel()
					if persistErr != nil {
						logger.LegacyPrintf("service.scheduled_test_runner", "quality model exclusion could not be saved: account=%d", plan.AccountID)
					}
					result.Status = "skipped"
					result.ErrorMessage = "model_unsupported"
					result.QualityJudgment = nil
				}
				results[index] = result
			}
		}()
	}
	wg.Wait()
	// Persist timeout failures with a fresh context even after the request deadline.
	saveCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	if sharedBatch != nil && randomQuestionErr == nil {
		for _, result := range results {
			result.PlanID = plan.ID
			result.QualityRoundID = until.Format(time.RFC3339Nano)
			result.QualityAction = "comparison_pending"
		}
		applyBatch := applyTriggeredQuality
		if plan.TriggerSource == quality5xxSource && plan.TriggerObservedAt != nil && s.qualityTrigger != nil {
			applyBatch = applyBatch && s.qualityTrigger.signalIsCurrent(plan.AccountID, *plan.TriggerObservedAt)
		}
		run := &QualityBatchRun{Apply: applyBatch, Plan: plan, Until: until, Results: results, Episode: plan.Quality5xxEpisode,
			Trigger: plan.TriggerSource, ObservedAt: plan.TriggerObservedAt}
		if err := s.sharedQualityBatchRepo().CollectQualityBatch(saveCtx, run); err != nil {
			logQualityBatchFailure()
			return false
		}
		// Release worker/concurrency slots before waiting for other accounts.
		// A later tick finalizes this durable run using the original lease/version.
		return true
	}
	if plan.PelicanConfig.Quality != nil && plan.PelicanConfig.Quality.UsesModelResults() {
		plan.QualityModelOutcomes = qualityPlanModelOutcomes(plan, results)
	}
	qualityAction := ""
	if plan.PelicanConfig.Quality != nil {
		qualityAction = "inconclusive"
		freshSignal := true
		if plan.TriggerSource == quality5xxSource && plan.TriggerObservedAt != nil && s.qualityTrigger != nil {
			freshSignal = s.qualityTrigger.signalIsCurrent(plan.AccountID, *plan.TriggerObservedAt)
		}
		if applyTriggeredQuality && freshSignal && (qualityRoundHasResults(results) || plan.PelicanConfig.Quality.Action == QualityActionOpenAITwoFAAdmission) {
			var actionErr error
			qualityAction, actionErr = s.planRepo.ApplyQualityOutcome(saveCtx, plan, until, qualityRoundOutcome(results))
			if actionErr != nil {
				qualityAction = "action_error"
				logger.LegacyPrintf("service.scheduled_test_runner", "quality plan=%d action failed: %v", plan.ID, actionErr)
			}
		}
	}
	succeeded := false
	for _, result := range results {
		result.QualityAction = qualityAction
		if plan.PelicanConfig.Quality != nil {
			result.QualityRoundID = until.Format(time.RFC3339Nano)
			result.PelicanConfig.QualityModelOutcomes = plan.QualityModelOutcomes
			result.PelicanConfig.QualityRestoreDiagnostic = nil
			if qualityAction == "restore_conflict" {
				result.PelicanConfig.QualityRestoreDiagnostic = plan.QualityRestoreDiagnostic
			}
			// A rejected transaction must never advertise uncommitted changes.
			if qualityAction != "action_error" && qualityAction != "action_conflict" && qualityAction != "restore_conflict" && qualityAction != "stale_run" {
				result.PelicanConfig.QualityModelActions = plan.QualityModelActions
			} else {
				result.PelicanConfig.QualityModelActions = nil
			}
		}
		if result.Status == "success" {
			succeeded = true
		}
		if err := s.scheduledSvc.SaveResult(saveCtx, plan.ID, plan.MaxResults, result); err != nil {
			logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d save failed: %v", plan.ID, err)
		}
	}
	if succeeded && plan.AutoRecover && plan.PelicanConfig.Quality == nil && !isBuiltinCandyPlan(plan.PelicanConfig) && !isOpenAICodexStateProbePlan(plan.PelicanConfig) {
		s.tryRecoverAccount(saveCtx, plan.AccountID, plan.ID)
	}
	finished := time.Now()
	if plan.TriggerSource == quality5xxSource {
		// The stored rule was validated before claiming. The execution-only
		// question may now be candy even for a state-probe BPS recovery rule.
		following, nextErr := computeNextRun(plan.CronExpression, finished)
		if nextErr != nil {
			return false
		}
		err = s.planRepo.FinishTriggeredQuality(saveCtx, plan, until, finished, following)
	} else {
		err = s.planRepo.FinishPelican(saveCtx, plan.ID, until, finished)
	}
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d finish failed: %v", plan.ID, err)
		return false
	}
	return true
}

// Each sample runs in its own goroutine, outside Gin recovery. Always return a
// result so an upstream adapter or judge panic cannot kill the process or leave
// the persistence loop with a nil entry. Panic values may contain request data.
func (s *ScheduledTestRunnerService) runPelicanSample(ctx context.Context, plan *ScheduledTestPlan) (result *ScheduledTestResult) {
	started := time.Now()
	failure := func(message string) *ScheduledTestResult {
		finished := time.Now()
		return &ScheduledTestResult{Status: "failed", ErrorMessage: message, StartedAt: started, FinishedAt: finished, LatencyMs: finished.Sub(started).Milliseconds(), PelicanConfig: plan.PelicanConfig}
	}
	defer func() {
		if recover() != nil {
			result = failure("scheduled_test_panic: background sample failed")
			logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d account=%d sample panicked", plan.ID, plan.AccountID)
		}
	}()
	if plan.PelicanConfig.QuestionKind == QualityRandomQuestionKind && (plan.PelicanConfig.RandomQuestion == nil || plan.PelicanConfig.Prompt == "" || plan.PelicanConfig.Quality == nil || plan.PelicanConfig.Quality.ExpectedAnswer == "") {
		return failure("random_question_not_prepared")
	}
	if batch := plan.PelicanConfig.QualityBatch; batch != nil && !time.Now().Before(batch.StartDeadline) {
		return failure("quality_batch_start_window_closed")
	}
	var err error
	result, err = s.runPelicanAnswer(ctx, plan)
	if err != nil {
		return failure(fmt.Sprint(err))
	}
	if result == nil {
		return failure("scheduled_test_empty_result: background sample returned no result")
	}
	result.PelicanConfig = plan.PelicanConfig
	// 探针题型的结果自带 correct/incorrect/unknown 判定，不经过判题模型。
	if plan.PelicanConfig.Quality != nil && result.Status == "success" && !isOpenAICodexStateProbePlan(plan.PelicanConfig) {
		var judgment *QualityJudgment
		if plan.PelicanConfig.Quality.Action == QualityActionOpenAITwoFAAdmission {
			judgment = &QualityJudgment{Verdict: "incorrect", Reason: "builtin_admission", AccountID: plan.AccountID}
			if strings.TrimSpace(result.ResponseText) == "21" {
				judgment.Verdict = "correct"
			}
		} else if plan.TriggerSource == quality5xxSource && isBuiltinCandyPlan(plan.PelicanConfig) {
			judgment = &QualityJudgment{Verdict: "incorrect", Reason: "builtin_candy", AccountID: plan.AccountID}
			if CandyAnswerCorrect(result.ResponseText) {
				judgment.Verdict = "correct"
			}
		} else if s.judgeQuality != nil {
			judgment = s.judgeQuality(ctx, plan.AccountID, plan.PelicanConfig, result.ResponseText)
		}
		applyQualityJudgment(result, judgment)
		result.FinishedAt = time.Now()
		if judgment == nil || judgment.Kind != "jev" {
			result.LatencyMs = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
		}
	}
	return result
}

// The generated SSE is captured in memory, so cap it before buffering, not just at persistence.
type pelicanRecorder struct {
	*httptest.ResponseRecorder
	cancel   context.CancelFunc
	overflow bool
}

func (w *pelicanRecorder) Write(data []byte) (int, error) {
	if w.overflow || w.Body.Len()+len(data) > 4<<20 {
		w.overflow = true
		w.cancel()
		return 0, io.ErrShortWrite
	}
	return w.ResponseRecorder.Write(data)
}
func (w *pelicanRecorder) WriteString(data string) (int, error) { return w.Write([]byte(data)) }

func parsePelicanOutput(body string) (string, string) {
	output, message := parseTestSSEOutput(body)
	complete := false
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event TestEvent
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil {
			continue
		}
		if event.Type == "test_complete" {
			complete = event.Success
			if !event.Success && message == "" {
				message = "Generation did not complete successfully"
			}
		}
	}
	if !complete && message == "" {
		message = "Generation stream ended before completion"
	}
	return output, message
}

func isBuiltinCandyPlan(cfg *PelicanTestConfig) bool {
	return cfg != nil && strings.TrimSpace(cfg.Prompt) == strings.TrimSpace(CandyPrompt)
}

// Missing kind preserves HTML validation for saved plans from older versions.
func intelligenceTestPrompt(cfg *PelicanTestConfig) string {
	if cfg.Quality != nil {
		return cfg.Prompt + "\n\n只输出最终答案，不要解释。"
	}
	if cfg.QuestionKind == ChannelMonitorV3ProbeQuestionKind {
		// The V1 challenge already asks for the bare number.
		return cfg.Prompt
	}
	contract := PelicanDeliveryContract
	if cfg.QuestionKind == "candy" || isBuiltinCandyPlan(cfg) {
		contract = "只输出最终整数，不要解释。"
	}
	return cfg.Prompt + "\n\n" + contract
}
func intelligenceTestOutputError(cfg *PelicanTestConfig, output string) string {
	if cfg.Quality != nil {
		// Completed answers are graded by the configured model in the runner.
		if strings.TrimSpace(output) == "" {
			return pelicanErrEmptyOutput
		}
		return ""
	}
	if strings.TrimSpace(output) == "" {
		return pelicanErrEmptyOutput
	}
	if cfg.QuestionKind == ChannelMonitorV3ProbeQuestionKind {
		// The V3 prober checks the arithmetic answer itself.
		return ""
	}
	if isBuiltinCandyPlan(cfg) && !CandyAnswerCorrect(output) {
		return "answer_mismatch: expected 21"
	}
	if cfg.QuestionKind != "candy" && !isBuiltinCandyPlan(cfg) && !pelicanHTMLPattern.MatchString(output) {
		return "Model did not return HTML or SVG"
	}
	return ""
}
