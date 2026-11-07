//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestBEpusdtCreateOrderThroughConfiguredInstance(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	var created map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/order/create-transaction", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&created))
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "data": map[string]any{
			"trade_id": "gateway-trade", "order_id": created["order_id"], "amount": "28.88", "fiat": "USD",
			"trade_type": created["trade_type"], "actual_amount": "28.90", "status": 1,
			"payment_url": "https://gateway.example/pay/checkout/gateway-trade"}})
	}))
	defer server.Close()
	settings := &paymentFulfillmentSettingRepoStub{values: map[string]string{SettingPaymentEnabled: "true", SettingOrderTimeoutMinutes: "1"}}
	configSvc := NewPaymentConfigService(client, settings, nil)
	instance, err := configSvc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{ProviderKey: payment.TypeBEpusdt,
		Name: "Crypto", Enabled: true, SupportedTypes: []string{"usdt.trc20", "usdc.base"},
		Config: map[string]string{"apiBase": server.URL, "apiToken": "test-token", "currency": "USD",
			"notifyUrl": "https://merchant.example/api/v1/payment/webhook/bepusdt", "returnUrl": "https://merchant.example/payment/result"}})
	require.NoError(t, err)
	user, err := client.User.Create().SetEmail("checkout@example.com").SetUsername("checkout-user").SetPasswordHash("hash").Save(ctx)
	require.NoError(t, err)
	userRepo := &mockUserRepo{getByIDUser: &User{ID: user.ID, Email: user.Email, Username: user.Username, Status: StatusActive}}
	svc := &PaymentService{entClient: client, configService: configSvc, loadBalancer: payment.NewDefaultLoadBalancer(client, nil),
		registry: payment.NewRegistry(), userRepo: userRepo}
	result, err := svc.CreateOrder(ctx, CreateOrderRequest{UserID: user.ID, Amount: 28.88, PaymentType: "usdc.base", OrderType: payment.OrderTypeBalance,
		ReturnURL: "https://merchant.example/payment/result", SrcHost: "merchant.example", ClientIP: "127.0.0.1"})
	require.NoError(t, err)
	require.Equal(t, "usdc.base", result.PaymentType)
	require.Equal(t, "USD", result.Currency)
	require.Equal(t, float64(120), created["timeout"])
	require.Equal(t, result.OutTradeNo, created["order_id"])
	require.Equal(t, "usdc.base", created["trade_type"])
	require.Contains(t, created["redirect_url"], "order_id=")
	order, err := client.PaymentOrder.Get(ctx, result.OrderID)
	require.NoError(t, err)
	require.Equal(t, "gateway-trade", order.PaymentTradeNo)
	require.Equal(t, payment.TypeBEpusdt, *order.ProviderKey)
	resolved, err := svc.GetWebhookProvider(ctx, payment.TypeBEpusdt, result.OutTradeNo)
	require.NoError(t, err)
	require.Equal(t, payment.TypeBEpusdt, resolved.ProviderKey())
	require.NotNil(t, instance)
}

