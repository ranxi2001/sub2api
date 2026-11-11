//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPelicanQuestionFrozenRounds(t *testing.T) {
	plan := &PelicanTestConfig{QuestionKind: "pelican", PromptMode: PelicanPromptModeRandom, ParallelCount: 3}
	for index := range pelicanQuestionBank {
		calls := 0
		round := freezePelicanQuestion(plan, "round", func(size int) int {
			calls++
			require.Equal(t, len(pelicanQuestionBank), size)
			return index
		})
		for range 3 {
			account := *round
			require.Equal(t, pelicanQuestionBank[index].id, account.PelicanQuestion.ID)
			require.Equal(t, round.ActualPrompt, intelligenceTestPrompt(&account))
		}
		require.Equal(t, 1, calls)
		require.Len(t, round.PelicanQuestion.Criteria, 4)
		require.Contains(t, round.ActualPrompt, PelicanDeliveryContract)
		body, err := json.Marshal(round)
		require.NoError(t, err)
		var saved PelicanTestConfig
		require.NoError(t, json.Unmarshal(body, &saved))
		require.Equal(t, round, &saved)
	}
	require.Empty(t, plan.Prompt)
	require.Nil(t, plan.PelicanQuestion)
	first := freezePelicanQuestion(plan, "one", func(int) int { return 0 })
	next := freezePelicanQuestion(plan, "two", func(int) int { return 1 })
	require.NotEqual(t, first.PelicanQuestion.ID, next.PelicanQuestion.ID)
}

func TestPelicanQuestionLegacyCustomAndManualIdentity(t *testing.T) {
	for _, mode := range []string{"", "custom"} {
		cfg := freezePelicanQuestion(&PelicanTestConfig{Prompt: "my custom SVG", PromptMode: mode}, "round", func(int) int { t.Fatal("custom must not select"); return 0 })
		require.Equal(t, "my custom SVG", cfg.Prompt)
		require.Nil(t, cfg.PelicanQuestion)
	}
	manual := freezePelicanQuestion(&PelicanTestConfig{PromptMode: PelicanPromptModeRandom}, "manual:00000000000000000000000000000000", nil)
	resolved, err := ResolveManualPelicanQuestion(manual.PelicanQuestion)
	require.NoError(t, err)
	require.Equal(t, manual.PelicanQuestion, resolved.PelicanQuestion)
	manual.PelicanQuestion.Version = "future"
	_, err = ResolveManualPelicanQuestion(manual.PelicanQuestion)
	require.Error(t, err)
}

func TestPelicanRandomScheduleValidation(t *testing.T) {
	for _, kind := range []string{"", "pelican"} {
		plan := pelicanPlan()
		plan.PelicanConfig.QuestionKind = kind
		plan.PelicanConfig.PromptMode = PelicanPromptModeRandom
		plan.PelicanConfig.Prompt = ""
		plan.PelicanConfig.PelicanQuestion = &PelicanQuestion{ID: "untrusted"}
		plan.PelicanConfig.ActualPrompt = "untrusted"
		_, err := nextPlanRun(plan, time.Now())
		require.NoError(t, err)
		require.Nil(t, plan.PelicanConfig.PelicanQuestion)
		require.Empty(t, plan.PelicanConfig.ActualPrompt)
	}
	for _, cfg := range []*PelicanTestConfig{
		{PromptMode: "typo"},
		{PromptMode: PelicanPromptModeRandom, QuestionKind: "candy"},
		{PromptMode: PelicanPromptModeRandom, Quality: &QualityPolicy{}},
	} {
		require.Error(t, validatePelicanPromptMode(cfg))
	}
}

func TestPelicanRandomScheduledAccountsShareDueRound(t *testing.T) {
	due := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	var expected *PelicanQuestion
	for _, id := range []int64{41, 42} {
		plans, results := &pelicanPlanRepo{}, &pelicanResults{}
		runner := &ScheduledTestRunnerService{planRepo: plans, scheduledSvc: NewScheduledTestService(plans, results)}
		plan := pelicanPlan()
		plan.AccountID, plan.NextRunAt = id, &due
		plan.PelicanConfig.PromptMode = PelicanPromptModeRandom
		plan.PelicanConfig.Prompt = ""
		original := plan.PelicanConfig
		runner.runPelican = func(_ context.Context, _ int64, _ string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
			return &ScheduledTestResult{Status: "success", ResponseText: "<svg></svg>"}, nil
		}
		require.True(t, runner.runPelicanPlan(context.Background(), plan))
		require.Len(t, results.results, 2)
		for _, result := range results.results {
			require.NotNil(t, result.PelicanConfig.PelicanQuestion)
			if expected == nil {
				expected = result.PelicanConfig.PelicanQuestion
			}
			require.Equal(t, expected, result.PelicanConfig.PelicanQuestion)
			require.Equal(t, intelligenceTestPrompt(result.PelicanConfig), result.PelicanConfig.ActualPrompt)
		}
		require.Nil(t, original.PelicanQuestion)
		require.Empty(t, original.Prompt)
	}
}

func TestPelicanRandomGroupSamplesShareQuestion(t *testing.T) {
	plan := groupTestPlan(2)
	plan.PelicanConfig.PromptMode = PelicanPromptModeRandom
	due := groupTestNow.Add(-time.Minute)
	plan.NextRunAt = &due
	repo := newGroupTestRepoFake(plan)
	router := &groupTestRouterFake{steps: []routeStep{{account: account(11, "fixture-a")}, {account: account(12, "fixture-b")}}}
	svc, _ := newGroupTestService(repo, router, func(_ context.Context, _ int64, _ string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
		return &ScheduledTestResult{Status: "success", ResponseText: "<svg></svg>"}, nil
	})
	svc.runScheduled(context.Background(), plan, groupTestNow)
	require.Len(t, repo.results, 2)
	expected := freezePelicanQuestion(&PelicanTestConfig{PromptMode: PelicanPromptModeRandom}, "scheduled:"+due.UTC().Format(time.RFC3339Nano), nil)
	for _, result := range repo.results {
		require.Equal(t, expected.PelicanQuestion, result.PelicanConfig.PelicanQuestion)
		require.Equal(t, expected.ActualPrompt, result.PelicanConfig.ActualPrompt)
	}
}
