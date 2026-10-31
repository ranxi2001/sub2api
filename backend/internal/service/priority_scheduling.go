package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sync"
	"time"
)

const prioritySchedulingSettingKey = "priority_scheduling_v1"

const (
	PriorityLatencyDeprioritize = "deprioritize"
	PriorityLatencyStop         = "stop"
)

// PrioritySchedulingConfig controls OpenAI text routing and optional scoped
// new-request stops. A stopped owner is rejected rather than reassigned.
// Account eligibility, continuity, profit gates and concurrency acquisition stay authoritative.
type PrioritySchedulingConfig struct {
	ErrorRateWeight            float64  `json:"error_rate_weight"`
	ErrorRateWindowMinutes     int      `json:"error_rate_window_minutes"`
	ErrorRatePriorSamples      int      `json:"error_rate_prior_samples"`
	OAuthQuotaPriority         bool     `json:"oauth_quota_priority"`
	OAuthQuotaThreshold        int      `json:"oauth_quota_threshold"`
	BalanceProtocols           bool     `json:"balance_protocols"`
	Enabled                    bool     `json:"enabled"`
	Mode                       string   `json:"mode"`
	GroupIDs                   []int64  `json:"group_ids"`
	Models                     []string `json:"models"`
	WindowMinutes              int      `json:"window_minutes"`
	MinSamples                 int      `json:"min_samples"`
	LatencyPercentile          int      `json:"latency_percentile"`
	LatencyMeasurement         string   `json:"latency_measurement"`
	RecoveryExplorationPercent int      `json:"recovery_exploration_percent"`
	TargetTTFTMs               int      `json:"target_ttft_ms"`
	LatencyPenaltyMode         string   `json:"latency_penalty_mode"`
	LatencyPenaltyMinutes      int      `json:"latency_penalty_minutes"`
	MaxLoadPercent             int      `json:"max_load_percent"`
	MinQualityPercent          int      `json:"min_quality_percent"`
	QualityMaxAgeHours         int      `json:"quality_max_age_hours"`
	QualityWeight              float64  `json:"quality_weight"`
	LatencyWeight              float64  `json:"latency_weight"`
	LoadWeight                 float64  `json:"load_weight"`
	CostWeight                 float64  `json:"cost_weight"`
}

func DefaultPrioritySchedulingConfig() PrioritySchedulingConfig {
	return PrioritySchedulingConfig{ErrorRateWeight: 25, ErrorRateWindowMinutes: 10, ErrorRatePriorSamples: 5, OAuthQuotaThreshold: 90, BalanceProtocols: true, Mode: "balanced", GroupIDs: []int64{}, Models: []string{}, WindowMinutes: 60, MinSamples: 5, TargetTTFTMs: 15000, LatencyPercentile: 90, LatencyMeasurement: "legacy_forward", LatencyPenaltyMode: PriorityLatencyDeprioritize, LatencyPenaltyMinutes: 10, MaxLoadPercent: 80, MinQualityPercent: 90, QualityMaxAgeHours: 24, QualityWeight: 30, LatencyWeight: 25, LoadWeight: 25, CostWeight: 20}
}

