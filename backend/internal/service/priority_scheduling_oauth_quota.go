package service

import (
	"fmt"
	"math"
	"time"
)

// This gate removes first-choice traffic, so it needs fresher evidence than
// the ordinary soft quota weight. Missing evidence leaves normal routing intact.
const priorityOAuthQuotaMaxAge = 5 * time.Minute

func priorityOAuthQuotaSpare(c PrioritySchedulingConfig, item openAIAccountCandidateScore, score PrioritySchedulingScore, now time.Time) int {
	a := item.account
	if !c.OAuthQuotaPriority || a == nil || !a.IsOpenAIOAuth() || !a.IsSchedulable() ||
		item.priorityUnhealthy || (c.ErrorRateWeight > 0 && score.SmoothedRate > .2) || score.Tier == "degraded" || score.LatencyRecovery || score.QualitySamples == 0 || float64(score.QualityPassed)*100/float64(score.QualitySamples) < float64(c.MinQualityPercent) ||
		!item.loadKnown || item.loadInfo == nil || item.loadInfo.WaitingCount > 0 || a.Concurrency <= 0 {
		return 0
	}
	updated, err := parseTime(fmt.Sprint(a.Extra["codex_usage_updated_at"]))
	if err != nil || updated.After(now) || now.Sub(updated) >= priorityOAuthQuotaMaxAge {
		return 0
	}
	five, seven := openAICanonicalQuotaWindows(a.Extra, now)
	valid := func(w openAICanonicalQuotaWindow) bool {
		return w.hasUsed && !w.reset && !math.IsNaN(w.usedPercent) && !math.IsInf(w.usedPercent, 0) && w.usedPercent >= 0 && w.usedPercent <= 100
	}
	if !valid(seven) || seven.usedPercent >= float64(c.OAuthQuotaThreshold) || !valid(five) || five.usedPercent >= 100 {
		return 0
	}
	// Unknown reset times must not turn a previous window into current evidence.
	for _, window := range []string{"5h", "7d"} {
		end, ok := openAICodexWindowResetAt(a.Extra, window)
		if !ok || !end.After(now) {
			return 0
		}
	}
	spare := int(math.Ceil(float64(a.Concurrency)*float64(c.MaxLoadPercent)/100)) - max(0, item.loadInfo.CurrentConcurrency)
	if item.rpmEnabled {
		spare = min(spare, item.rpmLimit-item.rpmCurrent)
	}
	// Share the estimate across bindings; atomic acquisition remains authoritative.
	return max(0, spare/priorityAccountGroupCount(a))
}

// Move a suffix of API-key candidates behind usable OAuth capacity, preserving
// the scheduler's order within both lists. Each spare OAuth slot can replace
// one spare API slot. Accounts are indivisible: the last moved account may
// exceed the remaining estimate, but stays immediately available for overflow.
// No account status, binding, or durable switch is changed.
func applyPriorityOAuthStandby(order []openAIAccountCandidateScore) []openAIAccountCandidateScore {
	budget := 0
	for i := range order {
		order[i].priorityAPIStandby = false
		budget += order[i].priorityOAuthSpare
	}
	if budget <= 0 {
		return order
	}
	for i := len(order) - 1; i >= 0 && budget > 0; i-- {
		item := &order[i]
		if !item.account.IsOpenAIApiKey() {
			continue
		}
		// Unknown/full/queued capacity must neither consume the estimate nor
		// conceal a genuinely usable API candidate further up the ranking.
		if !item.loadKnown || item.loadInfo == nil || item.loadInfo.WaitingCount > 0 || item.account.Concurrency <= item.loadInfo.CurrentConcurrency {
			continue
		}
		item.priorityAPIStandby = true
		budget -= max(1, item.account.Concurrency-max(0, item.loadInfo.CurrentConcurrency))
	}
	// Pull only qualified OAuth accounts ahead of the first standby key.
	// Other OAuth fallbacks must not jump a healthy API merely because it is
	// on standby (for example, a low-load but slow or untested OAuth account).
	first := len(order)
	for i, item := range order {
		if item.priorityAPIStandby {
			first = i
			break
		}
	}
	if first == len(order) {
		return order
	}
	out := make([]openAIAccountCandidateScore, 0, len(order))
	out = append(out, order[:first]...)
	for _, item := range order[first:] {
		if item.priorityOAuthSpare > 0 {
			out = append(out, item)
		}
	}
	for _, item := range order[first:] {
		if item.priorityOAuthSpare == 0 {
			out = append(out, item)
		}
	}
	return out
}
