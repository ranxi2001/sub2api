package service

import (
	"context"
	"math"
	"slices"
	"sort"
	"time"
)

const (
	priorityExplorationShare = 0.10
	// A configured concurrency is a ceiling, not proof of real upstream slots.
	// Without a cap, a 10k-slot relay receives 100x the routing weight of a
	// 100-slot account and latency penalties cannot offset it.
	priorityCapacityWeightCap = 16.0
)

// Ten-percentage-point cohorts prefer genuinely idle capacity while preserving
// weighted diversity among similarly loaded accounts. The atomic slot race
// below still rejects full accounts; the band describes the observed load and
// must not promote every empty low-concurrency account just because it may take
// the next request.
func priorityCapacityBand(item openAIAccountCandidateScore) int {
	if !item.loadKnown || item.loadInfo == nil || item.account.Concurrency <= 0 {
		return 10
	}
	load := float64(max(0, item.loadInfo.CurrentConcurrency)) / float64(item.account.Concurrency)
	if item.rpmEnabled && item.rpmLimit > 0 {
		load = math.Max(load, float64(max(0, item.rpmCurrent))/float64(item.rpmLimit))
	}
	band := int(math.Floor(load * 10))
	if item.loadInfo.WaitingCount > 0 {
		return 20 + max(band, item.loadInfo.WaitingCount)
	}
	return band
}

func (s *OpenAIGatewayService) balancesPriorityProtocols(req OpenAIAccountScheduleRequest) bool {
	if s == nil || req.Platform != PlatformOpenAI || !req.UseUpstreamTokenCost || req.RequiredImageCapability != "" {
		return false
	}
	cfg := s.prioritySchedulingRuntimeConfig()
	return cfg.BalanceProtocols && priorityApplies(cfg, req)
}

// Count distinct bindings, not group price/name or an inferred account class.
// AccountGroups supports older snapshots that do not populate GroupIDs.
func priorityAccountGroupCount(account *Account) int {
	if account == nil {
		return 1
	}
	groups := make(map[int64]struct{}, len(account.GroupIDs)+len(account.AccountGroups))
	for _, id := range account.GroupIDs {
		if id > 0 {
			groups[id] = struct{}{}
		}
	}
	for _, binding := range account.AccountGroups {
		if binding.GroupID > 0 {
			groups[binding.GroupID] = struct{}{}
		}
	}
	return max(1, len(groups))
}

// prioritySelectionWeight bounds the score preference so every usable account
// in a tier/priority cohort can receive traffic. Capacity uses real slots, not
// load_factor, and quota headroom is a soft preference rather than a new gate.
func prioritySelectionWeight(item openAIAccountCandidateScore, now time.Time) float64 {
	return prioritySelectionWeightWithQuota(item, openAIQuotaHeadroomFactor(item.account, now))
}

func prioritySelectionWeightWithQuota(item openAIAccountCandidateScore, headroom float64) float64 {
	remaining := 1.0
	if item.loadKnown && item.loadInfo != nil {
		remaining = math.Max(0.05, float64(max(1, item.account.Concurrency)-max(0, item.loadInfo.CurrentConcurrency))) /
			(1 + float64(max(0, item.loadInfo.WaitingCount)))
		remaining = math.Min(remaining, priorityCapacityWeightCap)
	}
	score := math.Mod(item.score, 200)
	if math.IsNaN(score) || math.IsInf(score, 0) {
		score = 0
	}
	preference := 0.5 + clamp01(score/100)
	if math.IsNaN(headroom) || math.IsInf(headroom, 0) {
		headroom = openAIQuotaHeadroomNeutralFactor
	}
	quota := 0.5 + 0.5*clamp01(headroom)
	// OAuth capacity is shared by every bound group. Prefer spare capacity
	// exposed to fewer groups without excluding broader accounts or reserving
	// idle slots when there is work they can serve. API-key routing weights keep
	// their existing model because upstream capacity may not be subscription-bound.
	groups := 1
	if item.account.IsOpenAIOAuth() {
		groups = priorityAccountGroupCount(item.account)
	}
	latency := item.priorityLatencyFactor
	if latency <= 0 || math.IsNaN(latency) || math.IsInf(latency, 0) {
		latency = 1
	}
	return remaining * preference * quota * latency / float64(groups)
}

