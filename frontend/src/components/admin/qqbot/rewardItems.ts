export type RewardKind = 'balance' | 'chances' | 'coupon'
export type RewardAmountField = 'amount' | 'min_amount' | 'invite_reward'
export type RewardValues = Partial<Record<RewardAmountField | 'max_amount' | 'chances' | 'coupon_template_id' | 'coupon_quantity', number>>

export function validRewardAmount(value: number) {
  return Number.isFinite(value) && value >= 0 && value <= 1000 && Number(value.toFixed(8)) === value
}

export function validCouponReward(reward: RewardValues) {
  const quantity = reward.coupon_quantity ?? 0
  const template = reward.coupon_template_id ?? 0
  return Number.isInteger(quantity) && quantity >= 0 && quantity <= 100
    && Number.isSafeInteger(template) && template >= 0 && (quantity === 0 || template > 0)
}
