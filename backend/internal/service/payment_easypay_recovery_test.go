//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

type easyPayRecoveryFixture struct {
	svc      *PaymentService
	client   *dbent.Client
	order    *dbent.PaymentOrder
	userRepo *mockUserRepo
	redeems  *paymentOrderLifecycleRedeemRepo
	mode     atomic.Int32
	queries  atomic.Int32
	target   atomic.Value
}

// The real EasyPay adapter calls an isolated loopback gateway; the SQLite order
// state and ordinary fulfillment/redeem services are used without replacing the
// confirmation, amount, identity or idempotency logic under test.
func newEasyPayRecoveryFixture(t *testing.T, status string, expires time.Time) *easyPayRecoveryFixture {
	t.Helper()
	ctx := context.Background()
	f := &easyPayRecoveryFixture{client: newPaymentOrderLifecycleTestClient(t)}
	f.target.Store("")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.queries.Add(1)
		if r.URL.Path != "/api.php" || r.URL.Query().Get("act") != "order" || r.URL.Query().Get("pid") != "fixture-merchant" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mode := f.mode.Load()
		if mode == 0 || (mode == 5 && r.URL.Query().Get("out_trade_no") != f.target.Load().(string)) {
			// Close before response headers to exercise the original EOF path.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		outTradeNo := r.URL.Query().Get("out_trade_no")
		money, paid := "50.00", 1
		if mode == 2 {
			money = "60.00"
		}
		if mode == 3 {
			paid = 0
		}
		if mode == 4 {
			outTradeNo = "another-merchant-order"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "status": paid, "money": money,
			"out_trade_no": outTradeNo, "trade_no": "fixture-transaction"})
	}))
	t.Cleanup(upstream.Close)
	configSvc := NewPaymentConfigService(f.client, &paymentFulfillmentSettingRepoStub{values: map[string]string{}}, nil)
	inst, err := configSvc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey: payment.TypeEasyPay, Name: "Fixture EasyPay", Enabled: true,
		SupportedTypes: []string{payment.TypeAlipay, payment.TypeWxpay},
		Config: map[string]string{"apiBase": upstream.URL, "pid": "fixture-merchant", "pkey": "fixture-secret",
			"notifyUrl": "https://merchant.example/notify", "returnUrl": "https://merchant.example/result"},
	})
	require.NoError(t, err)
	user, err := f.client.User.Create().SetEmail("recovery@example.com").SetUsername("recovery-user").SetPasswordHash("hash").Save(ctx)
	require.NoError(t, err)
	f.order, err = f.client.PaymentOrder.Create().SetUserID(user.ID).SetUserEmail(user.Email).SetUserName(user.Username).
		SetAmount(50).SetPayAmount(50).SetRechargeCode("EASYPAY-RECOVERY").SetOutTradeNo("fixture-order").
		SetPaymentType(payment.TypeAlipay).SetPaymentTradeNo("").SetProviderKey(payment.TypeEasyPay).
		SetProviderInstanceID(strconv.FormatInt(inst.ID, 10)).
		SetProviderSnapshot(map[string]any{"schema_version": 1, "provider_instance_id": strconv.FormatInt(inst.ID, 10),
			"provider_key": payment.TypeEasyPay, "merchant_id": "fixture-merchant", "currency": "CNY"}).
		SetOrderType(payment.OrderTypeBalance).SetStatus(status).SetExpiresAt(expires).
		SetUpdatedAt(expires).SetClientIP("127.0.0.1").SetSrcHost("merchant.example").Save(ctx)
	require.NoError(t, err)
	f.userRepo = &mockUserRepo{getByIDUser: &User{ID: user.ID, Email: user.Email, Username: user.Username}}
	f.userRepo.updateBalanceFn = func(_ context.Context, id int64, amount float64) error {
		require.Equal(t, user.ID, id)
		f.userRepo.getByIDUser.Balance += amount
		return nil
	}
	f.redeems = &paymentOrderLifecycleRedeemRepo{codesByCode: map[string]*RedeemCode{
		f.order.RechargeCode: {ID: 1, Code: f.order.RechargeCode, Type: RedeemTypeBalance, Value: 50, Status: StatusUnused},
	}}
	f.svc = &PaymentService{entClient: f.client, registry: payment.NewRegistry(), configService: configSvc,
		loadBalancer: payment.NewDefaultLoadBalancer(f.client, nil), userRepo: f.userRepo, providersLoaded: true,
		redeemService: NewRedeemService(f.redeems, f.userRepo, nil, nil, nil, f.client, nil, nil)}
	return f
}

