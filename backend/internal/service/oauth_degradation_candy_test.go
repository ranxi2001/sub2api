package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type candyReviewAccounts struct {
	AccountRepository
	requests []OAuthDegradationCandyReview
	revision string
}

func (r *candyReviewAccounts) ReviewOAuthDegradationCandy(_ context.Context, _ int64, review OAuthDegradationCandyReview) (string, error) {
	r.requests = append(r.requests, review)
	return r.revision, nil
}

func TestOAuthDegradationCandyConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		revision  string
		answers   []string
		confirmed bool
	}{
		{"below threshold", "", nil, false},
		{"persistent", "a", []string{"incorrect", "incorrect"}, true},
		{"first recovered", "a", []string{"correct", "incorrect"}, false},
		{"second recovered", "a", []string{"incorrect", "correct"}, false},
		{"limited twice", "a", []string{"429", "429"}, true},
		{"limited then wrong", "a", []string{"429", "incorrect"}, true},
		{"wrong then limited", "a", []string{"incorrect", "429"}, true},
		{"limited then recovered", "a", []string{"429", "correct"}, false},
		{"error text only", "a", []string{"text429", "incorrect"}, false},
		{"judge unavailable", "a", []string{"incorrect", "unknown"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &candyReviewAccounts{revision: tc.revision}
			runner := &ScheduledTestRunnerService{accountTestSvc: &AccountTestService{accountRepo: repo}}
			plan := qualityAnswerRetryPlan()
			plan.PelicanConfig.Prompt = CandyPrompt
			model := "failed-model"
			calls := 0
			runner.runPelican = func(_ context.Context, account int64, id string, cfg *PelicanTestConfig) (*ScheduledTestResult, error) {
				require.Equal(t, model, id)
				require.Equal(t, plan.AccountID, account)
				require.Equal(t, CandyPrompt, cfg.Prompt)
				answer := tc.answers[calls]
				calls++
				if answer == "429" {
					return &ScheduledTestResult{Status: "failed", PelicanConfig: &PelicanTestConfig{QualityHTTPStatus: 429}}, nil
				}
				if answer == "text429" {
					return &ScheduledTestResult{Status: "failed", ErrorMessage: "API returned 429: busy"}, nil
				}
				return &ScheduledTestResult{Status: "success", ResponseText: answer}, nil
			}
			runner.judgeQuality = func(_ context.Context, _ int64, _ *PelicanTestConfig, answer string) *QualityJudgment {
				return &QualityJudgment{Verdict: answer}
			}
			fail := &ScheduledTestResult{Status: "failed", ErrorMessage: "answer_mismatch", QualityJudgment: &QualityJudgment{Verdict: "incorrect"}, PelicanConfig: &PelicanTestConfig{ModelID: model}, StartedAt: time.Now()}
			runner.reviewDegradationCandyRound(t.Context(), plan, time.Now().Add(time.Minute), []*ScheduledTestResult{fail, fail})
			require.Equal(t, len(tc.answers), calls)
			if tc.confirmed {
				require.Len(t, repo.requests, 2)
				var limited int
				for _, answer := range tc.answers {
					if answer == "429" {
						limited++
					}
				}
				require.Equal(t, limited, repo.requests[1].RateLimited)
				require.Equal(t, 2-limited, repo.requests[1].Incorrect)
				require.Equal(t, tc.revision, repo.requests[1].Revision)
			} else {
				require.Len(t, repo.requests, 1)
			}
		})
	}
}
