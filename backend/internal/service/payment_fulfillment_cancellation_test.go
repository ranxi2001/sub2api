//go:build unit

package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

// These adapters use real Ent rows and the transaction carried by RedeemService.
// In-memory Use stubs cannot prove that a failed balance write rolls back a code.
type paymentCancellationRedeemRepo struct {
	RedeemCodeRepository
	client *dbent.Client
}

func (r *paymentCancellationRedeemRepo) Create(ctx context.Context, code *RedeemCode) error {
	row, err := r.client.RedeemCode.Create().SetCode(code.Code).SetType(code.Type).
		SetValue(code.Value).SetStatus(code.Status).Save(ctx)
	if err == nil {
		code.ID = row.ID
	}
	return err
}

func paymentCancellationCode(row *dbent.RedeemCode, err error) (*RedeemCode, error) {
	if dbent.IsNotFound(err) {
		return nil, ErrRedeemCodeNotFound
	}
	if err != nil {
		return nil, err
	}
	return &RedeemCode{ID: row.ID, Code: row.Code, Type: row.Type, Value: row.Value,
		Status: row.Status, UsedBy: row.UsedBy, UsedAt: row.UsedAt}, nil
}

func (r *paymentCancellationRedeemRepo) GetByCode(ctx context.Context, code string) (*RedeemCode, error) {
	return paymentCancellationCode(r.client.RedeemCode.Query().Where(redeemcode.CodeEQ(code)).Only(ctx))
}

func (r *paymentCancellationRedeemRepo) GetByID(ctx context.Context, id int64) (*RedeemCode, error) {
	return paymentCancellationCode(r.client.RedeemCode.Get(ctx, id))
}

func (r *paymentCancellationRedeemRepo) Use(ctx context.Context, id, userID int64) error {
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		return errors.New("fixture requires transactional redemption")
	}
	n, err := tx.RedeemCode.Update().Where(redeemcode.IDEQ(id), redeemcode.StatusEQ(StatusUnused)).
		SetStatus(StatusUsed).SetUsedBy(userID).SetUsedAt(time.Now()).Save(ctx)
	if err == nil && n != 1 {
		return ErrRedeemCodeUsed
	}
	return err
}

func newPaymentCancellationFixture(t *testing.T, status string) (*PaymentService, *dbent.PaymentOrder, *mockUserRepo) {
	t.Helper()
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, status, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetOrderType(payment.OrderTypeBalance).
		SetRechargeCode("CANCEL-FIXTURE").ClearPlanID().ClearSubscriptionGroupID().ClearSubscriptionDays().Save(ctx)
	require.NoError(t, err)
	users := &mockUserRepo{getByIDUser: &User{ID: order.UserID}}
	users.updateBalanceFn = func(ctx context.Context, id int64, amount float64) error {
		tx := dbent.TxFromContext(ctx)
		require.NotNil(t, tx)
		_, err := tx.User.UpdateOneID(id).AddBalance(amount).Save(ctx)
		return err
	}
	redeems := &paymentCancellationRedeemRepo{client: client}
	svc := &PaymentService{entClient: client, userRepo: users,
		redeemService: NewRedeemService(redeems, users, nil, nil, nil, client, nil, nil)}
	return svc, order, users
}

func paymentCancellationAuditCount(t *testing.T, svc *PaymentService, oid int64, action string) int {
	t.Helper()
	n, err := svc.entClient.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDEQ(strconv.FormatInt(oid, 10)), paymentauditlog.ActionEQ(action),
	).Count(context.Background())
	require.NoError(t, err)
	return n
}

func TestPaymentCancellationValidatedTransitionSurvivesDisconnect(t *testing.T) {
	svc, order, _ := newPaymentCancellationFixture(t, OrderStatusPending)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// toPaid is reached only after canonical provider/metadata/amount validation.
	require.NoError(t, svc.toPaid(ctx, order, "fixture-trade", order.PayAmount, payment.TypeAlipay))
	current, err := svc.entClient.PaymentOrder.Get(context.Background(), order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, current.Status)
	require.NotNil(t, current.PaidAt)
	require.Equal(t, 1, paymentCancellationAuditCount(t, svc, order.ID, "ORDER_PAID"))
}

