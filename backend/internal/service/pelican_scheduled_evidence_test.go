//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestPelicanScheduledActualPromptMatchesSentPayload(t *testing.T) {
	for _, mode := range []string{"custom", "", PelicanPromptModeRandom} {
		for _, refused := range []bool{false, true} {
			name := mode
			if mode == "" {
				name = "legacy"
			}
			if refused {
				name += "/refused"
			}
			t.Run(name, func(t *testing.T) {
				account := pelicanClaudeOAuthAccount()
				upstream := &claudeThinkingUpstream{thinkingTokens: 100, answerTokens: 100}
				if refused {
					upstream.refusal = "fixture-refusal"
				}
				answers := pelicanClaudeTestService(account, upstream)
				plans, results := &pelicanPlanRepo{}, &pelicanResults{}
				runner := &ScheduledTestRunnerService{planRepo: plans, scheduledSvc: NewScheduledTestService(plans, results), runPelican: answers.RunPelicanBackground}
				plan := pelicanPlan()
				plan.AccountID, plan.ModelID = account.ID, "claude-opus-5-5"
				plan.PelicanConfig.ParallelCount = 1
				plan.PelicanConfig.PromptMode = mode
				plan.PelicanConfig.ActualPrompt = "untrusted client evidence"
				original := plan.PelicanConfig
				require.True(t, runner.runPelicanPlan(context.Background(), plan))
				require.True(t, plans.finished)
				require.Len(t, upstream.bodies, 1)
				require.Len(t, results.results, 1)
				saved := results.results[0]
				sent := gjson.GetBytes(upstream.bodies[0], "messages.0.content.0.text").String()
				require.NotEmpty(t, sent)
				require.Equal(t, sent, saved.PelicanConfig.ActualPrompt, "saved actual prompt must match sent payload including delivery contract")
				require.Contains(t, sent, PelicanDeliveryContract)
				if refused {
					require.Equal(t, "failed", saved.Status)
					require.Contains(t, saved.ErrorMessage, pelicanErrRefused)
				} else {
					require.Equal(t, "success", saved.Status)
					require.Equal(t, pelicanClaudeTestHTML, saved.ResponseText)
				}
				if mode == PelicanPromptModeRandom {
					require.NotNil(t, saved.PelicanConfig.PelicanQuestion)
					require.Equal(t, PelicanQuestionBankVersion, saved.PelicanConfig.PelicanQuestion.Version)
					require.Len(t, saved.PelicanConfig.PelicanQuestion.Criteria, 4)
				} else {
					require.Nil(t, saved.PelicanConfig.PelicanQuestion)
					require.Equal(t, "draw a pelican\n\n"+PelicanDeliveryContract, sent)
				}
				require.NotSame(t, plan.PelicanConfig, saved.PelicanConfig)
				encoded, err := json.Marshal(saved.PelicanConfig)
				require.NoError(t, err)
				var history PelicanTestConfig
				require.NoError(t, json.Unmarshal(encoded, &history))
				require.Equal(t, sent, history.ActualPrompt)
				plan.PelicanConfig.Prompt = "edited later"
				plan.PelicanConfig.ActualPrompt = "edited evidence"
				require.Equal(t, sent, saved.PelicanConfig.ActualPrompt)
				require.NotEqual(t, "edited later", saved.PelicanConfig.Prompt)
				require.Empty(t, original.ActualPrompt, "rule validation strips client evidence")
			})
		}
	}
}

func TestPelicanSampleActualPromptFailureEvidence(t *testing.T) {
	for _, outcome := range []string{"error", "nil", "panic", "failed_without_snapshot", "judge_panic"} {
		t.Run(outcome, func(t *testing.T) {
			plan := pelicanPlan()
			plan.PelicanConfig.ActualPrompt = "untrusted client evidence"
			if outcome == "judge_panic" {
				plan.PelicanConfig.Quality = &QualityPolicy{}
			}
			want := intelligenceTestPrompt(plan.PelicanConfig)
			runner := &ScheduledTestRunnerService{
				runPelican: func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
					switch outcome {
					case "error":
						return nil, errors.New("fixture adapter error")
					case "nil":
						return nil, nil
					case "panic":
						panic("PRIVATE_ADAPTER_DATA")
					case "judge_panic":
						return &ScheduledTestResult{Status: "success", ResponseText: "fixture answer", StartedAt: time.Now()}, nil
					default:
						return &ScheduledTestResult{Status: "failed", ErrorMessage: "fixture failure"}, nil
					}
				},
				judgeQuality: func(context.Context, int64, *PelicanTestConfig, string) *QualityJudgment {
					panic("PRIVATE_JUDGE_DATA")
				},
			}
			result := runner.runPelicanSample(context.Background(), plan)
			require.Equal(t, "failed", result.Status)
			require.Equal(t, want, result.PelicanConfig.ActualPrompt, "failed invocation must retain assembled prompt evidence")
			require.NotContains(t, result.ErrorMessage, "PRIVATE")
			require.NotSame(t, plan.PelicanConfig, result.PelicanConfig)
			require.Equal(t, "untrusted client evidence", plan.PelicanConfig.ActualPrompt)
		})
	}
}

func TestPelicanSamplePreflightDoesNotInventActualPrompt(t *testing.T) {
	for _, reason := range []string{"unprepared", "closed_batch", "canceled"} {
		t.Run(reason, func(t *testing.T) {
			plan := pelicanPlan()
			plan.PelicanConfig.ActualPrompt = "untrusted client evidence"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch reason {
			case "unprepared":
				plan.PelicanConfig.QuestionKind = QualityRandomQuestionKind
			case "closed_batch":
				plan.PelicanConfig.QualityBatch = &QualityBatchEvidence{StartDeadline: time.Now().Add(-time.Minute)}
			case "canceled":
				cancel()
			}
			runner := &ScheduledTestRunnerService{runPelican: func(context.Context, int64, string, *PelicanTestConfig) (*ScheduledTestResult, error) {
				t.Fatal("preflight must not invoke an answer adapter")
				return nil, nil
			}}
			result := runner.runPelicanSample(ctx, plan)
			require.Equal(t, "failed", result.Status)
			require.Empty(t, result.PelicanConfig.ActualPrompt)
		})
	}
}

func TestPelicanSampleContainsMissingConfigPanic(t *testing.T) {
	plan := pelicanPlan()
	plan.PelicanConfig = nil
	runner := &ScheduledTestRunnerService{}
	result := runner.runPelicanSample(context.Background(), plan)
	require.Equal(t, "failed", result.Status)
	require.Contains(t, result.ErrorMessage, "scheduled_test_panic")
	require.Empty(t, result.PelicanConfig.ActualPrompt)
}
