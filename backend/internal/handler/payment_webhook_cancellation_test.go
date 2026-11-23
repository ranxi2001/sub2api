//go:build unit

package handler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/ent/redeemcode"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type cancellationWebhookProvider struct {
	webhookHandlerProviderStub
	t          *testing.T
	confirmErr error
	confirmed  int
}

func (p *cancellationWebhookProvider) VerifyNotification(ctx context.Context, raw string, headers map[string]string) (*payment.PaymentNotification, error) {
	require.NoError(p.t, ctx.Err(), "verification remains request bound")
	if headers["x-fixture-signature"] != "accepted-fixture" {
		return nil, errors.New("fixture signature rejected")
	}
	return p.webhookHandlerProviderStub.VerifyNotification(ctx, raw, headers)
}

func (p *cancellationWebhookProvider) ConfirmNotification(ctx context.Context, _ *payment.PaymentNotification) error {
	require.NoError(p.t, ctx.Err(), "upstream confirmation remains request bound")
	p.confirmed++
	return p.confirmErr
}

func TestPaymentWebhookCancellationCreditsOnceAfterVerification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []string{"disconnect", "invalid_signature", "upstream_rejected", "amount_mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			db, err := sql.Open("sqlite", "file:webhook_cancellation_"+scenario+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
			t.Cleanup(func() { _ = client.Close() })
			user, err := client.User.Create().SetEmail("webhook@example.com").SetPasswordHash("fixture").Save(ctx)
			require.NoError(t, err)
			order, err := client.PaymentOrder.Create().SetUserID(user.ID).SetUserEmail(user.Email).SetUserName("fixture-user").
				SetAmount(10).SetPayAmount(10).SetRechargeCode("WEBHOOK-CANCELLATION").SetOutTradeNo("fixture-order").
				SetPaymentType(payment.TypeAlipay).SetPaymentTradeNo("").SetOrderType(payment.OrderTypeBalance).SetStatus(service.OrderStatusPending).
				SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("merchant.example.com").Save(ctx)
			require.NoError(t, err)
			prov := &cancellationWebhookProvider{t: t, webhookHandlerProviderStub: webhookHandlerProviderStub{
				key: payment.TypeAlipay, notification: &payment.PaymentNotification{OrderID: order.OutTradeNo,
					TradeNo: "fixture-trade", Amount: 10, Status: payment.NotificationStatusSuccess},
			}}
			if scenario == "upstream_rejected" {
				prov.confirmErr = errors.New("fixture upstream rejected")
			}
			if scenario == "amount_mismatch" {
				prov.notification.Amount = 11
			}
			registry := payment.NewRegistry()
			registry.Register(prov)
			users := repository.NewUserRepository(client, db)
			redeems := service.NewRedeemService(repository.NewRedeemCodeRepository(client), users, nil, nil, nil, client, nil, nil)
			payments := service.NewPaymentService(client, registry, nil, redeems, nil, nil, users, nil, nil)
			h := NewPaymentWebhookHandler(payments, registry)
			requestCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			writes := 0
			client.User.Use(func(next dbent.Mutator) dbent.Mutator {
				return dbent.MutateFunc(func(execCtx context.Context, m dbent.Mutation) (dbent.Value, error) {
					writes++
					cancel() // callback disconnects while the real balance transaction is open
					require.NoError(t, execCtx.Err())
					_, bounded := execCtx.Deadline()
					require.True(t, bounded)
					return next.Mutate(execCtx, m)
				})
			})
			notify := func(callCtx context.Context) *httptest.ResponseRecorder {
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/payment/webhook/alipay",
					strings.NewReader("out_trade_no=fixture-order")).WithContext(callCtx)
				c.Request.Header.Set("X-Fixture-Signature", "accepted-fixture")
				if scenario == "invalid_signature" {
					c.Request.Header.Del("X-Fixture-Signature")
				}
				h.AlipayNotify(c)
				return recorder
			}
			response := notify(requestCtx)
			if scenario != "disconnect" {
				require.NotEqual(t, http.StatusOK, response.Code)
				require.Zero(t, writes, "rejected callbacks must never reach balance mutation")
				current, err := client.PaymentOrder.Get(ctx, order.ID)
				require.NoError(t, err)
				require.Equal(t, service.OrderStatusPending, current.Status)
				return
			}
			require.Equal(t, http.StatusOK, response.Code)
			require.Equal(t, "success", response.Body.String())
			require.ErrorIs(t, requestCtx.Err(), context.Canceled)
			require.Equal(t, http.StatusOK, notify(ctx).Code)
			require.Equal(t, 2, prov.confirmed)
			require.Equal(t, 1, writes)
			credited, err := client.User.Get(ctx, user.ID)
			require.NoError(t, err)
			require.Equal(t, 10.0, credited.Balance)
			code, err := client.RedeemCode.Query().Where(redeemcode.CodeEQ(order.RechargeCode)).Only(ctx)
			require.NoError(t, err)
			require.Equal(t, service.StatusUsed, code.Status)
			require.NotNil(t, code.UsedBy)
			require.Equal(t, user.ID, *code.UsedBy)
			current, err := client.PaymentOrder.Get(ctx, order.ID)
			require.NoError(t, err)
			require.Equal(t, service.OrderStatusCompleted, current.Status)
		})
	}
}
