package payment

import (
	"context"
	"fmt"
	"math"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
)

// InstanceQuote is calculated before routing so limits and the gateway use the
// same amount. It is carried with the chosen instance rather than recalculated.
type InstanceQuote struct {
	FeeRate        float64
	FeeSource      string
	PaymentSubtype string
	PayAmount      float64
	PayAmountText  string
}

type instanceQuoteContext struct {
	subtype string
	quote   func(*dbent.PaymentProviderInstance) (*InstanceQuote, error)
}

const instanceQuoteContextKey contextKey = "payment.instance_quote"

// NormalizeFeeRate removes binary floating-point noise from persisted provider
// rates before they reach currency rounding.
func NormalizeFeeRate(rate float64) float64 {
	normalized := math.Round(rate*100) / 100
	if normalized == 0 {
		return 0
	}
	return normalized
}

func WithInstanceQuote(ctx context.Context, subtype string, quote func(*dbent.PaymentProviderInstance) (*InstanceQuote, error)) context.Context {
	return context.WithValue(ctx, instanceQuoteContextKey, instanceQuoteContext{subtype: subtype, quote: quote})
}

func quoteContext(ctx context.Context) instanceQuoteContext {
	value, _ := ctx.Value(instanceQuoteContextKey).(instanceQuoteContext)
	return value
}

// StripeInstanceMethods returns only the methods this application supports.
// Empty legacy configurations retain the gateway's card-only default.
func StripeInstanceMethods(supported string) []string {
	methods := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, method := range strings.Split(supported, ",") {
		method = strings.TrimSpace(method)
		if IsStripePaymentSubtype(method) && !seen[method] {
			methods = append(methods, method)
			seen[method] = true
		}
	}
	if len(methods) == 0 && strings.TrimSpace(supported) == "" {
		return []string{TypeCard}
	}
	return methods
}

func IsStripePaymentSubtype(method string) bool {
	switch method {
	case TypeCard, TypeLink, TypeAlipay, TypeWxpay:
		return true
	default:
		return false
	}
}

func StripeInstanceSupportsSubtype(supported, subtype string) bool {
	for _, method := range StripeInstanceMethods(supported) {
		if method == subtype {
			return true
		}
	}
	return false
}

func ResolveInstanceFee(inst *dbent.PaymentProviderInstance, method string, global float64) (float64, string) {
	// Only legacy official-method aliases share fee keys. Card and Link must
	// retain their distinct keys even though checkout groups them under Stripe.
	method = strings.TrimSpace(method)
	switch method {
	case TypeAlipayDirect:
		method = TypeAlipay
	case TypeWxpayDirect:
		method = TypeWxpay
	}
	if rate, exists := inst.MethodFeeRates[method]; exists {
		return NormalizeFeeRate(rate), "method"
	}
	if inst.FeeRate != nil {
		return NormalizeFeeRate(*inst.FeeRate), "provider"
	}
	return global, "global"
}

// ResolveOrderInstanceFee prevents an unrestricted Stripe intent from charging
// one method's fee while accepting another method with a different fee.
func ResolveOrderInstanceFee(inst *dbent.PaymentProviderInstance, method, subtype string, global float64) (float64, string, error) {
	if inst.ProviderKey != TypeStripe {
		rate, source := ResolveInstanceFee(inst, method, global)
		return rate, source, nil
	}
	methods := StripeInstanceMethods(inst.SupportedTypes)
	if subtype != "" {
		methods = []string{subtype}
	} else if method != TypeStripe && IsStripePaymentSubtype(method) {
		methods = []string{method}
	}
	if len(methods) == 0 {
		return 0, "", fmt.Errorf("stripe instance has no supported payment methods")
	}
	rate, source := ResolveInstanceFee(inst, methods[0], global)
	for _, candidate := range methods[1:] {
		next, _ := ResolveInstanceFee(inst, candidate, global)
		if next != rate {
			return 0, "", fmt.Errorf("select a Stripe payment method before creating an order")
		}
	}
	return rate, source, nil
}
