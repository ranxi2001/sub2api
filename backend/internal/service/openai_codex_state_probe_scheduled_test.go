package service

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func stateProbePlanConfig() *PelicanTestConfig {
	return &PelicanTestConfig{QuestionKind: OpenAICodexStateProbeQuestionKind, ReasoningEffort: "medium", ParallelCount: 1}
}

func stateProbeTestService(upstream *stateProbeUpstream) *AccountTestService {
	return &AccountTestService{
		accountRepo:          &stateProbeAccountRepo{account: stateProbeAccount()},
		openaiGatewayService: &OpenAIGatewayService{httpUpstream: upstream},
	}
}

func TestRunPelicanBackgroundStateProbeHealthy(t *testing.T) {
	upstream := &stateProbeUpstream{replies: []stateProbeReply{
		stateProbeMint("ticket-1"),
		{status: http.StatusOK, body: stateProbeCompletedStream},
	}}
	svc := stateProbeTestService(upstream)

	result, err := svc.RunPelicanBackground(context.Background(), 7, "gpt-6-astra", stateProbePlanConfig())
	require.NoError(t, err)
	require.Equal(t, "success", result.Status)
	require.Empty(t, result.ErrorMessage)
	require.Contains(t, result.ResponseText, "判定：满血")
	require.Contains(t, result.ResponseText, "打票 HTTP 200 / 续接 HTTP 200")
	require.Equal(t, "gpt-6-astra", result.PelicanConfig.ModelID)
	require.Equal(t, OpenAICodexStateProbeQuestionKind, result.PelicanConfig.QuestionKind)
	require.Nil(t, result.QualityJudgment)
	require.Len(t, upstream.calls, 2)
	require.False(t, result.FinishedAt.Before(result.StartedAt))
}

func TestRunPelicanBackgroundStateProbeDegradedWithQuality(t *testing.T) {
	upstream := &stateProbeUpstream{replies: []stateProbeReply{
		stateProbeMint("ticket-1"),
		{status: http.StatusOK, state: "ticket-2", body: stateProbeCompletedStream},
	}}
	svc := stateProbeTestService(upstream)
	cfg := stateProbePlanConfig()
	cfg.Quality = &QualityPolicy{Action: "disable_scheduling"}

	result, err := svc.RunPelicanBackground(context.Background(), 7, "gpt-6-astra", cfg)
	require.NoError(t, err)
	require.Equal(t, "failed", result.Status)
	require.Equal(t, openAICodexStateDegradedError, result.ErrorMessage)
	require.Contains(t, result.ResponseText, "判定：降智")
	require.Contains(t, result.ResponseText, "续接回新票：是")
	require.NotNil(t, result.QualityJudgment)
	require.Equal(t, "incorrect", result.QualityJudgment.Verdict)
	// 探针结果自带判定，qualityOutcome 应把这一轮记为不通过。
	require.Equal(t, "failed", qualityOutcome([]*ScheduledTestResult{result}))
}

func TestRunPelicanBackgroundStateProbeInconclusive(t *testing.T) {
	upstream := &stateProbeUpstream{replies: []stateProbeReply{
		{status: http.StatusTooManyRequests, body: `{"error":{"message":"rate limited"}}`},
	}}
	svc := stateProbeTestService(upstream)
	cfg := stateProbePlanConfig()
	cfg.Quality = &QualityPolicy{Action: "disable_scheduling"}

	result, err := svc.RunPelicanBackground(context.Background(), 7, "gpt-6-astra", cfg)
	require.NoError(t, err)
	require.Equal(t, "failed", result.Status)
	require.Equal(t, openAICodexStateInconclusiveErrPrefix+": "+OpenAICodexStateFailureRateLimited, result.ErrorMessage)
	require.Contains(t, result.ResponseText, "判定：无法判断")
	require.Equal(t, "unknown", result.QualityJudgment.Verdict)
	// 无法判断不算降智证据：这一轮是 inconclusive，不触发处置。
	require.Equal(t, "inconclusive", qualityOutcome([]*ScheduledTestResult{result}))
}

