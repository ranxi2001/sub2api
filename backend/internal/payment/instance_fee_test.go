//go:build unit

package payment

import (
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestPaymentInstanceFeePrecedence(t *testing.T) {
	instanceRate := 2.5
	inst := &dbent.PaymentProviderInstance{FeeRate: &instanceRate, MethodFeeRates: map[string]float64{TypeAlipay: 0}}
	rate, source := ResolveInstanceFee(inst, TypeAlipay, 5)
	require.Equal(t, 0.0, rate)
	require.Equal(t, "method", source)
	rate, source = ResolveInstanceFee(inst, TypeAlipayDirect, 5)
	require.Equal(t, 0.0, rate)
	require.Equal(t, "method", source)
	rate, source = ResolveInstanceFee(inst, TypeWxpay, 5)
	require.Equal(t, 2.5, rate)
	require.Equal(t, "provider", source)
	inst.FeeRate = nil
	rate, source = ResolveInstanceFee(inst, TypeWxpay, 5)
	require.Equal(t, 5.0, rate)
	require.Equal(t, "global", source)
}

func TestPaymentInstanceFeeNormalizesPersistedFloatNoise(t *testing.T) {
	inst := &dbent.PaymentProviderInstance{
		ProviderKey:    TypeEasyPay,
		SupportedTypes: TypeAlipay,
		MethodFeeRates: map[string]float64{TypeAlipay: 0.30000000000000004},
	}
	rate, source := ResolveInstanceFee(inst, TypeAlipay, 2)
	require.Equal(t, 0.3, rate)
	require.Equal(t, "method", source)
	require.Equal(t, "1003.00", CalculatePayAmount(1000, rate))
}

func TestPaymentInstanceFeeRequiresStripeSubtypeWhenRatesDiffer(t *testing.T) {
	require.True(t, StripeInstanceSupportsSubtype("", TypeCard))
	require.False(t, StripeInstanceSupportsSubtype("unknown", TypeCard))
	require.False(t, StripeInstanceSupportsSubtype("card", TypeAlipay))
	inst := &dbent.PaymentProviderInstance{ProviderKey: TypeStripe, SupportedTypes: "card,alipay", MethodFeeRates: map[string]float64{TypeAlipay: 0}}
	_, _, err := ResolveOrderInstanceFee(inst, TypeStripe, "", 3)
	require.Error(t, err)
	rate, _, err := ResolveOrderInstanceFee(inst, TypeStripe, TypeAlipay, 3)
	require.NoError(t, err)
	require.Zero(t, rate)
	rate, _, err = ResolveOrderInstanceFee(inst, TypeStripe, TypeCard, 3)
	require.NoError(t, err)
	require.Equal(t, 3.0, rate)
	// Disabled method overrides cannot affect an otherwise uniform legacy intent.
	inst.SupportedTypes = "card"
	rate, _, err = ResolveOrderInstanceFee(inst, TypeStripe, "", 3)
	require.NoError(t, err)
	require.Equal(t, 3.0, rate)
}

func TestPaymentInstanceFeePreservesCardAndLinkOverrides(t *testing.T) {
	base := 2.0
	inst := &dbent.PaymentProviderInstance{ProviderKey: TypeStripe, SupportedTypes: "card,link", FeeRate: &base, MethodFeeRates: map[string]float64{TypeCard: 0, TypeLink: 3}}
	for _, tc := range []struct {
		method string
		rate   float64
	}{{TypeCard, 0}, {TypeLink, 3}} {
		rate, source, err := ResolveOrderInstanceFee(inst, TypeStripe, tc.method, 9)
		require.NoError(t, err)
		require.Equal(t, tc.rate, rate)
		require.Equal(t, "method", source)
	}
	_, _, err := ResolveOrderInstanceFee(inst, TypeStripe, "", 9)
	require.Error(t, err)
}

func TestFilterByLimitsUsesEachCandidateQuotedAmount(t *testing.T) {
	free := &dbent.PaymentProviderInstance{ID: 1, ProviderKey: TypeEasyPay, Limits: `{"alipay":{"singleMax":100}}`}
	fee := &dbent.PaymentProviderInstance{ID: 2, ProviderKey: TypeEasyPay, Limits: `{"alipay":{"singleMax":100}}`}
	candidates := []instanceCandidate{
		{inst: free, quote: &InstanceQuote{PayAmount: 100}},
		{inst: fee, quote: &InstanceQuote{PayAmount: 103}},
	}
	filtered := filterByLimits(candidates, TypeAlipay, 103)
	require.Len(t, filtered, 1)
	require.Equal(t, free.ID, filtered[0].inst.ID)
	free.Limits, fee.Limits = `{"alipay":{"dailyLimit":200}}`, `{"alipay":{"dailyLimit":200}}`
	candidates[0].dailyUsed, candidates[1].dailyUsed = 100, 100
	filtered = filterByLimits(candidates, TypeAlipay, 100)
	require.Len(t, filtered, 1)
	require.Equal(t, free.ID, filtered[0].inst.ID)
	free.Limits, fee.Limits = `{"alipay":{"singleMin":102}}`, `{"alipay":{"singleMin":102}}`
	filtered = filterByLimits(candidates, TypeAlipay, 100)
	require.Len(t, filtered, 1)
	require.Equal(t, fee.ID, filtered[0].inst.ID)
}
