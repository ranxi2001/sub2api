package qqbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/Wei-Shaw/sub2api/internal/coupon"
	"github.com/shopspring/decimal"
)

type GrowthReward struct {
	CouponTemplateID  int64   `json:"coupon_template_id,omitempty"`
	Type              string  `json:"type"`
	Amount            float64 `json:"amount"`
	Quantity          int     `json:"quantity"`
	PrizeName         string  `json:"prize_name"`
	ClaimInstructions string  `json:"claim_instructions"`
}

func (t GrowthTask) reward() GrowthReward {
	if t.Reward != nil {
		return *t.Reward
	}
	return GrowthReward{Type: "chances", Quantity: t.Chances}
}

func (c CheckinConfig) streakReward() GrowthReward {
	if c.StreakReward != nil {
		return *c.StreakReward
	}
	return GrowthReward{Type: "chances", Quantity: c.StreakChances}
}

func (r GrowthReward) enabled() bool {
	if r.Type == "balance" {
		return r.Amount > 0
	}
	return r.Quantity > 0
}

func (r GrowthReward) validate() error {
	if !validRewardAmount(r.Amount) || (r.Amount > 0 && (r.Amount < 1e-8 || !decimal.NewFromFloat(r.Amount).Equal(decimal.NewFromFloat(r.Amount).Round(8)))) || r.Quantity < 0 || r.Quantity > 1000 || len([]rune(r.PrizeName)) > 100 || len([]rune(r.ClaimInstructions)) > 500 || strings.IndexFunc(r.PrizeName, unicode.IsControl) >= 0 {
		return errors.New("奖励金额须为 0～1000 USD（最多 8 位小数），数量须为 0～1000，奖品名称最多 100 字且不能换行，领取说明最多 500 字")
	}
	switch r.Type {
	case "coupon":
		if r.Amount != 0 || r.Quantity > 100 || r.CouponTemplateID <= 0 {
			return errors.New("优惠券奖励须选择券模板，数量为 0～100")
		}
	case "balance":
		if r.Quantity != 0 {
			return errors.New("余额奖励不能同时配置奖励数量")
		}
	case "chances", "physical", "other":
		if r.Amount != 0 {
			return errors.New("非余额奖励不能同时配置奖励金额")
		}
		if r.Type != "chances" && r.Quantity > 0 && strings.TrimSpace(r.PrizeName) == "" {
			return errors.New("实物和自定义奖励须填写奖品名称")
		}
	default:
		return errors.New("奖励类型不支持")
	}
	return nil
}

func (r GrowthReward) description() string {
	switch r.Type {
	case "coupon":
		return fmt.Sprintf("充值优惠券 × %d", r.Quantity)
	case "balance":
		return "余额 USD " + decimal.NewFromFloat(r.Amount).String()
	case "chances":
		return fmt.Sprintf("%d 次抽奖机会", r.Quantity)
	default:
		return fmt.Sprintf("%s × %d", replyPlainLabel(r.PrizeName), r.Quantity)
	}
}

func (r GrowthReward) deliveryNote() string {
	switch r.Type {
	case "coupon":
		return "已发到网站券包，发送「我的优惠券」查看"
	case "balance":
		return "已到账"
	case "chances":
		return "已到账"
	default:
		instructions := strings.TrimSpace(r.ClaimInstructions)
		if instructions == "" {
			instructions = "请联系管理员领取。"
		}
		return "待管理员发放：" + replyPlainLabel(instructions)
	}
}

