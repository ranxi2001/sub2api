package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	priorityHistoryTTL          = 30 * time.Second
	priorityHistoryColdTTL      = 5 * time.Second
	priorityHistoryLaunchWindow = time.Second
	priorityHistoryMaxAge       = 2 * time.Minute
	priorityHistoryRetry        = 5 * time.Second
	priorityHistoryMaxScopes    = 32
	priorityHistoryMaxAccounts  = 2000
)

type prioritySignalEntry struct {
	windowEnd                              time.Time
	signals                                map[int64]PrioritySchedulingSignal
	accounts                               map[int64]time.Time
	expires, observed, lastUsed, attempted time.Time
	refreshing                             bool
	failure                                string
	generation                             uint64
	recomputeGeneration                    uint64
	requiredReadGeneration                 uint64
	completedReadGeneration                uint64
}

type prioritySchedulingState struct {
	mu          sync.Mutex
	entries     map[string]*prioritySignalEntry
	launches    [2]time.Time
	launchIndex int
	active      int
	latestInput *prioritySnapshotInput
}

type priorityHistoryResult struct {
	entry                   *prioritySignalEntry
	completedReadGeneration uint64
	windowEnd               *time.Time
	signals                 map[int64]PrioritySchedulingSignal
	status                  string
	observed                *time.Time
	failure                 string
	refreshing              bool
}

func (h priorityHistoryResult) ready() bool { return h.status == "ready" || h.status == "stale" }
func (h priorityHistoryResult) accountStatus(id int64) string {
	if h.status == "limited" || h.status == "unavailable" {
		return h.status
	}
	if _, ok := h.signals[id]; ok {
		if h.status == "stale" || h.failure != "" || (h.observed != nil && time.Since(*h.observed) > priorityHistoryTTL) {
			return "stale"
		}
		return "ready"
	}
	if h.failure != "" {
		return "error"
	}
	return "loading"
}

func (s *OpenAIGatewayService) prioritySignals(req OpenAIAccountScheduleRequest, c PrioritySchedulingConfig, accounts []openAIAccountCandidateScore) (map[int64]PrioritySchedulingSignal, bool) {
	h := s.priorityHistory(req, c, accounts)
	return h.signals, h.ready()
}

