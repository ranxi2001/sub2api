package qqbot

import (
	"math"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestGrowthAmountDaysConfig(t *testing.T) {
	for _, kind := range []string{"usage_amount_days", "usage_amount_streak", "recharge_days", "recharge_streak"} {
		t.Run(kind, func(t *testing.T) {
			task := GrowthTask{ID: "amount_day", Name: "Daily amount", Kind: kind, Target: 3, DailyAmount: .01, Chances: 1, Enabled: true}
			for _, periodic := range []bool{false, true} {
				config := func(task GrowthTask) CheckinConfig {
					growth := GrowthConfig{Tasks: []GrowthTask{}}
					if periodic {
						growth.PeriodicTasks = []GrowthTask{task}
					} else {
						growth.Tasks = []GrowthTask{task}
					}
					return CheckinConfig{Growth: growth}
				}
				require.NoError(t, validateGrowth(config(task)))
				for _, amount := range []float64{0, -1, .001, 1.001, 1e9 + 1, math.NaN(), math.Inf(1)} {
					invalid := task
					invalid.DailyAmount = amount
					require.Error(t, validateGrowth(config(invalid)), "amount %v", amount)
				}
				for _, target := range []float64{0, -1, .5, 3651, math.NaN(), math.Inf(1)} {
					invalid := task
					invalid.Target = target
					require.Error(t, validateGrowth(config(invalid)), "target %v", target)
				}
				for _, period := range []string{"daily", "weekly"} {
					invalid := task
					invalid.Period = period
					require.Error(t, validateGrowth(config(invalid)))
				}
			}
		})
	}
}

func TestDailyGrowthDatesThresholdOrderAndTimezone(t *testing.T) {
	day := func(value string) time.Time {
		date, err := time.Parse(time.RFC3339, value)
		require.NoError(t, err)
		return date
	}
	p := growthProgress{at: day("2026-11-12T12:00:00+08:00"), amountDays: map[string][]growthAmountDay{
		"usage": {
			{Day: day("2026-11-11T16:00:00Z"), Amount: decimal.RequireFromString(".01")},
			{Day: day("2026-11-10T16:00:00Z"), Amount: decimal.RequireFromString(".0099999999")},
			{Day: day("2026-11-08T16:00:00Z"), Amount: decimal.RequireFromString("10")},
			{Day: day("2026-11-09T00:00:00+08:00"), Amount: decimal.RequireFromString("10")},
			{Day: day("2026-11-12T16:00:00Z"), Amount: decimal.RequireFromString("100")},
		},
		"recharge": {{Day: day("2026-11-10T16:00:00Z"), Amount: decimal.RequireFromString(".01")}},
	}}
	task := GrowthTask{Kind: "usage_amount_days", DailyAmount: .01}
	dates := dailyGrowthDates(p, task)
	require.Len(t, dates, 2)
	require.Equal(t, "2026-11-09T00:00:00+08:00", dates[0].Format(time.RFC3339))
	require.Equal(t, "2026-11-12T00:00:00+08:00", dates[1].Format(time.RFC3339))
	task.DailyAmount = 10
	require.Len(t, dailyGrowthDates(p, task), 1)
	task.Kind, task.DailyAmount = "recharge_days", .01
	require.Equal(t, "2026-11-11", dailyGrowthDates(p, task)[0].Format("2006-01-02"))
}

func TestGrowthAmountDaysHistoricalEligibilityAndCurrentStreak(t *testing.T) {
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, zone)
	dates := []growthAmountDay{}
	for _, day := range []int{1, 2, 3, 4, 10, 11} {
		dates = append(dates, growthAmountDay{Day: time.Date(2026, 11, day, 0, 0, 0, 0, zone), Amount: decimal.NewFromInt(5)})
	}
	for _, source := range []string{"usage", "recharge"} {
		p := growthProgress{at: at, amountDays: map[string][]growthAmountDay{source: dates}}
		kind := "usage_amount_streak"
		if source == "recharge" {
			kind = "recharge_streak"
		}
		task := GrowthTask{Kind: kind, Target: 4, DailyAmount: 5}
		eligible, text := p.task(task)
		require.True(t, eligible, "historical longest run can settle after a monitor outage")
		require.Contains(t, text, "当前连续 2/4 天")
		require.Contains(t, text, "USD 5.00")
		p.at = at.AddDate(0, 0, 1)
		eligible, text = p.task(task)
		require.True(t, eligible)
		require.Contains(t, text, "当前连续 0/4 天")
		if source == "usage" {
			task.Kind = "usage_amount_days"
		} else {
			task.Kind = "recharge_days"
		}
		task.Target = 6
		eligible, text = p.task(task)
		require.True(t, eligible)
		require.Contains(t, text, "累计达标 6/6 天")
	}
}