func ValidatePrioritySchedulingConfig(c PrioritySchedulingConfig) error {
	if math.IsNaN(c.ErrorRateWeight) || math.IsInf(c.ErrorRateWeight, 0) || c.ErrorRateWeight < 0 || c.ErrorRateWeight > 100 || c.ErrorRateWindowMinutes < 1 || c.ErrorRateWindowMinutes > 60 || c.ErrorRatePriorSamples < 1 || c.ErrorRatePriorSamples > 1000 {
		return errors.New("error rate parameters outside allowed range")
	}
	if !slices.Contains([]int{50, 70, 90, 95}, c.LatencyPercentile) {
		return errors.New("latency percentile must be 50, 70, 90 or 95")
	}
	if c.LatencyMeasurement != "legacy_forward" && c.LatencyMeasurement != "attributed_v1" {
		return errors.New("invalid latency measurement")
	}
	if c.RecoveryExplorationPercent < 0 || c.RecoveryExplorationPercent > 10 {
		return errors.New("recovery exploration must be between 0 and 10 percent")
	}
	if c.LatencyPenaltyMode != "" && c.LatencyPenaltyMode != PriorityLatencyDeprioritize && c.LatencyPenaltyMode != PriorityLatencyStop {
		return errors.New("invalid latency penalty mode")
	}
	if c.LatencyPenaltyMinutes < 0 || c.LatencyPenaltyMinutes > 1440 {
		return errors.New("latency penalty must be between 0 and 1440 minutes")
	}
	if c.OAuthQuotaThreshold < 1 || c.OAuthQuotaThreshold > 100 {
		return errors.New("OAuth quota threshold must be between 1 and 100")
	}
	if !slices.Contains([]string{"experience", "balanced", "profit", "custom"}, c.Mode) {
		return errors.New("invalid scheduling mode")
	}
	if c.WindowMinutes < 5 || c.WindowMinutes > 1440 || c.MinSamples < 1 || c.MinSamples > 1000 || c.TargetTTFTMs < 100 || c.TargetTTFTMs > 120000 || c.MaxLoadPercent < 10 || c.MaxLoadPercent > 100 || c.MinQualityPercent < 0 || c.MinQualityPercent > 100 || c.QualityMaxAgeHours < 1 || c.QualityMaxAgeHours > 168 {
		return errors.New("scheduling thresholds outside allowed range")
	}
	if len(c.GroupIDs) > 100 || len(c.Models) > 100 {
		return errors.New("at most 100 groups or models allowed")
	}
	for _, id := range c.GroupIDs {
		if id <= 0 {
			return errors.New("group IDs must be positive")
		}
	}
	for _, model := range c.Models {
		if len(model) == 0 || len(model) > 200 {
			return errors.New("model names must contain 1–200 bytes")
		}
	}
	sum := 0.0
	for _, w := range []float64{c.QualityWeight, c.LatencyWeight, c.LoadWeight, c.CostWeight} {
		if math.IsNaN(w) || math.IsInf(w, 0) || w < 0 || w > 100 {
			return errors.New("weights must be between 0 and 100")
		}
		sum += w
	}
	if sum <= 0 {
		return errors.New("at least one weight must be positive")
	}
	return nil
}

func (c PrioritySchedulingConfig) applies(group *int64, model string) bool {
	return c.Enabled && (len(c.GroupIDs) == 0 || group != nil && slices.Contains(c.GroupIDs, *group)) && (len(c.Models) == 0 || slices.Contains(c.Models, model))
}

func (c PrioritySchedulingConfig) weights() [4]float64 {
	switch c.Mode {
	case "experience":
		return [4]float64{40, 35, 20, 5}
	case "profit":
		return [4]float64{20, 15, 20, 45}
	case "balanced":
		return [4]float64{30, 25, 25, 20}
	default:
		return [4]float64{c.QualityWeight, c.LatencyWeight, c.LoadWeight, c.CostWeight}
	}
}

type priorityConfigCache struct {
	saveMu     sync.Mutex
	mu         sync.Mutex
	config     PrioritySchedulingConfig
	expires    time.Time
	refreshing bool
	revision   uint64
}

func (s *SettingService) GetPrioritySchedulingConfig(ctx context.Context) (PrioritySchedulingConfig, error) {
	c := DefaultPrioritySchedulingConfig()
	raw, err := s.settingRepo.GetValue(ctx, prioritySchedulingSettingKey)
	if errors.Is(err, ErrSettingNotFound) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	// Existing stored configurations opt in explicitly; fresh installations use 25.
	c.ErrorRateWeight = 0
	if err = json.Unmarshal([]byte(raw), &c); err != nil {
		return c, err
	}
	return c, ValidatePrioritySchedulingConfig(c)
}
func (s *SettingService) SavePrioritySchedulingConfig(ctx context.Context, c PrioritySchedulingConfig) error {
	if c.LatencyPenaltyMode == "" {
		c.LatencyPenaltyMode = PriorityLatencyDeprioritize
	}
	if err := ValidatePrioritySchedulingConfig(c); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	cache := &s.prioritySchedulingConfig
	cache.saveMu.Lock()
	defer cache.saveMu.Unlock()
	if err = s.settingRepo.Set(ctx, prioritySchedulingSettingKey, string(raw)); err != nil {
		return err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.revision++
	cache.config = c
	cache.expires = time.Now().Add(5 * time.Second)
	return nil
}

// No database access on the request goroutine, including a cold start. A failed
// refresh disables the feature; other processes poll settings on a 5s interval.
func (s *SettingService) prioritySchedulingRuntimeConfig() PrioritySchedulingConfig {
	if s == nil || s.settingRepo == nil {
		return DefaultPrioritySchedulingConfig()
	}
	cache := &s.prioritySchedulingConfig
	cache.mu.Lock()
	defer cache.mu.Unlock()
	now := time.Now()
	if !cache.refreshing && now.After(cache.expires) {
		cache.refreshing = true
		revision := cache.revision
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			c, err := s.GetPrioritySchedulingConfig(ctx)
			cache.mu.Lock()
			defer cache.mu.Unlock()
			cache.refreshing = false
			if cache.revision != revision {
				return
			}
			if err != nil {
				c = DefaultPrioritySchedulingConfig()
			}
			cache.config = c
			cache.expires = time.Now().Add(5 * time.Second)
		}()
	}
	if now.After(cache.expires.Add(5 * time.Second)) {
		return DefaultPrioritySchedulingConfig()
	}
	return cache.config
}

