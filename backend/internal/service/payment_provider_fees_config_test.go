//go:build unit

package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestProviderFeeRateValidation(t *testing.T) {
	for _, value := range []float64{0, 0.01, 1.1, 1.13, 3.33, 99.99, 100} {
		require.NoError(t, validateProviderFeeRate(&value))
	}
	require.NoError(t, validateProviderFeeRate(nil))
	floatNoise := 0.30000000000000004
	require.NoError(t, validateProviderFeeRate(&floatNoise))
	require.Equal(t, 0.3, floatNoise)
	for _, value := range []float64{-1, 100.01, 1.001, math.NaN(), math.Inf(1)} {
		require.Error(t, validateProviderFeeRate(&value))
	}
}

func TestProviderFeePatchJSONDistinguishesInheritanceAndZero(t *testing.T) {
	var absent, inherit, free UpdateProviderInstanceRequest
	require.NoError(t, json.Unmarshal([]byte(`{}`), &absent))
	require.False(t, absent.FeeRate.Set)
	require.Nil(t, absent.MethodFeeRates)
	require.NoError(t, json.Unmarshal([]byte(`{"fee_rate":null,"method_fee_rates":{}}`), &inherit))
	require.True(t, inherit.FeeRate.Set)
	require.Nil(t, inherit.FeeRate.Value)
	require.NotNil(t, inherit.MethodFeeRates)
	require.Empty(t, inherit.MethodFeeRates)
	require.NoError(t, json.Unmarshal([]byte(`{"fee_rate":0,"method_fee_rates":{"alipay":0}}`), &free))
	require.True(t, free.FeeRate.Set)
	require.NotNil(t, free.FeeRate.Value)
	require.Zero(t, *free.FeeRate.Value)
	require.Equal(t, MethodFeeRatesPatch{"alipay": 0}, free.MethodFeeRates)
	for _, payload := range []string{
		`{"fee_rate":"0"}`, `{"fee_rate":false}`, `{"method_fee_rates":null}`,
		`{"method_fee_rates":{"alipay":null}}`, `{"method_fee_rates":{"alipay":"1"}}`,
	} {
		var req UpdateProviderInstanceRequest
		require.Error(t, json.Unmarshal([]byte(payload), &req), payload)
	}
}

func TestProviderMethodFeeRatesOnlyAcceptSupportedMethods(t *testing.T) {
	tests := []struct {
		provider, supported, method string
		valid                       bool
	}{
		{"easypay", "alipay,wxpay,custom", "custom", true},
		{"easypay", "alipay,wxpay", "card", false},
		{"stripe", "card,link,alipay,wxpay", "wxpay", true},
		{"stripe", "card", "link", false},
		{"stripe", "", "card", true},
		{"stripe", "stripe", "stripe", false},
		{"airwallex", "airwallex", "airwallex", true},
		{"airwallex", "airwallex", "card", false},
		{"alipay", "alipay_direct", "alipay", true},
		{"wxpay", "", "wxpay", true},
		{"bepusdt", "usdt.trc20", "usdt.trc20", true},
		{"bepusdt", "usdt.trc20", "usdt.erc20", false},
	}
	for _, test := range tests {
		err := validateProviderMethodFeeRates(test.provider, test.supported, map[string]float64{test.method: 0})
		if test.valid {
			require.NoError(t, err, "%+v", test)
		} else {
			require.Error(t, err, "%+v", test)
		}
	}
}

