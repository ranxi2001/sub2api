package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPrioritySchedulingCostRefreshReusesHistoryAndChangesSelection(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	c.Enabled, c.Mode = true, "profit"
	signal := withAccountPerformanceFixture(PrioritySchedulingSignal{
		Samples: 20, P90TTFTMs: 500, QualityPassed: 10, QualitySamples: 10,
		ProfitSamples: 20, Revenue: 1, BaseCost: 8,
	})
	reader := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: signal, 2: signal}}
	gateway := priorityGateway(c, reader)
	scheduler := &defaultOpenAIAccountScheduler{service: gateway}
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "gpt-test", UseUpstreamTokenCost: true}
	pool := []openAIAccountCandidateScore{priorityCandidate(1, 0.05, 0), priorityCandidate(2, 0.1, 0)}
	require.Eventually(t, func() bool {
		_, ready := gateway.prioritySignals(req, c, pool)
		return ready
	}, time.Second, time.Millisecond)
	score := func() (openAIAccountLoadPlan, map[int64]PrioritySchedulingScore) {
		t.Helper()
		plan := openAIAccountLoadPlan{candidates: append([]openAIAccountCandidateScore(nil), pool...), topK: 1}
		scheduler.applyPriorityScheduling(req, &plan)
		snapshot := gateway.PrioritySchedulingSnapshot()
		require.True(t, snapshot.HistoryReady)
		byID := make(map[int64]PrioritySchedulingScore)
		for _, candidate := range snapshot.Candidates {
			byID[candidate.AccountID] = candidate
		}
		return plan, byID
	}
	_, initial := score()
	require.Equal(t, 0.05, *initial[1].Rate)
	require.Greater(t, initial[1].Score, initial[2].Score)
	require.Greater(t, initial[1].SelectionWeight, initial[2].SelectionWeight)

	// Refreshed candidate metadata must immediately revalue cached base cost.
	// The higher cost now makes this account loss-making and changes its risk cohort.
	pool[0].account.Extra[AccountCostMultiplierExtraKey] = 0.2
	plan, refreshed := score()
	require.Equal(t, 0.2, *refreshed[1].Rate)
	require.InDelta(t, 1.6, refreshed[1].TheoreticalCost, 1e-9)
	require.InDelta(t, -0.6, *refreshed[1].Profit, 1e-9)
	require.Contains(t, refreshed[1].Reasons, "historical_loss")
	require.Less(t, refreshed[1].SelectionWeight, refreshed[2].SelectionWeight)
	order := scheduler.buildOpenAISelectionOrder(req, plan)
	require.Equal(t, int64(2), order[0].account.ID)

	pool[0].account.Extra[AccountCostMultiplierExtraKey] = 0.0
	_, zero := score()
	require.Zero(t, zero[1].TheoreticalCost)
	require.Equal(t, 1.0, *zero[1].Profit)
	require.NotContains(t, zero[1].Reasons, "historical_loss")
	require.Greater(t, zero[1].SelectionWeight, zero[2].SelectionWeight)
	reader.mu.Lock()
	defer reader.mu.Unlock()
	require.Equal(t, 1, reader.calls, "cost changes must not require a history refresh")
	require.Equal(t, signal, reader.signal[1], "revaluation must not mutate history")
}
