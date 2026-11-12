//go:build unit

package service

import (
	"context"
	"net/url"
	"strconv"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestGetAvailableMethodLimitsAggregatesEffectiveFees(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	_, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeStripe).SetName("first").SetConfig(`{"currency":"USD"}`).SetSupportedTypes("card,alipay").SetFeeRate(2).SetMethodFeeRates(map[string]float64{"alipay": 0, "wxpay": 99}).Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeStripe).SetName("second").SetConfig(`{"currency":"USD"}`).SetSupportedTypes("card,link").Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeStripe).SetName("disabled").SetConfig(`{"currency":"USD"}`).SetSupportedTypes("wxpay").SetFeeRate(99).SetEnabled(false).Save(ctx)
	require.NoError(t, err)
	svc := NewPaymentConfigService(client, &paymentConfigSettingRepoStub{values: map[string]string{SettingRechargeFeeRate: "3"}}, nil)
	resp, err := svc.GetAvailableMethodLimits(ctx)
	require.NoError(t, err)
	method := resp.Methods[payment.TypeStripe]
	require.Equal(t, 0.0, method.FeeRateMin)
	require.Equal(t, 3.0, method.FeeRateMax)
	require.Equal(t, MethodFeeRates{FeeRate: 2, FeeRateMin: 2, FeeRateMax: 3}, method.SubMethods["card"])
	require.Equal(t, MethodFeeRates{}, method.SubMethods["alipay"])
	require.NotContains(t, method.SubMethods, "wxpay")
	orders := &PaymentService{configService: svc}
	err = orders.validateStripeFeeSelection(ctx, CreateOrderRequest{PaymentType: payment.TypeStripe})
	require.Equal(t, "PAYMENT_SUBTYPE_REQUIRED", infraerrors.FromError(err).Reason)
	require.NoError(t, orders.validateStripeFeeSelection(ctx, CreateOrderRequest{PaymentType: payment.TypeStripe, PaymentSubtype: "card"}))
}

func TestCreateOrderUsesProviderMethodFeeAndPreservesSnapshot(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	settings := &paymentFulfillmentSettingRepoStub{values: map[string]string{SettingPaymentEnabled: "true", SettingRechargeFeeRate: "9"}}
	configSvc := NewPaymentConfigService(client, settings, nil)
	inst, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeEasyPay).SetName("test").SetConfig(`{"pid":"test","pkey":"test-key","apiBase":"https://gateway.example","notifyUrl":"https://merchant.example/notify","returnUrl":"https://merchant.example/payment/result"}`).SetSupportedTypes("alipay,wxpay").SetPaymentMode("popup").SetFeeRate(2).SetMethodFeeRates(map[string]float64{"alipay": 0}).Save(ctx)
	require.NoError(t, err)
	user, err := client.User.Create().SetEmail("fee@example.com").SetUsername("fee").SetPasswordHash("hash").Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client, configService: configSvc, loadBalancer: payment.NewDefaultLoadBalancer(client, nil), registry: payment.NewRegistry(), userRepo: &mockUserRepo{getByIDUser: &User{ID: user.ID, Email: user.Email, Username: user.Username, Status: StatusActive}}}
	for _, tc := range []struct {
		method, amount string
		rate           float64
	}{{"alipay", "100.00", 0}, {"wxpay", "102.00", 2}} {
		result, createErr := svc.CreateOrder(ctx, CreateOrderRequest{UserID: user.ID, Amount: 100, PaymentType: tc.method, ReturnURL: "https://merchant.example/payment/result", SrcHost: "merchant.example"})
		require.NoError(t, createErr)
		require.Equal(t, tc.rate, result.FeeRate)
		parsed, parseErr := url.Parse(result.PayURL)
		require.NoError(t, parseErr)
		require.Equal(t, tc.amount, parsed.Query().Get("money"))
		order, readErr := client.PaymentOrder.Get(ctx, result.OrderID)
		require.NoError(t, readErr)
		require.Equal(t, tc.rate, order.FeeRate)
		require.Equal(t, strconv.FormatInt(inst.ID, 10), *order.ProviderInstanceID)
		if tc.method == "alipay" {
			require.Equal(t, "method", order.ProviderSnapshot["fee_source"])
		} else {
			require.Equal(t, "provider", order.ProviderSnapshot["fee_source"])
		}
	}
	orders, err := client.PaymentOrder.Query().All(ctx)
	require.NoError(t, err)
	_, err = client.PaymentProviderInstance.UpdateOneID(inst.ID).SetFeeRate(50).SetMethodFeeRates(map[string]float64{"alipay": 50}).Save(ctx)
	require.NoError(t, err)
	settings.values[SettingRechargeFeeRate] = "70"
	for _, order := range orders {
		reloaded, reloadErr := svc.GetOrder(ctx, order.ID, user.ID)
		require.NoError(t, reloadErr)
		require.Equal(t, order.PayAmount, reloaded.PayAmount)
		require.Equal(t, order.FeeRate, reloaded.FeeRate)
		require.Equal(t, order.PayAmount, calculateGatewayRefundAmount(order.Amount, order.PayAmount, order.Amount, "CNY"))
	}
}