func (f *easyPayRecoveryFixture) notification() *payment.PaymentNotification {
	return &payment.PaymentNotification{OrderID: f.order.OutTradeNo, TradeNo: "fixture-transaction", Amount: 50,
		Status: payment.NotificationStatusSuccess, Metadata: map[string]string{"pid": "fixture-merchant"}}
}

func TestEasyPayRecoveryAfterUnconfirmedCallbackAndExpiry(t *testing.T) {
	ctx := context.Background()
	f := newEasyPayRecoveryFixture(t, OrderStatusPending, time.Now().Add(-10*time.Minute))
	prov, err := f.svc.getOrderProvider(ctx, f.order)
	require.NoError(t, err)
	require.Error(t, f.svc.ConfirmNotificationUpstream(ctx, prov, f.notification()))
	require.Zero(t, f.userRepo.getByIDUser.Balance, "unconfirmed notification must not credit")
	count, err := f.svc.ExpireTimedOutOrders(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	order, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusExpired, order.Status)
	// Model a recovery after process restart: no in-memory callback evidence or
	// extended updated_at grace is available or needed.
	_, err = f.client.PaymentOrder.UpdateOneID(order.ID).SetUpdatedAt(time.Now().Add(-10 * time.Minute)).Save(ctx)
	require.NoError(t, err)
	f.mode.Store(1)
	recovered, err := f.svc.ReconcileRecoverableEasyPayOrders(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	order, err = f.client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, order.Status)
	require.Equal(t, 50.0, f.userRepo.getByIDUser.Balance)
	require.Len(t, f.redeems.useCalls, 1)
	require.NoError(t, f.svc.ConfirmNotificationUpstream(ctx, prov, f.notification()))
	require.NoError(t, f.svc.HandlePaymentNotification(ctx, f.notification(), payment.TypeEasyPay))
	recovered, err = f.svc.ReconcileRecoverableEasyPayOrders(ctx)
	require.NoError(t, err)
	require.Zero(t, recovered)
	require.Equal(t, 50.0, f.userRepo.getByIDUser.Balance)
	require.Len(t, f.redeems.useCalls, 1, "duplicate callback and sweep must settle once")
}

func TestEasyPayRecoveryRejectsUnconfirmedAndMismatchedOrders(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode int32
	}{
		{"query_EOF", 0}, {"paid_amount_mismatch", 2}, {"upstream_unpaid", 3}, {"foreign_order_identity", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newEasyPayRecoveryFixture(t, OrderStatusExpired, time.Now().Add(-10*time.Minute))
			f.mode.Store(tc.mode)
			// Compare persisted snapshots: a DB round trip deliberately drops
			// time.Time's process-local monotonic clock reading.
			before, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
			require.NoError(t, err)
			recovered, err := f.svc.ReconcileRecoverableEasyPayOrders(ctx)
			require.NoError(t, err)
			require.Zero(t, recovered)
			order, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusExpired, order.Status)
			require.True(t, before.UpdatedAt.Equal(order.UpdatedAt), "retries must not mutate the persisted lifecycle timestamp: before=%s after=%s", before.UpdatedAt, order.UpdatedAt)
			require.True(t, before.ExpiresAt.Equal(order.ExpiresAt), "retries must not slide the persisted recovery deadline: before=%s after=%s", before.ExpiresAt, order.ExpiresAt)
			require.Empty(t, order.PaymentTradeNo, "failed confirmation must not replace the transaction reference")
			require.Zero(t, f.userRepo.getByIDUser.Balance)
			require.Empty(t, f.redeems.useCalls)
		})
	}
}

