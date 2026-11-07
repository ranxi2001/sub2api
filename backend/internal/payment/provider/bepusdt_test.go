//go:build unit

package provider

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func testBEpusdtConfig(base string) map[string]string {
	return map[string]string{"apiBase": base, "apiToken": "test-token", "currency": "CNY",
		"notifyUrl": "https://merchant.example/api/v1/payment/webhook/bepusdt", "returnUrl": "https://merchant.example/payment/result"}
}

func TestBEpusdtNativeCreateAllNetworksQueryAndCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var fields map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&fields))
		switch r.URL.Path {
		case "/api/v1/order/create-transaction":
			require.Equal(t, float64(28.88), fields["amount"])
			require.Equal(t, "CNY", fields["fiat"])
			require.Equal(t, "merchant-order", fields["order_id"])
			require.Equal(t, "https://merchant.example/payment/result?order_id=7", fields["redirect_url"])
			require.GreaterOrEqual(t, fields["timeout"].(float64), float64(120))
			require.LessOrEqual(t, fields["timeout"].(float64), float64(600))
			canonical := fmt.Sprintf("amount=28.88&fiat=CNY&name=Recharge&notify_url=https://merchant.example/api/v1/payment/webhook/bepusdt&order_id=merchant-order&redirect_url=https://merchant.example/payment/result?order_id=7&timeout=%v&trade_type=%vtest-token", fields["timeout"], fields["trade_type"])
			require.Equal(t, fmt.Sprintf("%x", md5.Sum([]byte(canonical))), fields["signature"])
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "data": map[string]any{
				"fiat": "CNY", "trade_id": "upstream-order", "order_id": "merchant-order", "trade_type": fields["trade_type"],
				"amount": "28.88", "actual_amount": "4.250", "token": "receiving-address", "status": 1,
				"payment_url": "https://gateway.example/pay/checkout/upstream-order"}})
		case "/api/v1/pay/info":
			require.Equal(t, "upstream-order", fields["trade_id"])
			require.NotContains(t, fields, "signature")
			_, _ = w.Write([]byte(`{"status_code":200,"data":{"trade_id":"upstream-order","order_id":"merchant-order","money":"28.88","actual_amount":"4.250","fiat":"CNY","trade_type":"usdt.trc20","status":2}}`))
		case "/api/v1/order/cancel-transaction":
			require.Equal(t, fmt.Sprintf("%x", md5.Sum([]byte("trade_id=upstream-ordertest-token"))), fields["signature"])
			_, _ = w.Write([]byte(`{"status_code":200,"data":{"trade_id":"upstream-order"}}`))
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	b, err := NewBEpusdt("7", testBEpusdtConfig(server.URL))
	require.NoError(t, err)
	for _, network := range payment.BEpusdtTradeTypes() {
		t.Run(network, func(t *testing.T) {
			result, err := b.CreatePayment(context.Background(), payment.CreatePaymentRequest{
				OrderID: "merchant-order", Amount: "28.88", PaymentType: network, Subject: "Recharge",
				ReturnURL: "https://merchant.example/payment/result?order_id=7", ExpiresAt: time.Now().Add(10 * time.Minute)})
			require.NoError(t, err)
			require.Equal(t, "upstream-order", result.TradeNo)
			require.Equal(t, "CNY", result.Currency)
			require.Equal(t, "https://gateway.example/pay/checkout/upstream-order", result.PayURL)
			require.Empty(t, result.QRCode, "the wallet address alone does not convey the exact amount and network")
		})
	}
	queried, err := b.QueryOrder(context.Background(), "upstream-order")
	require.NoError(t, err)
	require.Equal(t, payment.ProviderStatusPaid, queried.Status)
	require.Equal(t, 28.88, queried.Amount, "use fiat money, never actual_amount")
	require.Equal(t, "merchant-order", queried.Metadata["order_id"])
	require.NoError(t, b.CancelPayment(context.Background(), "upstream-order"))
}

func bepusdtSignedFixture(t *testing.T, amount any, actual string, status int) string {
	t.Helper()
	fields := map[string]any{"trade_id": "upstream-order", "order_id": "merchant-order", "amount": amount,
		"actual_amount": actual, "token": "wallet", "block_transaction_id": "chain-hash", "status": status}
	canonical := fmt.Sprintf("actual_amount=%s&amount=%v&block_transaction_id=chain-hash&order_id=merchant-order&status=%d&token=wallet&trade_id=upstream-ordertest-token", actual, amount, status)
	fields["signature"] = fmt.Sprintf("%x", md5.Sum([]byte(canonical)))
	raw, err := json.Marshal(fields)
	require.NoError(t, err)
	return string(raw)
}

