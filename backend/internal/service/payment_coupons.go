package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/coupon"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

type CouponTemplate = coupon.Template
type UserCoupon = coupon.Item
type CouponQuote struct {
	CouponID       int64   `json:"coupon_id"`
	Name           string  `json:"name"`
	Currency       string  `json:"currency"`
	OriginalAmount float64 `json:"original_amount"`
	PayBase        float64 `json:"pay_base"`
	PayAmount      float64 `json:"pay_amount"`
	Credited       float64 `json:"credited"`
	BonusAmount    float64 `json:"bonus_amount"`
	DiscountAmount float64 `json:"discount_amount"`
	CouponBonus    float64 `json:"coupon_bonus"`
	FeeRate        float64 `json:"fee_rate"`
}

func (s *PaymentService) ListCouponTemplates(ctx context.Context) ([]CouponTemplate, error) {
	rows, err := s.entClient.QueryContext(ctx, `SELECT id,name,kind,currency,min_amount,value,max_discount,valid_days,stackable,enabled,max_issues,per_user_limit,issued_count FROM recharge_coupon_templates ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []CouponTemplate{}
	for rows.Next() {
		var t CouponTemplate
		if err = rows.Scan(&t.ID, &t.Name, &t.Kind, &t.Currency, &t.MinAmount, &t.Value, &t.MaxDiscount, &t.ValidDays, &t.Stackable, &t.Enabled, &t.MaxIssues, &t.PerUserLimit, &t.IssuedCount); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *PaymentService) SaveCouponTemplate(ctx context.Context, id int64, t CouponTemplate) (CouponTemplate, error) {
	if err := t.Validate(); err != nil {
		return t, err
	}
	var q string
	args := []any{t.Name, t.Kind, t.Currency, t.MinAmount, t.Value, t.MaxDiscount, t.ValidDays, t.Stackable, t.Enabled, t.MaxIssues, t.PerUserLimit}
	if id == 0 {
		q = `INSERT INTO recharge_coupon_templates(name,kind,currency,min_amount,value,max_discount,valid_days,stackable,enabled,max_issues,per_user_limit) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`
	} else {
		q = `UPDATE recharge_coupon_templates SET name=$1,kind=$2,currency=$3,min_amount=$4,value=$5,max_discount=$6,valid_days=$7,stackable=$8,enabled=$9,max_issues=$10,per_user_limit=$11,updated_at=NOW() WHERE id=$12 RETURNING id`
		args = append(args, id)
	}
	rows, err := s.entClient.QueryContext(ctx, q, args...)
	if err != nil {
		return t, err
	}
	if rows.Next() {
		err = rows.Scan(&id)
	} else {
		err = coupon.ErrUnavailable
	}
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return t, err
	}
	return coupon.ReadTemplate(ctx, s.entClient, id, false)
}

type CouponIssueRequest struct {
	TemplateID int64   `json:"template_id"`
	UserIDs    []int64 `json:"user_ids"`
	Quantity   int     `json:"quantity"`
	RequestID  string  `json:"request_id"`
}

func (s *PaymentService) IssueCoupons(ctx context.Context, r CouponIssueRequest) ([]int64, error) {
	if len(r.UserIDs) == 0 || len(r.UserIDs) > 100 || r.Quantity < 1 || r.Quantity > 100 || len(r.UserIDs)*r.Quantity > 1000 || len(r.RequestID) < 16 || len(r.RequestID) > 80 {
		return nil, coupon.ErrInvalid
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	sort.Slice(r.UserIDs, func(i, j int) bool { return r.UserIDs[i] < r.UserIDs[j] })
	unique := make([]int64, 0, len(r.UserIDs))
	for _, uid := range r.UserIDs {
		if uid <= 0 {
			return nil, coupon.ErrInvalid
		}
		if len(unique) == 0 || unique[len(unique)-1] != uid {
			unique = append(unique, uid)
		}
	}
	r.UserIDs = unique
	payload, _ := json.Marshal(r)
	hash := fmt.Sprintf("%x", sha256.Sum256(payload))
	if _, err = tx.ExecContext(ctx, `INSERT INTO recharge_coupon_issuances(request_id,request_hash) VALUES($1,$2) ON CONFLICT DO NOTHING`, r.RequestID, hash); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT request_hash,coupon_ids FROM recharge_coupon_issuances WHERE request_id=$1 FOR UPDATE`, r.RequestID)
	if err != nil {
		return nil, err
	}
	var savedHash string
	var savedIDs []byte
	if rows.Next() {
		err = rows.Scan(&savedHash, &savedIDs)
	} else {
		err = coupon.ErrInvalid
	}
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if savedHash != hash {
		return nil, infraerrors.Conflict("COUPON_REQUEST_CHANGED", "同一发放请求不能修改内容")
	}
	if len(savedIDs) > 0 {
		var result []int64
		if err = json.Unmarshal(savedIDs, &result); err != nil {
			return nil, err
		}
		return result, tx.Commit()
	}
	for _, uid := range r.UserIDs {
		if _, err = tx.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, uid); err != nil {
			return nil, err
		}
	}
	ids := []int64{}
	seen := map[int64]bool{}
	for _, uid := range r.UserIDs {
		if seen[uid] {
			continue
		}
		seen[uid] = true
		issued, e := coupon.Issue(ctx, tx.Client(), r.TemplateID, uid, r.Quantity, "admin", fmt.Sprintf("admin:%s:%d", r.RequestID, uid), time.Now())
		if e != nil {
			return nil, e
		}
		ids = append(ids, issued...)
	}
	raw, _ := json.Marshal(ids)
	if _, err = tx.ExecContext(ctx, `UPDATE recharge_coupon_issuances SET coupon_ids=$2 WHERE request_id=$1`, r.RequestID, string(raw)); err != nil {
		return nil, err
	}
	return ids, tx.Commit()
}

