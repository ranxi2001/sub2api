//go:build integration

package qqbot

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGrowthMonitorGrantsWithoutCommandsAndRotatesIncompleteAccounts(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	insightsUsageTable(t, db)
	cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{{ID: "api", Name: "API", Kind: "usage_count", Target: 1, Enabled: true, Chances: 2}}}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	c, err := s.Config(ctx)
	require.NoError(t, err)
	var last int64
	for i := range 101 {
		last = user(t, db)
		bind(t, db, last, fmt.Sprintf("member-%d", i))
	}
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,created_at) VALUES($1,'test',NOW())`, last)
	require.NoError(t, err)
	cursor, err := s.settleGrowthBatch(ctx, c, 0)
	require.NoError(t, err)
	require.Positive(t, cursor)
	require.Less(t, cursor, last)
	cursor, err = s.settleGrowthBatch(ctx, c, cursor)
	require.NoError(t, err)
	require.Zero(t, cursor)
	var total int
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, last).Scan(&total))
	require.Equal(t, 2, total)
	_, err = s.settleGrowthBatch(ctx, c, 0)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes`).Scan(&total))
	require.Equal(t, 1, total)
}

func TestGrowthMonitorRechecksConfigBindingAndActiveAccount(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	active, disabled, deleted, unbound, forbidden := user(t, db), user(t, db), user(t, db), user(t, db), user(t, db)
	for _, uid := range []int64{active, disabled, deleted, forbidden} {
		bind(t, db, uid, fmt.Sprint(uid))
	}
	_, err := db.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, disabled)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE users SET deleted_at=NOW() WHERE id=$1`, deleted)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE qq_bot_bindings SET group_id='forbidden' WHERE user_id=$1`, forbidden)
	require.NoError(t, err)
	cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{{ID: "bind", Name: "Bind", Kind: "binding", Target: 1, Enabled: true, Reward: &GrowthReward{Type: "balance", Amount: 0.00000001}}, {ID: "off", Name: "Disabled", Kind: "binding", Target: 1, Chances: 10}}}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	c, err := s.Config(ctx)
	require.NoError(t, err)
	_, err = s.settleGrowthBatch(ctx, c, 0)
	require.NoError(t, err)
	for _, uid := range []int64{active, disabled, deleted, unbound, forbidden} {
		var amount float64
		require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&amount))
		if uid == active {
			require.Equal(t, 0.00000001, amount)
		} else {
			require.Zero(t, amount)
		}
	}
	bind(t, db, unbound, "later")
	wrong := c
	wrong.AppID = "other-app"
	_, n, err := s.processGrowthTasks(ctx, wrong, Job{GroupID: "group", MemberID: "later"}, time.Now(), false)
	require.NoError(t, err)
	require.Zero(t, n)
	c.Enabled = false
	_, err = s.SaveConfig(ctx, c)
	require.NoError(t, err)
	c.Enabled = true
	_, err = s.settleGrowthBatch(ctx, c, 0)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestGrowthCheckinAndMonitorGrantOnceWithAtomicRollback(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	cfg := CheckinConfig{Enabled: true, MinAmount: .5, MaxAmount: .5, Growth: GrowthConfig{Tasks: []GrowthTask{
		{ID: "binding", Name: "Binding", Kind: "binding", Target: 1, Enabled: true, Chances: 3},
		{ID: "day", Name: "First day", Kind: "checkin_total", Target: 1, Enabled: true, Reward: &GrowthReward{Type: "balance", Amount: 2}},
	}}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	c, err := s.Config(ctx)
	require.NoError(t, err)
	j := Job{GroupID: "group", MemberID: "member"}
	_, err = db.Exec(`ALTER TABLE redeem_codes ADD CONSTRAINT reject_growth CHECK(value<>2)`)
	require.NoError(t, err)
	_, err = s.reward(ctx, c, j, "checkin")
	require.Error(t, err)
	for _, table := range []string{"qq_bot_rewards", "qq_bot_reward_codes", "qq_bot_lottery_chance_grants", "redeem_codes"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count))
		require.Zero(t, count)
	}
	_, err = db.Exec(`ALTER TABLE redeem_codes DROP CONSTRAINT reject_growth`)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			switch i % 3 {
			case 0:
				_, err = s.reward(ctx, c, j, "checkin")
			case 1:
				_, err = s.settleGrowthBatch(ctx, c, 0)
			case 2:
				_, err = s.growthTasks(ctx, c, j)
			}
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 2.5, balance)
	var count int
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants`).Scan(&count))
	require.Equal(t, 3, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes`).Scan(&count))
	require.Equal(t, 2, count)
}
