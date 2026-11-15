//go:build integration

package qqbot

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestActivityPreviewAmountDaysUsesDateCursorWithoutSettling(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	bot := savedBot(t, db)
	uid := user(t, db)
	bind(t, db, uid, "member")
	insightsUsageTable(t, db)
	kinds := []string{"usage_amount_days", "usage_amount_streak", "recharge_days", "recharge_streak"}
	cfg := CheckinConfig{Growth: GrowthConfig{Tasks: []GrowthTask{}}}
	for _, kind := range kinds {
		task := GrowthTask{ID: "once_" + kind, Name: kind, Kind: kind, Target: 2, DailyAmount: 10, Enabled: true, Chances: 1}
		cfg.Growth.Tasks = append(cfg.Growth.Tasks, task)
		task.ID = "cycle_" + kind
		cfg.Growth.PeriodicTasks = append(cfg.Growth.PeriodicTasks, task)
	}
	require.NoError(t, bot.SaveCheckinConfig(ctx, cfg))
	base := growthPeriodStart("daily", time.Now()).AddDate(0, 0, -10)
	for i := 0; i < 3; i++ {
		at := base.AddDate(0, 0, i).Add(time.Hour)
		_, err := db.Exec(`INSERT INTO usage_logs(user_id,model,actual_cost,created_at) VALUES($1,'model',10,$2)`, uid, at)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO payment_orders(user_id,amount,paid_at,completed_at) VALUES($1,10,$2,$2)`, uid, at)
		require.NoError(t, err)
	}
	for _, task := range cfg.Growth.PeriodicTasks {
		_, err := db.Exec(`INSERT INTO qq_bot_growth_amount_day_progress(user_id,task_id,kind,last_reward_day,cycles) VALUES($1,$2,$3,$4,2)`, uid, task.ID, task.Kind, base.Format("2006-01-02"))
		require.NoError(t, err)
	}
	preview, err := bot.PreviewActivities(ctx, uid)
	require.NoError(t, err)
	require.Len(t, preview.Tasks, 8)
	for _, task := range preview.Tasks {
		require.True(t, task.Eligible, task.ID+": historic complete runs must remain payable")
		require.Contains(t, task.Progress, "每日")
		require.Contains(t, task.Progress, "USD 10.00")
		if len(task.ID) > len("periodic:") && task.ID[:len("periodic:")] == "periodic:" {
			require.Contains(t, task.Progress, "2/2 天")
			require.Equal(t, "已结算 2 轮；待结算", task.Status)
		}
	}
	for _, table := range []string{"qq_bot_rewards", "qq_bot_reward_codes", "qq_bot_lottery_chance_grants"} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		require.Zero(t, count, "preview wrote to "+table)
	}
	var cursors int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM qq_bot_growth_amount_day_progress WHERE user_id=$1 AND last_reward_day=$2 AND cycles=2`, uid, base.Format("2006-01-02")).Scan(&cursors))
	require.Equal(t, 4, cursors, "preview must not consume days or increment cycles")

	// After one more day has been consumed, the remaining historic day is
	// retained by cumulative tasks but an incomplete stale streak is zero.
	_, err = db.Exec(`UPDATE qq_bot_growth_amount_day_progress SET last_reward_day=$2,cycles=3 WHERE user_id=$1`, uid, base.AddDate(0, 0, 1).Format("2006-01-02"))
	require.NoError(t, err)
	preview, err = bot.PreviewActivities(ctx, uid)
	require.NoError(t, err)
	byID := map[string]PreviewTask{}
	for _, task := range preview.Tasks {
		byID[task.ID] = task
	}
	for _, kind := range kinds {
		task := byID["periodic:cycle_"+kind]
		require.False(t, task.Eligible, kind)
		require.Equal(t, "已结算 3 轮", task.Status)
		count := 1
		if isGrowthAmountStreakKind(kind) {
			count = 0
		}
		require.Contains(t, task.Progress, fmt.Sprintf("%d/2 天", count))
	}
}
