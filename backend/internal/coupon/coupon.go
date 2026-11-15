// Package coupon owns site-wide recharge coupon rules and transactional issuance.
// It is shared by payments and reward producers; QQ is only one issuer.
package coupon

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

type DB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

var ErrUnavailable = infraerrors.BadRequest("COUPON_UNAVAILABLE", "优惠券不可用、已过期或已被占用")
var ErrInvalid = infraerrors.BadRequest("COUPON_INVALID", "优惠券参数不正确")
var ErrLimit = infraerrors.BadRequest("COUPON_ISSUE_LIMIT", "优惠券已停发或达到发放上限")

type Template struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Kind         string  `json:"kind"`
	Currency     string  `json:"currency"`
	MinAmount    float64 `json:"min_amount"`
	Value        float64 `json:"value"`
	MaxDiscount  float64 `json:"max_discount"`
	ValidDays    int     `json:"valid_days"`
	Stackable    bool    `json:"stackable"`
	Enabled      bool    `json:"enabled"`
	MaxIssues    int     `json:"max_issues"`
	PerUserLimit int     `json:"per_user_limit"`
	IssuedCount  int     `json:"issued_count"`
}

func (t *Template) Validate() error {
	t.Name = strings.TrimSpace(t.Name)
	t.Currency = strings.ToUpper(strings.TrimSpace(t.Currency))
	if t.Name == "" || utf8.RuneCountInString(t.Name) > 100 || strings.ContainsAny(t.Name, "\r\n\t") || t.ValidDays < 1 || t.ValidDays > 3650 || t.MaxIssues < 0 || t.MaxIssues > 10000000 || t.PerUserLimit < 0 || t.PerUserLimit > 10000 {
		return ErrInvalid
	}
	switch t.Currency {
	case "CNY", "USD", "EUR", "HKD", "JPY", "GBP", "SGD", "AUD", "CAD":
	default:
		return ErrInvalid
	}
	for _, v := range []float64{t.MinAmount, t.Value, t.MaxDiscount} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e9 || !decimal.NewFromFloat(v).Equal(decimal.NewFromFloat(v).Round(2)) {
			return ErrInvalid
		}
	}
	if t.Value <= 0 {
		return ErrInvalid
	}
	switch t.Kind {
	case "fixed":
		if t.MinAmount <= t.Value {
			return infraerrors.BadRequest("COUPON_INVALID", "满减券门槛必须大于减免金额")
		}
	case "percent":
		if t.Value >= 100 {
			return ErrInvalid
		}
	case "bonus":
		if t.Value > 1000 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

type Item struct {
	ID             int64      `json:"id"`
	TemplateID     int64      `json:"template_id"`
	UserID         int64      `json:"user_id"`
	UserEmail      string     `json:"user_email,omitempty"`
	Snapshot       Template   `json:"snapshot"`
	Source         string     `json:"source"`
	Status         string     `json:"status"`
	OrderID        *int64     `json:"order_id"`
	OrderStatus    string     `json:"order_status"`
	DiscountAmount float64    `json:"discount_amount"`
	BonusAmount    float64    `json:"bonus_amount"`
	ExpiresAt      time.Time  `json:"expires_at"`
	UsedAt         *time.Time `json:"used_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

func ReadTemplate(ctx context.Context, db DB, id int64, lock bool) (Template, error) {
	query := `SELECT id,name,kind,currency,min_amount,value,max_discount,valid_days,stackable,enabled,max_issues,per_user_limit,issued_count FROM recharge_coupon_templates WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	rows, err := db.QueryContext(ctx, query, id)
	if err != nil {
		return Template{}, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return Template{}, err
		}
		return Template{}, ErrUnavailable
	}
	var t Template
	err = rows.Scan(&t.ID, &t.Name, &t.Kind, &t.Currency, &t.MinAmount, &t.Value, &t.MaxDiscount, &t.ValidDays, &t.Stackable, &t.Enabled, &t.MaxIssues, &t.PerUserLimit, &t.IssuedCount)
	return t, err
}

