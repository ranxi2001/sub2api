//go:build integration

package qqbot

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestGrowthAmountDaysUsageAggregationAndThresholds(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	uid, other := user(t, db), user(t, db)
	insightsUsageTable(t, db)
	at := time.Date(2026, 11, 12, 4, 0, 0, 0, time.UTC)
	_, err := db.Exec(`INSERT INTO usage_logs(user_id,model,actual_cost,created_at) VALUES
 ($1,'first',0.0049999999,'2026-11-09T15:59:59Z'),
 ($1,'second',0.005,'2026-11-09T15:59:59Z'),
 ($1,'midnight',0.005,'2026-11-09T16:00:00Z'),
 ($1,'same_day',0.005,'2026-11-10T05:00:00Z'),
 ($1,'larger',5,'2026-11-10T16:00:00Z'),
 ($1,'as_of',5,'2026-11-12T04:00:00Z'),
 ($1,'future',100,'2026-11-12T04:00:00.000001Z'),
 ($2,'other_user',100,'2026-11-08T16:00:00Z')`, uid, other)
	require.NoError(t, err)
	tasks := []GrowthTask{
		{ID: "small", Kind: "usage_amount_days", Target: 3, DailyAmount: .01},
		{ID: "large", Kind: "usage_amount_days", Target: 2, DailyAmount: 5},
		{ID: "streak", Kind: "usage_amount_streak", Target: 2, DailyAmount: 5},
	}
	progress, err := loadGrowthProgress(ctx, db, uid, tasks, at, false)
	require.NoError(t, err)
	p := progress["once"]
	require.Equal(t, at, p.at)
	require.Len(t, p.amountDays["usage"], 4)
	require.Equal(t, "2026-11-09", p.amountDays["usage"][0].Day.Format("2006-01-02"))
	require.True(t, p.amountDays["usage"][0].Amount.Equal(decimal.RequireFromString(".0099999999")))
	require.Len(t, dailyGrowthDates(p, tasks[0]), 3)
	require.Len(t, dailyGrowthDates(p, tasks[1]), 2)
	eligible, text := p.task(tasks[2])
	require.True(t, eligible)
	require.Contains(t, text, "当前连续 2/2 天")
}