func TestBEpusdtAdminConfigurationAndAvailableMethods(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := NewPaymentConfigService(client, nil, []byte("0123456789abcdef0123456789abcdef"))
	cfg := map[string]string{"apiBase": "https://gateway.example", "apiToken": "private-token", "currency": "USD",
		"notifyUrl": "https://merchant.example/api/v1/payment/webhook/bepusdt", "returnUrl": "https://merchant.example/payment/result"}
	instance, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey: payment.TypeBEpusdt, Name: "Crypto", Config: cfg, Enabled: true,
		SupportedTypes: []string{"usdt.trc20", "usdt.bep20", "usdc.base"},
	})
	require.NoError(t, err)
	listed, err := svc.ListProviderInstancesWithConfig(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.NotContains(t, listed[0].Config, "apiToken")
	require.Equal(t, "USD", listed[0].Config["currency"])
	limits, err := svc.GetAvailableMethodLimits(ctx)
	require.NoError(t, err)
	require.Len(t, limits.Methods, 3)
	require.Contains(t, limits.Methods, "usdt.bep20")
	require.Equal(t, "USD", limits.Methods["usdc.base"].Currency)
	require.NotContains(t, limits.Methods, "usdt.erc20", "unchecked networks must not be offered")
	_, err = svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{ProviderKey: payment.TypeBEpusdt,
		Name: "No networks selected", Config: cfg, Enabled: true})
	require.NoError(t, err)
	lb := payment.NewDefaultLoadBalancer(client, nil)
	selected, err := lb.SelectInstance(ctx, payment.TypeBEpusdt, "usdc.base", payment.StrategyRoundRobin, 10)
	require.NoError(t, err)
	require.Equal(t, payment.TypeBEpusdt, selected.ProviderKey)
	_, err = lb.SelectInstance(ctx, payment.TypeBEpusdt, "usdt.erc20", payment.StrategyRoundRobin, 10)
	require.Error(t, err, "empty supported_types must not enable unchecked networks")
	_, err = svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{Config: map[string]string{"apiToken": "", "currency": "USD"}})
	require.NoError(t, err)
	saved, err := client.PaymentProviderInstance.Get(ctx, instance.ID)
	require.NoError(t, err)
	decrypted, err := svc.decryptConfig(saved.Config)
	require.NoError(t, err)
	require.Equal(t, "private-token", decrypted["apiToken"])
	createPendingProviderConfigOrder(t, ctx, client, instance)
	for _, change := range []map[string]string{{"apiToken": "new-token"}, {"apiBase": "https://other.example"}, {"currency": "CNY"}} {
		_, err := svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{Config: change})
		require.ErrorContains(t, err, "pending orders")
	}
	_, err = svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{SupportedTypes: []string{"usdt.trc20"}})
	require.ErrorContains(t, err, "pending orders")
	_, err = svc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{SupportedTypes: []string{"usdt.invalid"}})
	require.ErrorContains(t, err, "unsupported bepusdt")
	_, err = svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{ProviderKey: payment.TypeBEpusdt, RefundEnabled: true})
	require.ErrorContains(t, err, "refunds are not supported")
}

func TestBEpusdtSnapshotCurrencyAndQueryIdentity(t *testing.T) {
	sel := &payment.InstanceSelection{InstanceID: "7", ProviderKey: payment.TypeBEpusdt, Config: map[string]string{"currency": "USD", "apiToken": "secret"}}
	snapshot := buildPaymentOrderProviderSnapshot(sel, CreateOrderRequest{PaymentType: "usdt.trc20"})
	require.Equal(t, "USD", snapshot["currency"])
	require.NotContains(t, snapshot, "apiToken")
	order := &dbent.PaymentOrder{OutTradeNo: "merchant-order", PaymentTradeNo: "upstream-order", PaymentType: "usdt.trc20", ProviderSnapshot: snapshot}
	require.Equal(t, "USD", PaymentOrderCurrency(order))
	require.Equal(t, "upstream-order", paymentOrderQueryReference(order, nil))
	require.NoError(t, validateProviderSnapshotMetadata(order, payment.TypeBEpusdt, map[string]string{"currency": "USD", "order_id": "merchant-order"}))
	for _, metadata := range []map[string]string{
		{"currency": "CNY", "order_id": "merchant-order"},
		{"currency": "USD", "order_id": "wrong-order"},
		{"currency": "USD", "order_id": "merchant-order", "trade_type": "usdt.bep20"},
	} {
		require.Error(t, validateProviderSnapshotMetadata(order, payment.TypeBEpusdt, metadata))
	}
	require.NoError(t, validateProviderRequest(payment.TypeBEpusdt, "Crypto", "usdt.trc20,usdt.bep20,usdc.base"))
	require.Error(t, validateProviderRequest(payment.TypeBEpusdt, "Crypto", "alipay"))
}

func TestBEpusdtFulfillmentRejectsWrongAmountsAndTransactions(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPending, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetProviderKey(payment.TypeBEpusdt).SetPaymentType("usdt.trc20").SetPayAmount(28.88).
		SetPaymentTradeNo("upstream-order").
		SetProviderSnapshot(map[string]any{"provider_key": payment.TypeBEpusdt, "currency": "CNY"}).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client}
	for _, paid := range []float64{4.25, 28.87, 28.89, 28.8801} {
		err := svc.HandlePaymentNotification(ctx, &payment.PaymentNotification{
			OrderID: order.OutTradeNo, TradeNo: "upstream-order", Amount: paid, Status: payment.NotificationStatusSuccess,
			Metadata: map[string]string{"currency": "CNY", "order_id": order.OutTradeNo},
		}, payment.TypeBEpusdt)
		require.ErrorContains(t, err, "amount mismatch")
	}
	err = svc.HandlePaymentNotification(ctx, &payment.PaymentNotification{
		OrderID: order.OutTradeNo, TradeNo: "wrong-trade", Amount: 28.88, Status: payment.NotificationStatusSuccess,
		Metadata: map[string]string{"currency": "CNY", "order_id": order.OutTradeNo},
	}, payment.TypeBEpusdt)
	require.ErrorContains(t, err, "transaction ID mismatch")
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPending, reloaded.Status)
}

