//go:build unit

package service

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOAuthDegradationThresholdEvidence(t *testing.T) {
	threshold := 50.0
	c := DefaultOAuthDegradationConfig()
	c.Enabled = true
	c.ThresholdPercent = &threshold
	for _, test := range []struct {
		name    string
		value   *float64
		samples int
		want    bool
	}{
		{"nil", nil, 3, false}, {"zero samples", &threshold, 0, false}, {"negative samples", &threshold, -1, false},
		{"low", floatPtrDegradation(49.9), 2, false}, {"equal", &threshold, 2, true}, {"high", floatPtrDegradation(90), 2, true},
		{"NaN", floatPtrDegradation(math.NaN()), 2, false}, {"infinite", floatPtrDegradation(math.Inf(1)), 2, false}, {"negative", floatPtrDegradation(-1), 2, false}, {"over 100", floatPtrDegradation(101), 2, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, c.Triggered(AccountPerformance{DegradationPercent: test.value, QualitySamples: test.samples}))
		})
	}
	c.Enabled = false
	require.False(t, c.Triggered(AccountPerformance{DegradationPercent: &threshold, QualitySamples: 2}))
	c.Enabled = true
	c.ThresholdPercent = nil
	require.Error(t, c.Validate())
	c.ThresholdPercent = floatPtrDegradation(0)
	require.NoError(t, c.Validate())
	c.ThresholdPercent = floatPtrDegradation(100)
	require.NoError(t, c.Validate())
	c.ThresholdPercent = floatPtrDegradation(math.NaN())
	require.Error(t, c.Validate())
}
func floatPtrDegradation(v float64) *float64 { return &v }

func TestOAuthDegradationZeroConcurrencyDoesNotReviveHistoricalModelScope(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 0, Credentials: map[string]any{}, Extra: map[string]any{OAuthDegradationActionExtraKey: map[string]any{"models": []string{"gpt-6-luna"}, "concurrency_cap": 0}}}
	require.True(t, a.IsModelSupported("gpt-6-luna"))
	require.True(t, a.IsModelSupported("gpt-6.1-sol"), "native model configuration controls availability independently of concurrency")
}

func TestOAuthDegradationFiniteConcurrencyCannotBecomeUnlimited(t *testing.T) {
	for _, proposed := range []int{-1, 0, 3, 10, 20} {
		a := &Account{Concurrency: 5, Extra: map[string]any{AutoConfigConcurrencyExtraKey: map[string]any{"degradation_hold": true}, OAuthDegradationActionExtraKey: map[string]any{"models": []string{"gpt-6-luna"}, "concurrency_cap": 10}}}
		expected := 5
		if proposed > 0 {
			expected = min(proposed, 5)
		}
		require.Equal(t, expected, a.LimitOAuthDegradationConcurrency(proposed))
	}
}

func TestOAuthDegradationAutomaticRecoveryUsesCurrentNativeConcurrency(t *testing.T) {
	for _, tc := range []struct {
		name              string
		current, proposed int
		hold              bool
		want              int
	}{
		{"active hold keeps current cap", 30, 60, true, 30},
		{"active hold permits tightening", 30, 20, true, 20},
		{"manual edit cleared hold", 30, 60, false, 60},
		{"manual unlimited setting", 0, 60, true, 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{Concurrency: tc.current, Extra: map[string]any{
				AutoConfigConcurrencyExtraKey:  map[string]any{"degradation_hold": tc.hold},
				OAuthDegradationActionExtraKey: map[string]any{"concurrency_cap": 10},
			}}
			require.Equal(t, tc.want, a.LimitOAuthDegradationConcurrency(tc.proposed), "the historical cap must not override an explicit native edit")
		})
	}
}
