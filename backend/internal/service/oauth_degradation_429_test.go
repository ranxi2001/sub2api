package service

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamstats"
	"github.com/stretchr/testify/require"
)

func TestOAuthDegradation429Conditions(t *testing.T) {
	c := DefaultOAuthDegradationConfig()
	c.Enabled = true
	c.ThresholdPercent = floatPtrDegradation(9)
	for _, tc := range []struct {
		name                     string
		total, failures, limited int64
		want                     string
	}{
		{"absent", 0, 0, 0, ""}, {"small", 19, 19, 19, ""}, {"minimum", 20, 16, 16, "upstream_429"},
		{"both boundaries", 25, 20, 16, "upstream_429"}, {"low error rate", 25, 19, 19, ""},
		{"low 429 share", 25, 20, 15, ""}, {"5xx only", 25, 25, 0, ""}, {"healthy", 25, 0, 0, ""},
		{"invalid failures", 20, 21, 20, ""}, {"invalid 429", 20, 16, 17, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := AccountPerformance{UpstreamError: upstreamstats.Evidence{Source: "process_local_http_responses", Total: tc.total, Failures: tc.failures, RateLimited: tc.limited}}
			require.Equal(t, tc.want, c.TriggerReason(m), "missing probe results must not hide HTTP evidence")
			m.DegradationPercent = floatPtrDegradation(0)
			m.QualitySamples = 2
			require.Equal(t, tc.want, c.TriggerReason(m), "a healthy probe does not erase HTTP pressure")
		})
	}
	m := AccountPerformance{DegradationPercent: floatPtrDegradation(100), QualitySamples: 1, UpstreamError: upstreamstats.Evidence{Source: "process_local_http_responses", Total: 25, Failures: 20, RateLimited: 16}}
	off := false
	c.ProbeEnabled = &off
	require.Equal(t, "upstream_429", c.TriggerReason(m))
	c.Upstream429Enabled = &off
	require.Error(t, c.Validate())
	require.Empty(t, c.TriggerReason(m))
	c.ProbeEnabled = nil
	require.Equal(t, "probe_degradation", c.TriggerReason(m))
	c.Enabled = false
	require.Empty(t, c.TriggerReason(m))
	c.Enabled = true
	c.ProbeEnabled = &off
	c.Upstream429Enabled = nil
	c.ThresholdPercent = nil
	require.NoError(t, c.Validate(), "429-only does not require a probe threshold")
	require.Equal(t, "upstream_429", c.TriggerReason(m))
	c.UpstreamErrorThresholdPercent = floatPtrDegradation(81)
	require.Empty(t, c.TriggerReason(m))
	c.UpstreamErrorThresholdPercent = nil
	c.Upstream429ShareThresholdPercent = floatPtrDegradation(81)
	require.Empty(t, c.TriggerReason(m))
	for _, invalid := range []float64{0, -1, 101, math.NaN(), math.Inf(1)} {
		c.Upstream429ShareThresholdPercent = floatPtrDegradation(invalid)
		require.Error(t, c.Validate())
	}
	var legacy OAuthDegradationConfig
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"threshold_percent":9,"target_concurrency":10,"models":[]}`), &legacy))
	require.True(t, legacy.UsesProbe())
	require.True(t, legacy.UsesUpstream429())
	require.Equal(t, 80.0, legacy.HTTPErrorThreshold())
	require.Equal(t, 80.0, legacy.HTTP429ShareThreshold())
}