func TestBEpusdtSuccessfulBalanceNotificationCreditsOnlyOnce(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPending, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetOrderType(payment.OrderTypeBalance).ClearPlanID().ClearSubscriptionGroupID().ClearSubscriptionDays().
		SetProviderKey(payment.TypeBEpusdt).SetPaymentType("usdt.trc20").SetPayAmount(28.88).
		SetPaymentTradeNo("upstream-order").
		SetProviderSnapshot(map[string]any{"provider_key": payment.TypeBEpusdt, "currency": "CNY"}).Save(ctx)
	require.NoError(t, err)
	redeemRepo := &paymentFulfillmentRedeemRepo{}
	credited := 0.0
	userRepo := &mockUserRepo{getByIDUser: &User{ID: order.UserID}}
	userRepo.updateBalanceFn = func(_ context.Context, id int64, amount float64) error {
		credited += amount
		return nil
	}
	redeemService := NewRedeemService(redeemRepo, userRepo, nil, &paymentFulfillmentRedeemCacheStub{}, nil, client, nil, nil)
	svc := &PaymentService{entClient: client, redeemService: redeemService, userRepo: userRepo}
	notification := &payment.PaymentNotification{OrderID: order.OutTradeNo, TradeNo: "upstream-order", Amount: 28.88,
		Status: payment.NotificationStatusSuccess, Metadata: map[string]string{"currency": "CNY", "order_id": order.OutTradeNo}}
	require.NoError(t, svc.HandlePaymentNotification(ctx, notification, payment.TypeBEpusdt))
	require.NoError(t, svc.HandlePaymentNotification(ctx, notification, payment.TypeBEpusdt))
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.Equal(t, order.Amount, credited)
	require.Len(t, redeemRepo.useCalls, 1)
}

func newBEpusdtLifecycleService(t *testing.T, upstream http.HandlerFunc) (*PaymentService, *dbent.PaymentOrder) {
	t.Helper()
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	configSvc := NewPaymentConfigService(client, nil, nil)
	cfg := map[string]string{"apiBase": server.URL, "apiToken": "test-token", "currency": "CNY",
		"notifyUrl": "https://merchant.example/api/v1/payment/webhook/bepusdt", "returnUrl": "https://merchant.example/payment/result"}
	instance, err := configSvc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey: payment.TypeBEpusdt, Name: "Crypto", Config: cfg, Enabled: true, SupportedTypes: []string{"usdt.trc20"},
	})
	require.NoError(t, err)
	instanceID := strconv.FormatInt(instance.ID, 10)
	selection := &payment.InstanceSelection{InstanceID: instanceID, ProviderKey: payment.TypeBEpusdt, Config: cfg}
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPending, time.Now())
	order, err = client.PaymentOrder.UpdateOneID(order.ID).
		SetProviderKey(payment.TypeBEpusdt).SetProviderInstanceID(instanceID).SetPaymentType("usdt.trc20").
		SetPaymentTradeNo("gateway-trade").SetPayAmount(28.88).
		SetProviderSnapshot(buildPaymentOrderProviderSnapshot(selection, CreateOrderRequest{PaymentType: "usdt.trc20"})).Save(ctx)
	require.NoError(t, err)
	return &PaymentService{entClient: client, configService: configSvc,
		loadBalancer: payment.NewDefaultLoadBalancer(client, nil), registry: payment.NewRegistry()}, order
}

