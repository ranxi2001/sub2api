package qqbot

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestGrowthConfigLegacyAndEmptyTasks(t *testing.T) {
	legacy := GrowthConfig{BindingChances: 2, Checkin30Chances: 4}
	tasks := legacy.tasks()
	require.Equal(t, []string{"binding", "first_usage", "checkin_30"}, []string{tasks[0].ID, tasks[1].ID, tasks[2].ID})
	require.True(t, tasks[0].Enabled)
	require.Equal(t, 2, tasks[0].Chances)
	require.False(t, tasks[1].Enabled)
	require.Equal(t, float64(30), tasks[2].Target)
	require.Equal(t, 7, (CheckinConfig{}).streakDays())
	legacy.Tasks = []GrowthTask{}
	raw, err := json.Marshal(legacy)
	require.NoError(t, err)
	var restored GrowthConfig
	require.NoError(t, json.Unmarshal(raw, &restored))
	require.NotNil(t, restored.Tasks)
	require.Empty(t, restored.tasks())
}

func TestGrowthTaskValidation(t *testing.T) {
	valid := GrowthTask{ID: "task_api_10", Name: "API milestone", Kind: "usage_count", Target: 10, Chances: 2, Enabled: true}
	for _, tc := range []struct {
		name string
		edit func(*GrowthTask)
	}{
		{"invalid id", func(v *GrowthTask) { v.ID = "task:1" }},
		{"empty name", func(v *GrowthTask) { v.Name = " " }},
		{"long name", func(v *GrowthTask) { v.Name = strings.Repeat("名", 41) }},
		{"multiline name", func(v *GrowthTask) { v.Name = "one\ntwo" }},
		{"unknown kind", func(v *GrowthTask) { v.Kind = "unknown" }},
		{"fractional count", func(v *GrowthTask) { v.Target = 1.5 }},
		{"zero count", func(v *GrowthTask) { v.Target = 0 }},
		{"excess count", func(v *GrowthTask) { v.Target = 1000001 }},
		{"nan target", func(v *GrowthTask) { v.Target = math.NaN() }},
		{"infinite target", func(v *GrowthTask) { v.Target = math.Inf(1) }},
		{"negative reward", func(v *GrowthTask) { v.Chances = -1 }},
		{"excess reward", func(v *GrowthTask) { v.Chances = 1001 }},
		{"binding target", func(v *GrowthTask) { v.Kind, v.Target = "binding", 2 }},
		{"excess days", func(v *GrowthTask) { v.Kind, v.Target = "checkin_total", 3651 }},
		{"zero streak", func(v *GrowthTask) { v.Kind, v.Target = "checkin_streak", 0 }},
		{"recharge precision", func(v *GrowthTask) { v.Kind, v.Target = "recharge_total", 1.001 }},
		{"zero recharge", func(v *GrowthTask) { v.Kind, v.Target = "recharge_total", 0 }},
		{"excess recharge", func(v *GrowthTask) { v.Kind, v.Target = "recharge_total", 1000000001 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := valid
			tc.edit(&task)
			require.Error(t, validateGrowth(CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{task}}}))
		})
	}
	for _, kind := range []string{"binding", "usage_count", "checkin_total", "checkin_streak", "recharge_total"} {
		task := valid
		task.Kind, task.Target, task.Chances = kind, 1, 0
		require.NoError(t, validateGrowth(CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{task}}}))
	}
	require.Error(t, validateGrowth(CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{valid, valid}}}))
	require.Error(t, validateGrowth(CheckinConfig{Growth: GrowthConfig{Tasks: make([]GrowthTask, maxGrowthTasks+1)}}))
	for _, days := range []int{-1, 3651} {
		require.Error(t, validateGrowth(CheckinConfig{StreakDays: days}))
	}
	for _, days := range []int{0, 1, 7, 3650} {
		require.NoError(t, validateGrowth(CheckinConfig{StreakDays: days}))
	}
}

func TestGrowthProgressThresholds(t *testing.T) {
	progress := growthProgress{total: 30, streak: 7, usage: 10, recharge: decimal.RequireFromString("9.99999999")}
	_, rechargeText := progress.task(GrowthTask{Kind: "recharge_total", Target: 10})
	require.Equal(t, "USD 9.99/10.00", rechargeText, "display must not round an unmet target up to completion")
	for _, tc := range []struct {
		kind     string
		target   float64
		eligible bool
	}{
		{"binding", 1, true}, {"usage_count", 10, true}, {"usage_count", 11, false},
		{"checkin_total", 30, true}, {"checkin_total", 31, false},
		{"checkin_streak", 7, true}, {"checkin_streak", 8, false},
		{"recharge_total", 10, false}, {"recharge_total", 9.99, true}, {"unknown", 1, false},
	} {
		eligible, _ := progress.task(GrowthTask{Kind: tc.kind, Target: tc.target})
		require.Equal(t, tc.eligible, eligible, "%s %.2f", tc.kind, tc.target)
	}
}

func TestPeriodicGrowthValidation(t *testing.T) {
	task := GrowthTask{ID: "repeat", Name: "Repeat", Kind: "usage_amount", Target: 10, Chances: 1, Enabled: true}
	for _, kind := range []string{"usage_amount", "usage_tokens", "usage_count", "recharge_total", "checkin_total"} {
		task.Kind = kind
		require.NoError(t, validateGrowth(CheckinConfig{Growth: GrowthConfig{PeriodicTasks: []GrowthTask{task}}}))
	}
	for _, kind := range []string{"binding", "checkin_streak", "unknown"} {
		task.Kind = kind
		require.Error(t, validateGrowth(CheckinConfig{Growth: GrowthConfig{PeriodicTasks: []GrowthTask{task}}}))
	}
	task.Kind = "usage_amount"
	for _, target := range []float64{0, -1, 0.001, 1.001, 1e9 + 1, math.Inf(1), math.NaN()} {
		task.Target = target
		require.Error(t, validateGrowth(CheckinConfig{Growth: GrowthConfig{PeriodicTasks: []GrowthTask{task}}}))
	}
	task.Target = 10
	require.Error(t, validateGrowth(CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{task}, PeriodicTasks: []GrowthTask{task}}}))
	require.Error(t, validateGrowth(CheckinConfig{Growth: GrowthConfig{PeriodicTasks: []GrowthTask{task, task}}}))
	require.Error(t, validateGrowth(CheckinConfig{Growth: GrowthConfig{PeriodicTasks: make([]GrowthTask, maxGrowthTasks+1)}}))
}
