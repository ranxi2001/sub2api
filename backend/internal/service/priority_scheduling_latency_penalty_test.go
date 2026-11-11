package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPriorityLatencyPenaltyCompatibilityAndBounds(t *testing.T) {
	cfg := DefaultPrioritySchedulingConfig()
	cfg.TargetTTFTMs = 3000
	// Persisted old configurations inherit the default, while explicit zero
	// retains the prior continuous penalty. Missing timestamps fail closed.
	old, err := json.Marshal(cfg)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(old, &body))
	delete(body, "latency_penalty_minutes")
	old, err = json.Marshal(body)
	require.NoError(t, err)
	settings := &SettingService{settingRepo: &prioritySettingStub{value: string(old)}}
	loaded, err := settings.GetPrioritySchedulingConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, 10, loaded.LatencyPenaltyMinutes)
	for _, minutes := range []int{0, 1, 1440} {
		cfg.LatencyPenaltyMinutes = minutes
		require.NoError(t, ValidatePrioritySchedulingConfig(cfg))
	}
	for _, minutes := range []int{-1, 1441} {
		cfg.LatencyPenaltyMinutes = minutes
		require.Error(t, ValidatePrioritySchedulingConfig(cfg))
	}
	now := time.Now()
	oldAt := now.Add(-time.Hour)
	signal := withAccountPerformanceFixture(PrioritySchedulingSignal{Samples: 7, P90TTFTMs: 5000, QualitySamples: 10, QualityPassed: 10, LatestLatencyAt: &oldAt, LatestBadLatencyAt: &oldAt})
	cfg.LatencyPenaltyMinutes = 0
	require.Equal(t, "degraded", scorePriorityCandidate(cfg, priorityCandidate(1, 0.11, 0), signal, now).Tier)
	cfg.LatencyPenaltyMinutes = 10
	signal.LatestLatencyAt = nil
	signal.LatestBadLatencyAt = nil
	require.Equal(t, "degraded", scorePriorityCandidate(cfg, priorityCandidate(1, 0.11, 0), signal, now).Tier)
}
