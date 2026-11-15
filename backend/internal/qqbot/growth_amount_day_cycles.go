package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Re-evaluate only dates after the last rewarded cycle. Unrewarded days still
// reflect refunds, retained source data and rule edits; rewarded days never
// become available again. The caller serializes settlement on the user row.
func settlePeriodicAmountDays(ctx context.Context, tx *sql.Tx, c Config, j Job, uid int64, task GrowthTask, progress growthProgress, at time.Time) (string, int, bool, error) {
	var cursor time.Time
	var cycles int64
	err := tx.QueryRowContext(ctx, `SELECT last_reward_day,cycles FROM qq_bot_growth_amount_day_progress WHERE user_id=$1 AND task_id=$2 AND kind=$3`, uid, task.ID, task.Kind).Scan(&cursor, &cycles)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, err
	}
	dates := dailyGrowthDates(progress, task)
	// SQL DATE values scan in UTC; compare calendar dates, not UTC instants.
	eligible := make([]time.Time, 0, len(dates))
	for _, d := range dates {
		if cursor.IsZero() || d.Format("2006-01-02") > cursor.Format("2006-01-02") {
			eligible = append(eligible, d)
		}
	}
	streak := isGrowthAmountStreakKind(task.Kind)
	needed := int(task.Target)
	if needed < 1 {
		return "", 0, false, errors.New("达标天数须大于零")
	}
	remaining, settled, grants := 0, 0, 0
	balanceChanged, budgetBlocked, pending := false, false, false
	var previous time.Time
	for _, d := range eligible {
		if streak && !previous.IsZero() && !d.Equal(previous.AddDate(0, 0, 1)) {
			remaining = 0
		}
		remaining++
		previous = d
		if remaining < needed {
			continue
		}
		if settled == maxPeriodicCyclesPerPass {
			pending = true
			break
		}
		reward := task.reward()
		key := fmt.Sprintf("qq-growth-cycle:%d:%s:%s:%d", uid, task.ID, task.Kind, cycles+1)
		granted, e := grantActivityReward(ctx, tx, c, j, uid, key, "growth", "周期任务："+task.Name, reward, at)
		if errors.Is(e, ErrRewardBudget) {
			budgetBlocked = true
			break
		}
		if e != nil {
			return "", 0, false, e
		}
		cycles++
		cursor = d
		settled++
		remaining = 0
		if granted {
			grants++
			balanceChanged = balanceChanged || reward.Type == "balance"
		}
	}
	if settled > 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_growth_amount_day_progress(user_id,task_id,kind,last_reward_day,cycles) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(user_id,task_id,kind) DO UPDATE SET last_reward_day=EXCLUDED.last_reward_day,cycles=EXCLUDED.cycles,updated_at=NOW()`, uid, task.ID, task.Kind, cursor.Format("2006-01-02"), cycles)
		if err != nil {
			return "", 0, false, err
		}
	}
	if streak && !pending && !budgetBlocked && !previous.IsZero() && previous.Before(growthPeriodStart("daily", at).AddDate(0, 0, -1)) {
		remaining = 0
	}
	status := fmt.Sprintf("已结算 %d 轮\n本轮进度：%d/%d 天（每日满 USD %.2f）", cycles, remaining, needed, task.DailyAmount)
	if pending {
		status += "\n待继续结算"
	}
	if budgetBlocked {
		status += "\n预算不足，等待后续结算"
	}
	return status, grants, balanceChanged, nil
}
