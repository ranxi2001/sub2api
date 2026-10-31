package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPriorityQuantileLegacyConfigAndDefaults(t *testing.T) {
	cfg, err := (&SettingService{settingRepo: &prioritySettingStub{value: `{"mode":"balanced","target_ttft_ms":15000}`}}).GetPrioritySchedulingConfig(context.Background())
	require.NoError(t, err)
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	var values map[string]any
	require.NoError(t, json.Unmarshal(raw, &values))
	require.Equal(t, float64(90), values["latency_percentile"])
	require.Equal(t, "legacy_forward", values["latency_measurement"])
	require.Equal(t, float64(0), values["recovery_exploration_percent"])
	require.Equal(t, 15000, DefaultPrioritySchedulingConfig().TargetTTFTMs)
	for _, q := range []int{50, 70, 90, 95, 0, 99} {
		cfg := DefaultPrioritySchedulingConfig()
		require.NoError(t, json.Unmarshal([]byte(`{"latency_percentile":`+jsonNumber(q)+`}`), &cfg))
		if q == 0 || q == 99 {
			require.Error(t, ValidatePrioritySchedulingConfig(cfg))
		} else {
			require.NoError(t, ValidatePrioritySchedulingConfig(cfg))
		}
	}
}

func jsonNumber(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestPrioritySelectedQuantileDoesNotRelabelP90(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	require.NoError(t, json.Unmarshal([]byte(`{"latency_percentile":70}`), &cfg))
	var signal PrioritySchedulingSignal
	require.NoError(t, json.Unmarshal([]byte(`{"samples":20,"p90_ttft_ms":30000,"selected_ttft_ms":9000,"latency_percentile":70,"latency_measurement":"legacy_forward","latency_evidence_version":1,"quality_samples":10,"quality_passed":10}`), &signal))
	score := scorePriorityCandidate(cfg, priorityCandidate(1, .1, 0), signal, time.Now())
	require.Equal(t, "eligible", score.Tier)
	require.Equal(t, 30000.0, score.P90TTFTMs)
	var values map[string]any
	b, _ := json.Marshal(score)
	require.NoError(t, json.Unmarshal(b, &values))
	require.Equal(t, 9000.0, values["selected_ttft_ms"])
}

func TestPriorityNewFastEvidenceDoesNotRenewPenalty(t *testing.T) {
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	bad := now.Add(-11 * time.Minute)
	cfg := DefaultPrioritySchedulingConfig()
	var signal PrioritySchedulingSignal
	raw := `{"samples":20,"p90_ttft_ms":30000,"selected_ttft_ms":30000,"latency_evidence_version":1,"latency_percentile":90,"latency_measurement":"legacy_forward","latest_latency_at":"` + now.Format(time.RFC3339) + `","latest_bad_latency_at":"` + bad.Format(time.RFC3339) + `","quality_samples":10,"quality_passed":10,"latest_quality_passed":true}`
	require.NoError(t, json.Unmarshal([]byte(raw), &signal))
	score := scorePriorityCandidate(cfg, priorityCandidate(1, .1, 0), signal, now)
	require.True(t, score.LatencyRecovery, "new fast traffic cannot extend an old bad sample's deadline")
	require.Equal(t, bad.Add(10*time.Minute), *score.LatencyPenaltyUntil)
	// Only a genuinely new bad sample renews the deadline.
	require.NoError(t, json.Unmarshal([]byte(`{"latest_bad_latency_at":"`+now.Format(time.RFC3339)+`"}`), &signal))
	score = scorePriorityCandidate(cfg, priorityCandidate(1, .1, 0), signal, now)
	require.False(t, score.LatencyRecovery)
	require.Equal(t, now.Add(10*time.Minute), *score.LatencyPenaltyUntil)
}

func TestPriorityAttributedMissingIsUnknownNotFast(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	require.NoError(t, json.Unmarshal([]byte(`{"latency_measurement":"attributed_v1"}`), &cfg))
	signal := PrioritySchedulingSignal{Samples: 0, P90TTFTMs: 0, QualitySamples: 10, QualityPassed: 10}
	score := scorePriorityCandidate(cfg, priorityCandidate(1, .1, 0), signal, time.Now())
	require.Equal(t, "insufficient", score.Tier)
	require.Contains(t, score.Reasons, "latency_insufficient")
}

func TestPriorityQuantileRecoveryIsOptInAndKeepsRiskGates(t *testing.T) {
	now := time.Now()
	bad := now.Add(-11 * time.Minute)
	passed := true
	value := 30000.0
	cfg := DefaultPrioritySchedulingConfig()
	signal := PrioritySchedulingSignal{Samples: 20, SelectedTTFTMs: &value, P90TTFTMs: value,
		LatencyPercentile: 90, LatencyMeasurement: "legacy_forward", LatencyEvidenceVersion: 1,
		LatestBadLatencyAt: &bad, LatestLatencyAt: &now, QualitySamples: 10, QualityPassed: 10,
		LatestQualityPassed: &passed}
	item := priorityCandidate(1, .1, 0)
	score := applyPriorityCandidate(cfg, &item, signal, now)
	require.True(t, score.LatencyRecovery)
	require.False(t, item.priorityExploration, "default recovery exploration is disabled")
	cfg.RecoveryExplorationPercent = 5
	applyPriorityCandidate(cfg, &item, signal, now)
	require.True(t, item.priorityExploration)
	for _, risk := range []string{"unknown_quality", "bad_quality", "latest_failed", "errors", "scoped_errors", "loss"} {
		t.Run(risk, func(t *testing.T) {
			sample := signal
			candidate := priorityCandidate(1, .1, 0)
			switch risk {
			case "unknown_quality":
				sample.QualitySamples = 0
			case "bad_quality":
				sample.QualityPassed = 0
			case "latest_failed":
				failed := false
				sample.LatestQualityPassed = &failed
			case "errors":
				candidate.errorRate = .8
			case "scoped_errors":
				candidate.priorityErrors.SmoothedRate = .8
			case "loss":
				sample.ProfitSamples = 20
				sample.Revenue = 1
				sample.BaseCost = 100
			}
			localConfig := cfg
			if risk == "errors" {
				localConfig.ErrorRateWeight = 0
			} // Existing account EWMA contract.
			applyPriorityCandidate(localConfig, &candidate, sample, now)
			require.False(t, candidate.priorityExploration)
		})
	}
	cfg.RecoveryExplorationPercent = 11
	require.Error(t, ValidatePrioritySchedulingConfig(cfg))
}

func TestPriorityQuantileCacheSeparatesMeasurementTargetAndRevision(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	req := OpenAIAccountScheduleRequest{RequestedModel: "m"}
	key := priorityHistoryKey(req, cfg)
	for _, mutate := range []func(*PrioritySchedulingConfig){
		func(c *PrioritySchedulingConfig) { c.LatencyPercentile = 70 },
		func(c *PrioritySchedulingConfig) { c.LatencyMeasurement = "attributed_v1" },
		func(c *PrioritySchedulingConfig) { c.TargetTTFTMs = 9000 },
	} {
		changed := cfg
		mutate(&changed)
		require.NotEqual(t, key, priorityHistoryKey(req, changed))
	}
	changed := cfg
	changed.RecoveryExplorationPercent = 5
	require.Equal(t, key, priorityHistoryKey(req, changed), "exploration is a policy change, not a new statistical population")
	signal := PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 1, LatencyEvidenceVersion: 1,
		LatencyPercentile: 90, LatencyMeasurement: "legacy_forward", QualitySamples: 10, QualityPassed: 10}
	cfg.LatencyMeasurement = "attributed_v1"
	score := scorePriorityCandidate(cfg, priorityCandidate(1, .1, 0), signal, time.Now())
	require.Equal(t, "insufficient", score.Tier)
	require.Nil(t, score.SelectedTTFTMs)
	raw, err := json.Marshal(score)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	require.Nil(t, body["selected_ttft_ms"], "unknown is not serialized as zero latency")
}
