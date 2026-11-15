package qqbot

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSiteGrowthRulesDescribeDailyAmountDaysAndRepeatedDateBoundaries(t *testing.T) {
	for _, tc := range []struct {
		kind, target string
		streak       bool
	}{
		{"usage_amount_days", "累计 3 天每日消费满 USD 12.34", false},
		{"usage_amount_streak", "连续 3 天每日消费满 USD 12.34", true},
		{"recharge_days", "累计 3 天每日有效充值满 USD 12.34", false},
		{"recharge_streak", "连续 3 天每日有效充值满 USD 12.34", true},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			task := GrowthTask{ID: "rule", Name: "每日金额任务", Kind: tc.kind, Target: 3, DailyAmount: 12.34, Enabled: true, Chances: 1}
			once := siteGrowthRules(GrowthConfig{Tasks: []GrowthTask{task}})
			require.Contains(t, once, "类型：一次性")
			require.Contains(t, once, tc.target)
			require.Contains(t, once, "按北京时间日期统计")
			repeated := siteGrowthRules(GrowthConfig{Tasks: []GrowthTask{}, PeriodicTasks: []GrowthTask{task}})
			require.Contains(t, repeated, tc.target)
			require.Contains(t, repeated, "每轮达标后可再获奖励")
			require.Contains(t, repeated, "已结算日期不重复使用")
			if tc.streak {
				require.Contains(t, repeated, "未达标的连续进度遇间断重新计算")
			}
		})
	}
}
