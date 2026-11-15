package qqbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/coupon"
)

func (s *Service) myCoupons(ctx context.Context, c Config, j Job, arg string) (string, error) {
	page := 1
	var err error
	if arg != "" {
		page, err = strconv.Atoi(strings.TrimSpace(arg))
		if err != nil || page < 1 || page > 10000 {
			return "用法：我的优惠券 [页码]", nil
		}
	}
	var uid int64
	err = s.db.QueryRowContext(ctx, `SELECT b.user_id FROM qq_bot_bindings b JOIN users u ON u.id=b.user_id WHERE b.app_id=$1 AND b.member_id=$2 AND u.status='active' AND u.deleted_at IS NULL`, c.AppID, j.MemberID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "请先绑定网站账号。", nil
	}
	if err != nil {
		return "", err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT snapshot,expires_at,CASE WHEN status='available' AND expires_at<=NOW() THEN 'expired' ELSE status END FROM user_recharge_coupons WHERE user_id=$1 ORDER BY CASE WHEN status='available' AND expires_at>NOW() THEN 0 ELSE 1 END,expires_at DESC,id DESC LIMIT 5 OFFSET $2`, uid, (page-1)*5)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	var b strings.Builder
	fmt.Fprintf(&b, "🎟 我的充值优惠券（第 %d 页）\n", page)
	n := 0
	labels := map[string]string{"available": "可用", "reserved": "待支付锁定", "used": "已使用", "revoked": "已作废", "expired": "已过期"}
	for rows.Next() {
		var raw []byte
		var expires time.Time
		var status string
		if err = rows.Scan(&raw, &expires, &status); err != nil {
			return "", err
		}
		var t coupon.Template
		if err = json.Unmarshal(raw, &t); err != nil {
			return "", err
		}
		rule := fmt.Sprintf("减 %s %.2f", t.Currency, t.Value)
		if t.Kind == "percent" {
			rule = fmt.Sprintf("减免 %.2f%%", t.Value)
		}
		if t.Kind == "bonus" {
			rule = fmt.Sprintf("加赠 %.2f%% 余额", t.Value)
		}
		if t.Kind == "percent" && t.MaxDiscount > 0 {
			rule += fmt.Sprintf("（最多减 %s %.2f）", t.Currency, t.MaxDiscount)
		}
		stacking := "不可叠加充值阶梯优惠"
		if t.Stackable {
			stacking = "可叠加充值阶梯优惠"
		}
		rule += "，" + stacking
		fmt.Fprintf(&b, "\n%s · %s\n充值满 %s %.2f，%s\n有效至 %s\n", replyPlainLabel(t.Name), labels[status], t.Currency, t.MinAmount, rule, expires.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04"))
		n++
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if n == 0 {
		_, _ = b.WriteString("暂无优惠券。\n")
	}
	_, _ = b.WriteString("在网站「我的优惠券」查看完整规则，在充值页选券；每单一张。")
	if n == 5 {
		fmt.Fprintf(&b, "\n下一页：我的优惠券 %d", page+1)
	}
	return b.String(), nil
}

func validateCouponRewardTemplates(ctx context.Context, db coupon.DB, config any) error {
	ids := map[int64]bool{}
	add := func(r GrowthReward) {
		if r.Type == "coupon" && r.Quantity > 0 {
			ids[r.CouponTemplateID] = true
		}
	}
	switch c := config.(type) {
	case CheckinConfig:
		if c.Enabled && c.CouponQuantity > 0 {
			ids[c.CouponTemplateID] = true
		}
		if c.Enabled {
			add(c.streakReward())
		}
		for _, m := range c.Milestones {
			if c.Enabled && m.Enabled {
				add(m.Reward)
			}
		}
		for _, task := range c.Growth.Tasks {
			if task.Enabled {
				add(task.reward())
			}
		}
		for _, task := range c.Growth.PeriodicTasks {
			if task.Enabled {
				add(task.reward())
			}
		}
	case InviteConfig:
		if c.Enabled && c.CouponQuantity > 0 {
			ids[c.CouponTemplateID] = true
		}
	case LotteryConfig:
		if !c.Enabled {
			break
		}
		for _, p := range c.Prizes {
			if p.CouponQuantity > 0 {
				ids[p.CouponTemplateID] = true
			}
		}
		if c.GuaranteeEnabled {
			for _, p := range c.GuaranteePrizes {
				if p.CouponQuantity > 0 {
					ids[p.CouponTemplateID] = true
				}
			}
		}
	case RankingConfig:
		if !c.Enabled || !c.RewardsEnabled {
			break
		}
		for _, prizes := range [][]RankingPrize{c.TokenRewards, c.AmountRewards} {
			for _, p := range prizes {
				add(p.Reward)
			}
		}
	}
	for id := range ids {
		t, err := coupon.ReadTemplate(ctx, db, id, false)
		if err != nil {
			return err
		}
		if !t.Enabled {
			return coupon.ErrLimit
		}
	}
	return nil
}

func validateOptionalCoupon(id int64, quantity int) error {
	if id < 0 || quantity < 0 || quantity > 100 || (quantity > 0 && id == 0) {
		return errors.New("优惠券奖励须选择券模板，每次发放 0～100 张")
	}
	return nil
}
func (c Config) inviteHasReward() bool {
	return c.InviteReward > 0 || (c.InviteCouponTemplateID > 0 && c.InviteCouponQuantity > 0)
}
func (c Config) inviteRewardDescription() string {
	text := fmt.Sprintf("USD %.2f", c.InviteReward)
	if c.InviteCouponQuantity > 0 {
		text += fmt.Sprintf(" + 充值优惠券 × %d", c.InviteCouponQuantity)
	}
	return text
}
