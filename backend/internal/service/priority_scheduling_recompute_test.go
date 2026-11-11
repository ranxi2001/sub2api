package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func priorityDiffRowsByAccount(rows []PrioritySchedulingDiffRow) map[int64]PrioritySchedulingDiffRow {
	out := make(map[int64]PrioritySchedulingDiffRow, len(rows))
	for _, row := range rows {
		out[row.AccountID] = row
	}
	return out
}

func TestDiffPrioritySnapshots(t *testing.T) {
	before := &PrioritySchedulingSnapshot{Candidates: []PrioritySchedulingScore{
		{AccountID: 1, AccountName: "A", Score: 80, SelectionWeight: 1.5, Tier: "eligible", PrioritySchedulingSignal: withAccountPerformanceFixture(PrioritySchedulingSignal{P90TTFTMs: 1000, Samples: 10, ErrorSamples: 1})},
		{AccountID: 2, AccountName: "B", Score: 70, SelectionWeight: 1.0, Tier: "insufficient", PrioritySchedulingSignal: withAccountPerformanceFixture(PrioritySchedulingSignal{P90TTFTMs: 5000, Samples: 5})},
		{AccountID: 4, AccountName: "D", Score: 50, SelectionWeight: 0.5, Tier: "degraded", PrioritySchedulingSignal: withAccountPerformanceFixture(PrioritySchedulingSignal{P90TTFTMs: 9000, Samples: 2})},
	}}
	after := &PrioritySchedulingSnapshot{Candidates: []PrioritySchedulingScore{
		{AccountID: 2, AccountName: "B", Score: 90, SelectionWeight: 2.0, Tier: "eligible", PrioritySchedulingSignal: withAccountPerformanceFixture(PrioritySchedulingSignal{P90TTFTMs: 2000, Samples: 8})},
		{AccountID: 1, AccountName: "A", Score: 60, SelectionWeight: 0.8, Tier: "degraded", PrioritySchedulingSignal: withAccountPerformanceFixture(PrioritySchedulingSignal{P90TTFTMs: 8000, Samples: 12, ErrorSamples: 3})},
		{AccountID: 3, AccountName: "C", Score: 85, SelectionWeight: 1.7, Tier: "eligible", PrioritySchedulingSignal: withAccountPerformanceFixture(PrioritySchedulingSignal{P90TTFTMs: 1500, Samples: 6})},
	}}
	rows := priorityDiffRowsByAccount(diffPrioritySnapshots(before, after))
	require.Len(t, rows, 4)

	require.Equal(t, 1, rows[1].OldRank)
	require.Equal(t, 2, rows[1].NewRank)
	require.True(t, rows[1].Changed)
	require.True(t, almostEqual(rows[1].NewP90TTFTMs, 8000, 0.001))

	require.Equal(t, 2, rows[2].OldRank)
	require.Equal(t, 1, rows[2].NewRank)
	require.True(t, rows[2].Changed)

	require.Equal(t, 3, rows[3].NewRank)
	require.Zero(t, rows[3].OldRank)
	require.True(t, rows[3].Changed)

	require.Equal(t, 3, rows[4].OldRank)
	require.Zero(t, rows[4].NewRank)
	require.True(t, rows[4].Changed)
}

func TestDiffPrioritySnapshotsUnchangedAndNil(t *testing.T) {
	snapshot := &PrioritySchedulingSnapshot{Candidates: []PrioritySchedulingScore{
		{AccountID: 1, AccountName: "A", Score: 80, SelectionWeight: 1.5, Tier: "eligible", PrioritySchedulingSignal: withAccountPerformanceFixture(PrioritySchedulingSignal{P90TTFTMs: 1000, Samples: 10})},
	}}
	for _, row := range diffPrioritySnapshots(snapshot, snapshot) {
		require.False(t, row.Changed)
	}
	require.Nil(t, diffPrioritySnapshots(snapshot, nil))
	for _, row := range diffPrioritySnapshots(nil, snapshot) {
		require.Zero(t, row.OldRank)
		require.True(t, row.Changed)
	}
}
