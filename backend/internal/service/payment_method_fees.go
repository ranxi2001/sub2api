package service

import (
	"context"
	"errors"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func (s *PaymentConfigService) globalPaymentFeeRate(ctx context.Context) (float64, error) {
	if s.settingRepo == nil {
		return 0, nil
	}
	value, err := s.settingRepo.GetValue(ctx, SettingRechargeFeeRate)
	if errors.Is(err, ErrSettingNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return pcParseFloat(value, 0), nil
}

func pcApplyMethodFees(limits *MethodLimits, instances []*dbent.PaymentProviderInstance, global float64) {
	var all []float64
	subtypes := map[string][]float64{}
	for _, inst := range instances {
		methods := []string{limits.PaymentType}
		if inst.ProviderKey == payment.TypeStripe && limits.PaymentType == payment.TypeStripe {
			methods = payment.StripeInstanceMethods(inst.SupportedTypes)
		}
		for _, method := range methods {
			rate, _ := payment.ResolveInstanceFee(inst, method, global)
			all = append(all, rate)
			if limits.PaymentType == payment.TypeStripe {
				subtypes[method] = append(subtypes[method], rate)
			}
		}
	}
	rates := aggregateMethodFeeRates(all)
	limits.FeeRate, limits.FeeRateMin, limits.FeeRateMax = rates.FeeRate, rates.FeeRateMin, rates.FeeRateMax
	if len(subtypes) > 0 {
		limits.SubMethods = make(map[string]MethodFeeRates, len(subtypes))
		for method, values := range subtypes {
			limits.SubMethods[method] = aggregateMethodFeeRates(values)
		}
	}
}

func aggregateMethodFeeRates(values []float64) MethodFeeRates {
	if len(values) == 0 {
		return MethodFeeRates{}
	}
	minRate, maxRate := values[0], values[0]
	for _, rate := range values[1:] {
		if rate < minRate {
			minRate = rate
		}
		if rate > maxRate {
			maxRate = rate
		}
	}
	return MethodFeeRates{FeeRate: minRate, FeeRateMin: minRate, FeeRateMax: maxRate}
}

func (s *PaymentService) validateStripeFeeSelection(ctx context.Context, req CreateOrderRequest) error {
	if req.PaymentType != payment.TypeStripe || req.PaymentSubtype != "" {
		return nil
	}
	limits, err := s.configService.GetAvailableMethodLimits(ctx)
	if err != nil {
		return err
	}
	stripe := limits.Methods[payment.TypeStripe]
	if stripe.FeeRateMin != stripe.FeeRateMax {
		return infraerrors.BadRequest("PAYMENT_SUBTYPE_REQUIRED", "select a Stripe payment method before creating an order")
	}
	return nil
}

// PaymentOrderSubtype is read from the immutable order snapshot, including when
// an existing payment is resumed after provider configuration changes.
func PaymentOrderSubtype(order *dbent.PaymentOrder) string {
	if order == nil {
		return ""
	}
	return psSnapshotStringValue(order.ProviderSnapshot["payment_subtype"])
}
