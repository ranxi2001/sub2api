package qqbot

import (
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

func TestGrowthRewardValidationAndLegacyFallback(t *testing.T) {
	for _, reward := range []GrowthReward{
		{Type: "chances", Quantity: 2}, {Type: "chances"}, {Type: "balance", Amount: 0.00000001}, {Type: "balance", Amount: 1000},
		{Type: "physical", Quantity: 1, PrizeName: "cup"}, {Type: "other", Quantity: 2, PrizeName: "service"},
	} {
		require.NoError(t, reward.validate())
	}
	for _, reward := range []GrowthReward{
		{Type: "unknown"}, {Type: "balance", Amount: math.NaN()}, {Type: "balance", Amount: 0.000000001},
		{Type: "balance", Amount: 1.000000001}, {Type: "balance", Amount: 1001}, {Type: "balance", Amount: -1},
		{Type: "balance", Amount: 1, Quantity: 1}, {Type: "chances", Amount: 1, Quantity: 1}, {Type: "chances", Quantity: -1},
		{Type: "chances", Quantity: 1001}, {Type: "physical", Quantity: 1}, {Type: "other", Quantity: 1, PrizeName: "bad\nname"},
	} {
		require.Error(t, reward.validate(), "%+v", reward)
	}
	require.Equal(t, 2, (GrowthTask{Chances: 2}).reward().Quantity)
	require.Equal(t, 3, (CheckinConfig{StreakChances: 3}).streakReward().Quantity)
	require.False(t, (GrowthTask{Chances: 5, Reward: &GrowthReward{Type: "balance"}}).reward().enabled(), "explicit zero must override legacy chances")
}

func TestGrowthTokenAndSpendingThresholds(t *testing.T) {
	p := growthProgress{tokens: decimal.NewFromInt(999), spent: decimal.RequireFromString("9.9999999999")}
	for _, kind := range []string{"usage_tokens", "usage_amount"} {
		target := float64(1000)
		if kind == "usage_amount" {
			target = 10
		}
		eligible, text := p.task(GrowthTask{Kind: kind, Target: target})
		require.False(t, eligible)
		if kind == "usage_amount" {
			require.Equal(t, "USD 9.99/10.00", text)
		}
	}
	for _, tc := range []struct {
		kind   string
		target float64
		valid  bool
	}{
		{"usage_tokens", 1, true}, {"usage_tokens", 1e15, true}, {"usage_tokens", 0, false}, {"usage_tokens", 1.5, false}, {"usage_tokens", 1e15 + 1, false},
		{"usage_amount", 0.01, true}, {"usage_amount", 1e9, true}, {"usage_amount", 0, false}, {"usage_amount", 1.001, false}, {"usage_amount", 1e9 + 1, false},
	} {
		err := validateGrowth(CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{{ID: "usage", Name: "Usage", Kind: tc.kind, Target: tc.target}}}})
		if tc.valid {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}
