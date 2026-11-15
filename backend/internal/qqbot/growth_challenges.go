package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func growthTaskPeriod(task GrowthTask) string {
	if task.Period == "" {
		return "once"
	}
	return task.Period
}

// Calendar challenges follow the same Beijing timezone as check-ins. Weekly
// challenges start on Monday, including when the server runs in another zone.
func growthPeriodStart(period string, at time.Time) time.Time {
	local := at.In(time.FixedZone("Asia/Shanghai", 8*3600))
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	if period == "weekly" {
		start = start.AddDate(0, 0, -(int(start.Weekday())+6)%7)
	}
	return start
}

func growthTaskKeySuffix(task GrowthTask, at time.Time) string {
	period := growthTaskPeriod(task)
	if period == "once" {
		return task.ID
	}
	return task.ID + ":" + period + ":" + growthPeriodStart(period, at).Format("2006-01-02")
}

func growthTaskRewardKey(uid int64, task GrowthTask, at time.Time) string {
	return fmt.Sprintf("qq-growth:%d:%s", uid, growthTaskKeySuffix(task, at))
}

func growthPreviousPeriodEnd(period string, at time.Time) time.Time {
	// PostgreSQL timestamps have microsecond precision: subtracting a single
	// nanosecond can round back to midnight and include the next period.
	return growthPeriodStart(period, at).Add(-time.Microsecond)
}

func growthTaskPeriodLabel(task GrowthTask) string {
	switch growthTaskPeriod(task) {
	case "daily":
		return "每日挑战"
	case "weekly":
		return "每周挑战"
	default:
		return "成长成就"
	}
}

// Callers decide which tasks require progress. Grouping by period avoids one
// aggregate scan per task; all thresholds use the same snapshot and cutoff.
func loadGrowthProgress(ctx context.Context, db queryRower, uid int64, tasks []GrowthTask, at time.Time, lockRecharge bool) (map[string]growthProgress, error) {
	progress := make(map[string]growthProgress)
	groups := make(map[string][]GrowthTask)
	for _, task := range tasks {
		period := growthTaskPeriod(task)
		groups[period] = append(groups[period], task)
	}
	for period, scoped := range groups {
		p := growthProgress{at: at}
		var usage, recharge, checkin, invites, usageAmountDays, rechargeAmountDays bool
		for _, task := range scoped {
			switch task.Kind {
			case "usage_amount_days", "usage_amount_streak":
				usageAmountDays = true
			case "recharge_days", "recharge_streak":
				rechargeAmountDays = true
			case "usage_count", "usage_tokens", "usage_amount", "usage_days", "model_count":
				usage = true
			case "recharge_total":
				recharge = true
			case "checkin_total", "checkin_streak":
				checkin = true
			case "invite_count":
				invites = true
			}
		}
		var start any
		if period != "once" {
			start = growthPeriodStart(period, at)
		}
		if usage {
			err := db.QueryRowContext(ctx, `SELECT COUNT(*),
 COALESCE(SUM(COALESCE(input_tokens,0)::numeric+COALESCE(output_tokens,0)::numeric+COALESCE(cache_creation_tokens,0)::numeric+COALESCE(cache_read_tokens,0)::numeric),0),
 COALESCE(SUM(actual_cost),0),COUNT(DISTINCT (created_at AT TIME ZONE 'Asia/Shanghai')::date),COUNT(DISTINCT NULLIF(BTRIM(model),''))
 FROM usage_logs WHERE user_id=$1 AND created_at <= $2 AND ($3::timestamptz IS NULL OR created_at >= $3)`, uid, at, start).Scan(&p.usage, &p.tokens, &p.spent, &p.days, &p.models)
			if err != nil {
				return nil, err
			}
		}
		if recharge {
			var err error
			p.recharge, err = rechargeSum(ctx, db, uid, nil, lockRecharge)
			if err != nil {
				return nil, err
			}
		}
		if usageAmountDays || rechargeAmountDays {
			p.amountDays = make(map[string][]growthAmountDay)
			for _, source := range []string{"usage", "recharge"} {
				if source == "usage" && !usageAmountDays || source == "recharge" && !rechargeAmountDays {
					continue
				}
				days, err := loadGrowthAmountDays(ctx, db, uid, source, at, lockRecharge)
				if err != nil {
					return nil, err
				}
				p.amountDays[source] = days
			}
		}
		if checkin {
			var err error
			p.total, p.streak, err = checkinProgress(ctx, db, uid, at)
			if err != nil {
				return nil, err
			}
		}
		if invites {
			// Only settled invitations count. The original payout validates the
			// invite relationship, binding and eligibility; the account ledger
			// remains stable across group changes and QQ rebindings.
			err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT source_user_id) FROM qq_bot_rewards
 WHERE user_id=$1 AND kind='invite' AND source_user_id IS NOT NULL AND source_user_id<>$1
 AND created_at <= $2 AND ($3::timestamptz IS NULL OR created_at >= $3)`, uid, at, start).Scan(&p.invites)
			if err != nil {
				return nil, err
			}
		}
		progress[period] = p
	}
	return progress, nil
}

// The monitor runs once per minute, so the last requests before midnight can
// first become visible after reset. Settle one previous period per calendar
// scope before the current period; grant keys keep retries and queries safe.
func settlePreviousGrowthPeriods(ctx context.Context, tx *sql.Tx, c Config, j Job, uid int64, tasks []GrowthTask, at time.Time) (int, bool, error) {
	grants, changed := 0, false
	for _, period := range []string{"daily", "weekly"} {
		previous := growthPreviousPeriodEnd(period, at)
		var pending []GrowthTask
		for _, task := range tasks {
			if growthTaskPeriod(task) != period || !task.Enabled || !task.reward().enabled() {
				continue
			}
			_, status, err := activityRewardSnapshot(ctx, tx, growthTaskRewardKey(uid, task, previous))
			if err != nil {
				return 0, false, err
			}
			if status == "" {
				pending = append(pending, task)
			}
		}
		if len(pending) == 0 {
			continue
		}
		progress, err := loadGrowthProgress(ctx, tx, uid, pending, previous, true)
		if err != nil {
			return 0, false, err
		}
		for _, task := range pending {
			eligible, _ := progress[period].task(task)
			if !eligible {
				continue
			}
			reward := task.reward()
			granted, err := grantActivityReward(ctx, tx, c, j, uid, growthTaskRewardKey(uid, task, previous), "growth", "补结算"+growthTaskPeriodLabel(task)+"："+task.Name, reward, at)
			if errors.Is(err, ErrRewardBudget) {
				continue
			}
			if err != nil {
				return 0, false, err
			}
			if granted {
				grants++
				changed = changed || reward.Type == "balance"
			}
		}
	}
	return grants, changed, nil
}
