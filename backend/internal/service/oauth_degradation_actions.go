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
	LimitConcurrency  *bool    `json:"limit_concurrency"`
	LimitModels       *bool    `json:"limit_models"`
	LimitGroups       bool     `json:"limit_groups"`
	GroupIDs          []int64  `json:"group_ids"`
	ThresholdPercent  *float64 `json:"threshold_percent"`
	TargetConcurrency int      `json:"target_concurrency"`
	Models            []string `json:"models"`
}

func DefaultOAuthDegradationConfig() OAuthDegradationConfig {
	// Bare "luna" is unresolved. Never silently choose a version for it.
	return OAuthDegradationConfig{TargetConcurrency: 10, Models: []string{"gpt-5.6-terra", "gpt-6.1-terra", "gpt-6-luna"}}
}

// Missing action flags retain the original policy for existing settings.
func (c OAuthDegradationConfig) RestrictsConcurrency() bool {
	return c.LimitConcurrency == nil || *c.LimitConcurrency
}
func (c OAuthDegradationConfig) RestrictsModels() bool {
	return c.LimitModels == nil || *c.LimitModels
}
func (c OAuthDegradationConfig) HasActions() bool {
	return c.RestrictsConcurrency() || c.RestrictsModels() || c.LimitGroups
}
func (c OAuthDegradationConfig) Validate() error {
	bad := func() error {
		return infraerrors.BadRequest("OAUTH_DEGRADATION_INVALID", "select at least one degradation action, a finite threshold (0–100), and valid settings for each selected action")
	}
	if c.Enabled && (c.ThresholdPercent == nil || !c.HasActions()) {
		return bad()
	}
	if p := c.ThresholdPercent; p != nil && (math.IsNaN(*p) || math.IsInf(*p, 0) || *p < 0 || *p > 100) {
		return bad()
	}
	if c.RestrictsConcurrency() && (c.TargetConcurrency < 1 || c.TargetConcurrency > 10000) {
		return bad()
	}
	if c.RestrictsModels() {
		if err := validateOAuthSelectedModels(c.Models); err != nil {
			return err
		}
	}
	if c.LimitGroups {
		if len(c.GroupIDs) == 0 || len(c.GroupIDs) > 100 {
			return bad()
		}
		seen := make(map[int64]bool, len(c.GroupIDs))
		for _, id := range c.GroupIDs {
			if id <= 0 || seen[id] {
				return bad()
			}
			seen[id] = true
		}
	}
	return nil
}
func (c OAuthDegradationConfig) Triggered(m AccountPerformance) bool {
	p := m.DegradationPercent
	return c.Validate() == nil && c.Enabled && c.ThresholdPercent != nil && m.QualitySamples > 0 && p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0 && *p <= 100 && *p >= *c.ThresholdPercent
}

type OAuthDegradationState struct {
	Revision           string    `json:"revision"`
	LimitConcurrency   *bool     `json:"limit_concurrency"`
	LimitModels        *bool     `json:"limit_models"`
	LimitGroups        bool      `json:"limit_groups"`
	GroupIDs           []int64   `json:"group_ids"`
	RemovedGroupIDs    []int64   `json:"removed_group_ids"`
	Models             []string  `json:"models"`
	ConcurrencyCap     int       `json:"concurrency_cap"`
	ThresholdPercent   float64   `json:"threshold_percent"`
	DegradationPercent float64   `json:"degradation_percent"`
	QualitySamples     int       `json:"quality_samples"`
	WindowStart        time.Time `json:"window_start"`
	WindowEnd          time.Time `json:"window_end"`
	AppliedAt          time.Time `json:"applied_at"`
}

func (s OAuthDegradationState) RestrictsConcurrency() bool {
	return s.LimitConcurrency == nil || *s.LimitConcurrency
}
func (s OAuthDegradationState) RestrictsModels() bool {
	return s.LimitModels == nil || *s.LimitModels
}

func (a *Account) HasOAuthDegradationAction() bool {
	_, ok := a.Extra[OAuthDegradationActionExtraKey]
	return ok
}
func (a *Account) degradationState() (OAuthDegradationState, bool) {
	var state OAuthDegradationState
	raw, err := json.Marshal(a.Extra[OAuthDegradationActionExtraKey])
	if err != nil || json.Unmarshal(raw, &state) != nil || (state.RestrictsModels() && state.Models == nil) {
		return state, false
	}
	return state, true
}

// HasOAuthDegradationModelLimit also keeps malformed legacy markers closed.
func (a *Account) HasOAuthDegradationModelLimit() bool {
	if !a.HasOAuthDegradationAction() {
		return false
	}
	state, ok := a.degradationState()
	return !ok || state.RestrictsModels()
}

// FilterOAuthDegradationGroups preserves ordinary bindings while preventing a
// stale edit or automated group move from reintroducing a disabled group.
func (a *Account) FilterOAuthDegradationGroups(proposed []int64) []int64 {
	state, ok := a.degradationState()
	if !ok || !state.LimitGroups {
		return proposed
	}
	allowed := make([]int64, 0, len(proposed))
	for _, id := range proposed {
		if !slices.Contains(state.GroupIDs, id) {
			allowed = append(allowed, id)
		}
	}
	return allowed
}

func (a *Account) oauthDegradationAllows(model string) bool {
	if !a.HasOAuthDegradationAction() {
		return true
	}
	state, ok := a.degradationState()
	return ok && (!state.RestrictsModels() || slices.Contains(state.Models, model))
}

// Other automated recovery policies may tighten the cap, never restore it.
func (a *Account) LimitOAuthDegradationConcurrency(proposed int) int {
	if !a.HasOAuthDegradationAction() {
		return proposed
	}
	// Never turn a finite limit into the project's <=0 (unlimited) value,
	// nor undo a later manual reduction while another policy recovers.
	ceiling := a.Concurrency
	state, ok := a.degradationState()
	if ok && !state.RestrictsConcurrency() {
		return proposed
	}
	if ok && state.ConcurrencyCap > 0 {
		if ceiling <= 0 {
			ceiling = state.ConcurrencyCap
		} else {
			ceiling = min(ceiling, state.ConcurrencyCap)
		}
	}
	if ceiling <= 0 {
		return ceiling
	}
	if proposed <= 0 {
		return ceiling
	}
	return min(proposed, ceiling)
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