func TestPaymentOrderSubtypeComesFromSnapshot(t *testing.T) {
	req := CreateOrderRequest{PaymentType: "stripe", PaymentSubtype: "card"}
	sel := &payment.InstanceSelection{ProviderKey: "stripe", InstanceID: "7", Quote: &payment.InstanceQuote{FeeSource: "method"}}
	snapshot := buildPaymentOrderProviderSnapshot(sel, req)
	order := &dbent.PaymentOrder{ProviderSnapshot: snapshot, FeeRate: 3, PayAmount: 103}
	require.Equal(t, "card", PaymentOrderSubtype(order))
	require.Equal(t, "method", snapshot["fee_source"])
	response := buildCreateOrderResponse(order, req, 103, sel, &payment.CreatePaymentResponse{}, "order_created")
	require.Equal(t, "card", response.PaymentSubtype)
	require.Equal(t, 3.0, response.FeeRate)
}

func TestProviderFeeRoutingUsesEligibleSubtypeAndQuotedAmount(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	_, err := client.PaymentProviderInstance.Create().SetProviderKey("stripe").SetConfig("{}").SetSupportedTypes("card").SetFeeRate(0).Save(ctx)
	require.NoError(t, err)
	expected, err := client.PaymentProviderInstance.Create().SetProviderKey("stripe").SetConfig("{}").SetSupportedTypes("alipay").SetMethodFeeRates(map[string]float64{"alipay": 2}).SetLimits(`{"stripe":{"singleMax":102}}`).Save(ctx)
	require.NoError(t, err)
	_, err = client.PaymentProviderInstance.Create().SetProviderKey("stripe").SetConfig("{}").SetSupportedTypes("alipay").SetFeeRate(5).SetLimits(`{"stripe":{"singleMax":102}}`).Save(ctx)
	require.NoError(t, err)
	quoteCtx := payment.WithInstanceQuote(ctx, "alipay", func(inst *dbent.PaymentProviderInstance) (*payment.InstanceQuote, error) {
		rate, source, resolveErr := payment.ResolveOrderInstanceFee(inst, "stripe", "alipay", 9)
		if resolveErr != nil {
			return nil, resolveErr
		}
		text, amount, amountErr := calculateCreateOrderPayAmount(100, rate, "CNY")
		return &payment.InstanceQuote{FeeRate: rate, FeeSource: source, PaymentSubtype: "alipay", PayAmount: amount, PayAmountText: text}, amountErr
	})
	for _, strategy := range []payment.Strategy{payment.StrategyRoundRobin, payment.StrategyLeastAmount} {
		selection, selectErr := payment.NewDefaultLoadBalancer(client, nil).SelectInstance(quoteCtx, "", "stripe", strategy, 109)
		require.NoError(t, selectErr)
		require.Equal(t, strconv.FormatInt(expected.ID, 10), selection.InstanceID)
		require.Equal(t, "102.00", selection.Quote.PayAmountText)
		require.Equal(t, 2.0, selection.Quote.FeeRate)
	}
}

func TestProviderFeeCardAndLinkHaveIndependentQuotesAndCheckoutRates(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	_, err := client.PaymentProviderInstance.Create().SetProviderKey("stripe").SetConfig("{}").SetSupportedTypes("card,link").SetFeeRate(2).SetMethodFeeRates(map[string]float64{"card": 0, "link": 3}).Save(ctx)
	require.NoError(t, err)
	config := NewPaymentConfigService(client, &paymentConfigSettingRepoStub{values: map[string]string{SettingRechargeFeeRate: "9"}}, nil)
	limits, err := config.GetAvailableMethodLimits(ctx)
	require.NoError(t, err)
	stripe := limits.Methods["stripe"]
	require.Equal(t, MethodFeeRates{}, stripe.SubMethods["card"])
	require.Equal(t, MethodFeeRates{FeeRate: 3, FeeRateMin: 3, FeeRateMax: 3}, stripe.SubMethods["link"])
	require.Equal(t, 0.0, stripe.FeeRateMin)
	require.Equal(t, 3.0, stripe.FeeRateMax)
	for _, tc := range []struct {
		subtype, text string
		rate          float64
	}{{"card", "100.00", 0}, {"link", "103.00", 3}} {
		quoteCtx := payment.WithInstanceQuote(ctx, tc.subtype, func(inst *dbent.PaymentProviderInstance) (*payment.InstanceQuote, error) {
			rate, source, feeErr := payment.ResolveOrderInstanceFee(inst, "stripe", tc.subtype, 9)
			if feeErr != nil {
				return nil, feeErr
			}
			text, amount, amountErr := calculateCreateOrderPayAmount(100, rate, "CNY")
			return &payment.InstanceQuote{FeeRate: rate, FeeSource: source, PaymentSubtype: tc.subtype, PayAmount: amount, PayAmountText: text}, amountErr
		})
		selection, selectErr := payment.NewDefaultLoadBalancer(client, nil).SelectInstance(quoteCtx, "", "stripe", payment.StrategyRoundRobin, 109)
		require.NoError(t, selectErr)
		require.Equal(t, tc.text, selection.Quote.PayAmountText)
		require.Equal(t, tc.rate, selection.Quote.FeeRate)
		request := buildProviderCreatePaymentRequest(CreateOrderRequest{PaymentType: "stripe", PaymentSubtype: tc.subtype}, selection, "test-order", tc.text, "test")
		require.Equal(t, tc.subtype, request.PaymentSubtype)
		require.Equal(t, tc.text, request.Amount)
	}
}