func TestEasyPayRecoveryFixedDeadlineAndExcludedStates(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		age          time.Duration
	}{
		{"past_fixed_deadline", OrderStatusExpired, easyPayOrderRecoveryWindow + time.Minute},
		{"cancelled", OrderStatusCancelled, time.Minute},
		{"refunded", OrderStatusRefunded, time.Minute},
		{"completed", OrderStatusCompleted, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newEasyPayRecoveryFixture(t, tc.status, time.Now().Add(-tc.age))
			f.mode.Store(1)
			_, err := f.client.PaymentOrder.UpdateOneID(f.order.ID).SetUpdatedAt(time.Now()).Save(ctx)
			require.NoError(t, err)
			recovered, err := f.svc.ReconcileRecoverableEasyPayOrders(ctx)
			require.NoError(t, err)
			require.Zero(t, recovered)
			require.Zero(t, f.queries.Load())
			require.Zero(t, f.userRepo.getByIDUser.Balance)
			if tc.name == "past_fixed_deadline" {
				require.NoError(t, f.svc.HandlePaymentNotification(ctx, f.notification(), payment.TypeEasyPay))
				order, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
				require.NoError(t, err)
				require.Equal(t, OrderStatusExpired, order.Status, "fresh updated_at must not reopen the recovery window")
				require.Zero(t, f.userRepo.getByIDUser.Balance)
				audits, err := f.svc.GetOrderAuditLogs(ctx, f.order.ID)
				require.NoError(t, err)
				require.Len(t, audits, 1)
				require.Equal(t, "PAYMENT_AFTER_EXPIRY", audits[0].Action)
				require.Contains(t, audits[0].Detail, "manual reconciliation required")
			}
		})
	}
}

func TestEasyPayRecoveryRejectsChangedMerchant(t *testing.T) {
	ctx := context.Background()
	f := newEasyPayRecoveryFixture(t, OrderStatusExpired, time.Now().Add(-10*time.Minute))
	f.mode.Store(1)
	f.order.ProviderSnapshot["merchant_id"] = "another-merchant"
	_, err := f.client.PaymentOrder.UpdateOneID(f.order.ID).SetProviderSnapshot(f.order.ProviderSnapshot).Save(ctx)
	require.NoError(t, err)
	recovered, err := f.svc.ReconcileRecoverableEasyPayOrders(ctx)
	require.NoError(t, err)
	require.Zero(t, recovered)
	order, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusExpired, order.Status)
	require.Empty(t, order.PaymentTradeNo)
	require.Zero(t, f.userRepo.getByIDUser.Balance)
}

func TestEasyPayRecoveryKeepsOtherProviderGrace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		age     time.Duration
		status  string
		balance float64
	}{
		{"outside_grace", 10 * time.Minute, OrderStatusExpired, 0},
		{"within_grace", time.Minute, OrderStatusCompleted, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newEasyPayRecoveryFixture(t, OrderStatusExpired, time.Now().Add(-10*time.Minute))
			// Ent's UpdateDefault refreshes updated_at on any order update.
			// Preserve the intended age while switching the fixture provider.
			var err error
			f.order, err = f.client.PaymentOrder.UpdateOneID(f.order.ID).SetProviderKey(payment.TypeAlipay).
				ClearProviderInstanceID().SetProviderSnapshot(map[string]any{}).
				SetUpdatedAt(time.Now().Add(-tc.age)).Save(ctx)
			require.NoError(t, err)
			recovered, err := f.svc.ReconcileRecoverableEasyPayOrders(ctx)
			require.NoError(t, err)
			require.Zero(t, recovered)
			require.Zero(t, f.queries.Load(), "official Alipay must not enter the EasyPay scan")
			require.NoError(t, f.svc.toPaid(ctx, f.order, "official-alipay-trade", 50, payment.TypeAlipay))
			order, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
			require.NoError(t, err)
			require.Equal(t, tc.status, order.Status, "official Alipay retains its existing five-minute grace")
			require.Equal(t, tc.balance, f.userRepo.getByIDUser.Balance)
			if tc.balance == 0 {
				require.Empty(t, f.redeems.useCalls)
			} else {
				require.Len(t, f.redeems.useCalls, 1)
			}
		})
	}
}

func TestEasyPayRecoveryRetainsOriginalProviderInstance(t *testing.T) {
	ctx := context.Background()
	f := newEasyPayRecoveryFixture(t, OrderStatusExpired, time.Now().Add(-10*time.Minute))
	instanceID, err := strconv.ParseInt(*f.order.ProviderInstanceID, 10, 64)
	require.NoError(t, err)
	require.Error(t, f.svc.configService.DeleteProviderInstance(ctx, instanceID), "recoverable orders must retain their original confirmation credentials")
	_, err = f.client.PaymentOrder.UpdateOneID(f.order.ID).SetExpiresAt(time.Now().Add(-easyPayOrderRecoveryWindow - time.Minute)).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, f.svc.configService.DeleteProviderInstance(ctx, instanceID), "the bounded recovery deadline must eventually release the instance")
}