func TestProviderFeeCRUDPreservesDefaultsAndPatchSemantics(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}
	// Existing creation paths omit both new fields and inherit the site rate.
	legacy, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeEasyPay).
		SetName("Legacy").SetConfig(`{}`).SetSupportedTypes("alipay,wxpay").SetEnabled(false).Save(ctx)
	require.NoError(t, err)
	require.Nil(t, legacy.FeeRate)
	require.Empty(t, legacy.MethodFeeRates)
	legacy, err = client.PaymentProviderInstance.Get(ctx, legacy.ID)
	require.NoError(t, err)
	require.Nil(t, legacy.FeeRate)
	require.NotNil(t, legacy.MethodFeeRates)

	rate := 2.5
	created, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey: payment.TypeEasyPay, Name: "Fees", SupportedTypes: []string{"alipay", "wxpay"},
		FeeRate: &rate, MethodFeeRates: MethodFeeRatesPatch{"alipay": 0.30000000000000004, "wxpay": 1.1},
	})
	require.NoError(t, err)
	require.Equal(t, &rate, created.FeeRate)
	require.Equal(t, map[string]float64{"alipay": 0.3, "wxpay": 1.1}, created.MethodFeeRates)
	require.Equal(t, "1003.00", payment.CalculatePayAmount(1000, created.MethodFeeRates["alipay"]))

	apply := func(payload string) {
		t.Helper()
		var req UpdateProviderInstanceRequest
		require.NoError(t, json.Unmarshal([]byte(payload), &req))
		_, err = svc.UpdateProviderInstance(ctx, created.ID, req)
		require.NoError(t, err)
		created, err = client.PaymentProviderInstance.Get(ctx, created.ID)
		require.NoError(t, err)
	}
	apply(`{"name":"Renamed"}`)
	require.Equal(t, &rate, created.FeeRate)
	require.Equal(t, map[string]float64{"alipay": 0.3, "wxpay": 1.1}, created.MethodFeeRates)
	apply(`{"fee_rate":0,"method_fee_rates":{"wxpay":2}}`)
	require.NotNil(t, created.FeeRate)
	require.Zero(t, *created.FeeRate)
	require.Equal(t, map[string]float64{"wxpay": 2}, created.MethodFeeRates)
	apply(`{"fee_rate":null,"method_fee_rates":{}}`)
	require.Nil(t, created.FeeRate)
	require.Empty(t, created.MethodFeeRates)

	listed, err := svc.ListProviderInstancesWithConfig(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	for _, inst := range listed {
		require.Nil(t, inst.FeeRate)
		require.NotNil(t, inst.MethodFeeRates)
	}
}

func TestInvalidProviderFeeUpdateDoesNotMutateInstance(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}
	created, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey: payment.TypeEasyPay, Name: "Original", SupportedTypes: []string{"alipay"},
	})
	require.NoError(t, err)
	for _, payload := range []string{
		`{"name":"Changed","fee_rate":-1}`,
		`{"name":"Changed","method_fee_rates":{"alipay":100.01}}`,
		`{"name":"Changed","method_fee_rates":{"alipay":1.001}}`,
		`{"name":"Changed","method_fee_rates":{"card":1}}`,
	} {
		var req UpdateProviderInstanceRequest
		require.NoError(t, json.Unmarshal([]byte(payload), &req))
		_, err = svc.UpdateProviderInstance(ctx, created.ID, req)
		require.Error(t, err)
		saved, err := client.PaymentProviderInstance.Get(ctx, created.ID)
		require.NoError(t, err)
		require.Equal(t, "Original", saved.Name)
		require.Nil(t, saved.FeeRate)
		require.Empty(t, saved.MethodFeeRates)
	}
}

func TestProviderFeeUpdatePreservesUnchangedDisabledMethodOverrides(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}
	created, err := svc.CreateProviderInstance(ctx, CreateProviderInstanceRequest{
		ProviderKey: payment.TypeEasyPay, Name: "Dormant fees", SupportedTypes: []string{"alipay", "wxpay"},
		MethodFeeRates: MethodFeeRatesPatch{"alipay": 0, "wxpay": 2},
	})
	require.NoError(t, err)
	updated, err := svc.UpdateProviderInstance(ctx, created.ID, UpdateProviderInstanceRequest{
		SupportedTypes: []string{"alipay"},
		MethodFeeRates: MethodFeeRatesPatch{"alipay": 1, "wxpay": 2},
	})
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"alipay": 1, "wxpay": 2}, updated.MethodFeeRates)
	_, err = svc.UpdateProviderInstance(ctx, created.ID, UpdateProviderInstanceRequest{
		MethodFeeRates: MethodFeeRatesPatch{"alipay": 1, "wxpay": 2},
	})
	require.NoError(t, err)
	for _, overrides := range []MethodFeeRatesPatch{
		{"alipay": 1, "wxpay": 3},
		{"alipay": 1, "wxpay": 2, "card": 0},
	} {
		_, err = svc.UpdateProviderInstance(ctx, created.ID, UpdateProviderInstanceRequest{MethodFeeRates: overrides})
		require.Error(t, err)
	}
	updated, err = svc.UpdateProviderInstance(ctx, created.ID, UpdateProviderInstanceRequest{
		SupportedTypes: []string{"alipay", "wxpay"},
		MethodFeeRates: MethodFeeRatesPatch{"alipay": 1, "wxpay": 3},
	})
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"alipay": 1, "wxpay": 3}, updated.MethodFeeRates)
}