// Cache by model/group/window, with a bounded union of recently seen accounts.
// Candidate churn must not erase evidence for peers. Never query on the request
// goroutine; two workers and bounded scopes also apply to dashboard refreshes.
func (s *OpenAIGatewayService) priorityHistory(req OpenAIAccountScheduleRequest, c PrioritySchedulingConfig, accounts []openAIAccountCandidateScore) priorityHistoryResult {
	reader, ok := s.usageLogRepo.(PrioritySchedulingSignalReader)
	if !ok {
		return priorityHistoryResult{status: "unavailable"}
	}
	if len(accounts) == 0 || len(accounts) > priorityHistoryMaxAccounts || len(priorityHistoryModel(req)) > 200 || len(req.RequestedModel) > 200 {
		return priorityHistoryResult{status: "limited"}
	}
	wanted := make(map[int64]struct{}, len(accounts))
	for _, a := range accounts {
		if a.account != nil {
			wanted[a.account.ID] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return priorityHistoryResult{status: "unavailable"}
	}
	key := priorityHistoryKey(req, c)
	state := &s.priorityScheduling
	state.mu.Lock()
	defer state.mu.Unlock()
	now := time.Now()
	if state.entries == nil {
		state.entries = make(map[string]*prioritySignalEntry)
	}
	entry := state.entries[key]
	if entry == nil {
		if len(state.entries) >= priorityHistoryMaxScopes {
			oldestKey := ""
			var oldest time.Time
			for k, e := range state.entries {
				if !e.refreshing && (oldestKey == "" || e.lastUsed.Before(oldest)) {
					oldestKey, oldest = k, e.lastUsed
				}
			}
			if oldestKey == "" {
				return priorityHistoryResult{status: "limited"}
			}
			delete(state.entries, oldestKey)
		}
		entry = &prioritySignalEntry{accounts: make(map[int64]time.Time)}
		state.entries[key] = entry
	}
	entry.lastUsed = now
	// Prefer current candidates, then retain recently seen peers up to the bound.
	for id, seen := range entry.accounts {
		if _, current := wanted[id]; !current && now.Sub(seen) > priorityHistoryMaxAge {
			delete(entry.accounts, id)
			delete(entry.signals, id)
		}
	}
	for id := range wanted {
		entry.accounts[id] = now
	}
	if len(entry.accounts) > priorityHistoryMaxAccounts {
		old := make([]int64, 0, len(entry.accounts)-len(wanted))
		for id := range entry.accounts {
			if _, current := wanted[id]; !current {
				old = append(old, id)
			}
		}
		slices.SortFunc(old, func(a, b int64) int { return entry.accounts[a].Compare(entry.accounts[b]) })
		for _, id := range old {
			if len(entry.accounts) <= priorityHistoryMaxAccounts {
				break
			}
			delete(entry.accounts, id)
			delete(entry.signals, id)
		}
	}
	missing := false
	for id := range wanted {
		if _, exists := entry.signals[id]; !exists {
			missing = true
			break
		}
	}
	due := entry.requiredReadGeneration > entry.completedReadGeneration || now.After(entry.expires) || (missing && entry.failure == "" && now.Sub(entry.attempted) >= time.Second)
	if !entry.refreshing && due && state.active < 2 && now.Sub(state.launches[state.launchIndex]) >= priorityHistoryLaunchWindow {
		ids := make([]int64, 0, len(entry.accounts))
		for id := range entry.accounts {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		// Copy the group value before the asynchronous reader outlives its caller.
		var group *int64
		if req.GroupID != nil {
			value := *req.GroupID
			group = &value
		}
		query := PrioritySchedulingQuery{LatencyPercentile: c.LatencyPercentile, LatencyMeasurement: c.LatencyMeasurement, TargetTTFTMs: c.TargetTTFTMs, AccountIDs: ids, Model: priorityHistoryModel(req), QualityModel: strings.TrimSpace(req.RequestedModel), GroupID: group, UsageUntil: now, UsageSince: now.Add(-time.Duration(c.WindowMinutes) * time.Minute), QualitySince: now.Add(-time.Duration(c.QualityMaxAgeHours) * time.Hour)}
		if req.ObservationEffortScoped {
			value := req.ObservationEffort
			query.ObservationEffort = &value
		}
		state.launches[state.launchIndex] = now
		state.launchIndex = (state.launchIndex + 1) % len(state.launches)
		entry.refreshing = true
		entry.attempted = now
		entry.generation++
		generation := entry.generation
		state.active++
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			signals, err := reader.ReadPrioritySchedulingSignals(ctx, query)
			failure := ""
			if err != nil {
				failure = "query_failed"
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
					failure = "timeout"
				}
			}
			if failure != "" {
				var sqlError interface{ SQLState() string }
				sqlState := ""
				if errors.As(err, &sqlError) {
					sqlState = sqlError.SQLState()
				}
				slog.Warn("priority_history_refresh_failed", "reason", failure, "sql_state", sqlState, "accounts", len(ids), "group_id", derefGroupID(query.GroupID), "model", query.Model)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			state.active--
			if state.entries[key] != entry || entry.generation != generation {
				return
			}
			entry.refreshing = false
			entry.completedReadGeneration = generation
			if err != nil {
				// Keep recent evidence, including known risk, through transient failures.
				entry.failure = failure
				entry.expires = time.Now().Add(priorityHistoryRetry)
				return
			}
			// Validate the entire read before replacing any successful evidence.
			// An omitted aggregate is incomplete, unlike an explicit zero row.
			for _, id := range ids {
				if _, returned := signals[id]; !returned {
					entry.failure = "incomplete"
					entry.expires = time.Now().Add(priorityHistoryRetry)
					return
				}
			}
			nextSignals := make(map[int64]PrioritySchedulingSignal, len(entry.accounts))
			// Candidates may join while SQL is running. Retain their prior evidence
			// until a read that includes them can replace it.
			for id, signal := range entry.signals {
				if _, tracked := entry.accounts[id]; tracked {
					nextSignals[id] = signal
				}
			}
			for _, id := range ids {
				if signal, returned := signals[id]; returned {
					if _, tracked := entry.accounts[id]; tracked {
						nextSignals[id] = signal
					}
				}
			}
			entry.signals = nextSignals
			entry.failure = ""
			entry.windowEnd = query.UsageUntil
			entry.observed = time.Now()
			// A completed call may commit just after the previous read. Revisit
			// cold accounts without billing hooks, synthetic samples or a query per
			// completion. Two workers, 32 scopes and the global rolling launch budget bound SQL.
			ttl := priorityHistoryTTL
			for _, signal := range entry.signals {
				if max(signal.Samples+signal.LatencyUnknownSamples, signal.ProfitSamples) < c.MinSamples {
					ttl = priorityHistoryColdTTL
					break
				}
			}
			entry.expires = entry.observed.Add(ttl)
		}()
	}
	// Completion metadata and evidence belong to the same locked observation.
	h := priorityHistoryResult{entry: entry, completedReadGeneration: entry.completedReadGeneration, signals: make(map[int64]PrioritySchedulingSignal, len(wanted)), status: "loading", failure: entry.failure, refreshing: entry.refreshing}
	if !entry.observed.IsZero() && now.Sub(entry.observed) <= priorityHistoryMaxAge {
		observed := entry.observed
		h.observed = &observed
		windowEnd := entry.windowEnd
		h.windowEnd = &windowEnd
		for id := range wanted {
			if signal, exists := entry.signals[id]; exists {
				h.signals[id] = signal
			}
		}
	}
	switch {
	case len(h.signals) == len(wanted):
		h.status = "ready"
		if now.Sub(entry.observed) > priorityHistoryTTL || entry.failure != "" {
			h.status = "stale"
		}
	case len(h.signals) > 0:
		h.status = "partial"
	case entry.failure != "":
		h.status = "error"
	}
	return h
}

// Optional sticky balancing must not schedule SQL or consume refresh workers.
// It may consult recent evidence already loaded by freely routed requests.
func (s *OpenAIGatewayService) cachedPriorityHistory(req OpenAIAccountScheduleRequest, c PrioritySchedulingConfig, accounts []openAIAccountCandidateScore) priorityHistoryResult {
	key := priorityHistoryKey(req, c)
	state := &s.priorityScheduling
	state.mu.Lock()
	defer state.mu.Unlock()
	h := priorityHistoryResult{signals: make(map[int64]PrioritySchedulingSignal)}
	e := state.entries[key]
	if e == nil || e.observed.IsZero() || time.Since(e.observed) > priorityHistoryMaxAge {
		return h
	}
	for _, item := range accounts {
		if signal, exists := e.signals[item.account.ID]; exists {
			h.signals[item.account.ID] = signal
		}
	}
	return h
}

// Management GETs must not launch workers, prune scope membership, acknowledge
// generations or recompute policy. Explicit POST recompute retains its own path.
func (s *OpenAIGatewayService) readOnlyPriorityHistory(req OpenAIAccountScheduleRequest, c PrioritySchedulingConfig, pool []openAIAccountCandidateScore) priorityHistoryResult {
	s.priorityScheduling.mu.Lock()
	defer s.priorityScheduling.mu.Unlock()
	h := priorityHistoryResult{status: "loading", signals: make(map[int64]PrioritySchedulingSignal)}
	entry := s.priorityScheduling.entries[priorityHistoryKey(req, c)]
	if entry == nil {
		return h
	}
	h.entry = entry
	h.completedReadGeneration = entry.completedReadGeneration
	h.refreshing = entry.refreshing
	h.failure = entry.failure
	if !entry.observed.IsZero() {
		observed, window := entry.observed, entry.windowEnd
		h.observed, h.windowEnd = &observed, &window
		for _, item := range pool {
			if value, ok := entry.signals[item.account.ID]; ok {
				h.signals[item.account.ID] = value
			}
		}
	}
	if len(h.signals) == len(pool) && len(pool) > 0 {
		h.status = "ready"
		if time.Since(entry.observed) > priorityHistoryTTL || h.failure != "" {
			h.status = "stale"
		}
	} else if len(h.signals) > 0 {
		h.status = "partial"
	} else if h.failure != "" {
		h.status = "error"
	}
	return h
}

func priorityHistoryKey(req OpenAIAccountScheduleRequest, c PrioritySchedulingConfig) string {
	// The action changes admission, not the measured population. Switching soft
	// and stop must retain the exact same model/group/measurement evidence.
	c.LatencyPenaltyMode = PriorityLatencyDeprioritize
	return fmt.Sprintf("%q/%q/%d/%t/%d/%d/%t/%q", priorityHistoryModel(req), strings.TrimSpace(req.RequestedModel), derefGroupID(req.GroupID), req.GroupID != nil, c.WindowMinutes, c.QualityMaxAgeHours, req.ObservationEffortScoped, req.ObservationEffort) + "/" + priorityConfigRevision(c)
}

// Include every scoring setting so stale scopes never mix targets or semantics.
func priorityConfigRevision(c PrioritySchedulingConfig) string {
	b, _ := json.Marshal(c)
	return fmt.Sprintf("%x", sha256.Sum256(b))[:16]
}
