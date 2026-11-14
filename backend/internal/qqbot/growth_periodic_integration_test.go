//go:build integration

package qqbot

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPeriodicGrowthPrecisionAndAllMetrics(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid, other := user(t, db), user(t, db)
	bind(t, db, uid, "member")
	insightsUsageTable(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{Enabled: true, Growth: GrowthConfig{Tasks: []GrowthTask{}, PeriodicTasks: []GrowthTask{
		{ID: "spend", Name: "Spend", Kind: "usage_amount", Target: 10, Enabled: true, Chances: 2},
		{ID: "tokens", Name: "Tokens", Kind: "usage_tokens", Target: 1000, Enabled: true, Reward: &GrowthReward{Type: "balance", Amount: .25}},
		{ID: "calls", Name: "Calls", Kind: "usage_count", Target: 2, Enabled: true, Reward: &GrowthReward{Type: "physical", Quantity: 1, PrizeName: "Cup", ClaimInstructions: "Contact admin"}},
		{ID: "days", Name: "Days", Kind: "checkin_total", Target: 2, Enabled: true, Reward: &GrowthReward{Type: "other", Quantity: 1, PrizeName: "Service"}},
		{ID: "recharge", Name: "Recharge", Kind: "recharge_total", Target: 10, Enabled: true, Chances: 3},
	}}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	loaded, err := s.CheckinConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, normalizeCheckinMilestones(cfg).Growth, loaded.Growth)
	require.Equal(t, normalizeCheckinMilestones(cfg).Milestones, loaded.Milestones)
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,actual_cost,billing_type,created_at) VALUES
 ($1,'past',600,200,100,1099,19.9999999999,1,NOW()),($1,'future',100000,0,0,0,1000,0,NOW()+INTERVAL '1 day'),($2,'other',100000,0,0,0,1000,0,NOW())`, uid, other)
	require.NoError(t, err)
	for i := 1; i <= 5; i++ {
		seedCheckin(t, db, uid, time.Now().AddDate(0, 0, -i))
	}
	_, err = db.Exec(`INSERT INTO payment_orders(user_id,amount,bonus_amount,refund_amount,status) VALUES($1,40,10,20,'PARTIALLY_REFUNDED')`, uid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO redeem_codes(code,type,value,status,used_by,used_at) VALUES('offline','balance',10,'used',$1,NOW()),('admin','admin_balance',100,'used',$1,NOW())`, uid)
	require.NoError(t, err)
	j := Job{GroupID: "group", MemberID: "member"}
	text, n, err := s.processGrowthTasks(ctx, c, j, time.Now(), true)
	require.NoError(t, err)
	require.Equal(t, 6, n) // spend 1, tokens 1, days 2, recharge 2
	require.Contains(t, text, "USD 9.99/10.00")
	require.Contains(t, text, "999/1000 Token")
	require.NotContains(t, text, "1/2 天", "sign-in progress belongs to the sign-in section")
	var cycles int
	var consumed string
	require.NoError(t, db.QueryRow(`SELECT cycles,consumed::text FROM qq_bot_periodic_growth_progress WHERE user_id=$1 AND task_id='days' AND kind='checkin_total'`, uid).Scan(&cycles, &consumed))
	require.Equal(t, 2, cycles)
	require.Equal(t, "4", consumed)
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,cache_read_tokens,actual_cost,created_at) VALUES($1,'exact',1,0.0000000001,NOW())`, uid)
	require.NoError(t, err)
	text, n, err = s.processGrowthTasks(ctx, c, j, time.Now(), true)
	require.NoError(t, err)
	require.Equal(t, 3, n) // second spend/token intervals and first request interval
	require.Contains(t, text, "USD 0.00/10.00")
	var chances int
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, uid).Scan(&chances))
	require.Equal(t, 10, chances)
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, .5, balance)
	items, err := s.ListRewardCodes(ctx, "", "pending", 1)
	require.NoError(t, err)
	require.Equal(t, 3, items.Total)
	for _, item := range items.Items {
		require.Equal(t, "growth", item.SourceKind)
		require.NoError(t, s.UpdateRewardCode(ctx, item.ID, "fulfill", "Delivered"))
	}
	_, n, err = s.processGrowthTasks(ctx, c, j, time.Now(), true)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestPeriodicGrowthConcurrentMonitorRebindAndRuleEdits(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	insightsUsageTable(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{}, PeriodicTasks: []GrowthTask{{ID: "spend", Name: "Spend", Kind: "usage_amount", Target: 10, Enabled: true, Chances: 1}}}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,actual_cost,created_at) VALUES($1,'past',25,NOW())`, uid)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = s.settleGrowthBatch(ctx, c, 0)
			} else {
				_, err = s.growthTasks(ctx, c, Job{GroupID: "group", MemberID: "member"})
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var chances int
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants`).Scan(&chances))
	require.Equal(t, 2, chances)
	_, err = db.Exec(`UPDATE qq_bot_bindings SET member_id='rebound',group_id='other-group' WHERE user_id=$1`, uid)
	require.NoError(t, err)
	j := Job{GroupID: "other-group", MemberID: "rebound"}
	text, err := s.growthTasks(ctx, c, j)
	require.NoError(t, err)
	require.Contains(t, text, "已结算 2 轮\n本轮进度：USD 5.00/10.00")
	// Editing the interval spends only the remainder; past rewards retain snapshots.
	task := &cfg.Growth.PeriodicTasks[0]
	task.Target, task.Name = 5, "Edited"
	task.Reward = &GrowthReward{Type: "balance", Amount: 1.25}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	_, n, err := s.processGrowthTasks(ctx, c, j, time.Now(), true)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 1.25, balance)
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants`).Scan(&chances))
	require.Equal(t, 2, chances)
	// Changing the metric keeps separate progress, including after switching back.
	task.Kind, task.Target = "usage_count", 1
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	_, n, err = s.processGrowthTasks(ctx, c, j, time.Now(), true)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	task.Kind, task.Target, task.Enabled = "usage_amount", 5, false
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,actual_cost,created_at) VALUES($1,'later',5,NOW())`, uid)
	require.NoError(t, err)
	_, n, err = s.processGrowthTasks(ctx, c, j, time.Now(), true)
	require.NoError(t, err)
	require.Zero(t, n)
	task.Enabled = true
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	_, err = s.settleGrowthBatch(ctx, c, 0)
	require.NoError(t, err)
	text, n, err = s.processGrowthTasks(ctx, c, j, time.Now(), true)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Contains(t, text, "已结算 4 轮\n本轮进度：USD 0.00/5.00")
	// Retention or refunds may reduce totals, but cannot reset paid progress.
	_, err = db.Exec(`UPDATE usage_logs SET actual_cost=0 WHERE user_id=$1`, uid)
	require.NoError(t, err)
	_, n, err = s.processGrowthTasks(ctx, c, j, time.Now(), true)
	require.NoError(t, err)
	require.Zero(t, n)
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,actual_cost,created_at) VALUES($1,'replacement',34.99,NOW())`, uid)
	require.NoError(t, err)
	text, n, err = s.processGrowthTasks(ctx, c, j, time.Now(), true)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Contains(t, text, "USD 4.99/5.00")
}