// Rebalance one movable turn only when another compatible account has both
// materially lower utilization and substantially more capacity per bound group.
// The durable session binding is preserved by the caller. Hard response/task
// ownership never enters this path, and failed load reads retain the binding.
func (s *defaultOpenAIAccountScheduler) shouldRebalancePrioritySticky(ctx context.Context, req OpenAIAccountScheduleRequest, sticky *Account) bool {
	if s == nil || s.service == nil || s.service.concurrencyService == nil || sticky == nil ||
		req.DisableStickyEscape || req.PreserveStickyBinding || req.PreviousResponseID != "" ||
		req.Platform != PlatformOpenAI || !req.UseUpstreamTokenCost || req.RequiredImageCapability != "" {
		return false
	}
	cfg := s.service.prioritySchedulingRuntimeConfig()
	if !priorityApplies(cfg, req) {
		return false
	}
	balanceProtocols := cfg.BalanceProtocols
	quotaSticky := cfg.OAuthQuotaPriority && sticky.IsOpenAIApiKey()
	// Optional balancing must not hold a healthy sticky turn behind slow dependencies.
	ctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	accounts, err := s.service.listSchedulableAccountsForRequest(ctx, req.GroupID, req.Platform, req.RequestedModel, req.RequireCompact, req.ExcludedIDs)
	if err != nil || len(accounts) < 2 {
		return false
	}
	loads := make([]AccountWithConcurrency, 0, len(accounts)+1)
	loads = append(loads, AccountWithConcurrency{ID: sticky.ID, MaxConcurrency: max(1, sticky.Concurrency)})
	eligible := make([]*Account, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if account.ID == sticky.ID || !account.IsSchedulable() || (!quotaSticky && account.Priority > sticky.Priority) ||
			!s.service.openAIAccountMatchesSchedulingGroup(account, req.GroupID) ||
			!s.isAccountRequestCompatible(ctx, account, req) || !s.isAccountTransportCompatible(account, req.RequiredTransport, req.RequestedModel) ||
			s.service.isExcelBPSCoolingDown(account, req.RequestedModel) {
			continue
		}
		if _, excluded := req.ExcludedIDs[account.ID]; excluded {
			continue
		}
		if !balanceProtocols && sticky.IsExcelBPSEnabledForModel(req.RequestedModel) && !account.IsExcelBPSEnabledForModel(req.RequestedModel) {
			continue
		}
		if req.RequireCompact && openAICompactSupportTier(account) < openAICompactSupportTier(sticky) {
			continue
		}
		if req.SubscriptionPriority && sticky.IsOpenAIChatGPTSubscription() && !account.IsOpenAIChatGPTSubscription() {
			continue
		}
		errorRate := 0.0
		if s.stats != nil {
			errorRate, _, _ = s.stats.snapshot(account.ID)
		}
		if errorRate > 0.2 {
			continue
		}
		eligible = append(eligible, account)
		loads = append(loads, AccountWithConcurrency{ID: account.ID, MaxConcurrency: max(1, account.Concurrency)})
	}
	if len(eligible) == 0 {
		return false
	}
	loadMap, err := s.service.concurrencyService.GetAccountsLoadBatch(ctx, loads)
	if err != nil || loadMap[sticky.ID] == nil {
		return false
	}
	historyPool := make([]openAIAccountCandidateScore, 0, len(eligible))
	for _, account := range eligible {
		historyPool = append(historyPool, openAIAccountCandidateScore{account: account})
	}
	if quotaSticky {
		historyPool = append(historyPool, openAIAccountCandidateScore{account: sticky})
		history := s.service.priorityHistory(req, cfg, historyPool)
		now := time.Now()
		for i := range historyPool {
			item := &historyPool[i]
			item.loadInfo = loadMap[item.account.ID]
			item.loadKnown = item.loadInfo != nil
			if s.stats != nil {
				item.errorRate, _, _ = s.stats.snapshot(item.account.ID)
			}
			if rpm, ok := accountRPMStateFromContext(ctx, item.account); ok {
				item.rpmEnabled, item.rpmLimit, item.rpmCurrent = rpm.Enabled, rpm.Limit, rpm.Current
			}
			applyPriorityCandidate(cfg, item, history.signals[item.account.ID], now)
		}
		for _, item := range buildPrioritySelectionOrder(historyPool, req) {
			if item.account.ID == sticky.ID && item.priorityAPIStandby {
				return true
			}
		}
	}
	history := s.service.cachedPriorityHistory(req, cfg, historyPool)
	current := loadMap[sticky.ID]
	utilization := float64(current.CurrentConcurrency) / float64(max(1, sticky.Concurrency))
	now := time.Now()
	weight := prioritySelectionWeight(openAIAccountCandidateScore{account: sticky, loadInfo: current, loadKnown: true, score: 50}, now)
	for _, account := range eligible {
		load := loadMap[account.ID]
		if load == nil || load.WaitingCount > 0 || load.CurrentConcurrency >= account.Concurrency {
			continue
		}
		candidate := openAIAccountCandidateScore{account: account, loadKnown: true, loadInfo: load}
		score := applyPriorityCandidate(cfg, &candidate, history.signals[account.ID], now)
		if candidate.priorityUnhealthy || candidate.priorityLatencyRecovery || slices.Contains(score.Reasons, "latency_above_target") {
			continue
		}
		if rpm, ok := accountRPMStateFromContext(ctx, account); ok && rpm.Enabled && rpm.Limit > 0 && rpm.Current >= rpm.Limit {
			continue
		}
		otherUtilization := float64(load.CurrentConcurrency) / float64(max(1, account.Concurrency))
		otherWeight := prioritySelectionWeight(openAIAccountCandidateScore{account: account, loadInfo: load, loadKnown: true, score: 50}, now)
		if utilization-otherUtilization >= 0.20 && otherWeight > 2*weight {
			return true
		}
	}
	return false
}

