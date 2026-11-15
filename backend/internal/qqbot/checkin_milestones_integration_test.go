//go:build integration

package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCheckinMilestonesCumulativeTiersAtomicAndConcurrent(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	c, err := s.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{Enabled: true, MinAmount: .1, MaxAmount: .1, Growth: GrowthConfig{Tasks: []GrowthTask{}}, Milestones: []CheckinMilestone{
		{ID: "seven", Name: "累计七天", Days: 7, Enabled: true, Reward: GrowthReward{Type: "balance", Amount: 1.25}},
		{ID: "thirty", Name: "累计三十天", Days: 30, Enabled: true, Reward: GrowthReward{Type: "chances", Quantity: 3}},
	}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	// Gaps intentionally break the streak; lifetime cumulative days still count.
	for i := 1; i <= 29; i++ {
		seedCheckin(t, db, uid, time.Now().AddDate(0, 0, -2*i))
	}
	_, err = db.Exec(`ALTER TABLE qq_bot_lottery_chance_grants ADD CONSTRAINT reject_milestone CHECK(chances<>3)`)
	require.NoError(t, err)
	j := Job{GroupID: "group", MemberID: "member"}
	_, err = s.reward(ctx, c, j, "checkin")
	require.Error(t, err)
	var total int
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_rewards WHERE user_id=$1 AND kind='checkin'`, uid).Scan(&total))
	require.Equal(t, 29, total)
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Zero(t, balance)
	_, err = db.Exec(`ALTER TABLE qq_bot_lottery_chance_grants DROP CONSTRAINT reject_milestone`)
	require.NoError(t, err)
	var wg sync.WaitGroup
	replies := make(chan string, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			text, err := s.reward(ctx, c, j, "checkin")
			replies <- text
			failures <- err
		}()
	}
	wg.Wait()
	close(replies)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	wins := 0
	for text := range replies {
		if text != "今日已签到。" {
			wins++
			require.Contains(t, text, "累计签到 30 天")
			require.Contains(t, text, "连签 1 天")
			require.Contains(t, text, "累计七天：余额 USD 1.25")
			require.Contains(t, text, "累计三十天：3 次抽奖机会")
		}
	}
	require.Equal(t, 1, wins)
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 1.35, balance)
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, uid).Scan(&total))
	require.Equal(t, 3, total)
	preview, err := s.PreviewActivities(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, 30, preview.CheckinTotal)
	require.Len(t, preview.CheckinMilestones, 2)
	require.Empty(t, preview.Tasks)
	for _, milestone := range preview.CheckinMilestones {
		require.False(t, milestone.Eligible)
		require.Equal(t, "fulfilled", milestone.Status)
	}
}

func TestCheckinMilestoneLegacyMigrationAndSectionsPreserveClaims(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	c, err := s.Config(ctx)
	require.NoError(t, err)
	legacy := CheckinConfig{Enabled: true, Growth: GrowthConfig{Tasks: []GrowthTask{
		{ID: "old7", Name: "Old seven", Kind: "checkin_total", Target: 7, Chances: 5, Enabled: true},
	}, PeriodicTasks: []GrowthTask{{ID: "repeat7", Name: "Every seven", Kind: "checkin_total", Target: 7, Chances: 2, Enabled: true}}}}
	raw, err := json.Marshal(legacy)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO settings(key,value) VALUES($1,$2)`, checkinConfigKey, string(raw))
	require.NoError(t, err)
	for i := 1; i <= 8; i++ {
		seedCheckin(t, db, uid, time.Now().AddDate(0, 0, -i))
	}
	_, err = db.Exec(`INSERT INTO qq_bot_lottery_chance_grants(source_key,user_id,chances) VALUES($1,$2,5)`, fmt.Sprintf("qq-growth:%d:old7", uid), uid)
	require.NoError(t, err)
	loaded, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	require.Empty(t, loaded.Growth.Tasks)
	require.Empty(t, loaded.Growth.PeriodicTasks)
	require.Len(t, loaded.Milestones, 2)
	_, n, err := s.processGrowthTasks(ctx, c, Job{GroupID: "group", MemberID: "member"}, time.Now(), false)
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the unclaimed repeating tier should settle")
	changed := loaded
	changed.Milestones = append([]CheckinMilestone{}, loaded.Milestones...)
	changed.Milestones[0].Reward = GrowthReward{Type: "balance", Amount: 99}
	require.NoError(t, s.SaveCheckinConfig(WithCheckinSection(WithConfigVersion(ctx, CheckinSectionVersion(loaded, "checkin")), "checkin"), changed))
	// A stale growth draft is isolated from the newly saved check-in rewards.
	tasksVersion := CheckinSectionVersion(loaded, "tasks")
	loaded.Growth.Tasks = []GrowthTask{{ID: "bind", Name: "Bind", Kind: "binding", Target: 1, Chances: 1}}
	require.NoError(t, s.SaveCheckinConfig(WithCheckinSection(WithConfigVersion(ctx, tasksVersion), "tasks"), loaded))
	// An older client omitting milestones must preserve them as well.
	legacy.Growth.Tasks = []GrowthTask{}
	legacy.Growth.PeriodicTasks = nil
	require.NoError(t, s.SaveCheckinConfig(ctx, legacy))
	loaded, err = s.CheckinConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, float64(99), loaded.Milestones[0].Reward.Amount)
	_, n, err = s.processGrowthTasks(ctx, c, Job{GroupID: "group", MemberID: "member"}, time.Now(), false)
	require.NoError(t, err)
	require.Zero(t, n)
	var balance float64
	var chances int
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Zero(t, balance, "editing a claimed tier must not issue its new reward")
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, uid).Scan(&chances))
	require.Equal(t, 7, chances)
}