func (s *PaymentService) ListCoupons(ctx context.Context, userID, templateID int64, status, search string, page, pageSize int) ([]UserCoupon, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	if page > 1000000 || len(search) > 254 {
		return nil, 0, coupon.ErrInvalid
	}
	switch status {
	case "", "available", "reserved", "used", "expired", "revoked":
	default:
		return nil, 0, coupon.ErrInvalid
	}
	const from = ` FROM user_recharge_coupons c JOIN users u ON u.id=c.user_id LEFT JOIN recharge_coupon_orders co ON co.order_id=c.order_id LEFT JOIN payment_orders o ON o.id=c.order_id WHERE ($1=0 OR c.user_id=$1) AND ($2=0 OR c.template_id=$2) AND ($3='' OR CASE WHEN c.status='available' AND c.expires_at<=NOW() THEN 'expired' ELSE c.status END=$3) AND ($4='' OR c.user_id::text=$4 OR c.id::text=$4 OR strpos(lower(u.email),lower($4))>0)`
	args := []any{userID, templateID, status, search}
	rows, err := s.entClient.QueryContext(ctx, `SELECT COUNT(*)`+from, args...)
	if err != nil {
		return nil, 0, err
	}
	var total int
	if rows.Next() {
		err = rows.Scan(&total)
	} else {
		err = rows.Err()
	}
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, 0, err
	}
	rows, err = s.entClient.QueryContext(ctx, `SELECT c.id,c.template_id,c.user_id,u.email,c.snapshot,c.source,CASE WHEN c.status='available' AND c.expires_at<=NOW() THEN 'expired' ELSE c.status END,c.order_id,c.expires_at,c.used_at,c.created_at,COALESCE(o.status,''),COALESCE(co.discount_amount,0),COALESCE(co.bonus_amount,0)`+from+` ORDER BY c.id DESC LIMIT $5 OFFSET $6`, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := []UserCoupon{}
	for rows.Next() {
		var c UserCoupon
		var raw []byte
		if err = rows.Scan(&c.ID, &c.TemplateID, &c.UserID, &c.UserEmail, &raw, &c.Source, &c.Status, &c.OrderID, &c.ExpiresAt, &c.UsedAt, &c.CreatedAt, &c.OrderStatus, &c.DiscountAmount, &c.BonusAmount); err != nil {
			return nil, 0, err
		}
		if err = json.Unmarshal(raw, &c.Snapshot); err != nil {
			return nil, 0, err
		}
		if userID > 0 {
			c.UserEmail = ""
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}
func (s *PaymentService) RevokeCoupon(ctx context.Context, id int64) error {
	result, err := s.entClient.ExecContext(ctx, `UPDATE user_recharge_coupons SET status='revoked' WHERE id=$1 AND status='available'`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return coupon.ErrUnavailable
	}
	return nil
}

func couponRechargeQuote(c coupon.Item, cfg *PaymentConfig, amount float64, currency string) (*CouponQuote, error) {
	if c.Status != "available" || !c.ExpiresAt.After(time.Now()) {
		return nil, coupon.ErrUnavailable
	}
	config := *cfg
	if !c.Snapshot.Stackable {
		config.RechargeBonusTiers = nil
	}
	base := quoteRechargeBonus(&config, amount, currency)
	discount, bonus, err := coupon.Calculate(c.Snapshot, amount, base.PayBase, base.Credited, currency, int32(payment.CurrencyMaxFractionDigits(currency)))
	if err != nil {
		return nil, err
	}
	payBase := decimal.NewFromFloat(base.PayBase).Sub(decimal.NewFromFloat(discount)).InexactFloat64()
	credited := decimal.NewFromFloat(base.Credited).Add(decimal.NewFromFloat(bonus)).Round(2).InexactFloat64()
	// All free credit is excluded from affiliate and recharge reward principal.
	free := decimal.NewFromFloat(credited).Sub(decimal.NewFromFloat(calculateCreditedBalance(payBase, cfg.BalanceRechargeMultiplier))).Round(2).InexactFloat64()
	_, pay, err := calculateCreateOrderPayAmountForOrderType(payBase, cfg.RechargeFeeRate, currency, payment.OrderTypeBalance, cfg.SubscriptionUSDToCNYRate)
	if err != nil {
		return nil, err
	}
	return &CouponQuote{CouponID: c.ID, Name: c.Snapshot.Name, Currency: currency, OriginalAmount: amount, PayBase: payBase, PayAmount: pay, Credited: credited, BonusAmount: free, DiscountAmount: discount, CouponBonus: bonus, FeeRate: cfg.RechargeFeeRate}, nil
}
func (s *PaymentService) QuoteCoupon(ctx context.Context, uid, id int64, amount float64, method string) (*CouponQuote, error) {
	cfg, err := s.configService.GetPaymentConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, infraerrors.Forbidden("PAYMENT_DISABLED", "payment system is disabled")
	}
	req := CreateOrderRequest{UserID: uid, Amount: amount, PaymentType: NormalizeVisibleMethod(method), OrderType: payment.OrderTypeBalance}
	if _, err = s.validateOrderInput(ctx, req, cfg); err != nil {
		return nil, err
	}
	currency, err := s.configService.ValidateMethodCurrencyConsistency(ctx, req.PaymentType)
	if err != nil {
		return nil, err
	}
	c, err := coupon.ReadOwned(ctx, s.entClient, id, uid, false)
	if err != nil {
		return nil, err
	}
	return couponRechargeQuote(c, cfg, amount, currency)
}
func (s *PaymentService) reserveOrderCoupon(ctx context.Context, tx *dbent.Tx, req CreateOrderRequest, o *dbent.PaymentOrder, cfg *PaymentConfig) error {
	if req.CouponID == 0 {
		return nil
	}
	c, err := coupon.ReadOwned(ctx, tx.Client(), req.CouponID, req.UserID, true)
	if err != nil {
		return err
	}
	feeConfig := *cfg
	feeConfig.RechargeFeeRate = o.FeeRate
	q, err := couponRechargeQuote(c, &feeConfig, req.Amount, req.couponQuote.Currency)
	if err != nil {
		return err
	}
	if q.PayAmount != o.PayAmount || q.Credited != o.Amount || q.BonusAmount != o.BonusAmount {
		return infraerrors.Conflict("COUPON_QUOTE_CHANGED", "优惠金额已变化，请重新下单")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE user_recharge_coupons SET status='reserved',order_id=$2 WHERE id=$1`, c.ID, o.ID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recharge_coupon_orders(order_id,coupon_id,user_id,original_amount,discount_amount,bonus_amount,currency) VALUES($1,$2,$3,$4,$5,$6,$7)`, o.ID, c.ID, req.UserID, req.Amount, q.DiscountAmount, q.CouponBonus, q.Currency)
	return err
}

// Order row must be locked before coupon rows, consistently with confirmation.
func releaseOrderCoupon(ctx context.Context, db coupon.DB, oid int64) error {
	_, err := db.ExecContext(ctx, `UPDATE user_recharge_coupons SET status='available',order_id=NULL WHERE order_id=$1 AND status='reserved'`, oid)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE recharge_coupon_orders SET status='released' WHERE order_id=$1 AND status='reserved'`, oid)
	return err
}
func (s *PaymentService) releaseFailedOrderCoupon(ctx context.Context, oid int64) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT status,paid_at FROM payment_orders WHERE id=$1 FOR UPDATE`, oid)
	if err != nil {
		return err
	}
	var status string
	var paid *time.Time
	if rows.Next() {
		err = rows.Scan(&status, &paid)
	} else {
		err = rows.Err()
		if err == nil {
			err = coupon.ErrUnavailable
		}
	}
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if paid == nil && (status == OrderStatusFailed || status == OrderStatusPending) {
		if _, err = tx.PaymentOrder.UpdateOneID(oid).SetStatus(OrderStatusFailed).Save(ctx); err != nil {
			return err
		}
		if err = releaseOrderCoupon(ctx, tx.Client(), oid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

var errCouponPaymentConflict = errors.New("coupon payment requires administrator review")

// An old payment may arrive after a cancelled order released its coupon. Keep
// the promised amounts intact, but require admin review when the coupon was
// subsequently reused or revoked. Never silently reduce a customer's credit.
func consumeOrderCoupon(ctx context.Context, db coupon.DB, o *dbent.PaymentOrder) error {
	rows, err := db.QueryContext(ctx, `SELECT coupon_id,status FROM recharge_coupon_orders WHERE order_id=$1`, o.ID)
	if err != nil {
		return err
	}
	var cid int64
	var state string
	found := rows.Next()
	if found {
		err = rows.Scan(&cid, &state)
	} else {
		err = rows.Err()
	}
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if !found {
		return coupon.ErrUnavailable
	}
	if state == "conflict" {
		return errCouponPaymentConflict
	}
	if state == "used" || state == "honored" {
		return nil
	}
	c, err := coupon.ReadOwned(ctx, db, cid, o.UserID, true)
	if err != nil {
		return err
	}
	if c.Status == "available" || ((c.Status == "reserved" || c.Status == "used") && c.OrderID != nil && *c.OrderID == o.ID) {
		if _, err = db.ExecContext(ctx, `UPDATE user_recharge_coupons SET status='used',order_id=$2,used_at=NOW() WHERE id=$1`, cid, o.ID); err != nil {
			return err
		}
		_, err = db.ExecContext(ctx, `UPDATE recharge_coupon_orders SET status='used',used_at=NOW() WHERE order_id=$1`, o.ID)
		return err
	}
	if _, err = db.ExecContext(ctx, `UPDATE recharge_coupon_orders SET status='conflict' WHERE order_id=$1`, o.ID); err != nil {
		return err
	}
	details, _ := json.Marshal(map[string]any{"coupon_id": cid, "reason": "late payment after coupon released and reused or revoked; original credit retained for manual review"})
	if _, err = db.ExecContext(ctx, `INSERT INTO payment_audit_logs(order_id,action,operator,detail,created_at) VALUES($1,'COUPON_LATE_PAYMENT_CONFLICT','system',$2,NOW())`, fmt.Sprint(o.ID), string(details)); err != nil {
		return err
	}
	return errCouponPaymentConflict
}

func orderHasCoupon(o *dbent.PaymentOrder) bool {
	if o == nil {
		return false
	}
	_, ok := o.ProviderSnapshot["recharge_coupon_id"]
	return ok
}
func (s *PaymentService) confirmCouponOrder(ctx context.Context, o *dbent.PaymentOrder, tradeNo string, paid float64, pk string) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT status,paid_at FROM payment_orders WHERE id=$1 FOR UPDATE`, o.ID)
	if err != nil {
		return err
	}
	var status string
	var paidAt *time.Time
	if rows.Next() {
		err = rows.Scan(&status, &paidAt)
	} else {
		err = coupon.ErrUnavailable
	}
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if paidAt != nil || psIsRefundStatus(status) || status == OrderStatusCompleted {
		if err = tx.Commit(); err != nil {
			return err
		}
		current, e := s.entClient.PaymentOrder.Get(ctx, o.ID)
		if e != nil {
			return e
		}
		if review, _ := current.ProviderSnapshot["coupon_review_required"].(bool); review {
			return nil
		}
		return s.alreadyProcessed(ctx, current)
	}
	// Reload the full persisted snapshot under the same row lock; a stale
	// caller must not carry pre-cancellation status or renew a recovery window.
	current, err := tx.PaymentOrder.Get(ctx, o.ID)
	if err != nil {
		return err
	}
	o = current
	now := time.Now()
	eligible, err := tx.PaymentOrder.Query().Where(paymentorder.IDEQ(o.ID), paymentRecoveryEligibility(ctx, o, pk, now)).Exist(ctx)
	if err != nil {
		return err
	}
	if !eligible {
		// A verified payment remains evidence, but never overrides a committed
		// cancellation or an elapsed canonical recovery deadline automatically.
		snapshot := make(map[string]any, len(o.ProviderSnapshot)+2)
		for key, value := range o.ProviderSnapshot {
			snapshot[key] = value
		}
		snapshot["coupon_review_required"] = true
		snapshot["coupon_recovery_reason"] = "canonical_recovery_ineligible"
		if _, err = tx.PaymentOrder.UpdateOneID(o.ID).SetPayAmount(paid).SetPaymentTradeNo(tradeNo).SetPaidAt(now).SetProviderSnapshot(snapshot).Save(ctx); err != nil {
			return err
		}
		if err = releaseOrderCoupon(ctx, tx.Client(), o.ID); err != nil {
			return err
		}
		detail, _ := json.Marshal(map[string]any{"tradeNo": tradeNo, "paidAmount": paid, "provider": pk, "previous_status": status, "expires_at": o.ExpiresAt, "reason": "canonical recovery policy requires manual reconciliation"})
		if _, err = tx.ExecContext(ctx, `INSERT INTO payment_audit_logs(order_id,action,operator,detail,created_at) VALUES($1,'PAYMENT_RECOVERY_REVIEW_REQUIRED','system',$2,NOW())`, fmt.Sprint(o.ID), string(detail)); err != nil {
			return err
		}
		return tx.Commit()
	}
	err = consumeOrderCoupon(ctx, tx.Client(), o)
	conflict := errors.Is(err, errCouponPaymentConflict)
	if err != nil && !conflict {
		return err
	}
	if _, err = tx.PaymentOrder.UpdateOneID(o.ID).SetStatus(OrderStatusPaid).SetPayAmount(paid).SetPaymentTradeNo(tradeNo).SetPaidAt(time.Now()).ClearFailedAt().ClearFailedReason().Save(ctx); err != nil {
		return err
	}
	if conflict {
		snapshot := o.ProviderSnapshot
		if snapshot == nil {
			snapshot = map[string]any{}
		}
		snapshot["coupon_review_required"] = true
		if _, err = tx.PaymentOrder.UpdateOneID(o.ID).SetStatus(OrderStatusFailed).SetFailedAt(time.Now()).SetFailedReason("优惠券已被其他订单占用或作废，付款已收到，请联系管理员审核到账").SetProviderSnapshot(snapshot).Save(ctx); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if conflict {
		return nil
	}
	s.writeAuditLog(ctx, o.ID, "ORDER_PAID", pk, map[string]any{"tradeNo": tradeNo, "paidAmount": paid, "coupon": true})
	return s.executeFulfillment(ctx, o.ID)
}

// CouponUsage keeps every reservation, release, redemption and reviewed conflict
// visible after a coupon moves to a different order.
type CouponUsage struct {
	OrderID        int64      `json:"order_id"`
	CouponID       int64      `json:"coupon_id"`
	UserID         int64      `json:"user_id"`
	UserEmail      string     `json:"user_email,omitempty"`
	Name           string     `json:"name"`
	Status         string     `json:"status"`
	OrderStatus    string     `json:"order_status"`
	OriginalAmount float64    `json:"original_amount"`
	DiscountAmount float64    `json:"discount_amount"`
	BonusAmount    float64    `json:"bonus_amount"`
	Currency       string     `json:"currency"`
	CreatedAt      time.Time  `json:"created_at"`
	UsedAt         *time.Time `json:"used_at"`
}

func (s *PaymentService) ListCouponUsage(ctx context.Context, uid int64, search, status string, page, size int) ([]CouponUsage, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	if page > 1000000 || len(search) > 254 {
		return nil, 0, coupon.ErrInvalid
	}
	switch status {
	case "", "reserved", "released", "used", "conflict", "honored":
	default:
		return nil, 0, coupon.ErrInvalid
	}
	const from = ` FROM recharge_coupon_orders r JOIN user_recharge_coupons c ON c.id=r.coupon_id JOIN users u ON u.id=r.user_id JOIN payment_orders o ON o.id=r.order_id WHERE ($1=0 OR r.user_id=$1) AND ($2='' OR r.coupon_id::text=$2 OR r.order_id::text=$2 OR r.user_id::text=$2 OR strpos(lower(u.email),lower($2))>0) AND ($3='' OR r.status=$3)`
	args := []any{uid, search, status}
	rows, err := s.entClient.QueryContext(ctx, `SELECT COUNT(*)`+from, args...)
	if err != nil {
		return nil, 0, err
	}
	var total int
	if rows.Next() {
		err = rows.Scan(&total)
	} else {
		err = rows.Err()
	}
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, 0, err
	}
	rows, err = s.entClient.QueryContext(ctx, `SELECT r.order_id,r.coupon_id,r.user_id,u.email,c.snapshot->>'name',r.status,o.status,r.original_amount,r.discount_amount,r.bonus_amount,r.currency,r.created_at,r.used_at`+from+` ORDER BY r.order_id DESC LIMIT $4 OFFSET $5`, append(args, size, (page-1)*size)...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := []CouponUsage{}
	for rows.Next() {
		var r CouponUsage
		if err = rows.Scan(&r.OrderID, &r.CouponID, &r.UserID, &r.UserEmail, &r.Name, &r.Status, &r.OrderStatus, &r.OriginalAmount, &r.DiscountAmount, &r.BonusAmount, &r.Currency, &r.CreatedAt, &r.UsedAt); err != nil {
			return nil, 0, err
		}
		if uid > 0 {
			r.UserEmail = ""
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

type PaymentCouponSummary struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	DiscountAmount float64 `json:"discount_amount"`
	BonusAmount    float64 `json:"bonus_amount"`
	ReviewRequired bool    `json:"review_required"`
}

func PaymentOrderCoupon(o *dbent.PaymentOrder) *PaymentCouponSummary {
	if !orderHasCoupon(o) {
		return nil
	}
	raw, _ := json.Marshal(o.ProviderSnapshot["recharge_coupon_id"])
	var id int64
	_ = json.Unmarshal(raw, &id)
	name, _ := o.ProviderSnapshot["coupon_name"].(string)
	discount, _ := o.ProviderSnapshot["coupon_discount"].(float64)
	bonus, _ := o.ProviderSnapshot["coupon_bonus"].(float64)
	review, _ := o.ProviderSnapshot["coupon_review_required"].(bool)
	return &PaymentCouponSummary{ID: id, Name: name, DiscountAmount: discount, BonusAmount: bonus, ReviewRequired: review}
}