// Keep recovery and active latency penalties out of the normal weighted race.
// Both are latency-limited; rank them together by score/capacity instead of
// claiming that an older bad sample is always better than a recent one. A
// latency-limited account may still be selected when every normal candidate
// loses its slot race, or through the explicit exploration lane below; it must
// never outrank a normal account merely because it is idle or has a large
// configured concurrency.
func prioritySelectionCategory(item openAIAccountCandidateScore) int {
	switch {
	case item.priorityUnhealthy:
		return 2
	case item.priorityLatencyRecovery || item.priorityLatencyPenalty:
		return 1
	default:
		return 0
	}
}

// Weighted sampling without replacement avoids a deterministic best-account
// monopoly and keeps all overflow candidates. Exponential races are O(n log n)
// and require no cross-instance counters or per-session scheduler state.
func buildPrioritySelectionOrder(pool []openAIAccountCandidateScore, req OpenAIAccountScheduleRequest) []openAIAccountCandidateScore {
	type choice struct {
		candidate openAIAccountCandidateScore
		key       float64
		band      int
	}
	now := time.Now()
	rng := newOpenAISelectionRNG(deriveOpenAISelectionSeed(req))
	choices := make([]choice, len(pool))
	for i, candidate := range pool {
		choices[i] = choice{candidate: candidate, key: -math.Log1p(-rng.nextFloat64()) / prioritySelectionWeight(candidate, now), band: priorityCapacityBand(candidate)}
	}
	sort.Slice(choices, func(i, j int) bool {
		a, b := choices[i].candidate, choices[j].candidate
		if categoryA, categoryB := prioritySelectionCategory(a), prioritySelectionCategory(b); categoryA != categoryB {
			return categoryA < categoryB
		}
		if choices[i].band != choices[j].band {
			return choices[i].band < choices[j].band
		}
		if int(a.score/200) != int(b.score/200) {
			return a.score > b.score
		}
		if a.account.Priority != b.account.Priority {
			return a.account.Priority < b.account.Priority
		}
		if choices[i].key != choices[j].key {
			return choices[i].key < choices[j].key
		}
		return a.account.ID < b.account.ID
	})

	// Cold accounts otherwise never gain enough evidence when an
	// established eligible cohort exists. Reserve a 10% exploration probability
	// for safe unknown accounts at that same explicit priority. When no eligible
	// cohort exists, the same lane carries recovery probes instead: recovery
	// accounts stay out of the ordinary race, but the configured recovery share
	// can still collect fresh samples. Known degraded accounts, busy accounts,
	// protocol partitions and hard bindings cannot use this lane; the ordinary
	// eligibility/profit/slot checks still follow.
	explorationRoll := rng.nextFloat64()
	topTier := 0
	if len(choices) > 0 {
		topTier = int(choices[0].candidate.score / 200)
	}
	if len(choices) > 1 && topTier >= 1 && explorationRoll < priorityExplorationShare {
		best := -1
		for i := 1; i < len(choices); i++ {
			candidate := choices[i].candidate
			if topTier == 2 && int(candidate.score/200) != 1 && !candidate.priorityLatencyRecovery {
				continue
			}
			if topTier == 1 && !candidate.priorityLatencyRecovery {
				continue
			}
			if candidate.priorityExploration &&
				(!candidate.priorityLatencyRecovery || explorationRoll < float64(candidate.priorityRecoveryExplorationPercent)/100) &&
				!candidate.priorityUnhealthy && choices[i].band <= choices[0].band &&
				candidate.account.Priority == choices[0].candidate.account.Priority &&
				(best < 0 || choices[i].key < choices[best].key) {
				best = i
			}
		}
		if best > 0 {
			selected := choices[best]
			copy(choices[1:best+1], choices[:best])
			choices[0] = selected
		}
	}
	order := make([]openAIAccountCandidateScore, len(choices))
	for i := range choices {
		order[i] = choices[i].candidate
	}
	return applyPriorityOAuthStandby(order)
}