func activityRewardClaimed(ctx context.Context, tx *sql.Tx, key string) (bool, error) {
	var claimed bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM qq_bot_reward_codes WHERE source_key=$1) OR EXISTS(SELECT 1 FROM qq_bot_lottery_chance_grants WHERE source_key=$1)`, key).Scan(&claimed)
	return claimed, err
}

// The caller holds the active user row lock. The entitlement, fulfillment
// snapshot, balance/chances and visible balance history commit atomically.
func grantActivityReward(ctx context.Context, tx *sql.Tx, c Config, j Job, uid int64, key, source, name string, reward GrowthReward, at time.Time) (bool, error) {
	if err := reward.validate(); err != nil {
		return false, err
	}
	if !reward.enabled() {
		return false, nil
	}
	claimed, err := activityRewardClaimed(ctx, tx, key)
	if err != nil || claimed {
		return false, err
	}
	if reward.Type == "balance" {
		if c.RewardBudgetDaily != nil {
			at = time.Now()
		}
		if err = checkRewardBudget(ctx, tx, c, reward.Amount, at); err != nil {
			return false, err
		}
	}
	status := "fulfilled"
	if reward.Type == "physical" || reward.Type == "other" {
		status = "pending"
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO qq_bot_reward_codes(app_id,code_hash,code_hint,batch_name,reward_type,amount,quantity,prize_name,claim_instructions,status,user_id,member_id,group_id,redeemed_at,fulfilled_at,created_at,source_kind,source_key)
 VALUES($1,NULL,'',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,CASE WHEN $8='fulfilled' THEN $12::timestamptz ELSE NULL END,$12,$13,$14)
 ON CONFLICT(source_key) DO NOTHING RETURNING id`, c.AppID, name, reward.Type, reward.Amount, reward.Quantity, strings.TrimSpace(reward.PrizeName), strings.TrimSpace(reward.ClaimInstructions), status, uid, j.MemberID, j.GroupID, at, source, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch reward.Type {
	case "coupon":
		_, err = coupon.Issue(ctx, tx, reward.CouponTemplateID, uid, reward.Quantity, "qq_"+source, key, at)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE qq_bot_reward_codes SET coupon_template_id=$2 WHERE id=$1`, id, reward.CouponTemplateID)
		}
	case "chances":
		_, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_lottery_chance_grants(source_key,user_id,chances) VALUES($1,$2,$3)`, key, uid, reward.Quantity)
	case "balance":
		var res sql.Result
		res, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance+$2,updated_at=NOW() WHERE id=$1 AND status='active' AND deleted_at IS NULL`, uid, reward.Amount)
		if err != nil {
			return false, err
		}
		n, e := res.RowsAffected()
		if e != nil {
			return false, e
		}
		if n != 1 {
			return false, errors.New("账户不可用")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO qq_bot_rewards(reward_key,app_id,group_id,member_id,user_id,kind,amount,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, key, c.AppID, j.GroupID, j.MemberID, uid, source, reward.Amount, at)
		if err != nil {
			return false, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO redeem_codes(code,type,value,status,used_by,used_at,notes) VALUES($1,'admin_balance',$2,'used',$3,$4,$5)`, "QQ"+randomID(), reward.Amount, uid, at, fmt.Sprintf("QQ %s reward #%d", source, id))
	}
	return err == nil, err
}

// Completed tasks display the immutable award and current fulfillment state,
// even if an administrator has since changed that task's configured reward.
func activityRewardSnapshot(ctx context.Context, tx *sql.Tx, key string) (GrowthReward, string, error) {
	var reward GrowthReward
	var status string
	err := tx.QueryRowContext(ctx, `SELECT reward_type,amount,quantity,prize_name,claim_instructions,status,COALESCE(coupon_template_id,0) FROM qq_bot_reward_codes WHERE source_key=$1`, key).Scan(&reward.Type, &reward.Amount, &reward.Quantity, &reward.PrizeName, &reward.ClaimInstructions, &status, &reward.CouponTemplateID)
	if !errors.Is(err, sql.ErrNoRows) {
		return reward, status, err
	}
	reward = GrowthReward{Type: "chances"}
	err = tx.QueryRowContext(ctx, `SELECT chances FROM qq_bot_lottery_chance_grants WHERE source_key=$1`, key).Scan(&reward.Quantity)
	if errors.Is(err, sql.ErrNoRows) {
		return GrowthReward{}, "", nil
	}
	return reward, "fulfilled", err
}
