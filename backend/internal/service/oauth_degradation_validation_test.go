//go:build unit

package service

import (
	"github.com/stretchr/testify/require"
	"math"
	"testing"
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

func TestOAuthDegradationZeroConcurrencyRetainsModelScope(t *testing.T) {
	a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 0, Credentials: map[string]any{}, Extra: map[string]any{OAuthDegradationActionExtraKey: map[string]any{"models": []string{"gpt-6-luna"}, "concurrency_cap": 0}}}
	require.True(t, a.IsModelSupported("gpt-6-luna"), "non-positive existing concurrency must not corrupt valid model scope")
	require.False(t, a.IsModelSupported("gpt-6.1-sol"))
}

func TestOAuthDegradationFiniteConcurrencyCannotBecomeUnlimited(t *testing.T) {
	for _, proposed := range []int{-1, 0, 3, 10, 20} {
		a := &Account{Concurrency: 5, Extra: map[string]any{OAuthDegradationActionExtraKey: map[string]any{"models": []string{"gpt-6-luna"}, "concurrency_cap": 10}}}
		expected := 5
		if proposed > 0 {
			expected = min(proposed, 5)
		}
		require.Equal(t, expected, a.LimitOAuthDegradationConcurrency(proposed))
	}
}
