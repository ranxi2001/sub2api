package service

import (
	"context"
	"errors"
	"math"
	"time"
)

var ErrPrioritySchedulingSnapshotUnavailable = errors.New("priority scheduling snapshot unavailable")
var ErrPrioritySchedulingRecomputeIncomplete = errors.New("priority scheduling history refresh incomplete")

// PrioritySchedulingDiffRow compares one account's last observed ranking with
// the ranking produced by a forced history refresh. It is a diagnostic diff:
// it never persists account priority or mutates routing state.
type PrioritySchedulingDiffRow struct {
	AccountID       int64    `json:"account_id"`
	AccountName     string   `json:"account_name"`
	OldRank         int      `json:"old_rank,omitempty"`
	NewRank         int      `json:"new_rank,omitempty"`
	OldScore        float64  `json:"old_score"`
	NewScore        float64  `json:"new_score"`
	OldWeight       float64  `json:"old_weight"`
	NewWeight       float64  `json:"new_weight"`
	OldTier         string   `json:"old_tier,omitempty"`
	NewTier         string   `json:"new_tier,omitempty"`
	OldP90TTFTMs    float64  `json:"old_p90_ttft_ms"`
	OldAvgTTFTMs    *float64 `json:"old_avg_ttft_ms"`
	NewAvgTTFTMs    *float64 `json:"new_avg_ttft_ms"`
	NewP90TTFTMs    float64  `json:"new_p90_ttft_ms"`
	OldSamples      int      `json:"old_samples"`
	NewSamples      int      `json:"new_samples"`
	OldErrorSamples int      `json:"old_error_samples"`
	NewErrorSamples int      `json:"new_error_samples"`
	Changed         bool     `json:"changed"`
	Reasons         []string `json:"reasons,omitempty"`
}

type PrioritySchedulingRecomputeResult struct {
	Before   *PrioritySchedulingSnapshot `json:"before"`
	After    *PrioritySchedulingSnapshot `json:"after"`
	Diff     []PrioritySchedulingDiffRow `json:"diff"`
	TimedOut bool                        `json:"timed_out"`
}

