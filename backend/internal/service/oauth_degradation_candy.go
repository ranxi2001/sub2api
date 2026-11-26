package service

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

type oauthDegradationCandyRepository interface {
	ReviewOAuthDegradationCandy(context.Context, int64, OAuthDegradationCandyReview) (string, error)
}

func wrongCandyAnswer(result *ScheduledTestResult) bool {
	return result != nil && result.Status == "failed" && result.ErrorMessage == "answer_mismatch" &&
		result.QualityJudgment != nil && result.QualityJudgment.Verdict == "incorrect"
}

func (s *ScheduledTestRunnerService) reviewDegradationCandyRound(ctx context.Context, plan *ScheduledTestPlan, until time.Time, results []*ScheduledTestResult) {
	if s.accountTestSvc == nil {
		return
	}
	repo, ok := s.accountTestSvc.accountRepo.(oauthDegradationCandyRepository)
	if !ok {
		return
	}
	for _, result := range results {
		if result == nil || result.PelicanConfig == nil || result.PelicanConfig.ModelID == "" {
			continue
		}
		if !wrongCandyAnswer(result) && (result.PelicanConfig.Quality429Streak < 3 || plan.PelicanConfig.Quality.Action == QualityActionObserveOnly) {
			continue
		}
		// One pair per claimed round, even when several models failed.
		sample := *plan
		cfg := *plan.PelicanConfig
		cfg.QualityHTTPStatus, cfg.Quality429Streak = 0, 0
		cfg.ModelID = result.PelicanConfig.ModelID
		sample.ModelID, sample.PelicanConfig, sample.degradationRetest = cfg.ModelID, &cfg, true
		review := OAuthDegradationCandyReview{PlanID: plan.ID, ModelID: sample.ModelID, LeaseUntil: until, PlanUpdatedAt: plan.UpdatedAt, StartedAt: result.StartedAt}
		revision, err := repo.ReviewOAuthDegradationCandy(ctx, plan.AccountID, review)
		if err != nil {
			slog.Error("oauth_degradation_candy_check", "account_id", plan.AccountID, "error", err)
			return
		}
		if revision == "" {
			return
		}
		review.Revision = revision
		for range 2 {
			if ctx.Err() != nil || !time.Now().Before(until) {
				return
			}
			retest := s.runPelicanSample(ctx, &sample)
			if wrongCandyAnswer(retest) {
				review.Incorrect++
			} else if retest != nil && retest.Status == "failed" && retest.PelicanConfig != nil && retest.PelicanConfig.QualityHTTPStatus == http.StatusTooManyRequests {
				review.RateLimited++
			}
		}
		if review.Confirmed() {
			if _, err := repo.ReviewOAuthDegradationCandy(ctx, plan.AccountID, review); err != nil {
				slog.Error("oauth_degradation_candy_apply", "account_id", plan.AccountID, "error", err)
			}
		}
		return
	}
}