func TestBEpusdtMissedWebhookRecoveredByQueryAndLateCallbackIsIdempotent(t *testing.T) {
	ctx := context.Background()
	var order *dbent.PaymentOrder
	queryCalls := 0
	svc, pending := newBEpusdtLifecycleService(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/pay/info", r.URL.Path)
		var fields map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&fields))
		require.Equal(t, "gateway-trade", fields["trade_id"])
		queryCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "data": map[string]any{
			"trade_id": "gateway-trade", "order_id": order.OutTradeNo, "money": "28.88", "actual_amount": "4.25",
			"fiat": "CNY", "trade_type": "usdt.trc20", "status": 2,
		}})
	})
	var err error
	order, err = svc.entClient.PaymentOrder.UpdateOneID(pending.ID).
		SetOrderType(payment.OrderTypeBalance).ClearPlanID().ClearSubscriptionGroupID().ClearSubscriptionDays().Save(ctx)
	require.NoError(t, err)
	redeemRepo := &paymentFulfillmentRedeemRepo{}
	credited := 0.0
	userRepo := &mockUserRepo{getByIDUser: &User{ID: order.UserID}}
	userRepo.updateBalanceFn = func(_ context.Context, _ int64, amount float64) error {
		credited += amount
		return nil
	}
	svc.userRepo = userRepo
	svc.redeemService = NewRedeemService(redeemRepo, userRepo, nil, &paymentFulfillmentRedeemCacheStub{}, nil, svc.entClient, nil, nil)
	verified, err := svc.VerifyOrderByOutTradeNo(ctx, order.OutTradeNo, order.UserID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, verified.Status)
	require.Equal(t, 1, queryCalls)
	require.NoError(t, svc.HandlePaymentNotification(ctx, &payment.PaymentNotification{
		OrderID: order.OutTradeNo, TradeNo: "gateway-trade", Amount: 28.88, Status: payment.NotificationStatusSuccess,
		Metadata: map[string]string{"currency": "CNY", "order_id": order.OutTradeNo, "trade_type": "usdt.trc20"},
	}, payment.TypeBEpusdt))
	require.Equal(t, order.Amount, credited)
	require.Len(t, redeemRepo.useCalls, 1)
}

func TestBEpusdtCancelPendingOrderClosesCorrectUpstreamTransaction(t *testing.T) {
	ctx := context.Background()
	var order *dbent.PaymentOrder
	var paths []string
	svc, pending := newBEpusdtLifecycleService(t, func(w http.ResponseWriter, r *http.Request) {
		var fields map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&fields))
		require.Equal(t, "gateway-trade", fields["trade_id"])
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/pay/info":
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "data": map[string]any{
				"trade_id": "gateway-trade", "order_id": order.OutTradeNo, "money": "28.88", "fiat": "CNY", "status": 1,
			}})
		case "/api/v1/order/cancel-transaction":
			require.NotEmpty(t, fields["signature"])
			_, _ = w.Write([]byte(`{"status_code":200,"data":{"trade_id":"gateway-trade"}}`))
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
		}
	})
	order = pending
	result, err := svc.CancelOrder(ctx, order.ID, order.UserID)
	require.NoError(t, err)
	require.Equal(t, checkPaidResultCancelled, result)
	require.Equal(t, []string{"/api/v1/pay/info", "/api/v1/order/cancel-transaction"}, paths)
	reloaded, err := svc.entClient.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCancelled, reloaded.Status)
}

func TestBEpusdtDuplicateSubscriptionNotificationDoesNotExtendTwice(t *testing.T) {
	ctx := context.Background()
	svc, order := newBEpusdtLifecycleService(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("notification must not make unexpected upstream calls: %s", r.URL.Path)
	})
	subRepo := newSubscriptionUserSubRepoStub()
	groupRepo := &subscriptionGroupRepoStub{group: &Group{
		ID: 7, Status: payment.EntityStatusActive, SubscriptionType: SubscriptionTypeSubscription,
	}}
	svc.groupRepo = groupRepo
	svc.subscriptionSvc = NewSubscriptionService(groupRepo, subRepo, nil, nil, nil)
	notification := &payment.PaymentNotification{
		OrderID: order.OutTradeNo, TradeNo: "gateway-trade", Amount: 28.88, Status: payment.NotificationStatusSuccess,
		Metadata: map[string]string{"currency": "CNY", "order_id": order.OutTradeNo, "trade_type": "usdt.trc20"},
	}
	require.NoError(t, svc.HandlePaymentNotification(ctx, notification, payment.TypeBEpusdt))
	subscription, err := subRepo.GetByUserIDAndGroupID(ctx, order.UserID, *order.SubscriptionGroupID)
	require.NoError(t, err)
	expiresAt := subscription.ExpiresAt
	require.NoError(t, svc.HandlePaymentNotification(ctx, notification, payment.TypeBEpusdt))
	assertPaymentSubscriptionExpiry(t, subRepo, order, expiresAt)
	require.Equal(t, 1, subRepo.createCalls)
	reloaded, err := svc.entClient.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
}
