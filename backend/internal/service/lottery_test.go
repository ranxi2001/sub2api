package service

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLotteryTicketAccounting(t *testing.T) {
	for _, tc := range []struct {
		name, mode                            string
		threshold, total, before, after, want int64
	}{
		{"single below", "single", 100, 0, 0, 99, 0},
		{"single exact", "single", 100, 0, 0, 100, 1},
		{"single large", "single", 100, 0, 0, 250, 1},
		{"single separate small recharges", "single", 100, 50, 0, 50, 0},
		{"cumulative carry", "cumulative", 100, 50, 0, 200, 2},
		{"partial refund cumulative", "cumulative", 100, 250, 250, 50, -2},
		{"partial refund single", "single", 100, 100, 100, 99, -1},
		{"refund still eligible", "single", 100, 250, 250, 200, 0},
		{"refund unrelated balance retained", "cumulative", 100, 350, 250, 50, -2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := lotteryTicketDelta(tc.mode, tc.threshold, tc.total, tc.before, tc.after)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	_, err := lotteryTicketDelta("cumulative", 100, math.MaxInt64, 0, 1)
	require.Error(t, err)
}

func TestLotteryMoneyPrecision(t *testing.T) {
	for _, amount := range []float64{0, 0.01, 0.1, 1.23456789, 999999999.99} {
		units, err := lotteryUnits(amount)
		require.NoError(t, err)
		require.Equal(t, amount, lotteryAmount(units))
	}
	for _, amount := range []float64{-1, math.NaN(), math.Inf(1), 1e10, 0.000000001} {
		_, err := lotteryUnits(amount)
		require.Error(t, err)
	}
}

func TestLotteryWeightedSelection(t *testing.T) {
	prizes := []LotteryPrizeView{{ID: 1, Weight: 2, Available: true}, {ID: 2, Weight: 1000, Available: false}, {ID: 3, Weight: 3, Available: true}}
	for n, expected := range []int64{1, 1, 3, 3, 3} {
		require.Equal(t, expected, selectLotteryPrize(prizes, int64(n)).ID)
	}
	require.Nil(t, selectLotteryPrize(prizes, 5))
}
