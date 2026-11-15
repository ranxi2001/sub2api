package qqbot

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestRechargeRequirementValidationAndAlternatives(t *testing.T) {
	for _, amount := range []float64{-1, .001, .00001, 1e9 + .01, math.NaN(), math.Inf(1)} {
		require.Error(t, validateRechargeRequirement(RechargeRequirement{MinRechargeTotal: amount}))
		require.Error(t, validateRechargeRequirement(RechargeRequirement{MinRecharge30d: amount}))
	}
	for _, amount := range []float64{0, .01, .29, 999999999.99, 1e9} {
		require.NoError(t, validateRechargeRequirement(RechargeRequirement{MinRechargeTotal: amount, MinRecharge30d: amount}))
	}
	state := func(total, recent string) RechargeEligibility {
		return RechargeEligibility{Total: decimal.RequireFromString(total), Recent: decimal.RequireFromString(recent)}
	}
	require.True(t, (RechargeRequirement{}).satisfied(state("0", "0")))
	r := RechargeRequirement{MinRechargeTotal: 100, MinRecharge30d: 10}
	require.False(t, r.satisfied(state("99.99999999", "9.99999999")))
	require.True(t, r.satisfied(state("100", "0")))
	require.True(t, r.satisfied(state("10", "10")))
	require.False(t, (RechargeRequirement{MinRechargeTotal: 100}).satisfied(state("0", "100")))
	require.False(t, (RechargeRequirement{MinRecharge30d: 10}).satisfied(state("100", "0")))
}

func TestRechargeConfigCompatibility(t *testing.T) {
	var checkin CheckinConfig
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"min_amount":0.1,"max_amount":0.5}`), &checkin))
	require.False(t, checkin.enabled(), "existing settings remain unrestricted")
	require.NoError(t, json.Unmarshal([]byte(`{"min_recharge_total":20,"min_recharge_30d":5}`), &checkin))
	require.Equal(t, RechargeRequirement{MinRechargeTotal: 20, MinRecharge30d: 5}, checkin.RechargeRequirement)
	raw, err := json.Marshal(checkin)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"min_recharge_total":20`)
}

func TestRechargeWindowValidationAndMessages(t *testing.T) {
	for _, days := range []int{-1, 3651, 1000000} {
		require.Error(t, validateRechargeRequirement(RechargeRequirement{RechargeWindowDays: days}))
	}
	for _, days := range []int{0, 1, 7, 30, 60, 3650} {
		require.NoError(t, validateRechargeRequirement(RechargeRequirement{RechargeWindowDays: days}))
	}
	legacy := RechargeRequirement{MinRecharge30d: 10}
	require.Equal(t, 30, legacy.windowDays())
	require.Equal(t, "近 30 天有效充值满 USD 10.00", legacy.describe())
	custom := RechargeRequirement{MinRecharge30d: 10, RechargeWindowDays: 7}
	require.Equal(t, "近 7 天有效充值满 USD 10.00", custom.describe())
	require.Contains(t, custom.failure("签到", RechargeEligibility{Total: decimal.NewFromInt(5), Recent: decimal.NewFromInt(2)}), "近 7 天充值：USD 2.00")
	require.Contains(t, custom.invite(), "近 7 天有效充值满 USD 10.00")
	require.Error(t, json.Unmarshal([]byte("{\"recharge_window_days\":1.5}"), &custom))
}

func TestRechargeMessagesExplainAlternativeThresholds(t *testing.T) {
	for _, tc := range []struct {
		name        string
		requirement RechargeRequirement
		want        string
	}{
		{"disabled", RechargeRequirement{}, "无需充值门槛"},
		{"lifetime only", RechargeRequirement{MinRechargeTotal: 100}, "累计有效充值满 USD 100.00"},
		{"recent only", RechargeRequirement{MinRecharge30d: 10, RechargeWindowDays: 7}, "近 7 天有效充值满 USD 10.00"},
		{"either with default window", RechargeRequirement{MinRechargeTotal: 100, MinRecharge30d: 10}, "累计有效充值满 USD 100.00 或 近 30 天有效充值满 USD 10.00"},
		{"either with custom window", RechargeRequirement{MinRechargeTotal: 100, MinRecharge30d: 10, RechargeWindowDays: 7}, "累计有效充值满 USD 100.00 或 近 7 天有效充值满 USD 10.00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.requirement.describe())
			require.Contains(t, tc.requirement.invite(), tc.want)
			if tc.requirement.enabled() {
				current := RechargeEligibility{Total: decimal.NewFromInt(20), Recent: decimal.NewFromInt(5)}
				for _, activity := range []string{"签到", "邀请奖励"} {
					message := tc.requirement.failure(activity, current)
					require.Contains(t, message, "暂不满足：\n"+tc.want)
					require.Contains(t, message, "累计充值：USD 20.00")
					require.Contains(t, message, fmt.Sprintf("近 %d 天充值：USD 5.00", tc.requirement.windowDays()))
				}
			}
		})
	}
}
