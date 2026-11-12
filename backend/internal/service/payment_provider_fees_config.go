package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// NullableFeeRateField preserves PATCH's absent / null / numeric distinction.
type NullableFeeRateField struct {
	Set   bool
	Value *float64
}

func (f *NullableFeeRateField) UnmarshalJSON(data []byte) error {
	f.Set = true
	f.Value = nil
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	var value float64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	f.Value = &value
	return nil
}

// MethodFeeRatesPatch replaces the whole override map. Use {} to clear it;
// null (including a null entry) is rejected rather than interpreted as zero.
type MethodFeeRatesPatch map[string]float64

func (f *MethodFeeRatesPatch) UnmarshalJSON(data []byte) error {
	var values map[string]*float64
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	if values == nil {
		return fmt.Errorf("method_fee_rates must be an object; use {} to clear overrides")
	}
	next := make(MethodFeeRatesPatch, len(values))
	for method, value := range values {
		if value == nil {
			return fmt.Errorf("method_fee_rates.%s must be a number; omit the key to inherit", method)
		}
		next[method] = *value
	}
	*f = next
	return nil
}

func validateProviderFeeRate(value *float64) error {
	if value == nil {
		return nil
	}
	v := *value
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
		return infraerrors.BadRequest("INVALID_PROVIDER_FEE_RATE", "provider fee rate must be between 0 and 100")
	}
	// Allow binary floating-point representation noise (e.g. 1.1 * 100).
	if math.Abs(v*100-math.Round(v*100)) > 1e-9 {
		return infraerrors.BadRequest("INVALID_PROVIDER_FEE_RATE", "provider fee rate allows at most 2 decimal places")
	}
	*value = payment.NormalizeFeeRate(v)
	return nil
}

func validateProviderMethodFeeRates(providerKey, supportedTypes string, rates map[string]float64) error {
	allowed := make(map[string]bool)
	for _, method := range splitTypes(supportedTypes) {
		allowed[strings.TrimSpace(method)] = true
	}
	switch providerKey {
	case payment.TypeStripe:
		if len(allowed) == 0 {
			allowed[payment.TypeCard] = true
		}
		for method := range allowed {
			if method != payment.TypeCard && method != payment.TypeLink && method != payment.TypeAlipay && method != payment.TypeWxpay {
				delete(allowed, method)
			}
		}
	case payment.TypeAirwallex:
		allowed = map[string]bool{payment.TypeAirwallex: true}
	case payment.TypeAlipay:
		allowed = map[string]bool{payment.TypeAlipay: true}
	case payment.TypeWxpay:
		allowed = map[string]bool{payment.TypeWxpay: true}
	}
	for method, rate := range rates {
		if !allowed[method] {
			return infraerrors.BadRequest("INVALID_PROVIDER_FEE_METHOD", "fee override requires a supported payment method").WithMetadata(map[string]string{"payment_type": method})
		}
		if err := validateProviderFeeRate(&rate); err != nil {
			return err
		}
		rates[method] = rate
	}
	return nil
}

// Full-map saves retain dormant overrides when a method is disabled. Only
// previously saved, unchanged values may survive outside the supported set;
// adding or editing such a value requires enabling its method first.
func validateProviderMethodFeeRatesUpdate(providerKey, supportedTypes string, rates, previous map[string]float64) error {
	changed := make(map[string]float64)
	for method, rate := range rates {
		if err := validateProviderFeeRate(&rate); err != nil {
			return err
		}
		rates[method] = rate
		old, exists := previous[method]
		if !exists || payment.NormalizeFeeRate(old) != rate {
			changed[method] = rate
		}
	}
	return validateProviderMethodFeeRates(providerKey, supportedTypes, changed)
}
