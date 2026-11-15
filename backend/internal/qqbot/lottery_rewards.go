package qqbot

import "fmt"

// Prize contents are independent: a win may credit balance, grant more draws,
// or do both. Zero-value legacy prizes retain their existing rules display.
func (p LotteryPrize) description() string {
	if p.CouponQuantity > 0 {
		return fmt.Sprintf("充值优惠券 × %d；余额 USD %.2f；抽奖 %d 次", p.CouponQuantity, p.Amount, p.Chances)
	}
	if p.Chances == 0 {
		return fmt.Sprintf("USD %.2f", p.Amount)
	}
	if p.Amount == 0 {
		return fmt.Sprintf("抽奖次数 %d 次", p.Chances)
	}
	return fmt.Sprintf("USD %.2f + 抽奖次数 %d 次", p.Amount, p.Chances)
}
