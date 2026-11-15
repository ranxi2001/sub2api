package qqbot

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckinMilestonesNormalizeLegacyTasksWithoutChangingClaims(t *testing.T) {
	cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{
		{ID: "api", Name: "API", Kind: "usage_count", Target: 3},
		{ID: "seven", Name: "Seven days", Kind: "checkin_total", Target: 7, Chances: 2, Enabled: true},
		{ID: "streak", Name: "Old streak", Kind: "checkin_streak", Target: 5, Reward: &GrowthReward{Type: "balance", Amount: 1}, Enabled: true},
	}, PeriodicTasks: []GrowthTask{{ID: "repeat", Name: "Each 30 days", Kind: "checkin_total", Target: 30, Chances: 3, Enabled: true}}}}
	normalized := normalizeCheckinMilestones(cfg)
	require.Len(t, normalized.Growth.Tasks, 1)
	require.Equal(t, "api", normalized.Growth.Tasks[0].ID)
	require.Empty(t, normalized.Growth.PeriodicTasks)
	require.Len(t, normalized.Milestones, 3)
	require.Equal(t, "seven", normalized.Milestones[0].ID)
	require.Equal(t, 2, normalized.Milestones[0].Reward.Quantity)
	require.Equal(t, "checkin_streak", normalized.Milestones[1].kind())
	require.True(t, normalized.Milestones[2].Repeat)
	require.Equal(t, normalized, normalizeCheckinMilestones(normalized), "normalization must be idempotent")
	require.NoError(t, validateCheckin(normalized))
}

func TestCheckinMilestoneValidation(t *testing.T) {
	valid := CheckinMilestone{ID: "seven", Name: "Seven", Days: 7, Reward: GrowthReward{Type: "chances", Quantity: 1}, Enabled: true}
	for _, edit := range []func(*CheckinMilestone){
		func(m *CheckinMilestone) { m.Days = 0 },
		func(m *CheckinMilestone) { m.Days = 3651 },
		func(m *CheckinMilestone) { m.ID = "bad:id" },
		func(m *CheckinMilestone) { m.Name = "bad\nname" },
		func(m *CheckinMilestone) { m.Kind = "usage_count" },
		func(m *CheckinMilestone) { m.Reward.Quantity = 1001 },
		func(m *CheckinMilestone) { m.Reward = GrowthReward{Type: "balance", Amount: math.NaN()} },
		func(m *CheckinMilestone) { m.Kind, m.Repeat = "checkin_streak", true },
	} {
		m := valid
		edit(&m)
		require.Error(t, validateCheckinMilestones(CheckinConfig{Milestones: []CheckinMilestone{m}}))
	}
	require.NoError(t, validateCheckinMilestones(CheckinConfig{Milestones: []CheckinMilestone{valid}}))
	require.Error(t, validateCheckinMilestones(CheckinConfig{Milestones: []CheckinMilestone{valid, valid}}))
	require.Error(t, validateCheckinMilestones(CheckinConfig{Milestones: make([]CheckinMilestone, maxCheckinMilestones+1)}))
	require.Error(t, validateCheckinMilestones(CheckinConfig{Milestones: []CheckinMilestone{valid}, Growth: GrowthConfig{Tasks: []GrowthTask{{ID: valid.ID}}}}))
}

func TestActivityRewardCadenceCannotResetExistingEntitlement(t *testing.T) {
	current := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{{ID: "daily", Period: "daily"}}}, Milestones: []CheckinMilestone{{ID: "days", Repeat: true}}}
	next := current
	next.Growth.Tasks = []GrowthTask{{ID: "daily", Period: "weekly"}}
	require.Error(t, validateActivityRewardCadences(current, next))
	next = current
	next.Milestones = []CheckinMilestone{{ID: "days", Repeat: false}}
	require.Error(t, validateActivityRewardCadences(current, next))
	next.Milestones[0].ID = "new_days"
	require.NoError(t, validateActivityRewardCadences(current, next))
	require.NoError(t, validateActivityRewardCadences(CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{{ID: "legacy"}}}}, CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{{ID: "legacy", Period: "once"}}}}))
}
