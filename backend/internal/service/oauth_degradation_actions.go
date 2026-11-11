package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
)

const OAuthDegradationActionExtraKey = "oauth_degradation_action"

const OAuthDegradationMinHTTPResponses int64 = 20

// Nil fields preserve default-on conditions for saved configurations.
type OAuthDegradationConfig struct {
	ProbeEnabled                     *bool    `json:"probe_enabled"`
	Upstream429Enabled               *bool    `json:"upstream_429_enabled"`
	UpstreamErrorThresholdPercent    *float64 `json:"upstream_error_threshold_percent"`
	Upstream429ShareThresholdPercent *float64 `json:"upstream_429_share_threshold_percent"`
	Enabled                          bool     `json:"enabled"`
	LimitConcurrency                 *bool    `json:"limit_concurrency"`
	LimitModels                      *bool    `json:"limit_models"`
	LimitGroups                      bool     `json:"limit_groups"`
	GroupIDs                         []int64  `json:"group_ids"`
	ThresholdPercent                 *float64 `json:"threshold_percent"`
	TargetConcurrency                int      `json:"target_concurrency"`
	Models                           []string `json:"models"`
}

func DefaultOAuthDegradationConfig() OAuthDegradationConfig {
	return OAuthDegradationConfig{TargetConcurrency: 10, Models: []string{"gpt-5.6-luna", "gpt-5.6-terra", "gpt-6-luna", "gpt-6.1-terra"}}
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
func (c OAuthDegradationConfig) UsesProbe() bool { return c.ProbeEnabled == nil || *c.ProbeEnabled }
func (c OAuthDegradationConfig) UsesUpstream429() bool {
	return c.Upstream429Enabled == nil || *c.Upstream429Enabled
}
func (c OAuthDegradationConfig) HTTPErrorThreshold() float64 {
	if c.UpstreamErrorThresholdPercent != nil {
		return *c.UpstreamErrorThresholdPercent
	}
	return 80
}
func (c OAuthDegradationConfig) HTTP429ShareThreshold() float64 {
	if c.Upstream429ShareThresholdPercent != nil {
		return *c.Upstream429ShareThresholdPercent
	}
	return 80
}
func (c OAuthDegradationConfig) Validate() error {
	bad := func() error {
		return infraerrors.BadRequest("OAUTH_DEGRADATION_INVALID", "select at least one condition and action; probe threshold must be 0–100 and HTTP thresholds greater than 0 and at most 100")
	}
	if c.Enabled && ((c.UsesProbe() && c.ThresholdPercent == nil) || (!c.UsesProbe() && !c.UsesUpstream429()) || !c.HasActions()) {
		return bad()
	}
	if p := c.ThresholdPercent; p != nil && (math.IsNaN(*p) || math.IsInf(*p, 0) || *p < 0 || *p > 100) {
		return bad()
	}
	for _, p := range []float64{c.HTTPErrorThreshold(), c.HTTP429ShareThreshold()} {
		if math.IsNaN(p) || math.IsInf(p, 0) || p <= 0 || p > 100 {
			return bad()
		}
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
func (c OAuthDegradationConfig) Triggered(m AccountPerformance) bool { return c.TriggerReason(m) != "" }

// HTTP pressure is independent of completed probe rounds. Never invent a probe
// sample or a wrong-answer verdict when the upstream could not answer.
func (c OAuthDegradationConfig) TriggerReason(m AccountPerformance) string {
	if c.Validate() != nil || !c.Enabled {
		return ""
	}
	p := m.DegradationPercent
	if c.UsesProbe() && c.ThresholdPercent != nil && m.QualitySamples > 0 && p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0) && *p >= 0 && *p <= 100 && *p >= *c.ThresholdPercent {
		return "probe_degradation"
	}
	e := m.UpstreamError
	if c.UsesUpstream429() && e.Source == "process_local_http_responses" && e.Total >= OAuthDegradationMinHTTPResponses && e.Failures > 0 && e.Failures <= e.Total && e.RateLimited > 0 && e.RateLimited <= e.Failures &&
		100*float64(e.Failures)/float64(e.Total) >= c.HTTPErrorThreshold() && 100*float64(e.RateLimited)/float64(e.Failures) >= c.HTTP429ShareThreshold() {
		return "upstream_429"
	}
	return ""
}

type OAuthDegradationState struct {
	TriggerReason                    string                  `json:"trigger_reason,omitempty"`
	UpstreamError                    *upstreamstats.Evidence `json:"upstream_error,omitempty"`
	UpstreamErrorThresholdPercent    float64                 `json:"upstream_error_threshold_percent,omitempty"`
	Upstream429ShareThresholdPercent float64                 `json:"upstream_429_share_threshold_percent,omitempty"`
	Revision                         string                  `json:"revision"`
	NativeControls                   bool                    `json:"native_controls,omitempty"`
	LimitConcurrency                 *bool                   `json:"limit_concurrency"`
	LimitModels                      *bool                   `json:"limit_models"`
	LimitGroups                      bool                    `json:"limit_groups"`
	GroupIDs                         []int64                 `json:"group_ids"`
	RemovedGroupIDs                  []int64                 `json:"removed_group_ids"`
	Models                           []string                `json:"models"`
	ConcurrencyCap                   int                     `json:"concurrency_cap"`
	ThresholdPercent                 float64                 `json:"threshold_percent"`
	DegradationPercent               float64                 `json:"degradation_percent"`
	QualitySamples                   int                     `json:"quality_samples"`
	WindowStart                      time.Time               `json:"window_start"`
	WindowEnd                        time.Time               `json:"window_end"`
	AppliedAt                        time.Time               `json:"applied_at"`

	// Admission preserves the current native switch after an unrepresentable
	// model scope paused it. This audit flag never overrides a manual resume.
	NativeSchedulingPaused bool `json:"native_scheduling_paused,omitempty"`
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

// Degradation history does not override current native group bindings.
func (a *Account) FilterOAuthDegradationGroups(proposed []int64) []int64 {
	return proposed
}

// Automated recovery must not undo the current native concurrency setting.
// Explicit account edits may raise it; the audit snapshot is not a ceiling.
func (a *Account) LimitOAuthDegradationConcurrency(proposed int) int {
	var state AutoConfigConcurrencyState
	raw, err := json.Marshal(a.Extra[AutoConfigConcurrencyExtraKey])
	if err != nil || json.Unmarshal(raw, &state) != nil || !state.DegradationHold {
		return proposed
	}
	ceiling := a.Concurrency
	if ceiling <= 0 {
		return proposed
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
