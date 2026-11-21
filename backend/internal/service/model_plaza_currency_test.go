package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestModelPlazaCurrencyRuntime(t *testing.T) {
	repo := &showcaseRuntimeSettingRepo{values: map[string]string{SettingKeyModelPlazaEnabled: "true"}}
	svc := &SettingService{settingRepo: repo}
	got := svc.GetModelPlazaRuntime(context.Background())
	require.Equal(t, float64(7), got.USDToCNYRate)
	require.Equal(t, "USD", got.Currency.CustomerCurrency)
	require.Equal(t, "USD", got.Currency.WalletCurrency)
	require.Equal(t, float64(1), got.Currency.WalletUnitValue)
	require.Empty(t, got.Currency.OfficialPlatformCurrencies)
	repo.values[SettingKeyModelPlazaUSDToCNYRate] = "7.2"
	repo.values[SettingKeyModelPlazaCurrency] = `{"wallet_currency":"CNY","wallet_unit_value":0.5,"customer_currency":"CNY","official_currency":"USD","official_platform_currencies":{"DeepSeek":"CNY"},"official_model_currencies":{"Custom/Model":"USD"}}`
	repo.values[SettingBalanceRechargeMult] = "100"
	got = svc.GetModelPlazaRuntime(context.Background())
	require.Equal(t, float64(7.2), got.USDToCNYRate)
	require.Equal(t, float64(0.5), got.Currency.WalletUnitValue, "payment multiplier is not a display valuation")
	require.Equal(t, "CNY", got.Currency.OfficialPlatformCurrencies["deepseek"])
	require.Equal(t, "USD", got.Currency.OfficialModelCurrencies["custom/model"])
	require.Nil(t, repo.saved)
	repo.readErr = errors.New("unavailable")
	require.False(t, svc.GetModelPlazaRuntime(context.Background()).Enabled)
}

func TestModelPlazaCurrencyValidation(t *testing.T) {
	for _, v := range []ModelPlazaCurrencySettings{
		{WalletCurrency: "EUR"}, {WalletCurrency: "USD", WalletUnitValue: 0}, {CustomerCurrency: "invalid"}, {WalletUnitValue: -1},
		{OfficialPlatformCurrencies: map[string]string{"": "CNY"}},
		{OfficialModelCurrencies: map[string]string{"model": "invalid"}},
		{OfficialModelCurrencies: map[string]string{"model": "USD", " Model ": "CNY"}},
	} {
		_, err := NormalizeModelPlazaCurrency(v)
		require.Error(t, err)
	}
	for _, raw := range []string{"", "-1", "NaN", "+Inf", "0"} {
		require.Equal(t, float64(7), modelPlazaUSDToCNYRate(raw))
	}
	for _, raw := range []string{"broken", `{"customer_currency":"invalid"}`} {
		require.Equal(t, "USD", parseModelPlazaCurrency(raw).CustomerCurrency)
	}
}