// RecomputePriorityScheduling forces the history cache for the last observed
// candidate pool to refresh and returns a before/after ranking diff. The pool
// itself stays the last selection observation; only latency, error and quality
// signals are re-read. Callers that want sessions to move must clear sticky
// bindings after reviewing the diff.
func (s *OpenAIGatewayService) RecomputePriorityScheduling(ctx context.Context) (*PrioritySchedulingRecomputeResult, error) {
	if s == nil {
		return nil, ErrPrioritySchedulingSnapshotUnavailable
	}
	state := &s.priorityScheduling
	state.mu.Lock()
	input := state.latestInput
	state.mu.Unlock()
	if input == nil {
		return nil, ErrPrioritySchedulingSnapshotUnavailable
	}
	// This snapshot uses one immutable owned copy of the last selection input.
	before := s.prioritySchedulingSnapshot(input)
	if before == nil {
		return nil, ErrPrioritySchedulingSnapshotUnavailable
	}
	state.mu.Lock()
	key := priorityHistoryKey(input.request, input.config)
	entry := state.entries[key]
	if entry == nil {
		state.mu.Unlock()
		return nil, ErrPrioritySchedulingSnapshotUnavailable
	}
	capturedInput := input
	capturedKey := key
	entry.recomputeGeneration++
	recomputeGeneration := entry.recomputeGeneration
	// Keep successful risk evidence available to routing throughout this refresh.
	// A reader already launched (including by the before snapshot) cannot satisfy
	// this request. Its completion makes room for the required subsequent read.
	requiredReadGeneration := entry.generation + 1
	entry.requiredReadGeneration = requiredReadGeneration
	state.mu.Unlock()

	deadline := time.Now().Add(5 * time.Second)
	for {
		after, history := s.prioritySchedulingSnapshotWithHistory(capturedInput)
		if after == nil {
			return nil, ErrPrioritySchedulingSnapshotUnavailable
		}
		state.mu.Lock()
		current := state.entries[capturedKey]
		valid := current == entry && current.recomputeGeneration == recomputeGeneration
		state.mu.Unlock()
		if !valid || history.entry != entry {
			return nil, ErrPrioritySchedulingSnapshotUnavailable
		}
		// Check the generation captured with this snapshot's signals, rather than
		// a newer completion that could race with snapshot scoring.
		if history.completedReadGeneration >= requiredReadGeneration {
			if after.HistoryError != "" || after.HistoryStatus == "error" || after.HistoryStatus == "unavailable" || after.HistoryStatus == "limited" {
				return nil, ErrPrioritySchedulingRecomputeIncomplete
			}
			if !after.HistoryRefreshing && after.HistoryStatus == "ready" {
				return &PrioritySchedulingRecomputeResult{Before: before, After: after, Diff: diffPrioritySnapshots(before, after)}, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, ErrPrioritySchedulingRecomputeIncomplete
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func diffPrioritySnapshots(before, after *PrioritySchedulingSnapshot) []PrioritySchedulingDiffRow {
	if after == nil {
		return nil
	}
	oldRows := make(map[int64]PrioritySchedulingScore, 0)
	oldRank := make(map[int64]int, 0)
	if before != nil {
		for index, candidate := range before.Candidates {
			oldRows[candidate.AccountID] = candidate
			oldRank[candidate.AccountID] = index + 1
		}
	}
	seen := make(map[int64]struct{}, len(after.Candidates))
	rows := make([]PrioritySchedulingDiffRow, 0, len(after.Candidates))
	for index, candidate := range after.Candidates {
		seen[candidate.AccountID] = struct{}{}
		old, ok := oldRows[candidate.AccountID]
		row := PrioritySchedulingDiffRow{
			AccountID:       candidate.AccountID,
			AccountName:     candidate.AccountName,
			OldRank:         oldRank[candidate.AccountID],
			NewRank:         index + 1,
			OldScore:        old.Score,
			NewScore:        candidate.Score,
			OldWeight:       old.SelectionWeight,
			NewWeight:       candidate.SelectionWeight,
			OldTier:         old.Tier,
			NewTier:         candidate.Tier,
			OldP90TTFTMs:    old.P90TTFTMs,
			OldAvgTTFTMs:    old.SelectedTTFTMs,
			NewAvgTTFTMs:    candidate.SelectedTTFTMs,
			NewP90TTFTMs:    candidate.P90TTFTMs,
			OldSamples:      old.Samples,
			NewSamples:      candidate.Samples,
			OldErrorSamples: old.ErrorSamples,
			NewErrorSamples: candidate.ErrorSamples,
			Reasons:         candidate.Reasons,
		}
		row.Changed = !ok || row.OldRank != row.NewRank ||
			!almostEqual(row.OldScore, row.NewScore, 0.01) ||
			!almostEqual(row.OldWeight, row.NewWeight, 0.001) ||
			row.OldTier != row.NewTier ||
			!almostEqualOptional(row.OldAvgTTFTMs, row.NewAvgTTFTMs, 1) ||
			row.OldSamples != row.NewSamples ||
			row.OldErrorSamples != row.NewErrorSamples
		rows = append(rows, row)
	}
	// Accounts that disappeared from the refreshed pool still deserve a row so
	// the operator can see that an option was lost, not silently dropped.
	if before != nil {
		for index, candidate := range before.Candidates {
			if _, ok := seen[candidate.AccountID]; ok {
				continue
			}
			rows = append(rows, PrioritySchedulingDiffRow{
				AccountID:       candidate.AccountID,
				AccountName:     candidate.AccountName,
				OldRank:         index + 1,
				OldScore:        candidate.Score,
				OldWeight:       candidate.SelectionWeight,
				OldTier:         candidate.Tier,
				OldP90TTFTMs:    candidate.P90TTFTMs,
				OldSamples:      candidate.Samples,
				OldErrorSamples: candidate.ErrorSamples,
				Changed:         true,
			})
		}
	}
	return rows
}

func almostEqual(a, b, tolerance float64) bool {
	return math.Abs(a-b) <= tolerance
}

func almostEqualOptional(a, b *float64, tolerance float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return almostEqual(*a, *b, tolerance)
}