func (s *OpenAIGatewayService) prioritySchedulingRuntimeConfig() PrioritySchedulingConfig {
	if s == nil {
		return DefaultPrioritySchedulingConfig()
	}
	return s.settingService.prioritySchedulingRuntimeConfig()
}

type PrioritySchedulingSignal struct {
	SelectedTTFTMs         *float64   `json:"selected_ttft_ms"`
	LatestBadLatencyAt     *time.Time `json:"latest_bad_latency_at,omitempty"`
	LatestLatencyAt        *time.Time `json:"latest_latency_at,omitempty"`
	LatestQualityPassed    *bool      `json:"latest_quality_passed,omitempty"`
	P50TTFTMs              float64    `json:"p50_ttft_ms"`
	P70TTFTMs              float64    `json:"p70_ttft_ms"`
	P90TTFTMs              float64    `json:"p90_ttft_ms"`
	P95TTFTMs              float64    `json:"p95_ttft_ms"`
	P99TTFTMs              float64    `json:"p99_ttft_ms"`
	ExperienceP90Ms        float64    `json:"experience_p90_ms"`
	Revenue                float64    `json:"revenue"`
	BaseCost               float64    `json:"-"`
	LatencyPercentile      int        `json:"latency_percentile"`
	LatencyMeasurement     string     `json:"latency_measurement"`
	LatencyEvidenceVersion int        `json:"latency_evidence_version"`
	LatencyUnknownSamples  int        `json:"latency_unknown_samples"`
	ExperienceSamples      int        `json:"experience_samples"`
	ProfitSamples          int        `json:"profit_samples"`
	Samples                int        `json:"samples"`
	SlowSamples            int        `json:"slow_samples"`
	ErrorSamples           int        `json:"error_samples"`
	UpstreamErrorRate      float64    `json:"upstream_error_rate"`
	QualityPassed          int        `json:"quality_passed"`
	QualitySamples         int        `json:"quality_samples"`
}

type PrioritySchedulingQuery struct {
	ObservationEffort  *string
	LatencyPercentile  int
	LatencyMeasurement string
	TargetTTFTMs       int
	AccountIDs         []int64
	Model              string // public requested model in persisted usage
	QualityModel       string // routing model used by account quality probes; defaults to Model
	GroupID            *int64
	UsageUntil         time.Time
	UsageSince         time.Time
	QualitySince       time.Time
}
type PrioritySchedulingSignalReader interface {
	ReadPrioritySchedulingSignals(context.Context, PrioritySchedulingQuery) (map[int64]PrioritySchedulingSignal, error)
}

