//go:build unit

package service

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/coupon"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

type repairF2Fixture struct {
	s        *PaymentService
	db       *sql.DB
	o        *dbent.PaymentOrder
	couponID int64
}

func newRepairF2Fixture(t *testing.T, withCoupon bool, provider string) repairF2Fixture {
	t.Helper()
	s, db := couponTestDB(t)
	ctx := context.Background()
	u := matureCouponTestUser(t, s.entClient)
	cfg := &PaymentConfig{BalanceRechargeMultiplier: 1, MaxPendingOrders: 10, OrderTimeoutMin: 15}
	req := CreateOrderRequest{UserID: u.ID, Amount: 100, OrderType: payment.OrderTypeBalance}
	pay, bonus := 100.0, 0.0
	if withCoupon {
		tpl, err := s.SaveCouponTemplate(ctx, 0, CouponTemplate{Name: "F2 synthetic", Kind: "fixed", Currency: "CNY", MinAmount: 100, Value: 10, ValidDays: 7, Enabled: true})
		require.NoError(t, err)
		ids, err := s.IssueCoupons(ctx, CouponIssueRequest{TemplateID: tpl.ID, UserIDs: []int64{u.ID}, Quantity: 1, RequestID: "repair-f2-synthetic-coupon"})
		require.NoError(t, err)
		item, err := coupon.ReadOwned(ctx, s.entClient, ids[0], u.ID, false)
		require.NoError(t, err)
		req.CouponID = ids[0]
		req.couponQuote, err = couponRechargeQuote(item, cfg, 100, "CNY")
		require.NoError(t, err)
		pay, bonus = req.couponQuote.PayAmount, req.couponQuote.BonusAmount
	}
	o, err := s.createOrderInTx(ctx, req, &User{ID: u.ID, Email: u.Email}, nil, cfg, 100, pay, 0, pay, bonus, nil)
	require.NoError(t, err)
	// A legacy provider binding exercises the real registry/query/confirmation
	// path without any live provider configuration or transport.
	// Legacy records do not carry an instance snapshot. Coupon accounting keeps
	// its currency in recharge_coupon_orders; an incomplete provider snapshot
	// would be rejected before reaching the recovery boundary being tested.
	snapshot := o.ProviderSnapshot
	delete(snapshot, "currency")
	o, err = s.entClient.PaymentOrder.UpdateOneID(o.ID).SetPaymentType(provider).SetProviderSnapshot(snapshot).Save(ctx)
	require.NoError(t, err)
	users := &mockUserRepo{getByIDUser: &User{ID: u.ID}}
	users.updateBalanceFn = func(ctx context.Context, id int64, amount float64) error {
		_, err := db.ExecContext(ctx, `UPDATE users SET balance=balance+$2 WHERE id=$1`, id, amount)
		return err
	}
	s.userRepo = users
	s.redeemService = NewRedeemService(&paymentFulfillmentRedeemRepo{}, users, nil, &paymentFulfillmentRedeemCacheStub{}, nil, s.entClient, nil, nil)
	s.registry, s.providersLoaded = payment.NewRegistry(), true
	return repairF2Fixture{s: s, db: db, o: o, couponID: req.CouponID}
}

type repairF2QueryBarrier struct {
	paymentOrderLifecycleQueryProvider
	entered, release chan struct{}
	calls            atomic.Int32
	amount           float64
}

func (p *repairF2QueryBarrier) QueryOrder(ctx context.Context, _ string) (*payment.QueryOrderResponse, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &payment.QueryOrderResponse{Status: payment.ProviderStatusPaid, Amount: p.amount, TradeNo: "repair-f2-paid-trade"}, nil
	}
	// The user's independent cancellation query does not report payment yet.
	return &payment.QueryOrderResponse{Status: payment.ProviderStatusPending}, nil
}

func TestRepairF2BackgroundPaymentCannotReverseCancellation(t *testing.T) {
	for _, actor := range []string{"user", "admin"} {
		for _, withCoupon := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/coupon_%t", actor, withCoupon), func(t *testing.T) {
				f := newRepairF2Fixture(t, withCoupon, payment.TypeEasyPay)
				ctx := context.Background()
				f.o = f.s.entClient.PaymentOrder.UpdateOneID(f.o.ID).SetExpiresAt(time.Now().Add(-time.Minute)).SaveX(ctx)
				peer := &repairF2QueryBarrier{paymentOrderLifecycleQueryProvider: paymentOrderLifecycleQueryProvider{key: payment.TypeEasyPay}, entered: make(chan struct{}), release: make(chan struct{}), amount: f.o.PayAmount}
				f.s.registry.Register(peer)
				var release sync.Once
				defer release.Do(func() { close(peer.release) })
				done := make(chan error, 1)
				go func() { _, err := f.s.ReconcileRecoverableEasyPayOrders(ctx); done <- err }()
				select {
				case <-peer.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("F2 background QueryOrder never entered")
				}
				_, err := f.s.cancelCore(ctx, f.o, OrderStatusCancelled, actor, "synthetic cancellation during QueryOrder")
				require.NoError(t, err)
				require.Equal(t, OrderStatusCancelled, f.s.entClient.PaymentOrder.GetX(ctx, f.o.ID).Status)
				if withCoupon {
					item, err := coupon.ReadOwned(ctx, f.s.entClient, f.couponID, f.o.UserID, false)
					require.NoError(t, err)
					require.Equal(t, "available", item.Status)
				}
				release.Do(func() { close(peer.release) })
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-time.After(8 * time.Second):
					t.Fatal("F2 background confirmation did not finish")
				}
				current := f.s.entClient.PaymentOrder.GetX(ctx, f.o.ID)
				require.Equal(t, OrderStatusCancelled, current.Status, "F2 background paid response must not reverse committed cancellation")
				require.Zero(t, f.s.entClient.User.GetX(ctx, f.o.UserID).Balance)
				if withCoupon {
					item, err := coupon.ReadOwned(ctx, f.s.entClient, f.couponID, f.o.UserID, false)
					require.NoError(t, err)
					require.Equal(t, "available", item.Status)
					require.NotNil(t, current.PaidAt, "F2 actual late payment must remain recorded for reconciliation")
					require.Equal(t, "repair-f2-paid-trade", current.PaymentTradeNo)
				}
			})
		}
	}
}

