//go:build integration

package qqbot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLotteryCountsHistoricalAndNewBalanceRedemptions(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, _ := lotteryFixture(t, 6.25)
	other := user(t, db)
	_, err := db.Exec(`INSERT INTO redeem_codes(code,type,value,status,used_by,used_at) VALUES
 ('historical','balance',3.50,'used',$1,NOW()-INTERVAL '1 year'),
 ('new-redemption','balance',0.25,'unused',NULL,NULL),
 ('unused','balance',100,'unused',$1,NOW()),
 ('disabled','balance',100,'disabled',$1,NOW()),
 ('missing-time','balance',100,'used',$1,NULL),
 ('admin-credit','admin_balance',100,'used',$1,NOW()),
 ('concurrency','concurrency',100,'used',$1,NOW()),
 ('subscription','subscription',100,'used',$1,NOW()),
 ('negative','balance',-100,'used',$1,NOW()),
 ('zero','balance',0,'used',$1,NOW()),
 ('other-user','balance',100,'used',$2,NOW())`, uid, other)
	require.NoError(t, err)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, 9.75, state.RechargeTotal)
	require.Zero(t, state.Earned)
	require.Zero(t, state.Remaining)
	require.Equal(t, 0.25, state.NextRecharge)

	_, err = db.Exec(`UPDATE redeem_codes SET status='used',used_by=$1,used_at=NOW() WHERE code='new-redemption'`, uid)
	require.NoError(t, err)
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, float64(10), state.RechargeTotal)
	require.Equal(t, int64(1), state.Earned)
	require.Equal(t, int64(1), state.Remaining)
	reply, err := s.lotteryInfo(ctx, c, Job{MemberID: "member"}, false)
	require.NoError(t, err)
	require.Contains(t, reply, "剩余抽奖次数：1 次")

	reply, err = s.drawLottery(ctx, c, Job{ID: "redeemed-draw", MemberID: "member", GroupID: "group"})
	require.NoError(t, err)
	require.Contains(t, reply, "奖励额度：USD 1.00")
	// The prize writes an admin_balance ledger row and must not create new chances.
	state, err = s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, float64(10), state.RechargeTotal)
	require.Equal(t, int64(1), state.Earned)
	require.Equal(t, int64(1), state.Used)
	require.Zero(t, state.Remaining)
	reply, err = s.drawLottery(ctx, c, Job{ID: "no-extra-draw", MemberID: "member", GroupID: "group"})
	require.NoError(t, err)
	require.Contains(t, reply, "抽奖次数不足")
}

func TestLotteryPaymentCodesNeverDuplicateOrdersOrBypassRefunds(t *testing.T) {
	ctx := context.Background()
	s, db, _, uid, _ := lotteryFixture(t, 0)
	_, err := db.Exec(`INSERT INTO payment_orders(user_id,amount,bonus_amount,recharge_code) VALUES($1,20,5,'online')`, uid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO redeem_codes(code,type,value,status,used_by,used_at) VALUES
 ('online','balance',20,'used',$1,NOW()),
 ('standalone','balance',5,'used',$1,NOW())`, uid)
	require.NoError(t, err)
	for _, tc := range []struct {
		name      string
		status    string
		refund    float64
		paid      bool
		completed bool
		total     float64
	}{
		{"completed", "COMPLETED", 0, true, true, 20},
		{"refund-failed", "REFUND_FAILED", 0, true, true, 20},
		{"partial-refund", "PARTIALLY_REFUNDED", 10, true, true, 12.5},
		{"refunded", "REFUNDED", 20, true, true, 5},
		{"refund-requested", "REFUND_REQUESTED", 20, true, true, 5},
		{"refunding", "REFUNDING", 20, true, true, 5},
		{"refund-pending", "REFUND_PENDING", 20, true, true, 5},
		{"pending", "PENDING", 0, false, false, 5},
		{"recharging", "RECHARGING", 0, true, false, 5},
		{"failed", "FAILED", 0, true, true, 5},
		{"missing-paid-time", "COMPLETED", 0, false, true, 5},
		{"missing-completed-time", "COMPLETED", 0, true, false, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Exec(`UPDATE payment_orders SET status=$2,refund_amount=$3,
 paid_at=CASE WHEN $4 THEN NOW() ELSE NULL END,completed_at=CASE WHEN $5 THEN NOW() ELSE NULL END
 WHERE user_id=$1 AND recharge_code='online'`, uid, tc.status, tc.refund, tc.paid, tc.completed)
			require.NoError(t, err)
			state, err := s.LotteryState(ctx, uid)
			require.NoError(t, err)
			require.Equal(t, tc.total, state.RechargeTotal)
		})
	}
}

func TestLotteryAccumulatesOrderAndRedeemDecimalsExactly(t *testing.T) {
	ctx := context.Background()
	s, db, _, uid, cfg := lotteryFixture(t, 0.1)
	cfg.RechargeStep = 0.3
	require.NoError(t, s.SaveLotteryConfig(ctx, cfg))
	_, err := db.Exec(`INSERT INTO redeem_codes(code,type,value,status,used_by,used_at)
 VALUES('decimal','balance',0.2,'used',$1,NOW())`, uid)
	require.NoError(t, err)
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, 0.3, state.RechargeTotal)
	require.Equal(t, int64(1), state.Remaining)
	require.Equal(t, 0.3, state.NextRecharge)
}

func TestLotteryConcurrentDrawsUseRedeemedChancesOnce(t *testing.T) {
	ctx := context.Background()
	s, db, c, uid, _ := lotteryFixture(t, 0)
	_, err := db.Exec(`INSERT INTO redeem_codes(code,type,value,status,used_by,used_at)
 VALUES('redeemed','balance',20,'used',$1,NOW())`, uid)
	require.NoError(t, err)
	var wg sync.WaitGroup
	var won, failed atomic.Int32
	s2 := New(db, testEncryptor{}, true, nil)
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bot := s
			if i%2 == 0 {
				bot = s2
			}
			reply, err := bot.drawLottery(ctx, c, Job{ID: fmt.Sprint(i), MemberID: "member", GroupID: "group"})
			if err != nil {
				failed.Add(1)
			}
			if strings.Contains(reply, "奖励额度：USD 1.00") {
				won.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.Zero(t, failed.Load())
	require.Equal(t, int32(2), won.Load())
	state, err := s.LotteryState(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, float64(20), state.RechargeTotal)
	require.Equal(t, int64(2), state.Earned)
	require.Equal(t, int64(2), state.Used)
	require.Zero(t, state.Remaining)
}
