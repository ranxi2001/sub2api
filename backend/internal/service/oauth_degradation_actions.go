package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"slices"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const OAuthDegradationActionExtraKey = "oauth_degradation_action"

type OAuthDegradationConfig struct {
	Enabled           bool     `json:"enabled"`
	ThresholdPercent  *float64 `json:"threshold_percent"`
	TargetConcurrency int      `json:"target_concurrency"`
	Models            []string `json:"models"`
}

func DefaultOAuthDegradationConfig() OAuthDegradationConfig {
	// Bare "luna" is unresolved. Never silently choose a version for it.
	return OAuthDegradationConfig{TargetConcurrency: 10, Models: []string{"gpt-5.6-terra", "gpt-6.1-terra", "gpt-6-luna"}}
}
func (c OAuthDegradationConfig) Validate() error {
	bad := func() error {
		return infraerrors.BadRequest("OAUTH_DEGRADATION_INVALID", "set a finite degradation threshold (0–100), target concurrency (1–10000), and exact model IDs")
	}
	if c.Enabled && c.ThresholdPercent == nil {
		return bad()
	}
	if p := c.ThresholdPercent; p != nil && (math.IsNaN(*p) || math.IsInf(*p, 0) || *p < 0 || *p > 100) {
		return bad()
	}
	if c.TargetConcurrency < 1 || c.TargetConcurrency > 10000 {
		return bad()
	}
	if err := validateOAuthSelectedModels(c.Models); err != nil {
		return err
	}
	return nil
}
func (c OAuthDegradationConfig) Triggered(m AccountPerformance) bool {
	p := m.DegradationPercent
	return c.Validate() == nil && c.Enabled && c.ThresholdPercent != nil && m.QualitySamples > 0 && p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0 && *p <= 100 && *p >= *c.ThresholdPercent
}

type OAuthDegradationState struct {
	Revision           string    `json:"revision"`
	Models             []string  `json:"models"`
	ConcurrencyCap     int       `json:"concurrency_cap"`
	ThresholdPercent   float64   `json:"threshold_percent"`
	DegradationPercent float64   `json:"degradation_percent"`
	QualitySamples     int       `json:"quality_samples"`
	WindowStart        time.Time `json:"window_start"`
	WindowEnd          time.Time `json:"window_end"`
	AppliedAt          time.Time `json:"applied_at"`
}

func (a *Account) HasOAuthDegradationAction() bool {
	_, ok := a.Extra[OAuthDegradationActionExtraKey]
	return ok
}
func (a *Account) degradationState() (OAuthDegradationState, bool) {
	var state OAuthDegradationState
	raw, err := json.Marshal(a.Extra[OAuthDegradationActionExtraKey])
	if err != nil || json.Unmarshal(raw, &state) != nil || state.ConcurrencyCap < 1 {
		return state, false
	}
	return state, true
}
func (a *Account) oauthDegradationAllows(model string) bool {
	if !a.HasOAuthDegradationAction() {
		return true
	}
	state, ok := a.degradationState()
	return ok && slices.Contains(state.Models, model)
}

// Other automated recovery policies may tighten the cap, never restore it.
func (a *Account) LimitOAuthDegradationConcurrency(proposed int) int {
	if !a.HasOAuthDegradationAction() {
		return proposed
	}
	state, ok := a.degradationState()
	if !ok {
		return min(proposed, a.Concurrency)
	}
	return min(proposed, state.ConcurrencyCap)
}

type oauthDegradationRepository interface {
	OAuthDegradationIDs(context.Context, int64, int) ([]int64, error)
	EvaluateOAuthDegradation(context.Context, int64, time.Time) error
}

func (s *AccountOpsService) evaluateOAuthDegradation(ctx context.Context) {
	repo, ok := s.accounts.(oauthDegradationRepository)
	if !ok {
		return
	}
	// The repository reads and locks the live policy, independently of the
	// request-upgrade switch, performance monitors and this process's cache.
	query, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ids, err := repo.OAuthDegradationIDs(query, s.degradationCursor, 100)
	if err != nil {
		slog.Error("oauth_degradation_list", "error", err)
		return
	}
	if len(ids) == 0 {
		s.degradationCursor = 0
		return
	}
	for _, id := range ids {
		if query.Err() != nil {
			return
		}
		s.degradationCursor = id
		if err := repo.EvaluateOAuthDegradation(query, id, time.Now().UTC()); err != nil {
			slog.Error("oauth_degradation_evaluate", "account_id", id, "error", err)
		}
	}
}
