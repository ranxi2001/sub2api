package qqbot

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckinConfigurableDistribution(t *testing.T) {
	// Exhaust every probability roll and within-band outcome, without flaky
	// statistical sampling. Each band size divides n in these ranges.
	for _, distribution := range []*CheckinRewardDistribution{
		nil, {1234, 4321, 4445}, {10000, 0, 0}, {0, 10000, 0}, {0, 0, 10000},
	} {
		for _, n := range []int64{4, 8, 20} {
			t.Run(fmt.Sprintf("%v/%d", distribution, n), func(t *testing.T) {
				const lo int64 = 12345678
				c := CheckinConfig{MinAmount: float64(lo) / 1e8, MaxAmount: float64(lo+n-1) / 1e8, RewardDistribution: distribution}
				counts := make([]int64, n)
				for roll := range int64(10000) {
					for sample := range n {
						calls := 0
						amount, err := randomCheckinAmountWith(c, func(limit int64) (int64, error) {
							calls++
							if calls == 1 {
								return roll, nil
							}
							return sample % limit, nil
						})
						require.NoError(t, err)
						require.Equal(t, 2, calls)
						offset := int64(math.Round(amount*1e8)) - lo
						require.GreaterOrEqual(t, offset, int64(0))
						require.Less(t, offset, n)
						counts[offset]++
					}
				}
				d := c.rewardDistribution()
				weights := []int{d.LowerHalfBPS, d.NextQuarterBPS, d.UpperQuarterBPS}
				sizes := []int64{n / 2, n / 4, n / 4}
				var totalOffset int64
				for k, count := range counts {
					band := 0
					if int64(k) >= n/2 {
						band = 1
					}
					if int64(k) >= 3*n/4 {
						band = 2
					}
					require.Equal(t, n*int64(weights[band])/sizes[band], count)
					totalOffset += int64(k) * count
				}
				mean := (float64(lo) + float64(totalOffset)/float64(n*10000)) / 1e8
				require.InDelta(t, mean, checkinExpectedAmount(c), 1e-15)
			})
		}
	}
}

func TestCheckinWeightedEndpointsAndNarrowRanges(t *testing.T) {
	for _, tc := range []struct {
		roll int64
		last bool
		want float64
	}{
		{0, false, 0}, {6999, true, 499.99999999},
		{7000, false, 500}, {8999, true, 749.99999999},
		{9000, false, 750}, {9999, true, 1000},
	} {
		calls := 0
		amount, err := randomCheckinAmountWith(CheckinConfig{MaxAmount: 1000}, func(limit int64) (int64, error) {
			calls++
			if calls == 1 {
				require.Equal(t, int64(10000), limit)
				return tc.roll, nil
			}
			if tc.last {
				return limit - 1, nil
			}
			return 0, nil
		})
		require.NoError(t, err)
		require.Equal(t, tc.want, amount)
	}
	require.InDelta(t, 387.4999999955, checkinExpectedAmount(CheckinConfig{MaxAmount: 1000}), 1e-10)
	for _, span := range []int64{1, 2, 3, 4, 5, 7} {
		c := CheckinConfig{MinAmount: .1, MaxAmount: .1 + float64(span)/1e8}
		var mean float64
		for band, roll := range []int64{0, 7000, 9000} {
			weights := []float64{.7, .2, .1}
			var first, last float64
			for _, useLast := range []bool{false, true} {
				calls := 0
				amount, err := randomCheckinAmountWith(c, func(limit int64) (int64, error) {
					calls++
					if calls == 1 {
						return roll, nil
					}
					require.Positive(t, limit)
					if useLast {
						return limit - 1, nil
					}
					return 0, nil
				})
				require.NoError(t, err)
				require.GreaterOrEqual(t, amount, c.MinAmount)
				require.LessOrEqual(t, amount, c.MaxAmount+1e-16)
				if useLast {
					last = amount
				} else {
					first = amount
				}
			}
			mean += (first + last) / 2 * weights[band]
		}
		require.InDelta(t, mean, checkinExpectedAmount(c), 1e-15)
	}
}

func TestCheckinWeightedFixedAndInvalidRanges(t *testing.T) {
	neverDraw := func(int64) (int64, error) {
		t.Fatal("fixed or invalid configuration must not consume randomness")
		return 0, nil
	}
	for _, value := range []float64{0, .12345678, 1000} {
		c := CheckinConfig{MinAmount: value, MaxAmount: value}
		amount, err := randomCheckinAmountWith(c, neverDraw)
		require.NoError(t, err)
		require.Equal(t, value, amount)
		require.InDelta(t, value, checkinExpectedAmount(c), 1e-12)
	}
	for _, c := range []CheckinConfig{
		{MinAmount: -1}, {MinAmount: 2, MaxAmount: 1}, {MaxAmount: 1001},
		{MaxAmount: .000000001}, {MaxAmount: math.NaN()}, {MaxAmount: math.Inf(1)},
		{RewardDistribution: &CheckinRewardDistribution{}},
		{RewardDistribution: &CheckinRewardDistribution{7000, 2000, 999}},
		{RewardDistribution: &CheckinRewardDistribution{-1, 10000, 1}},
		{RewardDistribution: &CheckinRewardDistribution{10001, 0, 0}},
	} {
		amount, err := randomCheckinAmountWith(c, neverDraw)
		require.Error(t, err)
		require.Zero(t, amount)
	}
	var c CheckinConfig
	require.Error(t, json.Unmarshal([]byte(`{"reward_distribution":{"lower_half_bps":1234.5}}`), &c))
	c = CheckinConfig{}
	require.NoError(t, json.Unmarshal([]byte(`{"min_amount":0.1,"max_amount":1}`), &c))
	require.Equal(t, CheckinRewardDistribution{7000, 2000, 1000}, c.rewardDistribution())
}

func TestCheckinWeightedRandomErrors(t *testing.T) {
	wantErr := errors.New("random source unavailable")
	for failAt := range 2 {
		calls := 0
		amount, err := randomCheckinAmountWith(CheckinConfig{MinAmount: 1, MaxAmount: 3}, func(int64) (int64, error) {
			calls++
			if calls-1 == failAt {
				return 0, wantErr
			}
			return 1, nil
		})
		require.ErrorIs(t, err, wantErr)
		require.Zero(t, amount)
		require.Equal(t, failAt+1, calls)
	}
}