func TestPaymentCancellationDuringBalanceWriteCreditsOnce(t *testing.T) {
	svc, order, users := newPaymentCancellationFixture(t, OrderStatusPending)
	type traceKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), traceKey{}, "fixture-trace"))
	defer cancel()
	write := users.updateBalanceFn
	writes := 0
	users.updateBalanceFn = func(execCtx context.Context, id int64, amount float64) error {
		writes++
		cancel()
		require.NoError(t, execCtx.Err(), "callback cancellation must not cancel financial execution")
		require.Equal(t, "fixture-trace", execCtx.Value(traceKey{}))
		deadline, ok := execCtx.Deadline()
		require.True(t, ok, "detached execution must have a deadline")
		require.WithinDuration(t, time.Now(), deadline, paymentFulfillmentLeaseDuration-time.Second)
		return write(execCtx, id, amount)
	}
	n := &payment.PaymentNotification{OrderID: order.OutTradeNo, TradeNo: "fixture-trade",
		Amount: order.PayAmount, Status: payment.NotificationStatusSuccess}
	require.NoError(t, svc.HandlePaymentNotification(ctx, n, payment.TypeAlipay))
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.NoError(t, svc.HandlePaymentNotification(context.Background(), n, payment.TypeAlipay))
	require.Equal(t, 1, writes)
	user, err := svc.entClient.User.Get(context.Background(), order.UserID)
	require.NoError(t, err)
	require.Equal(t, order.Amount, user.Balance)
	code, err := svc.redeemService.GetByCode(context.Background(), order.RechargeCode)
	require.NoError(t, err)
	require.Equal(t, StatusUsed, code.Status)
	require.Equal(t, 1, paymentCancellationAuditCount(t, svc, order.ID, "RECHARGE_SUCCESS"))
}

func TestPaymentCancellationFailedBalanceTransactionRollsBack(t *testing.T) {
	svc, order, users := newPaymentCancellationFixture(t, OrderStatusPaid)
	write := users.updateBalanceFn
	users.updateBalanceFn = func(ctx context.Context, id int64, amount float64) error {
		require.NoError(t, write(ctx, id, amount))
		return context.DeadlineExceeded // fail after both transactional mutations
	}
	err := svc.ExecuteBalanceFulfillment(context.Background(), order.ID)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	user, err := svc.entClient.User.Get(context.Background(), order.UserID)
	require.NoError(t, err)
	require.Zero(t, user.Balance)
	code, err := svc.redeemService.GetByCode(context.Background(), order.RechargeCode)
	require.NoError(t, err)
	require.Equal(t, StatusUnused, code.Status)
	require.Nil(t, code.UsedBy)
	require.Nil(t, code.UsedAt)
	current, err := svc.entClient.PaymentOrder.Get(context.Background(), order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusFailed, current.Status)
	require.NotNil(t, current.FailedAt)
	require.NotNil(t, current.FailedReason)
	require.Contains(t, *current.FailedReason, "context deadline exceeded")
	require.Equal(t, 1, paymentCancellationAuditCount(t, svc, order.ID, "FULFILLMENT_FAILED"))
}

func TestPaymentCancellationFailureFinalizationSurvivesCanceledContext(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(strconv.FormatBool(expired), func(t *testing.T) {
			svc, order, _ := newPaymentCancellationFixture(t, OrderStatusPaid)
			lease, err := svc.acquirePaymentFulfillmentLease(context.Background(), order)
			require.NoError(t, err)
			require.NotNil(t, lease)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if expired {
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
			}
			svc.markFailed(ctx, order.ID, lease, errors.New("fixture failure"))
			current, err := svc.entClient.PaymentOrder.Get(context.Background(), order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusFailed, current.Status)
			require.NotNil(t, current.FailedAt)
			require.NotNil(t, current.FailedReason)
			require.Equal(t, "fixture failure", *current.FailedReason)
			require.Equal(t, 1, paymentCancellationAuditCount(t, svc, order.ID, "FULFILLMENT_FAILED"))
		})
	}
}

