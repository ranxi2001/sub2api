package qqbot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	// DefaultRechargeWindowDays preserves the historical recharge window.
	DefaultRechargeWindowDays = 30
	maxRechargeWindowDays     = 3650
)

// RechargeRequirement describes the optional qualification for a group
// activity. If both thresholds are configured, satisfying either one is
// enough to participate. Zero disables that side of the requirement.
type RechargeRequirement struct {
	MinRechargeTotal float64 `json:"min_recharge_total"`
	// Keep the existing JSON key so saved thresholds survive upgrades.
	MinRecharge30d     float64 `json:"min_recharge_30d"`
	RechargeWindowDays int     `json:"recharge_window_days" binding:"min=1,max=3650"`
}

type RechargeEligibility struct {
	Total  decimal.Decimal
	Recent decimal.Decimal
}

func (r RechargeRequirement) windowDays() int {
	if r.RechargeWindowDays == 0 {
		return DefaultRechargeWindowDays
	}
	return r.RechargeWindowDays
}

func (c Config) inviteRechargeRequirement() RechargeRequirement {
	return RechargeRequirement{
		MinRechargeTotal: c.InviteMinRechargeTotal, MinRecharge30d: c.InviteMinRecharge30d,
		RechargeWindowDays: c.InviteRechargeWindowDays,
	}
}

func validRechargeThreshold(n float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 1e9 && decimal.NewFromFloat(n).Equal(decimal.NewFromFloat(n).Round(2))
}

func validateRechargeRequirement(r RechargeRequirement) error {
	// Zero is the legacy value when the window was not configurable.
	if r.RechargeWindowDays < 0 || r.RechargeWindowDays > maxRechargeWindowDays {
		return fmt.Errorf("充值统计天数需为 1 到 %d 的整数；未设置时默认 30 天", maxRechargeWindowDays)
	}
	if !validRechargeThreshold(r.MinRechargeTotal) || !validRechargeThreshold(r.MinRecharge30d) {
		return errors.New("充值参与门槛需在 0 到 1000000000 USD 之间，最多 2 位小数；0 表示不启用该门槛")
	}
	return nil
}

func (r RechargeRequirement) enabled() bool {
	return r.MinRechargeTotal > 0 || r.MinRecharge30d > 0
}

func (r RechargeRequirement) satisfied(e RechargeEligibility) bool {
	if !r.enabled() {
		return true
	}
	return (r.MinRechargeTotal > 0 && e.Total.GreaterThanOrEqual(decimal.NewFromFloat(r.MinRechargeTotal))) ||
		(r.MinRecharge30d > 0 && e.Recent.GreaterThanOrEqual(decimal.NewFromFloat(r.MinRecharge30d)))
}

// Avoid dollar delimiters: QQ clients can parse consecutive amounts as math.
func money(n float64) string { return fmt.Sprintf("USD %.2f", n) }

func (r RechargeRequirement) describe() string {
	parts := make([]string, 0, 2)
	if r.MinRechargeTotal > 0 {
		parts = append(parts, fmt.Sprintf("累计有效充值满 %s", money(r.MinRechargeTotal)))
	}
	if r.MinRecharge30d > 0 {
		parts = append(parts, fmt.Sprintf("近 %d 天有效充值满 %s", r.windowDays(), money(r.MinRecharge30d)))
	}
	if len(parts) == 0 {
		return "无需充值门槛"
	}
	return strings.Join(parts, " 或 ")
}

func (r RechargeRequirement) failure(activity string, e RechargeEligibility) string {
	return fmt.Sprintf("%s暂不满足：\n%s\n\n累计充值：USD %s\n近 %d 天充值：USD %s", activity, r.describe(), e.Total.StringFixed(2), r.windowDays(), e.Recent.StringFixed(2))
}

func (r RechargeRequirement) invite() string {
	return "充值门槛（邀请人账号）：" + r.describe() + "。"
}

func rechargeSum(ctx context.Context, db queryRower, uid int64, since *time.Time, lockSources bool) (decimal.Decimal, error) {
	orderQuery := `SELECT amount,bonus_amount,refund_amount,status FROM payment_orders
        WHERE user_id=$1 AND order_type='balance' AND paid_at IS NOT NULL AND completed_at IS NOT NULL`
	args := []any{uid}
	if since != nil {
		orderQuery += ` AND completed_at >= $2`
		args = append(args, *since)
	}
	orderQuery += ` ORDER BY id`
	if lockSources {
		orderQuery += ` FOR SHARE`
	}
	var raw string
	err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE
        WHEN status IN ('COMPLETED','REFUND_FAILED') THEN GREATEST(amount-bonus_amount,0)
        WHEN status='PARTIALLY_REFUNDED' AND amount>0 THEN ROUND(GREATEST(amount-bonus_amount,0)*GREATEST(amount-refund_amount,0)/amount,8)
        ELSE 0 END),0)::text FROM (`+orderQuery+`) orders`, args...).Scan(&raw)
	if err != nil {
		return decimal.Zero, err
	}
	total, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, err
	}

	codeQuery := `SELECT r.value FROM redeem_codes r
        WHERE r.used_by=$1 AND r.type='balance' AND r.status='used' AND r.used_at IS NOT NULL AND r.value>0
        AND NOT EXISTS(SELECT 1 FROM payment_orders p WHERE p.recharge_code=r.code)`
	codeArgs := []any{uid}
	if since != nil {
		codeQuery += ` AND r.used_at >= $2`
		codeArgs = append(codeArgs, *since)
	}
	codeQuery += ` ORDER BY r.id`
	if lockSources {
		codeQuery += ` FOR SHARE OF r`
	}
	if err = db.QueryRowContext(ctx, `SELECT COALESCE(SUM(value),0)::text FROM (`+codeQuery+`) codes`, codeArgs...).Scan(&raw); err != nil {
		return decimal.Zero, err
	}
	codeTotal, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, err
	}
	return total.Add(codeTotal), nil
}

func (r RechargeRequirement) eligibility(ctx context.Context, db queryRower, uid int64, lockSources bool) (RechargeEligibility, error) {
	if err := validateRechargeRequirement(r); err != nil {
		return RechargeEligibility{}, err
	}
	cutoff := time.Now().Add(-time.Duration(r.windowDays()) * 24 * time.Hour)
	total, err := rechargeSum(ctx, db, uid, nil, lockSources)
	if err != nil {
		return RechargeEligibility{}, err
	}
	recent, err := rechargeSum(ctx, db, uid, &cutoff, lockSources)
	if err != nil {
		return RechargeEligibility{}, err
	}
	return RechargeEligibility{Total: total, Recent: recent}, nil
}