type PrioritySchedulingScore struct {
	PriorityErrorEvidence
	ErrorPenalty        float64    `json:"error_penalty"`
	BaseScore           float64    `json:"base_score"`
	LatencyPenaltyUntil *time.Time `json:"latency_penalty_until,omitempty"`
	LatencyStopped      bool       `json:"latency_stopped"`
	LatencyRecovery     bool       `json:"latency_recovery,omitempty"`
	OAuthQuotaRole      string     `json:"oauth_quota_role,omitempty"`
	HistoryStatus       string     `json:"history_status"`
	CapacityBand        int        `json:"capacity_band"`
	BoundGroups         int        `json:"bound_groups"`
	SelectionWeight     float64    `json:"selection_weight"`
	ExplorationEligible bool       `json:"exploration_eligible"`
	TheoreticalCost     float64    `json:"theoretical_cost"`
	Profit              *float64   `json:"profit"`
	Margin              *float64   `json:"margin"`
	EconomicsSource     string     `json:"economics_source"`
	Priority            int        `json:"priority"`
	Concurrency         int        `json:"concurrency"`
	LoadFactor          int        `json:"load_factor"`
	AccountID           int64      `json:"account_id"`
	AccountName         string     `json:"account_name"`
	Score               float64    `json:"score"`
	Tier                string     `json:"tier"`
	Reasons             []string   `json:"reasons"`
	Rate                *float64   `json:"rate"`
	LoadPercent         *int       `json:"load_percent"`
	Waiting             int        `json:"waiting"`
	PrioritySchedulingSignal
}
type PrioritySchedulingSnapshot struct {
	LatencyPercentile  int                       `json:"latency_percentile"`
	LatencyMeasurement string                    `json:"latency_measurement"`
	LatencyPenaltyMode string                    `json:"latency_penalty_mode"`
	LatencyFallback    bool                      `json:"latency_fallback"`
	MinSamples         int                       `json:"min_samples"`
	WindowMinutes      int                       `json:"window_minutes"`
	HistoryWindowEnd   *time.Time                `json:"history_window_end,omitempty"`
	ConfigRevision     string                    `json:"config_revision"`
	EvaluatedAt        time.Time                 `json:"evaluated_at"`
	HistoryStatus      string                    `json:"history_status"`
	HistoryObservedAt  *time.Time                `json:"history_observed_at,omitempty"`
	HistoryError       string                    `json:"history_error,omitempty"`
	HistoryRefreshing  bool                      `json:"history_refreshing"`
	SelectionPolicy    string                    `json:"selection_policy"`
	At                 time.Time                 `json:"at"`
	Model              string                    `json:"model"`
	GroupID            *int64                    `json:"group_id"`
	Mode               string                    `json:"mode"`
	HistoryReady       bool                      `json:"history_ready"`
	Candidates         []PrioritySchedulingScore `json:"candidates"`
}

