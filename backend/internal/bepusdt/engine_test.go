//go:build unit

package bepusdt

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"github.com/v03413/bepusdt/app/model"
)

func TestBEpusdtEmbeddedWalletAllocationAndProtection(t *testing.T) {
	cleanup, err := InitializeForTest(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(cleanup)
	ctx := context.Background()
	require.Error(t, SaveWallet(ctx, model.Wallet{Address: "invalid", TradeType: "usdt.erc20", Status: 1}))
	require.NoError(t, SaveWallet(ctx, model.Wallet{Name: "Ethereum wallet", Address: "0x1111111111111111111111111111111111111111", TradeType: "usdt.erc20", Status: 1}))
	require.NoError(t, SaveRate(ctx, "CNY", "USDT", "7.2"))
	wallets, err := Wallets(ctx)
	require.NoError(t, err)
	require.Len(t, wallets, 1)
	request := CreateRequest{OrderID: "first-order", Amount: "28.80", TradeType: "usdt.erc20", Currency: "CNY", NotifyURL: "https://merchant.example/api/v1/payment/webhook/bepusdt", ReturnURL: "https://merchant.example/payment/result", ExpiresAt: time.Now().Add(5 * time.Minute)}
	first, err := Create(ctx, request)
	require.NoError(t, err)
	require.Equal(t, 4.0, decimal.RequireFromString(first.Amount).InexactFloat64())
	require.Equal(t, wallets[0].Address, first.Address)
	request.OrderID = "second-order"
	second, err := Create(ctx, request)
	require.NoError(t, err)
	require.NotEqual(t, first.Amount, second.Amount, "simultaneous orders need distinct transfer amounts")
	wallets[0].Status = 0
	require.ErrorContains(t, SaveWallet(ctx, wallets[0]), "pending payments")
	require.ErrorContains(t, DeleteWallet(ctx, wallets[0].ID), "pending payments")
	require.NoError(t, Cancel(ctx, first.TradeId))
	require.Error(t, Cancel(ctx, first.TradeId))
	require.NoError(t, Cancel(ctx, second.TradeId))
	require.NoError(t, DeleteWallet(ctx, wallets[0].ID))
}

func TestBEpusdtEmbeddedBEP20AllocationNormalizesWalletAddress(t *testing.T) {
	cleanup, err := InitializeForTest(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(cleanup)
	ctx := context.Background()
	address := "0xAbCdEf1234567890AbCdEf1234567890AbCdEf12"
	require.NoError(t, SaveWallet(ctx, model.Wallet{Address: address, TradeType: "usdt.bep20", Status: 1}))
	require.NoError(t, SaveRate(ctx, "USD", "USDT", "1"))
	req := CreateRequest{OrderID: "mixed-case-first", Amount: "28.80", TradeType: "usdt.bep20", Currency: "USD", ExpiresAt: time.Now().Add(5 * time.Minute)}
	first, err := Create(ctx, req)
	require.NoError(t, err)
	require.Equal(t, strings.ToLower(address), first.Address, "new EVM payment addresses are normalized")
	// Reproduce a historical order whose display address kept its original
	// casing while the scanner matched the normalized address.
	require.NoError(t, model.Db.Model(&model.Order{}).Where("id = ?", first.ID).Update("address", address).Error)
	first, err = Order(ctx, first.TradeId)
	require.NoError(t, err)
	require.Equal(t, address, first.Address)
	require.NotEqual(t, first.Address, first.MatchAddress)
	req.OrderID = "mixed-case-second"
	second, err := Create(ctx, req)
	require.NoError(t, err)
	require.NotEqual(t, first.Amount, second.Amount, "normalized wallet address must reserve the first payment amount")
	// Existing records without a normalized matching address remain reserved.
	require.NoError(t, model.Db.Model(&model.Order{}).Where("id = ?", first.ID).Update("match_address", "").Error)
	legacy, err := Order(ctx, first.TradeId)
	require.NoError(t, err)
	require.Equal(t, address, legacy.Address)
	require.Empty(t, legacy.MatchAddress)
	req.OrderID = "mixed-case-legacy"
	third, err := Create(ctx, req)
	require.NoError(t, err)
	require.NotEqual(t, first.Amount, third.Amount)
	require.NotEqual(t, second.Amount, third.Amount)
}

func TestBEpusdtEmbeddedPreservesMerchantDeadline(t *testing.T) {
	cleanup, err := InitializeForTest(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(cleanup)
	ctx := context.Background()
	require.NoError(t, SaveWallet(ctx, model.Wallet{Address: "0x1111111111111111111111111111111111111111", TradeType: "usdt.bep20", Status: 1}))
	require.NoError(t, SaveRate(ctx, "USD", "USDT", "1"))
	for _, timeout := range []time.Duration{2 * time.Minute, 5 * time.Minute, 2 * time.Hour} {
		t.Run(timeout.String(), func(t *testing.T) {
			deadline := time.Now().Add(timeout)
			order, err := Create(ctx, CreateRequest{OrderID: "deadline-" + timeout.String(), Amount: "10", TradeType: "usdt.bep20", Currency: "USD", ExpiresAt: deadline})
			require.NoError(t, err)
			require.True(t, deadline.Equal(order.ExpiredAt), "engine deadline must equal the merchant deadline")
			saved, err := Order(ctx, order.TradeId)
			require.NoError(t, err)
			require.True(t, deadline.Equal(saved.ExpiredAt), "deadline must survive persistence")
		})
	}
	_, err = Create(ctx, CreateRequest{OrderID: "expired-deadline", Amount: "10", TradeType: "usdt.bep20", Currency: "USD", ExpiresAt: time.Now().Add(-time.Second)})
	require.ErrorContains(t, err, "expired")
}

func TestBEpusdtEmbeddedSettingsAllowlistAndPersistence(t *testing.T) {
	dir := t.TempDir()
	cleanup, err := InitializeForTest(dir)
	require.NoError(t, err)
	ctx := context.Background()
	secret, err := Token()
	require.NoError(t, err)
	require.Len(t, secret, 64)
	require.Equal(t, "1", model.GetC(model.BlockOffsetConfirm))
	require.Error(t, SaveSettings(ctx, map[string]string{"api_auth_token": "tampered"}))
	require.Error(t, SaveSettings(ctx, map[string]string{"rate_sync_interval": "0"}))
	require.Error(t, SaveSettings(ctx, map[string]string{"rpc_endpoint_tron": "https://grpc.example"}))
	require.Error(t, SaveSettings(ctx, map[string]string{"rpc_endpoint_tron": "grpc.example:70000"}))
	require.Error(t, SaveSettings(ctx, map[string]string{"rpc_endpoint_base": "not-a-url"}))
	require.Error(t, SaveSettings(ctx, map[string]string{"rate_sync_coingecko_api_url": "file:///etc/passwd"}))
	require.NoError(t, SaveSettings(ctx, map[string]string{"rpc_endpoint_tron": "grpc.example:50051"}))
	require.NoError(t, SaveSettings(ctx, map[string]string{"rpc_endpoint_ethereum": "https://rpc.example", "rate_sync_interval": "3600"}))
	cleanup()
	cleanup, err = InitializeForTest(dir)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	saved, err := Settings()
	require.NoError(t, err)
	require.Equal(t, "https://rpc.example", saved["rpc_endpoint_ethereum"])
	persisted, err := Token()
	require.NoError(t, err)
	require.Equal(t, secret, persisted, "restart must preserve callback verification")
}