func TestPeriodicGrowthCatchupAndEligibility(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	insightsUsageTable(t, db)
	c, err := s.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{}, PeriodicTasks: []GrowthTask{{ID: "spend", Name: "Spend", Kind: "usage_amount", Target: 1, Enabled: true, Chances: 1}}}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	_, err = db.Exec(`INSERT INTO usage_logs(user_id,model,actual_cost,created_at) VALUES($1,'past',60.5,NOW())`, uid)
	require.NoError(t, err)
	text, n, err := s.processGrowthTasks(ctx, c, Job{GroupID: "group", MemberID: "member"}, time.Now(), true)
	require.NoError(t, err)
	require.Equal(t, 25, n)
	require.Contains(t, text, "待继续结算")
	_, err = db.Exec(`UPDATE users SET status='disabled' WHERE id=$1`, uid)
	require.NoError(t, err)
	_, err = s.settleGrowthBatch(ctx, c, 0)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes`).Scan(&count))
	require.Equal(t, 25, count)
	_, err = db.Exec(`UPDATE users SET status='active' WHERE id=$1`, uid)
	require.NoError(t, err)
	for range 2 {
		_, err = s.settleGrowthBatch(ctx, c, 0)
		require.NoError(t, err)
	}
	text, n, err = s.processGrowthTasks(ctx, c, Job{GroupID: "group", MemberID: "member"}, time.Now(), true)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Contains(t, text, "已结算 60 轮\n本轮进度：USD 0.50/1.00")
	require.NotContains(t, text, "待继续结算")
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_reward_codes`).Scan(&count))
	require.Equal(t, 60, count)
}

func TestPeriodicGrowthCheckinRollback(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	s := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	c, err := s.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{Enabled: true, MinAmount: .5, MaxAmount: .5, Growth: GrowthConfig{Tasks: []GrowthTask{}, PeriodicTasks: []GrowthTask{
		{ID: "first", Name: "First", Kind: "checkin_total", Target: 1, Enabled: true, Chances: 2},
		{ID: "second", Name: "Second", Kind: "checkin_total", Target: 1, Enabled: true, Reward: &GrowthReward{Type: "balance", Amount: 2}},
	}}}
	require.NoError(t, s.SaveCheckinConfig(ctx, cfg))
	_, err = db.Exec(`ALTER TABLE redeem_codes ADD CONSTRAINT reject_periodic CHECK(value<>2)`)
	require.NoError(t, err)
	j := Job{GroupID: "group", MemberID: "member"}
	_, err = s.reward(ctx, c, j, "checkin")
	require.Error(t, err)
	for _, table := range []string{"qq_bot_rewards", "qq_bot_reward_codes", "qq_bot_lottery_chance_grants", "redeem_codes", "qq_bot_periodic_growth_progress"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count))
		require.Zero(t, count, table)
	}
	_, err = db.Exec(`ALTER TABLE redeem_codes DROP CONSTRAINT reject_periodic`)
	require.NoError(t, err)
	_, err = s.reward(ctx, c, j, "checkin")
	require.NoError(t, err)
	_, n, err := s.processGrowthTasks(ctx, c, j, time.Now(), true)
	require.NoError(t, err)
	require.Zero(t, n)
	var balance float64
	var chances int
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 2.5, balance)
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants`).Scan(&chances))
	require.Equal(t, 2, chances)
}
