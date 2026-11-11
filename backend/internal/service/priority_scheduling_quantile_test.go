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

func TestPriorityQuantileCacheSeparatesMeasurementTargetAndRevision(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	req := OpenAIAccountScheduleRequest{RequestedModel: "m"}
	key := priorityHistoryKey(req, cfg)
	for _, mutate := range []func(*PrioritySchedulingConfig){
		func(c *PrioritySchedulingConfig) { c.LatencyPercentile = 70 },
		func(c *PrioritySchedulingConfig) { c.LatencyMeasurement = "attributed_v1" },
		func(c *PrioritySchedulingConfig) { c.TargetTTFTMs = 9000 },
		func(c *PrioritySchedulingConfig) { c.RecoveryExplorationPercent = 5 },
	} {
		changed := cfg
		mutate(&changed)
		require.NotEqual(t, key, priorityHistoryKey(req, changed))
	}
	signal := withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 20, P90TTFTMs: 1, LatencyEvidenceVersion: 1,
		LatencyPercentile: 90, LatencyMeasurement: "legacy_forward", QualitySamples: 10, QualityPassed: 10})
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
