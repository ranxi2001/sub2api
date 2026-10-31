//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestLotteryRefundAccountingFailurePreservesPendingState(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createCompletedRefundableOrderForTest(t, ctx, client, "lottery-recovery")
	client.User.Intercept(dbent.InterceptFunc(func(next dbent.Querier) dbent.Querier {
		return dbent.QuerierFunc(func(context.Context, dbent.Query) (dbent.Value, error) {
			return nil, errors.New("injected lottery accounting failure")
		})
	}))
	svc := &PaymentService{entClient: client, lottery: &LotteryService{client: client}}
	plan := &RefundPlan{OrderID: order.ID, Order: order, RefundAmount: order.Amount, BalanceToDeduct: order.Amount, DeductionType: payment.DeductionTypeBalance}
	result, err := svc.finishRefund(ctx, plan, &payment.RefundResponse{Status: payment.ProviderStatusSuccess, RefundID: "local-qa-refund"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Warning, "pending reconciliation")
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRefundPending, reloaded.Status)
	detail, err := svc.latestRefundPendingDetail(ctx, order.ID)
	require.NoError(t, err)
	require.True(t, detail.DeductionHeld)
	require.Equal(t, order.Amount, detail.BalanceDeducted)
	require.Equal(t, "local-qa-refund", detail.RefundID)
}
