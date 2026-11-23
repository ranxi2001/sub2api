//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPaymentCancellationExecutionDeadlineIsNotRenewed(t *testing.T) {
	svc, order, _ := newPaymentCancellationFixture(t, OrderStatusPaid)
	ctx, cancel := paymentFulfillmentContext(context.Background())
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.Less(t, time.Until(deadline), paymentFulfillmentLeaseDuration-paymentFailureFinalizationTimeout)
	cancel() // simulate exhaustion of the independent execution budget
	nested, cancelNested := paymentFulfillmentContext(ctx)
	defer cancelNested()
	nestedDeadline, ok := nested.Deadline()
	require.True(t, ok)
	require.Equal(t, deadline, nestedDeadline)
	require.ErrorIs(t, nested.Err(), context.Canceled)
	require.Error(t, svc.ExecuteBalanceFulfillment(nested, order.ID), "nested fulfillment must not restart expired execution")
	current, err := svc.entClient.PaymentOrder.Get(context.Background(), order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPaid, current.Status)
	require.Zero(t, paymentCancellationAuditCount(t, svc, order.ID, "RECHARGE_SUCCESS"))
}
