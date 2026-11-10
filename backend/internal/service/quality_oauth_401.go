package service

import (
	"context"
	"strings"
	"time"
)

// QualityOAuth401Round stores only classification, not provider error bodies,
// prompts, credential material or request metadata. It is an internal ledger
// field, never an editable rule or public result field.
type QualityOAuth401Round struct {
	AllDirect401        bool `json:"all_direct_401"`
	All401OrUnavailable bool `json:"all_401_or_unavailable"`
}

type QualityOAuth401Repository interface {
	CompleteQualityOAuth401Round(context.Context, *ScheduledTestPlan, time.Time, *QualityOAuth401Round) (bool, error)
}

// Classify one complete round before retention pruning or ledger redaction.
// A model/sample is not a separate round. Inconclusive/partial rounds reset the
// streak; neither judge errors nor a response body containing "401" qualifies.
func QualityOAuth401Evidence(plan *ScheduledTestPlan, results []*ScheduledTestResult) *QualityOAuth401Round {
	evidence := &QualityOAuth401Round{}
	if plan == nil || plan.PelicanConfig == nil || plan.PelicanConfig.Quality == nil || plan.PelicanConfig.TestChannel == "bps" {
		return evidence
	}
	cfg := plan.PelicanConfig
	models := max(1, len(cfg.ModelIDs))
	if cfg.ParallelCount < 1 || len(results) != models*cfg.ParallelCount {
		return evidence
	}
	evidence.AllDirect401, evidence.All401OrUnavailable = true, true
	for _, result := range results {
		if result == nil {
			return &QualityOAuth401Round{}
		}
		direct := result.DirectOAuth401 && result.Status == "failed" && strings.HasPrefix(result.ErrorMessage, "API returned 401:")
		unavailable := result.Status == "skipped" && result.ErrorMessage == "model_unavailable"
		evidence.AllDirect401 = evidence.AllDirect401 && direct
		evidence.All401OrUnavailable = evidence.All401OrUnavailable && (direct || unavailable)
	}
	return evidence
}
