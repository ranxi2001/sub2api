//go:build integration

package routes

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/coupon"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRepairR1CouponHoldRequiresAuthorizedExplicitApproval(t *testing.T) {
	dsn := os.Getenv("COUPON_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated COUPON_TEST_DSN required")
	}
	ctx := context.Background()
	root, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	name := fmt.Sprintf("repair_r1_%d", time.Now().UnixNano())
	_, err = root.Exec(`CREATE DATABASE ` + name)
	require.NoError(t, err)
	t.Cleanup(func() { _, err := root.Exec(`DROP DATABASE ` + name + ` WITH (FORCE)`); require.NoError(t, err) })
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	u.Path = "/" + name
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, repository.ApplyMigrations(ctx, db))
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	users := repository.NewUserRepository(client, db)
	redeems := repository.NewRedeemCodeRepository(client)
	redeemService := service.NewRedeemService(redeems, users, nil, nil, nil, client, nil, nil)
	payments := service.NewPaymentService(client, payment.NewRegistry(), nil, redeemService, nil, nil, users, nil, nil)
	cfg := &config.Config{}
	cfg.JWT.Secret = "synthetic-r1-signing-key-not-production"
	cfg.JWT.ExpireHour = 1
	auth := service.NewAuthService(client, users, redeems, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	userService := service.NewUserService(users, nil, nil, nil)
	tokens := map[string]string{}
	var beneficiary int64
	for _, role := range []string{service.RoleAdmin, service.RoleUser, service.RoleObserver} {
		created, err := client.User.Create().SetEmail(role + "@example.invalid").SetPasswordHash("synthetic").SetRole(role).Save(ctx)
		require.NoError(t, err)
		account, err := users.GetByID(ctx, created.ID)
		require.NoError(t, err)
		tokens[role], err = auth.GenerateToken(ctx, account)
		require.NoError(t, err)
		if role == service.RoleUser {
			beneficiary = created.ID
		}
	}
	router := gin.New()
	RegisterPaymentRoutes(router.Group("/api/v1"), &handler.PaymentHandler{}, &handler.PaymentWebhookHandler{}, admin.NewPaymentHandler(payments, nil), middleware.JWTAuthMiddleware(func(c *gin.Context) { c.AbortWithStatus(401) }), middleware.NewAdminAuthMiddleware(auth, userService, nil, nil), middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }), nil, nil, nil)
	template, err := payments.SaveCouponTemplate(ctx, 0, service.CouponTemplate{Name: "Synthetic hold", Kind: "fixed", Currency: "CNY", MinAmount: 100, Value: 10, ValidDays: 7, Enabled: true})
	require.NoError(t, err)
	for _, state := range []string{service.OrderStatusCancelled, service.OrderStatusExpired} {
		t.Run(state, func(t *testing.T) {
			ids, err := payments.IssueCoupons(ctx, service.CouponIssueRequest{TemplateID: template.ID, UserIDs: []int64{beneficiary}, Quantity: 1, RequestID: "repair-r1-" + state})
			require.NoError(t, err)
			order, err := client.PaymentOrder.Create().SetUserID(beneficiary).SetUserEmail("user@example.invalid").SetUserName("Synthetic user").SetClientIP("127.0.0.1").SetSrcHost("example.invalid").SetAmount(100).SetPayAmount(90).SetBonusAmount(10).SetPaymentType("alipay").SetRechargeCode("R1-" + state).SetOutTradeNo("synthetic-r1-" + state).SetOrderType(payment.OrderTypeBalance).SetStatus(state).SetExpiresAt(time.Now().Add(-48 * time.Hour)).SetPaidAt(time.Now()).SetPaymentTradeNo("synthetic-trade-" + state).SetProviderSnapshot(map[string]any{"recharge_coupon_id": ids[0], "coupon_name": "Synthetic hold", "coupon_discount": 10, "coupon_review_required": true, "coupon_recovery_reason": "canonical_recovery_ineligible"}).Save(ctx)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO recharge_coupon_orders(order_id,coupon_id,user_id,original_amount,discount_amount,currency,status) VALUES($1,$2,$3,100,10,'CNY','released')`, order.ID, ids[0], beneficiary)
			require.NoError(t, err)
			before := client.User.GetX(ctx, beneficiary).Balance
			request := func(role, body string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/admin/payment/orders/%d/retry", order.ID), strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				if role != "" {
					req.Header.Set("Authorization", "Bearer "+tokens[role])
				}
				router.ServeHTTP(w, req)
				return w
			}
			for _, role := range []string{"", service.RoleUser, service.RoleObserver} {
				w := request(role, `{"confirm":true}`)
				want := 403
				if role == "" {
					want = 401
				}
				require.Equal(t, want, w.Code, "R1 actual admin middleware must reject unauthorized approval: %s", w.Body.String())
			}
			for _, body := range []string{`{}`, `{"confirm":false}`} {
				w := request(service.RoleAdmin, body)
				require.Equal(t, 400, w.Code, "R1 paid coupon hold must not fulfill without explicit handler confirmation: %s", w.Body.String())
				require.Equal(t, state, client.PaymentOrder.GetX(ctx, order.ID).Status)
				require.Equal(t, before, client.User.GetX(ctx, beneficiary).Balance)
			}
			w := request(service.RoleAdmin, `{"confirm":true}`)
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, service.OrderStatusCompleted, client.PaymentOrder.GetX(ctx, order.ID).Status)
			require.Equal(t, before+100, client.User.GetX(ctx, beneficiary).Balance)
			w = request(service.RoleAdmin, `{"confirm":true}`)
			require.Contains(t, []int{200, 400, 409}, w.Code)
			require.Equal(t, before+100, client.User.GetX(ctx, beneficiary).Balance, "R1 repeated approval must not duplicate credit")
			item, err := coupon.ReadOwned(ctx, client, ids[0], beneficiary, false)
			require.NoError(t, err)
			require.Equal(t, "used", item.Status)
			var approvals int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM payment_audit_logs WHERE order_id=$1 AND action='COUPON_CONFLICT_HONORED'`, fmt.Sprint(order.ID)).Scan(&approvals))
			require.Equal(t, 1, approvals, "R1 exactly one durable manual approval audit")
		})
	}
}
