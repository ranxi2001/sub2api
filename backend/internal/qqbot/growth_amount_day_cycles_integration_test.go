//go:build integration

package qqbot

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func seedGrowthAmountDay(t *testing.T, db *sql.DB, uid int64, at time.Time, amount float64) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO usage_logs(user_id,model,actual_cost,created_at) VALUES($1,'model',$2,$3)`, uid, amount, at)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO payment_orders(user_id,amount,paid_at,completed_at) VALUES($1,$2,$3,$3)`, uid, amount, at)
	require.NoError(t, err)
}

func TestGrowthAmountDayCyclesAllKindsGapsAndRebinding(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	bot := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	insightsUsageTable(t, db)
	c, err := bot.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{}}}
	for _, kind := range []string{"usage_amount_days", "usage_amount_streak", "recharge_days", "recharge_streak"} {
		cfg.Growth.PeriodicTasks = append(cfg.Growth.PeriodicTasks, GrowthTask{ID: kind, Name: kind, Kind: kind, Target: 2, DailyAmount: 10, Enabled: true, Chances: 1})
	}
	require.NoError(t, bot.SaveCheckinConfig(ctx, cfg))
	base := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	for _, day := range []int{0, 1, 2, 4, 5, 7, 8} {
		seedGrowthAmountDay(t, db, uid, base.AddDate(0, 0, day), 10)
	}
	j := Job{GroupID: "group", MemberID: "member"}
	text, n, err := bot.processGrowthTasks(ctx, c, j, base.AddDate(0, 0, 8), true)
	require.NoError(t, err)
	require.Equal(t, 12, n, "three non-overlapping cycles for every metric")
	require.Contains(t, text, "每日满 USD 10.00")
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_growth_amount_day_progress WHERE user_id=$1 AND cycles=3`, uid).Scan(&count))
	require.Equal(t, 4, count)
	_, err = db.Exec(`UPDATE qq_bot_bindings SET member_id='rebound',group_id='other-group' WHERE user_id=$1`, uid)
	require.NoError(t, err)
	bot = New(db, testEncryptor{}, true, nil)
	j = Job{GroupID: "other-group", MemberID: "rebound"}
	_, n, err = bot.processGrowthTasks(ctx, c, j, base.AddDate(0, 0, 8), true)
	require.NoError(t, err)
	require.Zero(t, n, "restart and rebind must preserve rewarded dates")
	seedGrowthAmountDay(t, db, uid, base.AddDate(0, 0, 9), 10)
	_, n, err = bot.processGrowthTasks(ctx, c, j, base.AddDate(0, 0, 9), true)
	require.NoError(t, err)
	require.Equal(t, 2, n, "only cumulative tasks have two new days since their cursor")
	seedGrowthAmountDay(t, db, uid, base.AddDate(0, 0, 10), 10)
	_, n, err = bot.processGrowthTasks(ctx, c, j, base.AddDate(0, 0, 10), true)
	require.NoError(t, err)
	require.Equal(t, 2, n, "streak tasks may award the next non-overlapping pair")
	var chances int
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, uid).Scan(&chances))
	require.Equal(t, 16, chances)
}

func TestGrowthAmountDayCyclesAtomicConcurrentAndRuleEdits(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	bot := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	insightsUsageTable(t, db)
	c, err := bot.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{}, PeriodicTasks: []GrowthTask{
		{ID: "spend", Name: "Spend", Kind: "usage_amount_days", Target: 2, DailyAmount: 10, Enabled: true, Reward: &GrowthReward{Type: "balance", Amount: 1}},
		{ID: "recharge", Name: "Recharge", Kind: "recharge_streak", Target: 2, DailyAmount: 10, Enabled: true, Chances: 2},
	}}}
	require.NoError(t, bot.SaveCheckinConfig(ctx, cfg))
	base := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	for i, amount := range []float64{10, 10, 4, 5} {
		seedGrowthAmountDay(t, db, uid, base.AddDate(0, 0, i), amount)
	}
	_, err = db.Exec(`ALTER TABLE qq_bot_lottery_chance_grants ADD CONSTRAINT reject_growth_day_chances CHECK(chances<>2)`)
	require.NoError(t, err)
	j := Job{GroupID: "group", MemberID: "member"}
	at := base.AddDate(0, 0, 3)
	_, _, err = bot.processGrowthTasks(ctx, c, j, at, false)
	require.Error(t, err)
	var count int
	var balance float64
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Zero(t, balance)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_growth_amount_day_progress`).Scan(&count))
	require.Zero(t, count, "failed reward must roll back every cursor and balance")
	_, err = db.Exec(`ALTER TABLE qq_bot_lottery_chance_grants DROP CONSTRAINT reject_growth_day_chances`)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, e := bot.processGrowthTasks(ctx, c, j, at, false)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	require.NoError(t, db.QueryRow(`SELECT balance FROM users WHERE id=$1`, uid).Scan(&balance))
	require.Equal(t, 1.0, balance)
	// Lowering the daily threshold never makes already rewarded dates available.
	cfg.Growth.PeriodicTasks[0].DailyAmount = 5
	require.NoError(t, bot.SaveCheckinConfig(ctx, cfg))
	_, n, err := bot.processGrowthTasks(ctx, c, j, at, false)
	require.NoError(t, err)
	require.Zero(t, n, "only the fourth day qualifies after the cursor")
	cfg.Growth.PeriodicTasks[0].Target = 1
	require.NoError(t, bot.SaveCheckinConfig(ctx, cfg))
	_, n, err = bot.processGrowthTasks(ctx, c, j, at, false)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	cfg.Growth.PeriodicTasks[0].DailyAmount = 1
	require.NoError(t, bot.SaveCheckinConfig(ctx, cfg))
	_, n, err = bot.processGrowthTasks(ctx, c, j, at, false)
	require.NoError(t, err)
	require.Zero(t, n, "late qualification before the new cursor cannot replay history")
	// Refund after payment does not revoke a reward or rewind the cursor.
	_, err = db.Exec(`UPDATE payment_orders SET status='REFUNDED',refund_amount=amount WHERE user_id=$1`, uid)
	require.NoError(t, err)
	_, n, err = bot.processGrowthTasks(ctx, c, j, at, false)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestGrowthAmountDayCyclesBoundedCatchupAndBudget(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	bot := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	insightsUsageTable(t, db)
	c, err := bot.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{}, PeriodicTasks: []GrowthTask{
		{ID: "daily", Name: "Every day", Kind: "usage_amount_streak", Target: 1, DailyAmount: 1, Enabled: true, Reward: &GrowthReward{Type: "balance", Amount: 1}},
	}}}
	require.NoError(t, bot.SaveCheckinConfig(ctx, cfg))
	base := time.Now().AddDate(0, 0, -30)
	for day := range 30 {
		seedGrowthAmountDay(t, db, uid, base.AddDate(0, 0, day), 1)
	}
	budget := 0.0
	c.RewardBudgetDaily = &budget
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	j := Job{GroupID: "group", MemberID: "member"}
	text, n, err := bot.processGrowthTasks(ctx, c, j, time.Now(), false)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Contains(t, text, "预算不足")
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_growth_amount_day_progress`).Scan(&count))
	require.Zero(t, count)
	c.RewardBudgetDaily = nil
	_, err = bot.SaveConfig(ctx, c)
	require.NoError(t, err)
	for pass, want := range []int{25, 5, 0} {
		_, n, err = bot.processGrowthTasks(ctx, c, j, time.Now(), false)
		require.NoError(t, err, fmt.Sprint(pass))
		require.Equal(t, want, n)
	}
	require.NoError(t, db.QueryRow(`SELECT cycles FROM qq_bot_growth_amount_day_progress WHERE user_id=$1 AND task_id='daily'`, uid).Scan(&count))
	require.Equal(t, 30, count)
}

func TestGrowthAmountDaysAchievementsAutomaticHistoricalAndIndependentThresholds(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	bot := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	insightsUsageTable(t, db)
	c, err := bot.Config(ctx)
	require.NoError(t, err)
	cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{}}}
	for _, kind := range []string{"usage_amount_days", "usage_amount_streak", "recharge_days", "recharge_streak"} {
		cfg.Growth.Tasks = append(cfg.Growth.Tasks, GrowthTask{ID: kind, Name: kind, Kind: kind, Target: 2, DailyAmount: 10, Enabled: true, Chances: 1})
	}
	cfg.Growth.Tasks = append(cfg.Growth.Tasks, GrowthTask{ID: "higher_threshold", Name: "Higher amount", Kind: "usage_amount_days", Target: 2, DailyAmount: 20, Enabled: true, Chances: 1})
	require.NoError(t, bot.SaveCheckinConfig(ctx, cfg))
	base := time.Now().AddDate(0, 0, -10)
	seedGrowthAmountDay(t, db, uid, base, 10)
	seedGrowthAmountDay(t, db, uid, base.AddDate(0, 0, 1), 10)
	// The historical streak ended over a week ago. The monitor must still grant
	// its one-time achievement, without confusing another task's higher amount.
	_, err = bot.settleGrowthBatch(ctx, c, 0)
	require.NoError(t, err)
	var chances int
	require.NoError(t, db.QueryRow(`SELECT SUM(chances) FROM qq_bot_lottery_chance_grants WHERE user_id=$1`, uid).Scan(&chances))
	require.Equal(t, 4, chances)
	for i := range cfg.Growth.Tasks {
		cfg.Growth.Tasks[i].DailyAmount = 1
	}
	require.NoError(t, bot.SaveCheckinConfig(ctx, cfg))
	_, n, err := bot.processGrowthTasks(ctx, c, Job{GroupID: "group", MemberID: "member"}, time.Now(), false)
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the previously unclaimed higher-threshold task can grant")
	_, n, err = bot.processGrowthTasks(ctx, c, Job{GroupID: "group", MemberID: "member"}, time.Now(), false)
	require.NoError(t, err)
	require.Zero(t, n, "editing amounts never resets one-time entitlements")
}
