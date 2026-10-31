package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The production failure was a recovery-period account with 10k configured
// concurrency winning every free selection because capacity was compared before
// the latency recovery class. The normal candidate here has a worse capacity
// band (1 vs 0) but must still win every free selection.
func TestPriorityRecoveryRanksAfterNormalCandidate(t *testing.T) {
	now := time.Now()
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	cfg.TargetTTFTMs = 15000
	cfg.MinSamples = 2
	cfg.RecoveryExplorationPercent = 0

	normal := priorityCandidate(1, 0.12, 10) // concurrency 10, current 1 => band 1
	normalSignal := PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 5000}
	require.Equal(t, "insufficient", applyPriorityCandidate(cfg, &normal, normalSignal, now).Tier)

	recovering := priorityCandidate(2, 0.10, 0)
	recovering.account.Concurrency = 10000 // current 0 => band 0
	badAt := now.Add(-11 * time.Minute)
	recoveringSignal := PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 16000, LatestBadLatencyAt: &badAt}
	score := applyPriorityCandidate(cfg, &recovering, recoveringSignal, now)
	require.True(t, score.LatencyRecovery)
	require.True(t, recovering.priorityLatencyRecovery)
	require.Equal(t, "insufficient", score.Tier, "recovery remains insufficient, but must not enter the normal race")

	require.Less(t, priorityCapacityBand(recovering), priorityCapacityBand(normal), "recovery is the lower capacity band on purpose")

	counts := map[int64]int{}
	for i := 0; i < 2000; i++ {
		order := buildPrioritySelectionOrder([]openAIAccountCandidateScore{normal, recovering}, OpenAIAccountScheduleRequest{SessionHash: fmt.Sprint(i)})
		counts[order[0].account.ID]++
	}
	require.Equal(t, 2000, counts[1], "a recovery candidate must not outrank a normal candidate: %v", counts)
	require.Zero(t, counts[2])
}

// An actively penalized account must stay below a normal account even when it
// is idle (band 0) and the normal account is already serving (band 1).
func TestPriorityActivePenaltyRanksAfterNormalCandidate(t *testing.T) {
	now := time.Now()
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	cfg.TargetTTFTMs = 15000
	cfg.MinSamples = 2

	normal := priorityCandidate(1, 0.12, 10)
	normalSignal := PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 5000}
	applyPriorityCandidate(cfg, &normal, normalSignal, now)

	penalized := priorityCandidate(2, 0.10, 0)
	penalized.account.Concurrency = 10000
	badAt := now.Add(-time.Minute)
	score := applyPriorityCandidate(cfg, &penalized, PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 16000, LatestBadLatencyAt: &badAt}, now)
	require.Equal(t, "degraded", score.Tier)
	require.True(t, penalized.priorityLatencyPenalty)
	require.Less(t, priorityCapacityBand(penalized), priorityCapacityBand(normal))

	for i := 0; i < 500; i++ {
		order := buildPrioritySelectionOrder([]openAIAccountCandidateScore{normal, penalized}, OpenAIAccountScheduleRequest{SessionHash: fmt.Sprint(i)})
		require.Equal(t, int64(1), order[0].account.ID)
	}
}

// When quality history is missing there is no eligible cohort. The recovery
// exploration lane must still work instead of silently doing nothing.
func TestPriorityRecoveryExplorationWorksWithoutEligibleCohort(t *testing.T) {
	now := time.Now()
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	cfg.TargetTTFTMs = 15000
	cfg.MinSamples = 2
	cfg.RecoveryExplorationPercent = 10

	normal := priorityCandidate(1, 0.12, 0)
	normalScore := applyPriorityCandidate(cfg, &normal, PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 5000}, now)
	require.Equal(t, "insufficient", normalScore.Tier)

	passed := true
	recovering := priorityCandidate(2, 0.10, 0)
	badAt := now.Add(-11 * time.Minute)
	signal := PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 16000, QualitySamples: 10, QualityPassed: 10, LatestQualityPassed: &passed, LatestBadLatencyAt: &badAt}
	score := applyPriorityCandidate(cfg, &recovering, signal, now)
	require.True(t, score.LatencyRecovery)
	require.True(t, recovering.priorityExploration)

	counts := map[int64]int{}
	for i := 0; i < 5000; i++ {
		order := buildPrioritySelectionOrder([]openAIAccountCandidateScore{normal, recovering}, OpenAIAccountScheduleRequest{SessionHash: fmt.Sprintf("recovery-no-eligible-%d", i)})
		counts[order[0].account.ID]++
	}
	require.InDelta(t, 500, counts[2], 100, "recovery exploration should stay near 10%%: %v", counts)
	require.Greater(t, counts[1], 4300)
}

func TestPriorityPoolNeedsFallback(t *testing.T) {
	require.False(t, priorityPoolNeedsFallback(nil))
	require.False(t, priorityPoolNeedsFallback([]openAIAccountCandidateScore{{}}))
	require.False(t, priorityPoolNeedsFallback([]openAIAccountCandidateScore{{priorityLatencyRecovery: true}, {}}))
	require.True(t, priorityPoolNeedsFallback([]openAIAccountCandidateScore{{priorityLatencyRecovery: true}}))
	require.True(t, priorityPoolNeedsFallback([]openAIAccountCandidateScore{{priorityLatencyPenalty: true}}))
	require.True(t, priorityPoolNeedsFallback([]openAIAccountCandidateScore{{priorityUnhealthy: true}}))
}

func TestPrioritySnapshotMarksRecoveryOnlyPoolAsFallback(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.Enabled = true
	cfg.TargetTTFTMs = 15000
	cfg.MinSamples = 2
	now := time.Now()
	badAt := now.Add(-11 * time.Minute)
	r := &priorityReaderStub{signal: map[int64]PrioritySchedulingSignal{2: {Samples: 10, P90TTFTMs: 16000, LatestBadLatencyAt: &badAt}}}
	gateway := priorityGateway(cfg, r)
	scheduler, ok := newDefaultOpenAIAccountScheduler(gateway, nil).(*defaultOpenAIAccountScheduler)
	require.True(t, ok)
	item := priorityCandidate(2, 0.1, 0)
	item.priorityLatencyRecovery = true
	scheduler.recordPrioritySnapshot(OpenAIAccountScheduleRequest{Platform: PlatformOpenAI, RequestedModel: "m", UseUpstreamTokenCost: true}, cfg, []openAIAccountCandidateScore{item}, now)
	snap := gateway.PrioritySchedulingSnapshot()
	require.NotNil(t, snap)
	require.True(t, snap.LatencyFallback)
	require.Equal(t, PriorityLatencyDeprioritize, snap.LatencyPenaltyMode)
}

func TestPrioritySelectionWeightCapsConfiguredConcurrency(t *testing.T) {
	now := time.Now()
	makeCandidate := func(id int64, concurrency int) openAIAccountCandidateScore {
		item := priorityCandidate(id, 0.1, 0)
		item.account.Concurrency = concurrency
		item.score = 250
		return item
	}
	small := prioritySelectionWeight(makeCandidate(1, 8), now)
	large := prioritySelectionWeight(makeCandidate(2, 10000), now)
	huge := prioritySelectionWeight(makeCandidate(3, 100000), now)
	require.Less(t, small, large)
	require.InDelta(t, large, huge, 1e-9, "configured concurrency above the cap must not add weight")
}
