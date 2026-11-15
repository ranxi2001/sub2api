package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Catch-up is bounded per task, so large historical totals cannot hold the user
// and configuration locks indefinitely. The monitor continues on later passes.
const maxPeriodicCyclesPerPass = 25

func (p growthProgress) value(kind string) decimal.Decimal {
	switch kind {
	case "usage_count":
		return decimal.NewFromInt(int64(p.usage))
	case "checkin_total":
		return decimal.NewFromInt(int64(p.total))
	case "usage_tokens":
		return p.tokens
	case "usage_amount":
		return p.spent
	case "recharge_total":
		return p.recharge
	default:
		return decimal.Zero
	}
}

func periodicProgressText(kind string, remaining, target decimal.Decimal) string {
	switch kind {
	case "usage_amount", "recharge_total":
		return fmt.Sprintf("USD %s/%s", remaining.Truncate(2).StringFixed(2), target.StringFixed(2))
	case "usage_tokens":
		return fmt.Sprintf("%s/%s Token", remaining.StringFixed(0), target.StringFixed(0))
	case "checkin_total":
		return fmt.Sprintf("%s/%s 天", remaining.StringFixed(0), target.StringFixed(0))
	default:
		return fmt.Sprintf("%s/%s 次", remaining.StringFixed(0), target.StringFixed(0))
	}
}

// The caller holds the active user row and config locks. Progress, cycle keys
// and reward snapshots commit together. Historical retained data counts from
// zero; a decreasing total (refund/retention) never resets consumed progress.
func settlePeriodicGrowth(ctx context.Context, tx *sql.Tx, c Config, j Job, uid int64, tasks []GrowthTask, progress growthProgress, at time.Time) (string, int, bool, error) {
	text := "**周期性成长任务**"
	grants, balanceChanged := 0, false
	for i, task := range tasks {
		reward := task.reward()
		status := "未开启"
		if task.Enabled && reward.enabled() {
			if isGrowthAmountDayKind(task.Kind) {
				detail, n, changed, e := settlePeriodicAmountDays(ctx, tx, c, j, uid, task, progress, at)
				if e != nil {
					return "", 0, false, e
				}
				grants += n
				balanceChanged = balanceChanged || changed
				status = detail
				if reward.Type == "physical" || reward.Type == "other" {
					status += "\n奖品由管理员发放"
				}
				text += fmt.Sprintf("\n\n**%d. %s**\n状态：%s\n每轮奖励：%s", i+1, replyPlainLabel(task.Name), status, reward.description())
				continue
			}
			consumed, cycles := decimal.Zero, int64(0)
			err := tx.QueryRowContext(ctx, `SELECT consumed,cycles FROM qq_bot_periodic_growth_progress WHERE user_id=$1 AND task_id=$2 AND kind=$3`, uid, task.ID, task.Kind).Scan(&consumed, &cycles)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return "", 0, false, err
			}
			target := decimal.NewFromFloat(task.Target)
			if !target.IsPositive() {
				return "", 0, false, errors.New("周期任务目标必须大于零")
			}
			remaining := decimal.Max(decimal.Zero, progress.value(task.Kind).Sub(consumed))
			settled := 0
			for remaining.GreaterThanOrEqual(target) && settled < maxPeriodicCyclesPerPass {
				cycles++
				key := fmt.Sprintf("qq-growth-cycle:%d:%s:%s:%d", uid, task.ID, task.Kind, cycles)
				granted, err := grantActivityReward(ctx, tx, c, j, uid, key, "growth", "周期任务："+task.Name, reward, at)
				if err != nil {
					if errors.Is(err, ErrRewardBudget) {
						cycles--
						break
					}
					return "", 0, false, err
				}
				consumed, remaining = consumed.Add(target), remaining.Sub(target)
				settled++
				if granted {
					grants++
					balanceChanged = balanceChanged || reward.Type == "balance"
				}
			}
			if settled > 0 {
				_, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_periodic_growth_progress(user_id,task_id,kind,consumed,cycles) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(user_id,task_id,kind) DO UPDATE SET consumed=EXCLUDED.consumed,cycles=EXCLUDED.cycles,updated_at=NOW()`, uid, task.ID, task.Kind, consumed, cycles)
				if err != nil {
					return "", 0, false, err
				}
			}
			status = fmt.Sprintf("已结算 %d 轮\n本轮进度：%s", cycles, periodicProgressText(task.Kind, remaining, target))
			if remaining.GreaterThanOrEqual(target) {
				status += "\n待继续结算"
			}
			if reward.Type == "physical" || reward.Type == "other" {
				status += "\n奖品由管理员发放"
			}
		}
		text += fmt.Sprintf("\n\n**%d. %s**\n状态：%s\n每轮奖励：%s", i+1, replyPlainLabel(task.Name), status, reward.description())
	}
	return text, grants, balanceChanged, nil
}
