package service

import (
	"context"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/internal/payment"
)

// This is a reconciliation deadline, not an extension of the payment URL's
// lifetime. It is anchored to the original expires_at, never to updated_at or
// a callback/retry timestamp. Older payments require manual reconciliation.
const (
	easyPayOrderRecoveryWindow  = 24 * time.Hour
	easyPayRecoveryCheckTimeout = 15 * time.Second
)

type easyPayBackgroundRecoveryKey struct{}

func easyPayRecoveryProviderPredicate() predicate.PaymentOrder {
	return paymentorder.Or(
		paymentorder.ProviderKeyEQ(payment.TypeEasyPay),
		paymentorder.ProviderKeyHasPrefix(payment.TypeEasyPay+"_"),
		paymentorder.PaymentTypeEQ(payment.TypeEasyPay),
		paymentorder.PaymentTypeHasPrefix(payment.TypeEasyPay+"_"),
	)
}

// ReconcileRecoverableEasyPayOrders checks recently expired EasyPay orders.
// Expiry closes checkout even if QueryOrder is temporarily unavailable. Keep
// checking those EasyPay orders in a separate bounded batch, using their frozen
// provider instance and the ordinary amount/identity/idempotent fulfillment
// checks. Cancelled, refunded and already fulfilled orders are never selected.
func (s *PaymentService) ReconcileRecoverableEasyPayOrders(ctx context.Context) (int, error) {
	now := time.Now()
	ctx, cancel := context.WithTimeout(ctx, easyPayRecoveryCheckTimeout)
	defer cancel()
	ctx = context.WithValue(ctx, easyPayBackgroundRecoveryKey{}, true)
	s.easyPayRecoveryMu.Lock()
	defer s.easyPayRecoveryMu.Unlock()
	query := func(after int64) ([]*dbent.PaymentOrder, error) {
		return s.entClient.PaymentOrder.Query().Where(
			easyPayRecoveryProviderPredicate(),
			paymentorder.StatusIn(OrderStatusPending, OrderStatusExpired),
			paymentorder.ExpiresAtLTE(now),
			paymentorder.ExpiresAtGTE(now.Add(-easyPayOrderRecoveryWindow)),
			paymentorder.IDGT(after),
		).Order(dbent.Asc(paymentorder.FieldID)).Limit(pendingPaymentReconcileLimit).All(ctx)
	}
	orders, err := query(s.easyPayRecoveryAfter)
	if err == nil && len(orders) == 0 && s.easyPayRecoveryAfter != 0 {
		s.easyPayRecoveryAfter = 0
		orders, err = query(0)
	}
	if err != nil {
		return 0, fmt.Errorf("query recoverable EasyPay orders: %w", err)
	}
	recovered := 0
	for _, order := range orders {
		if err := ctx.Err(); err != nil {
			return recovered, err
		}
		// Advance even on an upstream outage. No status/timestamp changes are
		// needed to keep one failing order from starving later batches.
		s.easyPayRecoveryAfter = order.ID
		prov, err := s.getOrderProvider(ctx, order)
		if err != nil || prov == nil || payment.GetBasePaymentType(prov.ProviderKey()) != payment.TypeEasyPay {
			continue
		}
		if s.reconcilePaid(ctx, order) == checkPaidResultAlreadyPaid {
			recovered++
		}
	}
	return recovered, nil
}
