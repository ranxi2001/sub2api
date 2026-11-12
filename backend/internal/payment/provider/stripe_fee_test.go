//go:build unit

package provider

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
	stripe "github.com/stripe/stripe-go/v85"
)

type stripeFeeBackend struct {
	stripeRefundBackend
	params *stripe.PaymentIntentCreateParams
}

func (b *stripeFeeBackend) Call(_ string, _ string, _ string, params stripe.ParamsContainer, result stripe.LastResponseSetter) error {
	b.params = params.(*stripe.PaymentIntentCreateParams)
	intent := result.(*stripe.PaymentIntent)
	intent.ID, intent.ClientSecret = "pi_fee", "test_secret"
	return nil
}

func TestStripeFeeOrderLocksSelectedMethodAndAmount(t *testing.T) {
	backend := &stripeFeeBackend{}
	client := stripe.NewClient("sk_test", stripe.WithBackends(&stripe.Backends{API: backend}))
	provider := &Stripe{config: map[string]string{"currency": "CNY"}, initialized: true, sc: client}
	_, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID: "test-order", Amount: "102.50", PaymentType: "stripe", PaymentSubtype: "wxpay", InstanceSubMethods: "card,link,alipay,wxpay",
	})
	require.NoError(t, err)
	require.Equal(t, int64(10250), *backend.params.Amount)
	require.Len(t, backend.params.PaymentMethodTypes, 1)
	require.Equal(t, "wechat_pay", *backend.params.PaymentMethodTypes[0])
	_, err = provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		Amount: "100.00", PaymentType: "stripe", PaymentSubtype: "alipay", InstanceSubMethods: "card",
	})
	require.Error(t, err)
	for _, tc := range []struct {
		method, amount string
		minor          int64
	}{{"card", "100.00", 10000}, {"link", "103.00", 10300}} {
		_, err = provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "test-" + tc.method, Amount: tc.amount, PaymentType: "stripe", PaymentSubtype: tc.method, InstanceSubMethods: "card,link"})
		require.NoError(t, err)
		require.Equal(t, tc.minor, *backend.params.Amount)
		require.Len(t, backend.params.PaymentMethodTypes, 1)
		require.Equal(t, tc.method, *backend.params.PaymentMethodTypes[0])
	}
}
