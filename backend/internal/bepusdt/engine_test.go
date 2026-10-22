//go:build unit

package bepusdt

import (
	"context"
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
