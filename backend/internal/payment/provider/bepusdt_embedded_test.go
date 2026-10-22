//go:build unit

package provider

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/bepusdt"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
	"github.com/v03413/bepusdt/app/model"
	"testing"
	"time"
)

func TestBEpusdtEmbeddedProviderWithoutGatewayOrToken(t *testing.T) {
	cleanup, err := bepusdt.InitializeForTest(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(cleanup)
	ctx := context.Background()
	require.NoError(t, bepusdt.SaveWallet(ctx, model.Wallet{Address: "0x1111111111111111111111111111111111111111", TradeType: "usdc.base", Status: 1}))
	require.NoError(t, bepusdt.SaveRate(ctx, "USD", "USDC", "1"))
	prov, err := CreateProvider(payment.TypeBEpusdt, "1", map[string]string{"mode": "embedded", "currency": "USD", "notifyUrl": "https://merchant.example/api/v1/payment/webhook/bepusdt", "returnUrl": "https://merchant.example/payment/result"})
	require.NoError(t, err)
	require.IsType(t, &EmbeddedBEpusdt{}, prov)
	result, err := prov.CreatePayment(ctx, payment.CreatePaymentRequest{OrderID: "native-order", Amount: "28.88", PaymentType: "usdc.base", ExpiresAt: time.Now().Add(5 * time.Minute)})
	require.NoError(t, err)
	require.Equal(t, "https://merchant.example/payment/crypto/native-order", result.PayURL)
	query, err := prov.QueryOrder(ctx, result.TradeNo)
	require.NoError(t, err)
	require.Equal(t, payment.ProviderStatusPending, query.Status)
	require.Equal(t, 28.88, query.Amount)
	require.Equal(t, "usdc.base", query.Metadata["trade_type"])
	require.NoError(t, prov.(payment.CancelableProvider).CancelPayment(ctx, result.TradeNo))
	query, err = prov.QueryOrder(ctx, result.TradeNo)
	require.NoError(t, err)
	require.Equal(t, payment.ProviderStatusFailed, query.Status)
}
