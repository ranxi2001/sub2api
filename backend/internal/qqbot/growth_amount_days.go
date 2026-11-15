package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

type growthAmountDay struct {
	Day    time.Time
	Amount decimal.Decimal
}

func isGrowthAmountDayKind(kind string) bool {
	return growthAmountDaySource(kind) != ""
}

func growthAmountDaySource(kind string) string {
	switch kind {
	case "usage_amount_days", "usage_amount_streak":
		return "usage"
	case "recharge_days", "recharge_streak":
		return "recharge"
	default:
		return ""
	}
}

func isGrowthAmountStreakKind(kind string) bool {
	return kind == "usage_amount_streak" || kind == "recharge_streak"
}

// Keep daily totals, not one prefiltered date list: tasks in the same snapshot
// can require different daily amounts without rescanning the source tables.
func loadGrowthAmountDays(ctx context.Context, db queryRower, uid int64, source string, at time.Time, lockSources bool) ([]growthAmountDay, error) {
	var entries string
	switch source {
	case "usage":
		entries = `SELECT (created_at AT TIME ZONE 'Asia/Shanghai')::date AS day, COALESCE(actual_cost,0) AS amount
 FROM usage_logs WHERE user_id=$1 AND created_at <= $2`
	case "recharge":
		orders := `SELECT amount,bonus_amount,refund_amount,status,completed_at FROM payment_orders
 WHERE user_id=$1 AND order_type='balance' AND paid_at IS NOT NULL AND completed_at IS NOT NULL
 AND paid_at <= $2 AND completed_at <= $2 ORDER BY id`
		codes := `SELECT r.value,r.used_at FROM redeem_codes r
 WHERE r.used_by=$1 AND r.type='balance' AND r.status='used' AND r.used_at IS NOT NULL AND r.used_at <= $2 AND r.value>0
 AND NOT EXISTS(SELECT 1 FROM payment_orders p WHERE p.recharge_code=r.code) ORDER BY r.id`
		if lockSources {
			orders += ` FOR SHARE`
			codes += ` FOR SHARE OF r`
		}
		// Match rechargeSum: exclude bonuses, suspend unfinished refunds, apply
		// partial refunds to principal pro rata, and never double count codes
		// linked to any payment order. The original completion date owns net value.
		entries = `SELECT (completed_at AT TIME ZONE 'Asia/Shanghai')::date AS day, CASE
 WHEN status IN ('COMPLETED','REFUND_FAILED') THEN GREATEST(amount-bonus_amount,0)
 WHEN status='PARTIALLY_REFUNDED' AND amount>0 THEN ROUND(GREATEST(amount-bonus_amount,0)*GREATEST(amount-refund_amount,0)/amount,8)
 ELSE 0 END AS amount FROM (` + orders + `) orders
 UNION ALL SELECT (used_at AT TIME ZONE 'Asia/Shanghai')::date AS day,value AS amount FROM (` + codes + `) codes`
	default:
		return nil, errors.New("按日金额统计来源不支持")
	}
	var raw []byte
	err := db.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('day',day,'amount',amount::text) ORDER BY day),'[]'::jsonb)
 FROM (SELECT day,SUM(amount) AS amount FROM (`+entries+`) entries GROUP BY day) totals`, uid, at).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Day    string          `json:"day"`
		Amount decimal.Decimal `json:"amount"`
	}
	if err = json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	days := make([]growthAmountDay, 0, len(rows))
	for _, row := range rows {
		date, err := time.ParseInLocation("2006-01-02", row.Day, time.FixedZone("Asia/Shanghai", 8*3600))
		if err != nil {
			return nil, err
		}
		days = append(days, growthAmountDay{Day: date, Amount: row.Amount})
	}
	return days, nil
}

// Return one Beijing midnight per qualifying day in ascending order. No
// rounding occurs at the threshold: 0.0099999999 never satisfies USD 0.01.
func dailyGrowthDates(p growthProgress, task GrowthTask) []time.Time {
	source := growthAmountDaySource(task.Kind)
	if source == "" || task.DailyAmount <= 0 {
		return nil
	}
	threshold := decimal.NewFromFloat(task.DailyAmount)
	seen := map[string]bool{}
	var dates []time.Time
	for _, day := range p.amountDays[source] {
		if !day.Amount.GreaterThanOrEqual(threshold) || !p.at.IsZero() && day.Day.After(p.at) {
			continue
		}
		date := growthPeriodStart("daily", day.Day)
		key := date.Format("2006-01-02")
		if !seen[key] {
			seen[key] = true
			dates = append(dates, date)
		}
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })
	return dates
}

func growthAmountDateRuns(dates []time.Time, at time.Time) (longest, current int) {
	for index, date := range dates {
		if index == 0 || !date.Equal(dates[index-1].AddDate(0, 0, 1)) {
			current = 0
		}
		current++
		if current > longest {
			longest = current
		}
	}
	if at.IsZero() {
		at = time.Now()
	}
	if len(dates) == 0 || dates[len(dates)-1].Before(growthPeriodStart("daily", at).AddDate(0, 0, -1)) {
		current = 0
	}
	return longest, current
}
