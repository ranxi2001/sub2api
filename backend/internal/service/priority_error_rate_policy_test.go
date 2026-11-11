//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPriorityErrorRateWeightedRecoveryAndIsolation(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	c.ErrorRateWeight = 100
	now := time.Now()
	stats := newOpenAIAccountRuntimeStats()
	req := OpenAIAccountScheduleRequest{RequestedModel: "m", RequiredCapability: OpenAIEndpointCapabilityResponses}
	scheduler := &defaultOpenAIAccountScheduler{stats: stats}
	for i := 0; i < 100; i++ {
		stats.attempts.report(priorityAttemptKey{accountID: 1, model: "m", endpoint: "/v1/responses"}, true, now)
		stats.attempts.report(priorityAttemptKey{accountID: 2, model: "m", endpoint: "/v1/responses"}, false, now)
	}
	signal := withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 500, QualityPassed: 10, QualitySamples: 10})
	good, bad := priorityCandidate(1, .1, 0), priorityCandidate(2, .1, 0)
	good.priorityErrors = scheduler.priorityErrorEvidence(req, c, good.account.ID, now)
	bad.priorityErrors = scheduler.priorityErrorEvidence(req, c, bad.account.ID, now)
	sg := applyPriorityCandidate(c, &good, signal, now)
	sb := applyPriorityCandidate(c, &bad, signal, now)
	require.Greater(t, sg.Score, sb.Score)
	require.Greater(t, prioritySelectionWeight(good, now), prioritySelectionWeight(bad, now))
	counts := map[int64]int{}
	for i := 0; i < 2000; i++ {
		req.SessionHash = fmt.Sprint(i)
		order := buildPrioritySelectionOrder([]openAIAccountCandidateScore{good, bad}, req)
		counts[order[0].account.ID]++
	}
	require.Greater(t, counts[good.account.ID], counts[bad.account.ID])
	require.Greater(t, counts[bad.account.ID], 100, "failed account retains recovery traffic")
	req.RequestedModel = "other"
	bad.priorityErrors = scheduler.priorityErrorEvidence(req, c, bad.account.ID, now)
	other := applyPriorityCandidate(c, &bad, signal, now)
	require.InDelta(t, sg.Score, other.Score, 1e-9, "a different model does not inherit failures")
	req.RequestedModel = "m"
	bad.priorityErrors = scheduler.priorityErrorEvidence(req, c, bad.account.ID, now.Add(10*time.Minute))
	recovered := applyPriorityCandidate(c, &bad, signal, now.Add(10*time.Minute))
	require.Nil(t, recovered.PriorityErrorEvidence.ErrorRate)
	require.InDelta(t, sg.Score, recovered.Score, 1e-9)
}

func TestPriorityErrorRateConfigBounds(t *testing.T) {
	for _, change := range []func(*PrioritySchedulingConfig){
		func(c *PrioritySchedulingConfig) { c.ErrorRateWeight = -1 }, func(c *PrioritySchedulingConfig) { c.ErrorRateWeight = 101 }, func(c *PrioritySchedulingConfig) { c.ErrorRateWeight = math.NaN() }, func(c *PrioritySchedulingConfig) { c.ErrorRateWeight = math.Inf(1) },
		func(c *PrioritySchedulingConfig) { c.ErrorRateWindowMinutes = 0 }, func(c *PrioritySchedulingConfig) { c.ErrorRateWindowMinutes = 61 }, func(c *PrioritySchedulingConfig) { c.ErrorRatePriorSamples = 0 }, func(c *PrioritySchedulingConfig) { c.ErrorRatePriorSamples = 1001 },
	} {
		c := DefaultPrioritySchedulingConfig()
		change(&c)
		require.Error(t, ValidatePrioritySchedulingConfig(c))
	}
	c := DefaultPrioritySchedulingConfig()
	c.ErrorRateWeight = 0
	require.NoError(t, ValidatePrioritySchedulingConfig(c))
}

func TestPriorityErrorRateSnapshotJSONAndAdminPriority(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	now := time.Now()
	item := priorityCandidate(1, .1, 0)
	item.priorityErrors = PriorityErrorEvidence{ErrorRate: func() *float64 { v := .25; return &v }(), SmoothedRate: 1.0 / 9, Successes: 3, Failures: 1, WindowMinutes: 10, PriorSamples: 5, ObservedAt: now, Endpoint: "/v1/responses"}
	score := scorePriorityCandidate(c, item, PrioritySchedulingSignal{}, now)
	raw, err := json.Marshal(score)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, .25, decoded["error_rate"])
	require.Equal(t, .1, decoded["rate"], "account cost stays independently visible")
	require.Equal(t, 3.0, decoded["error_successes"])
	require.Equal(t, 1.0, decoded["error_failures"])
	require.Equal(t, 5.0, decoded["error_prior_samples"])
	observed, ok := decoded["error_observed_at"].(string)
	require.True(t, ok, "snapshot JSON must expose when live error evidence was read")
	parsed, err := time.Parse(time.RFC3339Nano, observed)
	require.NoError(t, err)
	require.True(t, parsed.Equal(now))
	signal := withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 500, QualityPassed: 10, QualitySamples: 10})
	bad, good := priorityCandidate(1, .1, 0), priorityCandidate(2, .1, 0)
	bad.account.Priority = 10
	good.account.Priority = 20
	bad.priorityErrors = PriorityErrorEvidence{SmoothedRate: 1}
	applyPriorityCandidate(c, &bad, signal, now)
	applyPriorityCandidate(c, &good, signal, now)
	for i := 0; i < 100; i++ {
		order := buildPrioritySelectionOrder([]openAIAccountCandidateScore{bad, good}, OpenAIAccountScheduleRequest{SessionHash: fmt.Sprint(i)})
		require.Equal(t, bad.account.ID, order[0].account.ID, "new penalty must preserve explicit priority within the cohort")
	}
}

func TestPriorityErrorRateUnknownQualityAndSafeExploration(t *testing.T) {
	c := DefaultPrioritySchedulingConfig()
	item := priorityCandidate(1, .1, 0)
	signal := withAccountPerformanceFixture(PrioritySchedulingSignal{QualityPassed: 10, QualitySamples: 10})
	applyPriorityCandidate(c, &item, signal, time.Now())
	require.True(t, item.priorityExploration)
	item.priorityErrors.SmoothedRate = .8
	score := applyPriorityCandidate(c, &item, signal, time.Now())
	require.False(t, item.priorityExploration)
	require.NotContains(t, score.Reasons, "recent_errors", "scoped errors remain a soft normal-lane penalty")
	signal.QualitySamples = 0
	item.priorityErrors.SmoothedRate = 0
	applyPriorityCandidate(c, &item, signal, time.Now())
	require.False(t, item.priorityExploration)
}
