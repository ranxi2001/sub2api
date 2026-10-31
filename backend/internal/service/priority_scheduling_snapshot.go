package service

import (
	"cmp"
	"slices"
	"time"
)

// Store only scoring inputs, never credentials, request bodies or session IDs.
// These owned copies cannot be mutated by a later account/load cache update.
type prioritySnapshotInput struct {
	limited    bool
	fallback   bool
	at         time.Time
	request    OpenAIAccountScheduleRequest
	config     PrioritySchedulingConfig
	candidates []openAIAccountCandidateScore
	quota      map[int64]float64
}

func newPrioritySnapshotInput(req OpenAIAccountScheduleRequest, c PrioritySchedulingConfig, pool []openAIAccountCandidateScore, at time.Time) *prioritySnapshotInput {
	input := &prioritySnapshotInput{limited: len(pool) > priorityHistoryMaxAccounts, at: at, request: OpenAIAccountScheduleRequest{RequestedModel: req.RequestedModel, HistoryModel: req.HistoryModel, InboundEndpoint: req.InboundEndpoint, RequiredCapability: req.RequiredCapability, RequireCompact: req.RequireCompact}, config: c, quota: make(map[int64]float64)}
	input.request.ObservationEffort = req.ObservationEffort
	input.request.ObservationEffortScoped = req.ObservationEffortScoped
	if req.GroupID != nil {
		id := *req.GroupID
		input.request.GroupID = &id
	}
	for _, item := range pool[:min(len(pool), priorityHistoryMaxAccounts)] {
		a := item.account
		factor := a.EffectiveLoadFactor()
		projection := &Account{ID: a.ID, Name: a.Name, Platform: a.Platform, Type: a.Type, Priority: a.Priority, Concurrency: a.Concurrency, LoadFactor: &factor, Extra: map[string]any{AccountCostMultiplierExtraKey: a.CostMultiplier()}}
		// Retain binding IDs without retaining mutable group objects.
		projection.GroupIDs = slices.Clone(a.GroupIDs)
		for _, binding := range a.AccountGroups {
			projection.GroupIDs = append(projection.GroupIDs, binding.GroupID)
		}
		item.account = projection
		if item.loadInfo != nil {
			load := *item.loadInfo
			item.loadInfo = &load
		}
		input.candidates = append(input.candidates, item)
		input.quota[a.ID] = openAIQuotaHeadroomFactor(a, at)
	}
	return input
}

// Reading the dashboard refreshes history asynchronously and re-evaluates the
// last observed pool. At still denotes the selection/load observation; a new
// EvaluatedAt must never be presented as a new live capacity measurement.
func (s *OpenAIGatewayService) PrioritySchedulingSnapshot() *PrioritySchedulingSnapshot {
	if s == nil {
		return nil
	}
	state := &s.priorityScheduling
	state.mu.Lock()
	input := state.latestInput
	state.mu.Unlock()
	if input == nil {
		return nil
	}
	return s.prioritySchedulingSnapshot(input)
}

func (s *OpenAIGatewayService) prioritySchedulingSnapshot(input *prioritySnapshotInput) *PrioritySchedulingSnapshot {
	snapshot, _ := s.prioritySchedulingSnapshotWithHistory(input)
	return snapshot
}

func (s *OpenAIGatewayService) prioritySchedulingSnapshotWithHistory(input *prioritySnapshotInput) (*PrioritySchedulingSnapshot, priorityHistoryResult) {
	if s == nil || input == nil {
		return nil, priorityHistoryResult{}
	}
	h := priorityHistoryResult{status: "limited"}
	if !input.limited {
		h = s.priorityHistory(input.request, input.config, input.candidates)
	}
	now := time.Now()
	var group *int64
	if input.request.GroupID != nil {
		id := *input.request.GroupID
		group = &id
	}
	snapshot := &PrioritySchedulingSnapshot{LatencyPercentile: input.config.LatencyPercentile, LatencyMeasurement: input.config.LatencyMeasurement, LatencyPenaltyMode: input.config.LatencyPenaltyMode, LatencyFallback: input.fallback, MinSamples: input.config.MinSamples, WindowMinutes: input.config.WindowMinutes, ConfigRevision: priorityConfigRevision(input.config), At: input.at, EvaluatedAt: now, Model: priorityHistoryModel(input.request), GroupID: group, Mode: input.config.Mode, SelectionPolicy: "capacity_first", HistoryReady: h.ready(), HistoryStatus: h.status, HistoryObservedAt: h.observed, HistoryWindowEnd: h.windowEnd, HistoryError: h.failure, HistoryRefreshing: h.refreshing, Candidates: make([]PrioritySchedulingScore, 0, len(input.candidates))}
	if input.config.OAuthQuotaPriority {
		snapshot.SelectionPolicy = "oauth_quota_priority"
	}
	for _, item := range input.candidates {
		// Roles describe the observed selection, not a new decision using stale load.
		role := ""
		if item.priorityOAuthSpare > 0 {
			role = "preferred"
		}
		if item.priorityAPIStandby {
			role = "standby"
		}
		if s.openaiAccountStats != nil {
			key := priorityRequestAttemptKey(input.request, item.account.ID)
			item.priorityErrors = s.openaiAccountStats.attempts.snapshot(key, now, input.config.ErrorRateWindowMinutes, input.config.ErrorRatePriorSamples)
		}
		score := applyPriorityCandidate(input.config, &item, h.signals[item.account.ID], now)
		score.OAuthQuotaRole = role
		score.ExplorationEligible = item.priorityExploration
		score.BoundGroups = priorityAccountGroupCount(item.account)
		score.CapacityBand = priorityCapacityBand(item)
		score.SelectionWeight = prioritySelectionWeightWithQuota(item, input.quota[item.account.ID])
		score.HistoryStatus = h.accountStatus(item.account.ID)
		snapshot.Candidates = append(snapshot.Candidates, score)
	}
	slices.SortStableFunc(snapshot.Candidates, comparePrioritySnapshotScores)
	snapshot.Candidates = snapshot.Candidates[:min(len(snapshot.Candidates), 100)]
	return snapshot, h
}

// Keep the dashboard order identical to the actual selection order: normal
// candidates first, then latency-limited accounts (recovery or active penalty),
// and known quality/error/loss risk last. Capacity bands only break ties inside
// a class.
func comparePrioritySnapshotScores(a, b PrioritySchedulingScore) int {
	category := func(v PrioritySchedulingScore) int {
		switch {
		case slices.Contains(v.Reasons, "quality_below_target") ||
			slices.Contains(v.Reasons, "recent_errors") ||
			slices.Contains(v.Reasons, "historical_loss"):
			return 2
		case v.LatencyRecovery || slices.Contains(v.Reasons, "latency_above_target"):
			return 1
		default:
			return 0
		}
	}
	if v := cmp.Compare(category(a), category(b)); v != 0 {
		return v
	}
	if v := cmp.Compare(a.CapacityBand, b.CapacityBand); v != 0 {
		return v
	}
	tier := func(v PrioritySchedulingScore) int {
		if v.LatencyRecovery {
			return 0
		}
		switch v.Tier {
		case "eligible":
			return 2
		case "insufficient":
			return 1
		default:
			return 0
		}
	}
	if v := cmp.Compare(tier(b), tier(a)); v != 0 {
		return v
	}
	if v := cmp.Compare(a.Priority, b.Priority); v != 0 {
		return v
	}
	if v := cmp.Compare(b.SelectionWeight, a.SelectionWeight); v != 0 {
		return v
	}
	return cmp.Compare(a.AccountID, b.AccountID)
}
