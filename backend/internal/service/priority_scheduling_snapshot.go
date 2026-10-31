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
	snapshot, _ := s.prioritySchedulingSnapshotWithHistory(input, true)
	return snapshot
}

func (s *OpenAIGatewayService) prioritySchedulingSnapshot(input *prioritySnapshotInput) *PrioritySchedulingSnapshot {
	snapshot, _ := s.prioritySchedulingSnapshotWithHistory(input)
	return snapshot
}

func (s *OpenAIGatewayService) prioritySchedulingSnapshotWithHistory(input *prioritySnapshotInput, readOnly ...bool) (*PrioritySchedulingSnapshot, priorityHistoryResult) {
	if s == nil || input == nil {
		return nil, priorityHistoryResult{}
	}
	h := priorityHistoryResult{status: "limited"}
	if !input.limited {
		if len(readOnly) > 0 && readOnly[0] {
			h = s.readOnlyPriorityHistory(input.request, input.config, input.candidates)
		} else {
			h = s.priorityHistory(input.request, input.config, input.candidates)
		}
	}
	now := time.Now()
	var group *int64
	if input.request.GroupID != nil {
		id := *input.request.GroupID
		group = &id
	}
	snapshot := &PrioritySchedulingSnapshot{LatencyPercentile: input.config.LatencyPercentile, LatencyMeasurement: input.config.LatencyMeasurement, MinSamples: input.config.MinSamples, WindowMinutes: input.config.WindowMinutes, ConfigRevision: priorityConfigRevision(input.config), At: input.at, EvaluatedAt: now, Model: priorityHistoryModel(input.request), GroupID: group, Mode: input.config.Mode, SelectionPolicy: "capacity_first", HistoryReady: h.ready(), HistoryStatus: h.status, HistoryObservedAt: h.observed, HistoryWindowEnd: h.windowEnd, HistoryError: h.failure, HistoryRefreshing: h.refreshing, Candidates: make([]PrioritySchedulingScore, 0, len(input.candidates))}
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

func comparePrioritySnapshotScores(a, b PrioritySchedulingScore) int {
	risk := func(v PrioritySchedulingScore) bool {
		return slices.Contains(v.Reasons, "quality_below_target") || slices.Contains(v.Reasons, "recent_errors") || slices.Contains(v.Reasons, "historical_loss")
	}
	if risk(a) != risk(b) {
		if risk(a) {
			return 1
		}
		return -1
	}
	if v := cmp.Compare(a.CapacityBand, b.CapacityBand); v != 0 {
		return v
	}
	tier := func(t string) int {
		switch t {
		case "eligible":
			return 2
		case "insufficient":
			return 1
		default:
			return 0
		}
	}
	if v := cmp.Compare(tier(b.Tier), tier(a.Tier)); v != 0 {
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