func TestBEpusdtVerifySignedCallbacks(t *testing.T) {
	b, err := NewBEpusdt("7", testBEpusdtConfig("https://gateway.example"))
	require.NoError(t, err)
	for _, amount := range []any{28.88, "28.88", float64(42)} {
		n, err := b.VerifyNotification(context.Background(), bepusdtSignedFixture(t, amount, "4.250", 2), nil)
		require.NoError(t, err)
		require.Equal(t, "merchant-order", n.OrderID)
		require.Equal(t, "upstream-order", n.TradeNo)
		require.Equal(t, payment.NotificationStatusSuccess, n.Status)
		require.NotEqual(t, 4.25, n.Amount)
	}
	for _, status := range []int{1, 3, 4, 5, 6} {
		n, err := b.VerifyNotification(context.Background(), bepusdtSignedFixture(t, 28.88, "4.250", status), nil)
		require.NoError(t, err)
		require.Nil(t, n)
	}
	for _, amount := range []any{0, -1, "NaN", "Inf"} {
		_, err := b.VerifyNotification(context.Background(), bepusdtSignedFixture(t, amount, "4.250", 2), nil)
		require.Error(t, err)
	}
	var tampered map[string]any
	require.NoError(t, json.Unmarshal([]byte(bepusdtSignedFixture(t, 28.88, "4.250", 2)), &tampered))
	tampered["amount"] = 100
	raw, _ := json.Marshal(tampered)
	_, err = b.VerifyNotification(context.Background(), string(raw), nil)
	require.ErrorContains(t, err, "signature")
	for _, raw := range []string{`null`, `{"amount":{}}`, `{"signature":""}`, `[]`} {
		_, err := b.VerifyNotification(context.Background(), raw, nil)
		require.Error(t, err)
	}
}

func TestBEpusdtRejectsInvalidConfigurationAndOrders(t *testing.T) {
	for _, key := range []string{"apiBase", "apiToken", "notifyUrl", "returnUrl"} {
		cfg := testBEpusdtConfig("https://gateway.example")
		delete(cfg, key)
		_, err := NewBEpusdt("7", cfg)
		require.Error(t, err)
	}
	for _, base := range []string{"ftp://gateway.example", "https://user:pass@gateway.example", "https://gateway.example?token=secret", "https://gateway.example#api"} {
		_, err := NewBEpusdt("7", testBEpusdtConfig(base))
		require.Error(t, err)
	}
	cfg := testBEpusdtConfig("https://gateway.example")
	cfg["currency"] = "USDT"
	_, err := NewBEpusdt("7", cfg)
	require.Error(t, err)
	b, err := NewBEpusdt("7", testBEpusdtConfig("https://gateway.example"))
	require.NoError(t, err)
	for _, request := range []payment.CreatePaymentRequest{
		{OrderID: "order", Amount: "0", PaymentType: "usdt.trc20"},
		{OrderID: "order", Amount: "NaN", PaymentType: "usdt.trc20"},
		{OrderID: "order", Amount: "0.001", PaymentType: "usdt.trc20"},
		{OrderID: "order", Amount: "10", PaymentType: "usdt.invalid"},
		{OrderID: "order", Amount: "10", PaymentType: "usdt.trc20", ExpiresAt: time.Now().Add(-time.Minute)},
	} {
		_, err := b.CreatePayment(context.Background(), request)
		require.Error(t, err)
	}
}

func TestBEpusdtRejectsBadGatewayResponsesAndRedirects(t *testing.T) {
	for _, response := range []string{
		`{"status_code":400,"message":"no wallet available"}`,
		`{"status_code":200,"data":{"trade_id":"other","fiat":"CNY","money":"28.88","status":2}}`,
		`{"status_code":200,"data":{"trade_id":"upstream-order","fiat":"USD","money":"28.88","status":2}}`,
		`{"status_code":200,"data":{"trade_id":"upstream-order","fiat":"CNY","money":"NaN","status":2}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(response)) }))
		b, err := NewBEpusdt("7", testBEpusdtConfig(server.URL))
		require.NoError(t, err)
		_, err = b.QueryOrder(context.Background(), "upstream-order")
		require.Error(t, err)
		server.Close()
	}
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	b, err := NewBEpusdt("7", testBEpusdtConfig(server.URL))
	require.NoError(t, err)
	require.Error(t, b.CancelPayment(context.Background(), "upstream-order"))
	require.False(t, redirected)
}
