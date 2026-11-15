import type { QQGrowthReward } from '@/api/qqbot'

export function normalizeGrowthReward(reward?: QQGrowthReward, chances = 0): QQGrowthReward {
  return { type: 'chances', amount: 0, quantity: chances, prize_name: '', claim_instructions: '', ...reward }
}

export function rewardEnabled(reward: QQGrowthReward) {
  return reward.type === 'balance' ? reward.amount > 0 : reward.quantity > 0
}

export function validGrowthReward(reward: QQGrowthReward) {
  if (!['balance', 'chances', 'physical', 'other', 'coupon'].includes(reward.type)
    || !Number.isFinite(reward.amount) || reward.amount < 0 || reward.amount > 1000 || Number(reward.amount.toFixed(8)) !== reward.amount
    || !Number.isInteger(reward.quantity) || reward.quantity < 0 || reward.quantity > 1000
    || Array.from(reward.prize_name).length > 100 || /[\r\n\t]/.test(reward.prize_name)
    || Array.from(reward.claim_instructions).length > 500) return false
  if (reward.type === 'coupon') return reward.amount === 0 && Number.isSafeInteger(reward.coupon_template_id) && (reward.coupon_template_id ?? 0) > 0 && reward.quantity <= 100
  if (reward.type === 'balance') return reward.quantity === 0
  if (reward.amount !== 0) return false
  return reward.type === 'chances' || reward.quantity === 0 || reward.prize_name.trim().length > 0
}
