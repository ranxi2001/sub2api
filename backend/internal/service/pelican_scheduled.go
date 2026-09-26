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

func (s *AccountTestService) RunPelicanBackground(ctx context.Context, accountID int64, model string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &pelicanRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	c, _ := gin.CreateTestContext(w)
	c.Request = (&http.Request{Header: make(http.Header)}).WithContext(ctx)
	err := s.TestPelicanAccountConnection(c, accountID, model, intelligenceTestPrompt(cfg), cfg.ReasoningEffort)
	output, message := parsePelicanOutput(w.Body.String())
	if w.overflow {
		output = ""
		message = "Response exceeds 4 MiB capture limit"
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
		message = "Output exceeds 2 MiB history limit"
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

func (s *ScheduledTestRunnerService) runPelicanPlan(ctx context.Context, plan *ScheduledTestPlan) {
	now := time.Now()
	next, err := nextPlanRun(plan, now)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d invalid config: %v", plan.ID, err)
		return
	}
	// Persisted lease prevents duplicate execution across ticks and server replicas.
	// It also recovers automatically after a process crash.
	until := now.Add(15 * time.Minute).Truncate(time.Microsecond)
	claimed, err := s.planRepo.ClaimPelican(ctx, plan, now, until, next)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d claim failed: %v", plan.ID, err)
	}
	if err != nil || !claimed {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	results := make([]*ScheduledTestResult, plan.PelicanConfig.ParallelCount)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index] = s.runPelicanSample(runCtx, plan)
		}(i)
	}
	wg.Wait()
	// Persist timeout failures with a fresh context even after the request deadline.
	saveCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	qualityAction := ""
	if plan.PelicanConfig.Quality != nil {
		var actionErr error
		qualityAction, actionErr = s.planRepo.ApplyQualityOutcome(saveCtx, plan, until, qualityOutcome(results))
		if actionErr != nil {
			qualityAction = "action_error"
			logger.LegacyPrintf("service.scheduled_test_runner", "quality plan=%d action failed: %v", plan.ID, actionErr)
		}
	}
	succeeded := false
	for _, result := range results {
		result.QualityAction = qualityAction
		if plan.PelicanConfig.Quality != nil {
			result.QualityRoundID = until.Format(time.RFC3339Nano)
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
	if err := s.planRepo.FinishPelican(saveCtx, plan.ID, until, time.Now()); err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "pelican plan=%d finish failed: %v", plan.ID, err)
	}
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
	var err error
	result, err = s.runPelican(ctx, plan.AccountID, plan.ModelID, plan.PelicanConfig)
	if err != nil {
		return failure(fmt.Sprint(err))
	}
	if result == nil {
		return failure("scheduled_test_empty_result: background sample returned no result")
	}
	// 探针题型的结果自带 correct/incorrect/unknown 判定，不经过判题模型。
	if plan.PelicanConfig.Quality != nil && result.Status == "success" && !isOpenAICodexStateProbePlan(plan.PelicanConfig) {
		var judgment *QualityJudgment
		if s.judgeQuality != nil {
			judgment = s.judgeQuality(ctx, plan.AccountID, plan.PelicanConfig, result.ResponseText)
		}
		applyQualityJudgment(result, judgment)
		result.FinishedAt = time.Now()
		result.LatencyMs = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
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
			return "Model returned empty output"
		}
		return ""
	}
	if isBuiltinCandyPlan(cfg) && strings.TrimSpace(output) != "21" {
		return "answer_mismatch: expected 21"
	}
	if strings.TrimSpace(output) == "" {
		return "Model returned empty output"
	}
	if cfg.QuestionKind != "candy" && !isBuiltinCandyPlan(cfg) && !pelicanHTMLPattern.MatchString(output) {
		return "Model did not return HTML or SVG"
	}
	return ""
}
