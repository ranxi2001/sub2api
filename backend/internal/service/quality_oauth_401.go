package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// QualityOAuth401Round stores only classification, not provider error bodies,
// prompts, credential material or request metadata. It is an internal ledger
// field, never an editable rule or public result field.
type QualityOAuth401Round struct {
	AllDirect401        bool `json:"all_direct_401"`
	All401OrUnavailable bool `json:"all_401_or_unavailable"`
	HasDirectRevoked401 bool `json:"has_direct_revoked_401,omitempty"`
}

// Only a structured code on the proven own-credential response grants trust.
// Messages, including embedded/quoted JSON and negated codes, are not evidence.
func qualityOAuth401RevokedCode(body []byte) bool {
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	switch payload.Error.Code {
	case "token_revoked", "token_invalidated", "refresh_token_invalidated", "refresh_token_reused", "invalid_grant":
		return true
	default:
		return false
	}
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
		evidence.HasDirectRevoked401 = evidence.HasDirectRevoked401 || direct && result.DirectOAuthRevoked401
	}
	if !evidence.All401OrUnavailable {
		evidence.HasDirectRevoked401 = false
	}
	return evidence
}
