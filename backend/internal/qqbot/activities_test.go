package qqbot

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckinValidationAndRandomPrecision(t *testing.T) {
	for _, c := range []CheckinConfig{
		{MinAmount: -1, MaxAmount: 1}, {MinAmount: 2, MaxAmount: 1}, {MaxAmount: 1001},
		{MaxAmount: math.NaN()}, {MaxAmount: math.Inf(1)}, {MaxAmount: .000000001},
	} {
		require.Error(t, validateCheckin(c))
	}
	for _, c := range []CheckinConfig{
		{MinAmount: 0, MaxAmount: 0}, {MinAmount: .12345678, MaxAmount: .12345678},
		{MinAmount: .00000001, MaxAmount: .00000002}, {MinAmount: 999, MaxAmount: 1000},
	} {
		for range 100 {
			amount, err := randomCheckinAmount(c)
			require.NoError(t, err)
			require.GreaterOrEqual(t, amount, c.MinAmount)
			require.LessOrEqual(t, amount, c.MaxAmount)
			require.InDelta(t, math.Round(amount*1e8), amount*1e8, .001)
		}
	}
}

func TestLotteryGuaranteeValidationAndLegacyConfig(t *testing.T) {
	legacy := defaultLotteryConfig()
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"title":"legacy","recharge_step":10,"chances_per_step":1,"prizes":[{"name":"win","amount":1,"probability_bps":10000}]}`), &legacy))
	require.NoError(t, validateLottery(legacy))
	require.False(t, legacy.GuaranteeEnabled)
	require.Zero(t, legacy.drawsUntilGuarantee(9))

	for name, mutate := range map[string]func(*LotteryConfig){
		"zero interval":           func(c *LotteryConfig) { c.GuaranteeEvery = 0 },
		"negative interval":       func(c *LotteryConfig) { c.GuaranteeEvery = -1 },
		"large interval":          func(c *LotteryConfig) { c.GuaranteeEvery = 1000001 },
		"empty pool":              func(c *LotteryConfig) { c.GuaranteePrizes = nil },
		"large pool":              func(c *LotteryConfig) { c.GuaranteePrizes = make([]LotteryPrize, 21) },
		"empty name":              func(c *LotteryConfig) { c.GuaranteePrizes[0].Name = " " },
		"zero reward":             func(c *LotteryConfig) { c.GuaranteePrizes[0].Amount = 0 },
		"negative reward":         func(c *LotteryConfig) { c.GuaranteePrizes[0].Amount = -1 },
		"large reward":            func(c *LotteryConfig) { c.GuaranteePrizes[0].Amount = 1001 },
		"invalid precision":       func(c *LotteryConfig) { c.GuaranteePrizes[0].Amount = .000000001 },
		"nan reward":              func(c *LotteryConfig) { c.GuaranteePrizes[0].Amount = math.NaN() },
		"infinite reward":         func(c *LotteryConfig) { c.GuaranteePrizes[0].Amount = math.Inf(1) },
		"zero probability":        func(c *LotteryConfig) { c.GuaranteePrizes[0].ProbabilityBPS = 0 },
		"wrong probability total": func(c *LotteryConfig) { c.GuaranteePrizes[0].ProbabilityBPS = 9999 },
	} {
		t.Run(name, func(t *testing.T) {
			c := defaultLotteryConfig()
			c.GuaranteeEnabled = true
			mutate(&c)
			require.Error(t, validateLottery(c))
		})
	}
	for _, interval := range []int64{1, 1000000} {
		c := defaultLotteryConfig()
		c.GuaranteeEnabled, c.GuaranteeEvery = true, interval
		require.NoError(t, validateLottery(c))
	}
}

func TestLotteryGuaranteeProgressAndIndependentDistribution(t *testing.T) {
	c := defaultLotteryConfig()
	c.GuaranteeEnabled, c.GuaranteeEvery = true, 3
	c.GuaranteePrizes = []LotteryPrize{
		{Name: "small guarantee", Amount: .00000001, ProbabilityBPS: 1234},
		{Name: "large guarantee", Amount: 1000, ProbabilityBPS: 8766},
	}
	require.NoError(t, validateLottery(c))
	for used, want := range []int64{3, 2, 1, 3, 2, 1, 3} {
		require.Equal(t, want, c.drawsUntilGuarantee(int64(used)))
	}
	counts := map[string]int{}
	for roll := range int64(10000) {
		counts[lotteryPrizeAt(c.GuaranteePrizes, roll).Name]++
	}
	require.Equal(t, map[string]int{"small guarantee": 1234, "large guarantee": 8766}, counts)
	c.GuaranteeEnabled = false
	require.Zero(t, c.drawsUntilGuarantee(2))
}
func TestLotteryProbabilityDistributionAndValidation(t *testing.T) {
	c := defaultLotteryConfig()
	require.NoError(t, validateLottery(c))
	counts := map[string]int{}
	for i := range int64(10000) {
		counts[lotteryPrizeAt(c.Prizes, i).Name]++
	}
	require.Equal(t, 1000, counts[c.Prizes[0].Name])
	require.Equal(t, 9000, counts[c.Prizes[1].Name])
	for _, mutate := range []func(*LotteryConfig){
		func(c *LotteryConfig) { c.RechargeStep = 0 }, func(c *LotteryConfig) { c.RechargeStep = .001 },
		func(c *LotteryConfig) { c.RechargeStep = math.NaN() }, func(c *LotteryConfig) { c.ChancesPerStep = 0 },
		func(c *LotteryConfig) { c.ChancesPerStep = 1001 }, func(c *LotteryConfig) { c.Title = " " },
		func(c *LotteryConfig) { c.Prizes = nil }, func(c *LotteryConfig) { c.Prizes[0].Amount = -1 },
		func(c *LotteryConfig) { c.Prizes[0].ProbabilityBPS = 0 }, func(c *LotteryConfig) { c.Prizes[0].ProbabilityBPS = 1001 },
	} {
		c := defaultLotteryConfig()
		mutate(&c)
		require.Error(t, validateLottery(c))
	}
}