func TestGrowthAmountDaysRechargeNetRefundCodesAndCutoff(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	uid, other := user(t, db), user(t, db)
	at := time.Date(2026, 11, 12, 4, 0, 0, 0, time.UTC)
	_, err := db.Exec(`INSERT INTO payment_orders(user_id,amount,bonus_amount,refund_amount,status,paid_at,completed_at,order_type,recharge_code) VALUES
 ($1,20,5,0,'COMPLETED','2026-11-08T12:00:00Z','2026-11-08T16:00:00Z','balance','online'),
 ($1,20,5,10,'PARTIALLY_REFUNDED','2026-11-09T12:00:00Z','2026-11-09T16:00:00Z','balance','partial'),
 ($1,10,2,0,'REFUND_FAILED','2026-11-10T12:00:00Z','2026-11-10T16:00:00Z','balance','refund_failed'),
 ($1,100,100,0,'COMPLETED','2026-11-10T12:00:00Z','2026-11-10T16:00:00Z','balance','gift'),
 ($1,100,0,0,'COMPLETED','2026-11-13T00:00:00Z','2026-11-11T16:00:00Z','balance','future_paid'),
 ($1,100,0,0,'COMPLETED','2026-11-11T00:00:00Z','2026-11-12T04:00:00.000001Z','balance','future_completed'),
 ($1,100,0,0,'COMPLETED',NULL,'2026-11-11T16:00:00Z','balance','unpaid'),
 ($1,100,0,0,'COMPLETED','2026-11-11T00:00:00Z',NULL,'balance','unfinished'),
 ($1,100,0,0,'REFUNDING','2026-11-11T00:00:00Z','2026-11-11T16:00:00Z','balance','refunding'),
 ($1,100,0,0,'REFUND_PENDING','2026-11-11T00:00:00Z','2026-11-11T16:00:00Z','balance','pending'),
 ($1,100,0,100,'REFUNDED','2026-11-11T00:00:00Z','2026-11-11T16:00:00Z','balance','refunded'),
 ($1,100,0,0,'COMPLETED','2026-11-11T00:00:00Z','2026-11-11T16:00:00Z','subscription','subscription'),
 ($2,100,0,0,'PENDING',NULL,NULL,'balance','linked_to_other')`, uid, other)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO redeem_codes(code,type,value,status,used_by,used_at) VALUES
 ('standalone','balance',2.5,'used',$1,'2026-11-09T16:00:00Z'),
 ('online','balance',20,'used',$1,'2026-11-08T16:00:00Z'),
 ('linked_to_other','balance',100,'used',$1,'2026-11-11T16:00:00Z'),
 ('future_code','balance',100,'used',$1,'2026-11-12T04:00:00.000001Z'),
 ('negative','balance',-1,'used',$1,'2026-11-11T16:00:00Z'),
 ('wrong_type','subscription',100,'used',$1,'2026-11-11T16:00:00Z'),
 ('unused','balance',100,'unused',$1,'2026-11-11T16:00:00Z'),
 ('other','balance',100,'used',$2,'2026-11-11T16:00:00Z')`, uid, other)
	require.NoError(t, err)
	for _, lock := range []bool{false, true} {
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		days, err := loadGrowthAmountDays(ctx, tx, uid, "recharge", at, lock)
		require.NoError(t, err)
		amounts := map[string]string{}
		for _, day := range days {
			amounts[day.Day.Format("2006-01-02")] = day.Amount.String()
		}
		require.Equal(t, map[string]string{"2026-11-09": "15", "2026-11-10": "10", "2026-11-11": "8", "2026-11-12": "0"}, amounts)
		p := growthProgress{at: at, amountDays: map[string][]growthAmountDay{"recharge": days}}
		require.Len(t, dailyGrowthDates(p, GrowthTask{Kind: "recharge_days", DailyAmount: 10}), 2)
		require.Len(t, dailyGrowthDates(p, GrowthTask{Kind: "recharge_streak", DailyAmount: 8}), 3)
		require.NoError(t, tx.Rollback())
	}
	// Refunds reduce the original completion day, never create a new refund day.
	_, err = db.Exec(`UPDATE payment_orders SET status='PARTIALLY_REFUNDED',refund_amount=10 WHERE recharge_code='online'`)
	require.NoError(t, err)
	days, err := loadGrowthAmountDays(ctx, db, uid, "recharge", at, false)
	require.NoError(t, err)
	require.Equal(t, "2026-11-09", days[0].Day.Format("2006-01-02"))
	require.True(t, days[0].Amount.Equal(decimal.RequireFromString("7.5")))
}

func TestGrowthAmountDaysRechargeLocksSources(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	uid := user(t, db)
	_, err := db.Exec(`INSERT INTO payment_orders(user_id,amount,recharge_code) VALUES($1,10,'online')`, uid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO redeem_codes(code,type,value,status,used_by,used_at) VALUES('standalone','balance',2,'used',$1,NOW())`, uid)
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = loadGrowthAmountDays(ctx, tx, uid, "recharge", time.Now(), true)
	require.NoError(t, err)
	for _, query := range []string{`UPDATE payment_orders SET refund_amount=1 WHERE user_id=$1`, `UPDATE redeem_codes SET value=1 WHERE used_by=$1`} {
		update, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		_, err = update.Exec(`SET LOCAL lock_timeout='50ms'`)
		require.NoError(t, err)
		_, err = update.Exec(query, uid)
		require.ErrorContains(t, err, "lock timeout")
		require.NoError(t, update.Rollback())
	}
}