// Issue must run inside the caller's transaction. Stable source keys make retries
// idempotent across channels. Template locking serializes issuance caps.
func Issue(ctx context.Context, db DB, templateID, userID int64, quantity int, source, key string, at time.Time) ([]int64, error) {
	if templateID <= 0 || userID <= 0 || quantity < 1 || quantity > 100 || len(source) > 40 || key == "" || len(key) > 220 {
		return nil, ErrInvalid
	}
	// Lock account before template, matching reward settlement lock order.
	if _, err := db.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID); err != nil {
		return nil, err
	}
	t, err := ReadTemplate(ctx, db, templateID, true)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, quantity)
	for i := 0; i < quantity; i++ {
		k := fmt.Sprintf("%s:%d", key, i)
		rows, e := db.QueryContext(ctx, `SELECT id,user_id,template_id FROM user_recharge_coupons WHERE source_key=$1`, k)
		if e != nil {
			return nil, e
		}
		var existing, owner, tid int64
		found := rows.Next()
		if found {
			e = rows.Scan(&existing, &owner, &tid)
		} else {
			e = rows.Err()
		}
		if closeErr := rows.Close(); e == nil {
			e = closeErr
		}
		if e != nil {
			return nil, e
		}
		if found {
			if owner != userID || tid != templateID {
				return nil, ErrInvalid
			}
			ids = append(ids, existing)
			continue
		}
		if !t.Enabled || (t.MaxIssues > 0 && t.IssuedCount >= t.MaxIssues) {
			return nil, ErrLimit
		}
		if t.PerUserLimit > 0 {
			rows, e = db.QueryContext(ctx, `SELECT COUNT(*) FROM user_recharge_coupons WHERE template_id=$1 AND user_id=$2`, templateID, userID)
			if e != nil {
				return nil, e
			}
			var count int
			if rows.Next() {
				e = rows.Scan(&count)
			} else {
				e = rows.Err()
			}
			if closeErr := rows.Close(); e == nil {
				e = closeErr
			}
			if e != nil {
				return nil, e
			}
			if count >= t.PerUserLimit {
				return nil, ErrLimit
			}
		}
		snapshot, e := json.Marshal(t)
		if e != nil {
			return nil, e
		}
		rows, e = db.QueryContext(ctx, `INSERT INTO user_recharge_coupons(template_id,user_id,snapshot,source,source_key,expires_at,created_at) SELECT $1,id,$3,$4,$5,$6,$7 FROM users WHERE id=$2 AND status='active' AND deleted_at IS NULL RETURNING id`, templateID, userID, string(snapshot), source, k, at.AddDate(0, 0, t.ValidDays), at)
		if e != nil {
			return nil, e
		}
		var id int64
		if rows.Next() {
			e = rows.Scan(&id)
		} else {
			e = ErrUnavailable
		}
		if closeErr := rows.Close(); e == nil {
			e = closeErr
		}
		if e != nil {
			return nil, e
		}
		if _, e = db.ExecContext(ctx, `UPDATE recharge_coupon_templates SET issued_count=issued_count+1,updated_at=NOW() WHERE id=$1`, templateID); e != nil {
			return nil, e
		}
		t.IssuedCount++
		ids = append(ids, id)
	}
	return ids, nil
}

func ReadOwned(ctx context.Context, db DB, id, uid int64, lock bool) (Item, error) {
	q := `SELECT id,template_id,user_id,snapshot,source,status,order_id,expires_at,used_at,created_at FROM user_recharge_coupons WHERE id=$1 AND user_id=$2`
	if lock {
		q += ` FOR UPDATE`
	}
	rows, err := db.QueryContext(ctx, q, id, uid)
	if err != nil {
		return Item{}, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return Item{}, err
		}
		return Item{}, ErrUnavailable
	}
	var c Item
	var raw []byte
	err = rows.Scan(&c.ID, &c.TemplateID, &c.UserID, &raw, &c.Source, &c.Status, &c.OrderID, &c.ExpiresAt, &c.UsedAt, &c.CreatedAt)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c.Snapshot)
	return c, err
}

// Calculate returns discount in payment currency and additional balance in USD.
// Discounted principal is tracked separately by the payment service.
func Calculate(t Template, amount, payBase, credited float64, currency string, digits int32) (discount, bonus float64, err error) {
	if err = t.Validate(); err != nil {
		return
	}
	if t.Currency != currency {
		return 0, 0, infraerrors.BadRequest("COUPON_CURRENCY_MISMATCH", "优惠券不适用于当前支付币种")
	}
	if amount < t.MinAmount {
		return 0, 0, infraerrors.BadRequest("COUPON_MIN_AMOUNT", "充值金额未达到优惠券门槛")
	}
	value := decimal.NewFromFloat(t.Value)
	base := decimal.NewFromFloat(payBase)
	switch t.Kind {
	case "fixed":
		discount = value.Round(digits).InexactFloat64()
	case "percent":
		discount = base.Mul(value).Div(decimal.NewFromInt(100)).Round(digits).InexactFloat64()
	case "bonus":
		bonus = decimal.NewFromFloat(credited).Mul(value).Div(decimal.NewFromInt(100)).Round(2).InexactFloat64()
	}
	if t.Kind != "bonus" && t.MaxDiscount > 0 {
		discount = decimal.Min(decimal.NewFromFloat(discount), decimal.NewFromFloat(t.MaxDiscount)).Round(digits).InexactFloat64()
	}
	if discount >= payBase || discount < 0 || (discount == 0 && bonus == 0) {
		return 0, 0, infraerrors.BadRequest("COUPON_NO_BENEFIT", "当前充值金额无法使用此券，请调整金额")
	}
	return
}