func TestPaymentCancellationStaleCleanupCannotOverwriteNewLease(t *testing.T) {
	svc, order, _ := newPaymentCancellationFixture(t, OrderStatusPaid)
	ctx := context.Background()
	oldLease, err := svc.acquirePaymentFulfillmentLease(ctx, order)
	require.NoError(t, err)
	require.NotNil(t, oldLease)
	newVersion := oldLease.version.Add(time.Second)
	_, err = svc.entClient.PaymentOrder.UpdateOneID(order.ID).SetUpdatedAt(newVersion).Save(ctx)
	require.NoError(t, err)
	staleCtx, cancel := context.WithCancel(ctx)
	cancel()
	svc.markFailed(staleCtx, order.ID, oldLease, errors.New("old worker failure"))
	current, err := svc.entClient.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRecharging, current.Status)
	require.True(t, current.UpdatedAt.Equal(newVersion))
	require.Nil(t, current.FailedAt)
	require.Zero(t, paymentCancellationAuditCount(t, svc, order.ID, "FULFILLMENT_FAILED"))
	// Also prove independent cleanup actually works for the current owner.
	svc.markFailed(staleCtx, order.ID, &paymentFulfillmentLease{version: current.UpdatedAt}, errors.New("current failure"))
	current, err = svc.entClient.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusFailed, current.Status)
}

func TestPaymentCancellationFailureFinalizationIsBoundedAndRetryable(t *testing.T) {
	svc, order, _ := newPaymentCancellationFixture(t, OrderStatusPaid)
	lease, err := svc.acquirePaymentFulfillmentLease(context.Background(), order)
	require.NoError(t, err)
	require.NotNil(t, lease)
	failWrite := true
	svc.entClient.PaymentOrder.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok, "failure cleanup must have its own deadline")
			require.Positive(t, time.Until(deadline))
			require.LessOrEqual(t, time.Until(deadline), 10*time.Second)
			require.NoError(t, ctx.Err())
			if failWrite {
				return nil, errors.New("fixture cleanup database error")
			}
			return next.Mutate(ctx, m)
		})
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.markFailed(ctx, order.ID, lease, errors.New("fixture execution error"))
	current, err := svc.entClient.PaymentOrder.Get(context.Background(), order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusRecharging, current.Status)
	require.Zero(t, paymentCancellationAuditCount(t, svc, order.ID, "FULFILLMENT_FAILED"))
	failWrite = false
	svc.markFailed(ctx, order.ID, lease, errors.New("fixture execution error"))
	current, err = svc.entClient.PaymentOrder.Get(context.Background(), order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusFailed, current.Status)
	require.Equal(t, 1, paymentCancellationAuditCount(t, svc, order.ID, "FULFILLMENT_FAILED"))
}

func TestPaymentCancellationDirectFulfillmentSurvivesDisconnect(t *testing.T) {
	for _, orderType := range []string{payment.OrderTypeBalance, payment.OrderTypeSubscription} {
		t.Run(orderType, func(t *testing.T) {
			svc, order, _ := newPaymentCancellationFixture(t, OrderStatusPaid)
			if orderType == payment.OrderTypeSubscription {
				var err error
				order, err = svc.entClient.PaymentOrder.UpdateOneID(order.ID).SetOrderType(orderType).
					SetSubscriptionGroupID(7).SetSubscriptionDays(30).Save(context.Background())
				require.NoError(t, err)
				_, err = svc.entClient.PaymentAuditLog.Create().SetOrderID(strconv.FormatInt(order.ID, 10)).
					SetAction("SUBSCRIPTION_ASSIGNED").SetOperator("system").Save(context.Background())
				require.NoError(t, err)
				groups := &subscriptionGroupRepoStub{group: &Group{ID: 7, Status: payment.EntityStatusActive}}
				svc.groupRepo = groups
				svc.subscriptionSvc = NewSubscriptionService(groups, userSubRepoNoop{}, nil, nil, nil)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if orderType == payment.OrderTypeBalance {
				require.NoError(t, svc.ExecuteBalanceFulfillment(ctx, order.ID))
			} else {
				require.NoError(t, svc.ExecuteSubscriptionFulfillment(ctx, order.ID))
			}
			current, err := svc.entClient.PaymentOrder.Get(context.Background(), order.ID)
			require.NoError(t, err)
			require.Equal(t, OrderStatusCompleted, current.Status)
		})
	}
}

func TestPaymentCancellationCouponConfirmationSurvivesDisconnect(t *testing.T) {
	f := newRepairF2Fixture(t, true, payment.TypeAlipay)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, f.s.toPaid(ctx, f.o, "fixture-coupon-trade", f.o.PayAmount, payment.TypeAlipay))
	current, err := f.s.entClient.PaymentOrder.Get(context.Background(), f.o.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, current.Status)
	var couponStatus string
	require.NoError(t, f.db.QueryRow(`SELECT status FROM user_recharge_coupons WHERE id=$1`, f.couponID).Scan(&couponStatus))
	require.Equal(t, "used", couponStatus)
}