func TestEasyPayRecoveryWorkerContinuesAfterPendingQueryFailure(t *testing.T) {
	ctx := context.Background()
	f := newEasyPayRecoveryFixture(t, OrderStatusExpired, time.Now().Add(-10*time.Minute))
	_, err := f.client.PaymentOrder.Create().SetUserID(f.order.UserID).SetUserEmail(f.order.UserEmail).SetUserName(f.order.UserName).
		SetAmount(50).SetPayAmount(50).SetRechargeCode("PENDING-OUTAGE").SetOutTradeNo("pending-outage").
		SetPaymentType(f.order.PaymentType).SetPaymentTradeNo("").SetNillableProviderKey(f.order.ProviderKey).
		SetNillableProviderInstanceID(f.order.ProviderInstanceID).SetProviderSnapshot(f.order.ProviderSnapshot).
		SetOrderType(payment.OrderTypeBalance).SetStatus(OrderStatusPending).SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").SetSrcHost("merchant.example").Save(ctx)
	require.NoError(t, err)
	f.target.Store(f.order.OutTradeNo)
	f.mode.Store(5)
	NewPaymentOrderExpiryService(f.svc, time.Minute).runOnce()
	order, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, order.Status)
	require.Equal(t, 50.0, f.userRepo.getByIDUser.Balance)
	require.GreaterOrEqual(t, f.queries.Load(), int32(2), "the pending failure must not skip late recovery")
}

func TestEasyPayRecoveryDoesNotUndoConcurrentCancellation(t *testing.T) {
	ctx := context.Background()
	f := newEasyPayRecoveryFixture(t, OrderStatusExpired, time.Now().Add(-10*time.Minute))
	f.mode.Store(1)
	// Simulate a stale scanner snapshot followed by a concurrent administrator
	// cancellation before the atomic PAID transition.
	_, err := f.client.PaymentOrder.UpdateOneID(f.order.ID).SetStatus(OrderStatusCancelled).Save(ctx)
	require.NoError(t, err)
	result := f.svc.reconcilePaid(context.WithValue(ctx, easyPayBackgroundRecoveryKey{}, true), f.order)
	require.Empty(t, result)
	order, err := f.client.PaymentOrder.Get(ctx, f.order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCancelled, order.Status)
	require.Zero(t, f.userRepo.getByIDUser.Balance)
	require.Empty(t, f.redeems.useCalls)
}

func TestEasyPayRecoveryRotatesPastFailedBatch(t *testing.T) {
	ctx := context.Background()
	f := newEasyPayRecoveryFixture(t, OrderStatusExpired, time.Now().Add(-10*time.Minute))
	var last *dbent.PaymentOrder
	for i := 1; i <= pendingPaymentReconcileLimit; i++ {
		var err error
		last, err = f.client.PaymentOrder.Create().SetUserID(f.order.UserID).SetUserEmail(f.order.UserEmail).SetUserName(f.order.UserName).
			SetAmount(50).SetPayAmount(50).SetRechargeCode(fmt.Sprintf("RECOVER-%d", i)).SetOutTradeNo(fmt.Sprintf("recover-order-%d", i)).
			SetPaymentType(f.order.PaymentType).SetPaymentTradeNo("").SetNillableProviderKey(f.order.ProviderKey).
			SetNillableProviderInstanceID(f.order.ProviderInstanceID).SetProviderSnapshot(f.order.ProviderSnapshot).
			SetOrderType(payment.OrderTypeBalance).SetStatus(OrderStatusExpired).SetExpiresAt(f.order.ExpiresAt).
			SetUpdatedAt(f.order.UpdatedAt).SetClientIP("127.0.0.1").SetSrcHost("merchant.example").Save(ctx)
		require.NoError(t, err)
	}
	f.redeems.codesByCode[last.RechargeCode] = &RedeemCode{ID: 2, Code: last.RechargeCode, Type: RedeemTypeBalance, Value: 50, Status: StatusUnused}
	f.target.Store(last.OutTradeNo)
	f.mode.Store(5)
	recovered, err := f.svc.ReconcileRecoverableEasyPayOrders(ctx)
	require.NoError(t, err)
	require.Zero(t, recovered)
	require.Equal(t, int32(pendingPaymentReconcileLimit), f.queries.Load())
	recovered, err = f.svc.ReconcileRecoverableEasyPayOrders(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, recovered, "earlier persistent failures must not starve the next order")
	require.Equal(t, 50.0, f.userRepo.getByIDUser.Balance)
	require.Len(t, f.redeems.useCalls, 1)
}