func scorePriorityCandidate(c PrioritySchedulingConfig, item openAIAccountCandidateScore, signal PrioritySchedulingSignal, now time.Time) PrioritySchedulingScore {
	// Only unversioned legacy P90 inputs may use the old field. Attributed or
	// non-P90 evidence must never silently fall back to a different population.
	if signal.LatencyEvidenceVersion == 0 && c.LatencyMeasurement == "legacy_forward" && c.LatencyPercentile == 90 {
		if signal.Samples > 0 && signal.P90TTFTMs > 0 {
			value := signal.P90TTFTMs
			signal.SelectedTTFTMs = &value
		}
		signal.LatencyPercentile = 90
		signal.LatencyMeasurement = "legacy_forward"
	} else if signal.LatencyPercentile != c.LatencyPercentile || signal.LatencyMeasurement != c.LatencyMeasurement {
		signal.Samples = 0
		signal.SelectedTTFTMs = nil
		signal.LatencyPercentile = c.LatencyPercentile
		signal.LatencyMeasurement = c.LatencyMeasurement
	}
	selectedLatency := 0.0
	if signal.SelectedTTFTMs != nil {
		selectedLatency = *signal.SelectedTTFTMs
	}
	rate := item.account.CostMultiplier()
	theoreticalCost := signal.BaseCost * rate
	out := PrioritySchedulingScore{Rate: &rate, TheoreticalCost: theoreticalCost, AccountID: item.account.ID, AccountName: item.account.Name, Priority: openAIAccountSchedulingPriority(item.account), Concurrency: item.account.Concurrency, LoadFactor: item.account.EffectiveLoadFactor(), Tier: "eligible", Reasons: []string{}, PrioritySchedulingSignal: signal}
	if item.loadInfo != nil {
		out.Waiting = max(0, item.loadInfo.WaitingCount)
	}
	quality, latency, load, cost := 0.5, 0.5, 0.5, 0.5
	degraded, unknown := false, false
	if signal.QualitySamples > 0 {
		quality = clamp01(float64(signal.QualityPassed) / float64(signal.QualitySamples))
		if quality*100 < float64(c.MinQualityPercent) {
			degraded = true
			out.Reasons = append(out.Reasons, "quality_below_target")
		}
	} else {
		unknown = true
		out.Reasons = append(out.Reasons, "quality_unknown")
	}
	if signal.Samples >= c.MinSamples && selectedLatency > 0 {
		latency = 1 / (1 + selectedLatency/float64(c.TargetTTFTMs))
		if selectedLatency > float64(c.TargetTTFTMs) {
			// Renew only on a genuinely bad sample, never arbitrary new traffic.
			// Missing timestamps retain the penalty rather than invent recovery.
			badAt := signal.LatestBadLatencyAt
			if c.LatencyPenaltyMinutes > 0 && badAt != nil {
				until := badAt.Add(time.Duration(c.LatencyPenaltyMinutes) * time.Minute)
				out.LatencyPenaltyUntil = &until
				out.LatencyRecovery = !now.Before(until)
			}
			if out.LatencyRecovery {
				unknown = true
				out.Reasons = append(out.Reasons, "latency_recovery")
			} else {
				degraded = true
				out.Reasons = append(out.Reasons, "latency_above_target")
				out.LatencyStopped = c.LatencyPenaltyMode == PriorityLatencyStop
			}
		}
	} else {
		unknown = true
		out.Reasons = append(out.Reasons, "latency_insufficient")
	}
	if item.loadKnown && item.loadInfo != nil {
		percent := 0
		if item.account.Concurrency > 0 {
			percent = int(100 * float64(item.loadInfo.CurrentConcurrency) / float64(item.account.Concurrency))
		}
		out.LoadPercent = &percent
		// A configured load factor may express a routing preference, but must
		// not make a nearly full account look idle to this capacity policy.
		load = (1 - clamp01(float64(item.loadInfo.CurrentConcurrency)/float64(max(1, item.account.Concurrency)))) / (1 + float64(max(0, item.loadInfo.WaitingCount)))
		if percent >= c.MaxLoadPercent || item.loadInfo.WaitingCount > 0 {
			degraded = true
			out.Reasons = append(out.Reasons, "busy")
		}
	} else {
		unknown = true
		out.Reasons = append(out.Reasons, "load_unknown")
	}
	if signal.ProfitSamples >= c.MinSamples && signal.Revenue >= 0 && theoreticalCost >= 0 && signal.Revenue+theoreticalCost > 0 {
		// Round the multiplication before subtracting, matching the displayed
		// cost even on architectures that fuse multiply/subtract. Decimal money
		// can still differ by a few ulps at break-even; that is not loss evidence.
		profit := signal.Revenue - float64(theoreticalCost)
		if math.Abs(profit) <= 1e-12*math.Max(signal.Revenue, theoreticalCost) {
			profit = 0
		}
		out.Profit = &profit
		if signal.Revenue > 0 {
			margin := profit / signal.Revenue
			out.Margin = &margin
		}
		// Revenue/(revenue+cost) maps break-even to 0.5 and zero-cost
		// revenue to 1, retaining loss information without unstable division
		// by a near-zero margin. Use sums, not a mean of per-request margins.
		cost = signal.Revenue / (signal.Revenue + theoreticalCost)
		out.EconomicsSource = "usage"
		if profit < 0 {
			degraded = true
			out.Reasons = append(out.Reasons, "historical_loss")
		}
	} else if item.account.IsOpenAIApiKey() && out.Rate != nil {
		cost = 1 / (1 + *out.Rate)
		out.EconomicsSource = "rate"
	} else {
		unknown = true
		out.EconomicsSource = "unknown"
		out.Reasons = append(out.Reasons, "profit_insufficient")
	}
	// Realtime failures are an additional penalty, never inferred from zero-cost logs.
	if c.ErrorRateWeight == 0 && item.errorRate > 0.2 {
		degraded = true
		out.Reasons = append(out.Reasons, "recent_errors")
	}
	w := c.weights()
	sum := w[0] + w[1] + w[2] + w[3]
	out.BaseScore = 100 * (w[0]*quality + w[1]*latency + w[2]*load + w[3]*cost) / sum
	out.PriorityErrorEvidence = item.priorityErrors
	out.ErrorPenalty = 1 - clamp01(item.errorRate)
	if c.ErrorRateWeight > 0 {
		out.ErrorPenalty = priorityErrorPenalty(c, item.priorityErrors)
	}
	out.Score = out.BaseScore * out.ErrorPenalty
	if degraded {
		out.Tier = "degraded"
	} else if unknown {
		out.Tier = "insufficient"
	}
	return out
}

func (s *defaultOpenAIAccountScheduler) applyPriorityScheduling(req OpenAIAccountScheduleRequest, plan *openAIAccountLoadPlan) bool {
	c := s.service.prioritySchedulingRuntimeConfig()
	if req.Platform != PlatformOpenAI || req.RequiredImageCapability != "" || !req.UseUpstreamTokenCost || !priorityApplies(c, req) {
		return false
	}
	history := s.service.priorityHistory(req, c, plan.candidates)
	now := time.Now()
	for i := range plan.candidates {
		plan.candidates[i].priorityErrors = s.priorityErrorEvidence(req, c, plan.candidates[i].account.ID, now)
		applyPriorityCandidate(c, &plan.candidates[i], history.signals[plan.candidates[i].account.ID], now)
	}
	plan.priorityScheduling = true
	plan.topK = len(plan.candidates)
	plan.includeOverflowFallback = true
	plan.selectionOrder = s.buildOpenAISelectionOrder(req, *plan)
	s.recordPrioritySnapshot(req, c, plan.selectionOrder, now)
	return true
}

