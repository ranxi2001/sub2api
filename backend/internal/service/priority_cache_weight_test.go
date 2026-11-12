//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func priorityCacheConfig(t *testing.T, mode string, weight float64) PrioritySchedulingConfig {
	t.Helper()
	c := DefaultPrioritySchedulingConfig()
	c.Enabled, c.Mode = true, mode
	require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{"cache_weight":%g}`, weight)), &c))
	return c
}

func TestPriorityCacheWeightScoresAndSelection(t *testing.T) {
	for _, mode := range []string{"experience", "balanced", "profit", "custom"} {
		t.Run(mode, func(t *testing.T) {
			cfg := priorityCacheConfig(t, mode, 100)
			low, high := performanceFixture(30, 4000, 0), performanceFixture(30, 4000, 0)
			lowPercent, highPercent := 10.0, 90.0
			low.Performance.AvgCachePercent, low.Performance.CacheSamples = &lowPercent, 20
			high.Performance.AvgCachePercent, high.Performance.CacheSamples = &highPercent, 20
			now := time.Now()
			before := scorePriorityCandidate(cfg, priorityCandidate(1, .1, 0), low, now)
			after := scorePriorityCandidate(cfg, priorityCandidate(2, .1, 0), high, now)
			require.InDelta(t, 40, after.Score-before.Score, 1e-8)
			require.Equal(t, before.Tier, after.Tier)
			reader := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{1: low, 2: high}}
			gateway := priorityGateway(cfg, reader)
			req := OpenAIAccountScheduleRequest{RequestedModel: "fixture-model", Platform: PlatformOpenAI, UseUpstreamTokenCost: true}
			pool := []openAIAccountCandidateScore{priorityCandidate(1, .1, 0), priorityCandidate(2, .1, 0)}
			gateway.prioritySignals(req, cfg, pool)
			require.Eventually(t, func() bool { _, ready := gateway.prioritySignals(req, cfg, pool); return ready }, time.Second, time.Millisecond)
			plan := openAIAccountLoadPlan{candidates: pool, topK: 1}
			scheduler := &defaultOpenAIAccountScheduler{service: gateway}
			require.True(t, scheduler.applyPriorityScheduling(req, &plan))
			require.Greater(t, plan.candidates[1].score, plan.candidates[0].score)
			snapshot := gateway.PrioritySchedulingSnapshot()
			require.Equal(t, high.Performance.AvgCachePercent, snapshot.Candidates[0].Performance.AvgCachePercent)
		})
	}
}

func TestPriorityCacheWeightDefaultAndMissingEvidence(t *testing.T) {
	base := performanceFixture(30, 4000, 0)
	item := priorityCandidate(1, .1, 0)
	unchanged := scorePriorityCandidate(DefaultPrioritySchedulingConfig(), item, base, time.Now())
	for _, weight := range []float64{0, 100} {
		cfg := priorityCacheConfig(t, "balanced", weight)
		for _, tc := range []struct {
			value   *float64
			samples int
		}{
			{nil, 0}, {float64Ptr(100), 0}, {float64Ptr(90), 4}, {float64Ptr(-1), 20}, {float64Ptr(101), 20},
		} {
			signal := performanceFixture(30, 4000, 0)
			signal.Performance.AvgCachePercent, signal.Performance.CacheSamples = tc.value, tc.samples
			score := scorePriorityCandidate(cfg, item, signal, time.Now())
			if weight == 0 {
				require.Equal(t, unchanged.Score, score.Score)
				require.Equal(t, unchanged.Tier, score.Tier)
				require.NotContains(t, score.Reasons, "cache_insufficient")
			} else {
				require.InDelta(t, (unchanged.BaseScore+50)/2, score.BaseScore, 1e-8)
				require.Contains(t, score.Reasons, "cache_insufficient")
				require.Equal(t, "insufficient", score.Tier)
			}
		}
	}
	for _, value := range []float64{0, 100} {
		cfg := priorityCacheConfig(t, "custom", 100)
		cfg.QualityWeight, cfg.LatencyWeight, cfg.LoadWeight, cfg.CostWeight = 0, 0, 0, 0
		require.NoError(t, ValidatePrioritySchedulingConfig(cfg))
		base.Performance.AvgCachePercent, base.Performance.CacheSamples = &value, cfg.MinSamples
		score := scorePriorityCandidate(cfg, item, base, time.Now())
		require.Equal(t, value, score.BaseScore)
		require.NotContains(t, score.Reasons, "cache_insufficient")
	}
	for _, weight := range []float64{-1, 101} {
		require.Error(t, ValidatePrioritySchedulingConfig(priorityCacheConfig(t, "balanced", weight)))
	}
}
