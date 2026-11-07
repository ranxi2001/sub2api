//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/bepusdt"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"github.com/v03413/bepusdt/app/model"
)

func TestBEpusdtEmbeddedCheckoutOwnershipAndConfirmedFulfillment(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%t", expired), func(t *testing.T) {
			testBEpusdtEmbeddedCheckout(t, expired)
		})
	}
}

func testBEpusdtEmbeddedCheckout(t *testing.T, expired bool) {
	cleanup, err := bepusdt.InitializeForTest(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(cleanup)
	ctx := context.Background()
	require.NoError(t, bepusdt.SaveWallet(ctx, model.Wallet{Address: "0x1111111111111111111111111111111111111111", TradeType: "usdc.base", Status: 1}))
	require.NoError(t, bepusdt.SaveRate(ctx, "USD", "USDC", "1"))
	client := newPaymentConfigServiceTestClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	settings := &paymentFulfillmentSettingRepoStub{values: map[string]string{SettingPaymentEnabled: "true"}}
	configSvc := NewPaymentConfigService(client, settings, nil)
	instance, err := configSvc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{ProviderKey: payment.TypeBEpusdt, Name: "Built-in", Enabled: true,
		SupportedTypes: []string{"usdc.base"}, Config: map[string]string{"mode": "embedded", "currency": "USD", "notifyUrl": "https://merchant.example/api/v1/payment/webhook/bepusdt", "returnUrl": "https://merchant.example/payment/result"}})
	require.NoError(t, err)
	user, err := client.User.Create().SetEmail("native@example.com").SetUsername("native-user").SetPasswordHash("hash").Save(ctx)
	require.NoError(t, err)
	credited := 0.0
	userRepo := &mockUserRepo{getByIDUser: &User{ID: user.ID, Email: user.Email, Username: user.Username, Status: StatusActive}}
	userRepo.updateBalanceFn = func(_ context.Context, _ int64, amount float64) error { credited += amount; return nil }
	redeemRepo := &paymentFulfillmentRedeemRepo{}
	redeemService := NewRedeemService(redeemRepo, userRepo, nil, &paymentFulfillmentRedeemCacheStub{}, nil, client, nil, nil)
	svc := &PaymentService{entClient: client, configService: configSvc, loadBalancer: payment.NewDefaultLoadBalancer(client, nil), registry: payment.NewRegistry(), userRepo: userRepo, redeemService: redeemService}
	created, err := svc.CreateOrder(ctx, CreateOrderRequest{UserID: user.ID, Amount: 28.88, PaymentType: "usdc.base", OrderType: payment.OrderTypeBalance,
		ReturnURL: "https://merchant.example/payment/result", SrcHost: "merchant.example", ClientIP: "127.0.0.1"})
	require.NoError(t, err)
	require.Contains(t, created.PayURL, "/payment/crypto/"+created.OutTradeNo)
	checkout, err := svc.GetCryptoCheckout(ctx, created.OutTradeNo, user.ID)
	require.NoError(t, err)
	require.Equal(t, "28.88", checkout.CryptoAmount)
	require.Equal(t, "USDC", checkout.Crypto)
	require.Equal(t, "0x1111111111111111111111111111111111111111", checkout.Address)
	_, err = svc.GetCryptoCheckout(ctx, created.OutTradeNo, user.ID+1)
	require.ErrorContains(t, err, "not found")
	_, err = configSvc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{Config: map[string]string{"mode": "external"}})
	require.ErrorContains(t, err, "pending orders")
	local, err := client.PaymentOrder.Get(ctx, created.OrderID)
	require.NoError(t, err)
	trade, err := bepusdt.Order(ctx, local.PaymentTradeNo)
	require.NoError(t, err)
	if expired {
		_, err = client.PaymentOrder.UpdateOneID(local.ID).SetExpiresAt(time.Now().Add(-time.Second)).Save(ctx)
		require.NoError(t, err)
		count, err := svc.ExpireTimedOutOrders(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, count)
		trade, err = bepusdt.Order(ctx, local.PaymentTradeNo)
		require.NoError(t, err)
		require.Equal(t, model.OrderStatusWaiting, trade.Status, "local expiry must not cancel collection or disable backfill")
		require.True(t, trade.SetExpired())
		_, err = client.PaymentOrder.UpdateOneID(local.ID).SetUpdatedAt(time.Now().Add(-10 * time.Minute)).Save(ctx)
		require.NoError(t, err)

	}
	require.NoError(t, trade.MarkConfirming(10, "sender", "chain-transaction", time.Now(), decimal.RequireFromString("28.88")))
	_, err = svc.VerifyOrderByOutTradeNo(ctx, created.OutTradeNo, user.ID)
	require.NoError(t, err)
	require.Zero(t, credited, "a detected transfer must not be credited before confirmation")
	checkout, err = svc.GetCryptoCheckout(ctx, created.OutTradeNo, user.ID)
	require.NoError(t, err)
	require.True(t, checkout.Confirming)
	if !expired {
		_, err = svc.CancelOrder(ctx, local.ID, user.ID)
		require.ErrorContains(t, err, "no longer waiting")
		stillPending, err := client.PaymentOrder.Get(ctx, local.ID)
		require.NoError(t, err)
		require.Equal(t, OrderStatusPending, stillPending.Status)
	}
	require.NoError(t, trade.SetSuccess())
	for i := 0; i < 2; i++ {
		fulfilled, err := svc.VerifyOrderByOutTradeNo(ctx, created.OutTradeNo, user.ID)
		require.NoError(t, err)
		require.Equal(t, OrderStatusCompleted, fulfilled.Status)
	}
	require.Equal(t, 28.88, credited)
	require.Len(t, redeemRepo.useCalls, 1)
	_, err = configSvc.UpdateProviderInstance(ctx, instance.ID, UpdateProviderInstanceRequest{Config: map[string]string{"mode": "external", "apiBase": "https://gateway.example", "apiToken": "external-token"}})
	require.NoError(t, err)
	checkout, err = svc.GetCryptoCheckout(ctx, created.OutTradeNo, user.ID)
	require.NoError(t, err, "completed checkout must retain the snapshotted collection mode")
	require.Equal(t, OrderStatusCompleted, checkout.Status)
	require.NoError(t, model.Db.Model(&model.Order{}).Where("id = ?", trade.ID).Update("money", "1").Error)
	_, err = svc.GetCryptoCheckout(ctx, created.OutTradeNo, user.ID)
	require.ErrorContains(t, err, "amount mismatch")
}