func TestRunPelicanBackgroundStateProbeBusyRecordsInconclusive(t *testing.T) {
	upstream := &stateProbeUpstream{}
	svc := stateProbeTestService(upstream)
	release, ok := svc.beginOpenAICodexStateProbe(7)
	require.True(t, ok)
	defer release()

	result, err := svc.RunPelicanBackground(context.Background(), 7, "gpt-6-astra", stateProbePlanConfig())
	require.NoError(t, err)
	require.Equal(t, "failed", result.Status)
	require.Contains(t, result.ErrorMessage, openAICodexStateInconclusiveErrPrefix)
	require.Contains(t, result.ResponseText, "已有一次探针在进行中")
	require.Empty(t, upstream.calls)
}

func TestStateProbePlanValidation(t *testing.T) {
	now := time.Now()
	plan := pelicanPlan()
	plan.PelicanConfig = stateProbePlanConfig()

	// 探针题型不需要题目文本。
	_, err := nextPlanRun(plan, now)
	require.NoError(t, err)
	require.Equal(t, plan.ModelID, plan.PelicanConfig.ModelID)

	// 并行数必须为 1。
	bad := pelicanPlan()
	bad.PelicanConfig = stateProbePlanConfig()
	bad.PelicanConfig.ParallelCount = 2
	_, err = nextPlanRun(bad, now)
	require.ErrorContains(t, err, "parallel")

	// 模型仍然必填。
	noModel := pelicanPlan()
	noModel.ModelID = " "
	noModel.PelicanConfig = stateProbePlanConfig()
	_, err = nextPlanRun(noModel, now)
	require.Error(t, err)

	// 质量规则允许探针题型：不需要参考答案与判题模型。
	quality := pelicanPlan()
	quality.PelicanConfig = stateProbePlanConfig()
	quality.PelicanConfig.Quality = &QualityPolicy{Action: "disable_scheduling"}
	_, err = nextPlanRun(quality, now)
	require.NoError(t, err)

	// 糖果质量规则仍要求参考答案。
	candy := pelicanPlan()
	candy.PelicanConfig = &PelicanTestConfig{QuestionKind: "candy", Prompt: "q", ReasoningEffort: "medium", ParallelCount: 1, Quality: &QualityPolicy{Action: "disable_scheduling"}}
	_, err = nextPlanRun(candy, now)
	require.ErrorContains(t, err, "expected answer")
}

func TestStateProbeQualityPlanSkipsJudge(t *testing.T) {
	plans := &qualityPlanRepo{}
	results := &pelicanResults{}
	runner := &ScheduledTestRunnerService{planRepo: plans, scheduledSvc: NewScheduledTestService(plans, results)}
	runner.judgeQuality = func(context.Context, int64, *PelicanTestConfig, string) *QualityJudgment {
		t.Fatal("state probe plans must not call the quality judge")
		return nil
	}
	var mu sync.Mutex
	runner.runPelican = func(_ context.Context, _ int64, _ string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
		mu.Lock()
		defer mu.Unlock()
		snapshot := *cfg
		return &ScheduledTestResult{Status: "success", ResponseText: "判定：满血", PelicanConfig: &snapshot, QualityJudgment: &QualityJudgment{Verdict: "correct", Reason: "门票延续正常"}}, nil
	}

	plan := pelicanPlan()
	plan.PelicanConfig = stateProbePlanConfig()
	plan.PelicanConfig.Quality = &QualityPolicy{Action: "disable_scheduling"}
	plan.AutoRecover = false
	runner.runOnePlan(context.Background(), plan)
	require.Len(t, results.results, 1)
	require.Equal(t, "success", results.results[0].Status)
	require.Equal(t, "correct", results.results[0].QualityJudgment.Verdict)
	require.Equal(t, []string{"passed"}, plans.outcomes)
	require.True(t, plans.finished)
}
