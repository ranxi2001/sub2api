package service

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// SaveQuality429Round commits classification, the guarded action and raw samples together.
type Quality429Repository interface {
	SaveQuality429Round(context.Context, *ScheduledTestPlan, time.Time, []*ScheduledTestResult, string) (string, error)
}

// A model contributes at most once per complete round. Mixed outcomes for one
// model cannot establish that its terminal result was a rate limit.
func Quality429Models(plan *ScheduledTestPlan, results []*ScheduledTestResult) []string {
	cfg := plan.PelicanConfig
	models := qualityPlanModels(plan)
	if cfg == nil || cfg.Quality == nil || cfg.ParallelCount < 1 || len(results) != len(models)*cfg.ParallelCount {
		return nil
	}
	count, limited := map[string]int{}, map[string]int{}
	for _, r := range results {
		if r == nil || r.PelicanConfig == nil {
			return nil
		}
		model := r.PelicanConfig.ModelID
		count[model]++
		if r.Status == "failed" && r.PelicanConfig.QualityHTTPStatus == http.StatusTooManyRequests {
			limited[model]++
		}
	}
	var found []string
	for _, model := range models {
		if model == "" || count[model] != cfg.ParallelCount {
			return nil
		}
		if limited[model] == cfg.ParallelCount {
			found = append(found, model)
		}
	}
	return found
}

func (s *AccountTestService) sendHTTPErrorAndEnd(c *gin.Context, status int, errorMsg string) error {
	c.Set("quality_terminal_http_status", status)
	return s.sendErrorAndEnd(c, errorMsg)
}