func TestRepairF2ProviderRecoveryWindowsApplyToCoupons(t *testing.T) {
	for _, provider := range []string{payment.TypeAlipay, payment.TypeBEpusdt, payment.TypeEasyPay} {
		for _, outside := range []bool{false, true} {
			for _, withCoupon := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/outside_%t/coupon_%t", provider, outside, withCoupon), func(t *testing.T) {
					f := newRepairF2Fixture(t, withCoupon, provider)
					ctx, now := context.Background(), time.Now()
					window := paymentGraceMinutes * time.Minute
					if provider == payment.TypeBEpusdt {
						window = bepusdtOrderRecoveryWindow
					}
					if provider == payment.TypeEasyPay {
						window = easyPayOrderRecoveryWindow
					}
					age := window / 2
					if outside {
						age = window + time.Minute
					}
					snapshot := f.o.ProviderSnapshot
					if snapshot == nil {
						snapshot = map[string]any{}
					}
					if provider == payment.TypeBEpusdt {
						snapshot["collection_mode"] = "embedded"
					}
					updated, expires := now.Add(-age), now.Add(-time.Hour)
					if provider == payment.TypeEasyPay {
						expires, updated = now.Add(-age), now
					} // updated_at must not renew the fixed window.
					f.o = f.s.entClient.PaymentOrder.UpdateOneID(f.o.ID).SetStatus(OrderStatusExpired).SetExpiresAt(expires).SetUpdatedAt(updated).SetProviderSnapshot(snapshot).SaveX(ctx)
					tx, err := f.s.entClient.Tx(ctx)
					require.NoError(t, err)
					if withCoupon {
						require.NoError(t, releaseOrderCoupon(ctx, tx.Client(), f.o.ID))
					}
					require.NoError(t, tx.Commit())
					require.NoError(t, f.s.confirmPayment(ctx, f.o.ID, "repair-f2-late-trade", f.o.PayAmount, provider, nil))
					current := f.s.entClient.PaymentOrder.GetX(ctx, f.o.ID)
					if outside {
						require.Equal(t, OrderStatusExpired, current.Status, "F2 coupon must preserve the canonical provider recovery deadline")
						require.Zero(t, f.s.entClient.User.GetX(ctx, f.o.UserID).Balance)
						if withCoupon {
							require.NotNil(t, current.PaidAt, "F2 late payment evidence must survive the manual hold")
							require.Equal(t, "repair-f2-late-trade", current.PaymentTradeNo)
							item, err := coupon.ReadOwned(ctx, f.s.entClient, f.couponID, f.o.UserID, false)
							require.NoError(t, err)
							require.Equal(t, "available", item.Status)
							// Repeated provider delivery must remain on hold; only the
							// existing explicit administrator action can honor it.
							require.NoError(t, f.s.confirmPayment(ctx, f.o.ID, "repair-f2-late-trade", f.o.PayAmount, provider, nil))
							require.Zero(t, f.s.entClient.User.GetX(ctx, f.o.UserID).Balance)
							require.NoError(t, f.s.RetryFulfillment(ctx, f.o.ID))
							require.Equal(t, OrderStatusCompleted, f.s.entClient.PaymentOrder.GetX(ctx, f.o.ID).Status)
							require.Equal(t, 100.0, f.s.entClient.User.GetX(ctx, f.o.UserID).Balance)
							require.Error(t, f.s.RetryFulfillment(ctx, f.o.ID))
							item, err = coupon.ReadOwned(ctx, f.s.entClient, f.couponID, f.o.UserID, false)
							require.NoError(t, err)
							require.Equal(t, "used", item.Status, "F2 manual approval cannot leave the coupon reusable")
						}
					} else {
						require.Equal(t, OrderStatusCompleted, current.Status, "F2 eligible provider recovery must remain available")
						require.Equal(t, 100.0, f.s.entClient.User.GetX(ctx, f.o.UserID).Balance)
					}
				})
			}
		}
	}
}
