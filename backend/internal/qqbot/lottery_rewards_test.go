package qqbot

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLotteryPrizeChancesValidationAndLegacyCompatibility(t *testing.T) {
	legacy := defaultLotteryConfig()
	require.NoError(t, json.Unmarshal([]byte(`{"prizes":[{"name":"legacy","amount":1,"probability_bps":10000}]}`), &legacy))
	require.Zero(t, legacy.Prizes[0].Chances)
	require.NoError(t, validateLottery(legacy))
	for _, chances := range []int{0, 1, 1000} {
		cfg := defaultLotteryConfig()
		cfg.Prizes = []LotteryPrize{{Name: "再来一次", Chances: chances, ProbabilityBPS: 10000}}
		require.NoError(t, validateLottery(cfg))
		cfg.Prizes[0].Amount = .12345678
		require.NoError(t, validateLottery(cfg))
	}
	for _, chances := range []int{-1, 1001} {
		cfg := defaultLotteryConfig()
		cfg.Prizes[0].Chances = chances
		require.Error(t, validateLottery(cfg))
		cfg = defaultLotteryConfig()
		cfg.GuaranteeEnabled = true
		cfg.GuaranteePrizes[0].Chances = chances
		require.Error(t, validateLottery(cfg))
	}
	cfg := defaultLotteryConfig()
	cfg.GuaranteeEnabled = true
	cfg.GuaranteePrizes = []LotteryPrize{{Name: "保底再来一次", Chances: 1, ProbabilityBPS: 10000}}
	require.NoError(t, validateLottery(cfg))
	cfg.GuaranteePrizes[0].Chances = 0
	require.Error(t, validateLottery(cfg))
}

func TestLotteryPrizeDescriptionsAndReturnedChances(t *testing.T) {
	require.Equal(t, "USD 0.00", (LotteryPrize{}).description())
	require.Equal(t, "抽奖次数 2 次", (LotteryPrize{Chances: 2}).description())
	require.Equal(t, "USD 1.25 + 抽奖次数 3 次", (LotteryPrize{Amount: 1.25, Chances: 3}).description())
	state := LotteryState{Remaining: 4, Used: 1}
	cfg := defaultLotteryConfig()
	cfg.GuaranteeEnabled, cfg.GuaranteeEvery = true, 3
	result := lotteryResult(cfg, state, []lotteryOutcome{{prize: LotteryPrize{Name: "再来一次", Chances: 1}}})
	require.Contains(t, result, "奖励抽奖次数：1 次")
	require.Contains(t, result, "剩余抽奖次数：4 次")
	require.Contains(t, result, "距保底 1 次")
	result = lotteryResult(cfg, state, []lotteryOutcome{
		{prize: LotteryPrize{Name: "双倍幸运", Amount: .125, Chances: 2}},
		{prize: LotteryPrize{Name: "双倍幸运", Amount: .125, Chances: 2}, guaranteed: true},
		{prize: LotteryPrize{Name: "谢谢参与"}},
	})
	require.Contains(t, result, "双倍幸运 × 2")
	require.Contains(t, result, "合计奖励额度：USD 0.25")
	require.Contains(t, result, "合计奖励抽奖次数：4 次")
	require.Contains(t, result, "剩余抽奖次数：5 次")
	require.Contains(t, result, "本次触发保底抽奖 1 次")
}
