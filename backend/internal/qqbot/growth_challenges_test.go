package qqbot

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGrowthChallengeCalendarKeys(t *testing.T) {
	sunday := time.Date(2026, 11, 8, 15, 59, 59, 0, time.UTC)
	monday := sunday.Add(time.Second)
	task := GrowthTask{ID: "explorer"}
	require.Equal(t, "qq-growth:42:explorer", growthTaskRewardKey(42, task, sunday))
	task.Period = "daily"
	require.Equal(t, "qq-growth:42:explorer:daily:2026-11-08", growthTaskRewardKey(42, task, sunday))
	require.Equal(t, "qq-growth:42:explorer:daily:2026-11-09", growthTaskRewardKey(42, task, monday))
	task.Period = "weekly"
	require.Equal(t, "qq-growth:42:explorer:weekly:2026-11-02", growthTaskRewardKey(42, task, sunday))
	require.Equal(t, "qq-growth:42:explorer:weekly:2026-11-09", growthTaskRewardKey(42, task, monday))
	require.Equal(t, growthTaskRewardKey(42, task, monday), growthTaskRewardKey(42, task, monday.In(time.FixedZone("Other", -7*3600))))
}

func TestGrowthChallengeValidation(t *testing.T) {
	for _, tc := range []struct {
		kind, period string
		target       float64
		valid        bool
	}{
		{"usage_count", "daily", 10, true}, {"model_count", "weekly", 3, true},
		{"usage_days", "weekly", 7, true}, {"usage_days", "weekly", 8, false},
		{"usage_days", "daily", 1, true}, {"usage_days", "daily", 2, false},
		{"invite_count", "weekly", 2, true}, {"invite_count", "once", 2, true},
		{"model_count", "once", 10001, false}, {"model_count", "once", 1.5, false},
		{"binding", "daily", 1, false}, {"recharge_total", "weekly", 1, false},
		{"usage_count", "unknown", 1, false},
	} {
		t.Run(tc.kind+"_"+tc.period, func(t *testing.T) {
			task := GrowthTask{ID: "challenge", Name: "Challenge", Kind: tc.kind, Period: tc.period, Target: tc.target, Chances: 1, Enabled: true}
			err := validateGrowth(CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{task}}})
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestGrowthChallengeProgress(t *testing.T) {
	p := growthProgress{days: 3, models: 2, invites: 4}
	for _, tc := range []struct {
		kind     string
		target   float64
		eligible bool
		label    string
	}{
		{"usage_days", 3, true, "3/3 天"},
		{"model_count", 3, false, "2/3 个模型"},
		{"invite_count", 4, true, "4/4 人"},
	} {
		eligible, label := p.task(GrowthTask{Kind: tc.kind, Target: tc.target})
		require.Equal(t, tc.eligible, eligible)
		require.Equal(t, tc.label, label)
	}
}
