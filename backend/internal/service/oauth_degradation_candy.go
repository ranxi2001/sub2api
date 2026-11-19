package service

import (
	"context"
	"log/slog"
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
		if !wrongCandyAnswer(result) || result.PelicanConfig == nil || result.PelicanConfig.ModelID == "" {
			continue
		}
		// One pair per claimed round, even when several models failed.
		sample := *plan
		cfg := *plan.PelicanConfig
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
			if wrongCandyAnswer(s.runPelicanSample(ctx, &sample)) {
				review.Incorrect++
			}
		}
		if review.Incorrect == 2 {
			if _, err := repo.ReviewOAuthDegradationCandy(ctx, plan.AccountID, review); err != nil {
				slog.Error("oauth_degradation_candy_apply", "account_id", plan.AccountID, "error", err)
			}
		}
		return
	}
}
