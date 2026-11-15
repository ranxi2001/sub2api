//go:build unit

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/coupon"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestCouponQuoteAccounting(t *testing.T) {
	cfg := &PaymentConfig{BalanceRechargeMultiplier: 2, RechargeBonusMode: "bonus", RechargeBonusTiers: []RechargeBonusTier{{MinAmount: 100, BonusPercent: 20}}}
	c := coupon.Item{ID: 1, Status: "available", ExpiresAt: time.Now().Add(time.Hour), Snapshot: coupon.Template{Name: "test", Kind: "fixed", Currency: "CNY", MinAmount: 100, Value: 10, ValidDays: 7}}
	q, err := couponRechargeQuote(c, cfg, 100, "CNY")
	require.NoError(t, err)
	require.Equal(t, 90.0, q.PayBase)
	require.Equal(t, 200.0, q.Credited)
	require.Equal(t, 20.0, q.BonusAmount)
	c.Snapshot.Stackable = true
	q, err = couponRechargeQuote(c, cfg, 100, "CNY")
	require.NoError(t, err)
	require.Equal(t, 240.0, q.Credited)
	require.Equal(t, 60.0, q.BonusAmount)
	c.Snapshot.Kind = "bonus"
	q, err = couponRechargeQuote(c, cfg, 100, "CNY")
	require.NoError(t, err)
	require.Equal(t, 264.0, q.Credited)
	require.Equal(t, 64.0, q.BonusAmount)
	require.Equal(t, 24.0, q.CouponBonus)
	c.Status = "reserved"
	_, err = couponRechargeQuote(c, cfg, 100, "CNY")
	require.ErrorIs(t, err, coupon.ErrUnavailable)
	c.Status = "revoked"
	_, err = couponRechargeQuote(c, cfg, 100, "CNY")
	require.ErrorIs(t, err, coupon.ErrUnavailable)
	c.Status = "available"
	c.ExpiresAt = time.Now().Add(-time.Second)
	_, err = couponRechargeQuote(c, cfg, 100, "CNY")
	require.ErrorIs(t, err, coupon.ErrUnavailable)
}
func couponTestDB(t *testing.T) (*PaymentService, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("COUPON_TEST_DSN")
	if dsn == "" {
		t.Skip("COUPON_TEST_DSN required")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("coupon_%d", time.Now().UnixNano())
	_, err = admin.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE"); _ = admin.Close() })
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Schema.Create(context.Background()))
	_, err = db.Exec(`CREATE TABLE qq_bot_reward_codes(id BIGSERIAL PRIMARY KEY,reward_type TEXT,source_kind TEXT,source_key TEXT,code_hash TEXT,status TEXT)`)
	require.NoError(t, err)
	raw, err := migrations.FS.ReadFile("376_recharge_coupons.sql")
	require.NoError(t, err)
	for range 2 {
		_, err = db.Exec(string(raw))
		require.NoError(t, err)
	}
	return &PaymentService{entClient: client}, db
}
func TestCouponPostgresLifecycle(t *testing.T) {
	s, db := couponTestDB(t)
	ctx := context.Background()
	u := matureCouponTestUser(t, s.entClient)
	other := matureCouponTestUser(t, s.entClient)
	template, err := s.SaveCouponTemplate(ctx, 0, CouponTemplate{Name: "满100减10", Kind: "fixed", Currency: "CNY", MinAmount: 100, Value: 10, ValidDays: 7, Enabled: true, MaxIssues: 10, PerUserLimit: 5})
	require.NoError(t, err)
	issue := CouponIssueRequest{TemplateID: template.ID, UserIDs: []int64{u.ID}, Quantity: 1, RequestID: "coupon-test-request-001"}
	ids, err := s.IssueCoupons(ctx, issue)
	require.NoError(t, err)
	again, err := s.IssueCoupons(ctx, issue)
	require.NoError(t, err)
	require.Equal(t, ids, again)
	changed := issue
	changed.Quantity = 2
	_, err = s.IssueCoupons(ctx, changed)
	require.Error(t, err)
	c, err := coupon.ReadOwned(ctx, s.entClient, ids[0], u.ID, false)
	require.NoError(t, err)
	_, err = coupon.ReadOwned(ctx, s.entClient, ids[0], other.ID, false)
	require.ErrorIs(t, err, coupon.ErrUnavailable)
	template.Value = 20
	_, err = s.SaveCouponTemplate(ctx, template.ID, template)
	require.NoError(t, err)
	c, err = coupon.ReadOwned(ctx, s.entClient, ids[0], u.ID, false)
	require.NoError(t, err)
	require.Equal(t, 10.0, c.Snapshot.Value)
	cfg := &PaymentConfig{BalanceRechargeMultiplier: 2, OrderTimeoutMin: 15, MaxPendingOrders: 10}
	q, err := couponRechargeQuote(c, cfg, 100, "CNY")
	require.NoError(t, err)
	req := CreateOrderRequest{UserID: u.ID, CouponID: c.ID, Amount: 100, OrderType: payment.OrderTypeBalance, PaymentType: "alipay", couponQuote: q}
	user := &User{ID: u.ID, Email: u.Email, Username: u.Username}
	var wg sync.WaitGroup
	results := make(chan *dbent.PaymentOrder, 2)
	failures := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, e := s.createOrderInTx(ctx, req, user, nil, cfg, q.Credited, q.PayBase, 0, q.PayAmount, q.BonusAmount, nil)
			results <- o
			failures <- e
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	var order *dbent.PaymentOrder
	successes := 0
	for e := range failures {
		if e == nil {
			successes++
		} else {
			require.ErrorIs(t, e, coupon.ErrUnavailable)
		}
	}
	require.Equal(t, 1, successes)
	for o := range results {
		if o != nil {
			order = o
		}
	}
	require.Equal(t, 1, s.entClient.PaymentOrder.Query().CountX(ctx))
	require.ErrorIs(t, s.RevokeCoupon(ctx, c.ID), coupon.ErrUnavailable)
	tx, err := s.entClient.Tx(ctx)
	require.NoError(t, err)
	_, err = tx.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusCancelled).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, releaseOrderCoupon(ctx, tx.Client(), order.ID))
	require.NoError(t, tx.Commit())
	c, err = coupon.ReadOwned(ctx, s.entClient, c.ID, u.ID, false)
	require.NoError(t, err)
	require.Equal(t, "available", c.Status)
	next, err := s.createOrderInTx(ctx, req, user, nil, cfg, q.Credited, q.PayBase, 0, q.PayAmount, q.BonusAmount, nil)
	require.NoError(t, err)
	tx, err = s.entClient.Tx(ctx)
	require.NoError(t, err)
	require.ErrorIs(t, consumeOrderCoupon(ctx, tx.Client(), order), errCouponPaymentConflict)
	require.NoError(t, tx.Commit())
	previous := s.entClient.PaymentOrder.GetX(ctx, order.ID)
	require.Equal(t, 200.0, previous.Amount)
	require.Equal(t, 20.0, previous.BonusAmount)
	var state string
	require.NoError(t, db.QueryRow(`SELECT status FROM recharge_coupon_orders WHERE order_id=$1`, order.ID).Scan(&state))
	require.Equal(t, "conflict", state)
	tx, err = s.entClient.Tx(ctx)
	require.NoError(t, err)
	require.NoError(t, consumeOrderCoupon(ctx, tx.Client(), next))
	require.NoError(t, tx.Commit())
	c, err = coupon.ReadOwned(ctx, s.entClient, c.ID, u.ID, false)
	require.NoError(t, err)
	require.Equal(t, "used", c.Status)
	require.Equal(t, next.ID, *c.OrderID)
	tx, err = s.entClient.Tx(ctx)
	require.NoError(t, err)
	require.NoError(t, consumeOrderCoupon(ctx, tx.Client(), next))
	require.NoError(t, tx.Commit())
	items, total, err := s.ListCoupons(ctx, u.ID, 0, "used", "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, items, 1)
	require.Equal(t, 10.0, items[0].DiscountAmount)
	empty, n, err := s.ListCoupons(ctx, other.ID, 0, "", "", 1, 20)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Empty(t, empty)
	issue.RequestID = "coupon-rollback-request"
	issue.UserIDs = []int64{u.ID, 99999999}
	_, err = s.IssueCoupons(ctx, issue)
	require.Error(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM user_recharge_coupons`).Scan(&count))
	require.Equal(t, 1, count)
	history, total, err := s.ListCouponUsage(ctx, u.ID, "", "", 1, 20)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, history, 2)
	_, total, err = s.ListCouponUsage(ctx, other.ID, "", "", 1, 20)
	require.NoError(t, err)
	require.Zero(t, total)
	require.NoError(t, s.confirmCouponOrder(ctx, order, "late-trade", order.PayAmount, "test"))
	require.NoError(t, s.confirmCouponOrder(ctx, order, "late-trade", order.PayAmount, "test"))
	held := s.entClient.PaymentOrder.GetX(ctx, order.ID)
	require.Equal(t, OrderStatusFailed, held.Status)
	require.NotNil(t, held.PaidAt)
	require.True(t, PaymentOrderCoupon(held).ReviewRequired)
	require.Error(t, s.ExecuteBalanceFulfillment(ctx, held.ID))
	unpaid := s.entClient.PaymentOrder.UpdateOneID(next.ID).SetStatus(OrderStatusFailed).SaveX(ctx)
	require.Nil(t, unpaid.PaidAt)
	require.Error(t, s.ExecuteBalanceFulfillment(ctx, unpaid.ID))
	template.ID = 0
	template.MaxIssues = 1
	template.PerUserLimit = 0
	template, err = s.SaveCouponTemplate(ctx, 0, template)
	require.NoError(t, err)
	es := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := s.IssueCoupons(ctx, CouponIssueRequest{TemplateID: template.ID, UserIDs: []int64{u.ID}, Quantity: 1, RequestID: fmt.Sprintf("coupon-cap-request-%03d", i)})
			es <- e
		}(i)
	}
	wg.Wait()
	close(es)
	successes = 0
	for e := range es {
		if e == nil {
			successes++
		} else {
			require.ErrorIs(t, e, coupon.ErrLimit)
		}
	}
	require.Equal(t, 1, successes)
	templates, err := s.ListCouponTemplates(ctx)
	require.NoError(t, err)
	require.Len(t, templates, 2)
}

func TestCouponPaymentFulfillmentAndManualReview(t *testing.T) {
	s, _ := couponTestDB(t)
	ctx := context.Background()
	u := matureCouponTestUser(t, s.entClient)
	template, err := s.SaveCouponTemplate(ctx, 0, CouponTemplate{Name: "test", Kind: "fixed", Currency: "CNY", MinAmount: 100, Value: 10, ValidDays: 7, Enabled: true})
	require.NoError(t, err)
	ids, err := s.IssueCoupons(ctx, CouponIssueRequest{TemplateID: template.ID, UserIDs: []int64{u.ID}, Quantity: 2, RequestID: "fulfillment-coupon-request"})
	require.NoError(t, err)
	cfg := &PaymentConfig{BalanceRechargeMultiplier: 2, MaxPendingOrders: 10}
	create := func(id int64) *dbent.PaymentOrder {
		c, e := coupon.ReadOwned(ctx, s.entClient, id, u.ID, false)
		require.NoError(t, e)
		q, e := couponRechargeQuote(c, cfg, 100, "CNY")
		require.NoError(t, e)
		o, e := s.createOrderInTx(ctx, CreateOrderRequest{UserID: u.ID, CouponID: id, Amount: 100, OrderType: payment.OrderTypeBalance, couponQuote: q}, &User{ID: u.ID, Email: u.Email}, nil, cfg, q.Credited, q.PayBase, 0, q.PayAmount, q.BonusAmount, nil)
		require.NoError(t, e)
		return o
	}
	credited := 0.0
	repo := &paymentFulfillmentRedeemRepo{}
	users := &mockUserRepo{getByIDUser: &User{ID: u.ID}}
	users.updateBalanceFn = func(_ context.Context, id int64, amount float64) error { credited += amount; return nil }
	s.userRepo = users
	s.redeemService = NewRedeemService(repo, users, nil, &paymentFulfillmentRedeemCacheStub{}, nil, s.entClient, nil, nil)
	o := create(ids[0])
	require.NoError(t, s.confirmCouponOrder(ctx, o, "trade-1", 90, "test"))
	require.NoError(t, s.confirmCouponOrder(ctx, o, "trade-1", 90, "test"))
	require.Equal(t, 200.0, credited)
	c, err := coupon.ReadOwned(ctx, s.entClient, ids[0], u.ID, false)
	require.NoError(t, err)
	require.Equal(t, "used", c.Status)
	// Simulate an existing refund transition: the spent coupon stays spent.
	s.entClient.PaymentOrder.UpdateOneID(o.ID).SetStatus(OrderStatusRefunded).ExecX(ctx)
	c, err = coupon.ReadOwned(ctx, s.entClient, ids[0], u.ID, false)
	require.NoError(t, err)
	require.Equal(t, "used", c.Status)
	expired := create(ids[1])
	_, err = s.cancelCore(ctx, expired, OrderStatusExpired, "system", "test expiry")
	require.NoError(t, err)
	c, err = coupon.ReadOwned(ctx, s.entClient, ids[1], u.ID, false)
	require.NoError(t, err)
	require.Equal(t, "available", c.Status)
	failed := create(ids[1])
	require.NoError(t, s.releaseFailedOrderCoupon(ctx, failed.ID))
	require.Equal(t, OrderStatusFailed, s.entClient.PaymentOrder.GetX(ctx, failed.ID).Status)
	require.NoError(t, s.releaseFailedOrderCoupon(ctx, failed.ID))
	latest := create(ids[1])
	require.NoError(t, s.confirmCouponOrder(ctx, failed, "late-trade", 90, "test"))
	require.Equal(t, 200.0, credited)
	require.NoError(t, s.RetryFulfillment(ctx, failed.ID))
	require.Equal(t, 400.0, credited)
	require.Error(t, s.RetryFulfillment(ctx, failed.ID))
	require.Equal(t, 400.0, credited)
	c, err = coupon.ReadOwned(ctx, s.entClient, ids[1], u.ID, false)
	require.NoError(t, err)
	require.Equal(t, "reserved", c.Status)
	require.Equal(t, latest.ID, *c.OrderID)
	_, err = s.cancelCore(ctx, latest, OrderStatusCancelled, "user", "test cancel")
	require.NoError(t, err)
	c, err = coupon.ReadOwned(ctx, s.entClient, ids[1], u.ID, false)
	require.NoError(t, err)
	require.Equal(t, "available", c.Status)
	history, _, err := s.ListCouponUsage(ctx, u.ID, "", "honored", 1, 20)
	require.NoError(t, err)
	require.Len(t, history, 1)
}

func TestCouponOrderSummaryDoesNotExposeProviderCredentials(t *testing.T) {
	order := &dbent.PaymentOrder{ProviderSnapshot: map[string]any{"recharge_coupon_id": float64(7), "coupon_name": "满100减10", "coupon_discount": float64(10), "coupon_bonus": float64(0), "currency": "CNY", "coupon_review_required": true, "api_key": "private-key", "private_config": "secret"}}
	summary := PaymentOrderCoupon(order)
	require.NotNil(t, summary)
	require.True(t, summary.ReviewRequired)
	raw, err := json.Marshal(summary)
	require.NoError(t, err)
	require.Contains(t, string(raw), "满100减10")
	require.NotContains(t, string(raw), "private-key")
	require.NotContains(t, string(raw), "secret")
	require.Nil(t, PaymentOrderCoupon(&dbent.PaymentOrder{}))
}

func TestCouponCancellationPaymentRaceAndExpiredReservation(t *testing.T) {
	s, db := couponTestDB(t)
	ctx := context.Background()
	u := matureCouponTestUser(t, s.entClient)
	tpl, err := s.SaveCouponTemplate(ctx, 0, CouponTemplate{Name: "race", Kind: "fixed", Currency: "CNY", MinAmount: 100, Value: 10, ValidDays: 7, Enabled: true})
	require.NoError(t, err)
	ids, err := s.IssueCoupons(ctx, CouponIssueRequest{TemplateID: tpl.ID, UserIDs: []int64{u.ID}, Quantity: 4, RequestID: "race-payment-coupon-batch"})
	require.NoError(t, err)
	cfg := &PaymentConfig{BalanceRechargeMultiplier: 1, MaxPendingOrders: 10}
	repo := &paymentFulfillmentRedeemRepo{}
	users := &mockUserRepo{getByIDUser: &User{ID: u.ID}}
	credited := 0.0
	users.updateBalanceFn = func(context.Context, int64, float64) error { credited += 100; return nil }
	s.userRepo = users
	s.redeemService = NewRedeemService(repo, users, nil, &paymentFulfillmentRedeemCacheStub{}, nil, s.entClient, nil, nil)
	for i, id := range ids {
		c, e := coupon.ReadOwned(ctx, s.entClient, id, u.ID, false)
		require.NoError(t, e)
		q, e := couponRechargeQuote(c, cfg, 100, "CNY")
		require.NoError(t, e)
		o, e := s.createOrderInTx(ctx, CreateOrderRequest{UserID: u.ID, CouponID: id, Amount: 100, OrderType: payment.OrderTypeBalance, couponQuote: q}, &User{ID: u.ID, Email: u.Email}, nil, cfg, q.Credited, q.PayBase, 0, q.PayAmount, q.BonusAmount, nil)
		require.NoError(t, e)
		if i == 0 {
			_, e = db.Exec(`UPDATE user_recharge_coupons SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, id)
			require.NoError(t, e)
		}
		errs := make(chan error, 2)
		go func() { _, e := s.cancelCore(ctx, o, OrderStatusCancelled, "user", "race"); errs <- e }()
		go func() { errs <- s.confirmCouponOrder(ctx, o, fmt.Sprintf("race-%d", i), 90, "test") }()
		require.NoError(t, <-errs)
		require.NoError(t, <-errs)
		require.Equal(t, OrderStatusCompleted, s.entClient.PaymentOrder.GetX(ctx, o.ID).Status)
		c, e = coupon.ReadOwned(ctx, s.entClient, id, u.ID, false)
		require.NoError(t, e)
		require.Equal(t, "used", c.Status)
	}
	require.Equal(t, 400.0, credited)
}

func matureCouponTestUser(t *testing.T, client *dbent.Client) *dbent.User {
	t.Helper()
	user, err := client.User.Create().SetEmail(fmt.Sprintf("coupon-%d@example.invalid", time.Now().UnixNano())).SetPasswordHash("synthetic").Save(context.Background())
	require.NoError(t, err)
	return user
}