func (s *defaultOpenAIAccountScheduler) recordPrioritySnapshot(req OpenAIAccountScheduleRequest, c PrioritySchedulingConfig, pool []openAIAccountCandidateScore, now time.Time) {
	input := newPrioritySnapshotInput(req, c, pool, now)
	if req.priorityLatencyPool != nil && req.priorityLatencyPool.fallbackActive() {
		input.fallback = true
	} else {
		input.fallback = priorityPoolNeedsFallback(pool)
	}
	state := &s.service.priorityScheduling
	state.mu.Lock()
	// A slower earlier selection must not replace a newer observation.
	if state.latestInput == nil || !state.latestInput.at.After(now) {
		state.latestInput = input
	}
	state.mu.Unlock()
}

// A pool needs an explicit fallback when it contains no normal candidate at
// all. Recovery and actively penalized accounts remain selectable, but only
// after every normal candidate has lost its slot race. The dashboard must say
// so: otherwise a recovery-period account looks like ordinary capacity.
func priorityPoolNeedsFallback(pool []openAIAccountCandidateScore) bool {
	if len(pool) == 0 {
		return false
	}
	for _, item := range pool {
		if !item.priorityUnhealthy && !item.priorityLatencyPenalty && !item.priorityLatencyRecovery {
			return false
		}
	}
	return true
}

func applyPriorityCandidate(c PrioritySchedulingConfig, item *openAIAccountCandidateScore, signal PrioritySchedulingSignal, now time.Time) PrioritySchedulingScore {
	score := scorePriorityCandidate(c, *item, signal, now)
	item.priorityUnhealthy = slices.Contains(score.Reasons, "quality_below_target") ||
		slices.Contains(score.Reasons, "recent_errors") || slices.Contains(score.Reasons, "historical_loss")
	item.priorityLatencyPenalty = slices.Contains(score.Reasons, "latency_above_target")
	item.priorityLatencyFactor = 1
	if score.Samples >= c.MinSamples && score.SelectedTTFTMs != nil && *score.SelectedTTFTMs > 0 {
		ratio := *score.SelectedTTFTMs / float64(c.TargetTTFTMs)
		// Experience constrains profit preference even when every account
		// misses the target. Keep a floor for recovery/overflow traffic.
		item.priorityLatencyFactor = math.Max(0.02, 1/(1+ratio*ratio))
	}
	// Experience tiers remain distinct within the same risk/capacity cohort;
	// current congestion is considered before historical score differences.
	offset := 0.0
	switch score.Tier {
	case "eligible":
		offset = 400
	case "insufficient":
		// Recovery is still latency-limited; keep it in the same internal
		// cohort as an active penalty so weighted diversity can choose the
		// least slow fallback instead of promoting every recovery account.
		if !score.LatencyRecovery {
			offset = 200
		}
	}
	item.score = offset + score.Score
	qualityReady := score.QualitySamples > 0 &&
		float64(score.QualityPassed)/float64(score.QualitySamples)*100 >= float64(c.MinQualityPercent)
	item.priorityRecoveryExplorationPercent = c.RecoveryExplorationPercent
	item.priorityLatencyRecovery = score.LatencyRecovery
	item.priorityExploration = (!score.LatencyRecovery || c.RecoveryExplorationPercent > 0) && (item.account.IsOpenAIOAuth() || item.account.IsOpenAIApiKey()) &&
		score.Tier == "insufficient" && qualityReady && (c.ErrorRateWeight == 0 || item.priorityErrors.SmoothedRate <= .2) &&
		(!score.LatencyRecovery || (score.LatestQualityPassed != nil && *score.LatestQualityPassed)) &&
		(score.ProfitSamples < c.MinSamples || score.Samples < c.MinSamples || score.LatencyRecovery) &&
		item.loadKnown && score.LoadPercent != nil && *score.LoadPercent < c.MaxLoadPercent
	item.priorityOAuthSpare = priorityOAuthQuotaSpare(c, *item, score, now)
	item.priorityAPIStandby = false
	return score
}
