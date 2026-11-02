import type { LotteryPrize } from '@/api/lottery'

export type ReelPrize = Pick<LotteryPrize, 'id' | 'name' | 'kind' | 'amount' | 'plan_name' | 'validity_days'>
export const REEL_LENGTH = 70
export const WINNER_INDEX = 55
export const REEL_DURATION = 5600
export const REEL_STAGGER = 150

export function landingFraction(random: () => number = Math.random) {
  // Keep the pointer inside the winning card, including at an edge landing.
  if (random() < 0.75) return random() < 0.5 ? 0.09 + random() * 0.09 : 0.82 + random() * 0.09
  return 0.35 + random() * 0.3
}

export function reelOffset(index: number, cardWidth: number, gap: number, fraction: number) {
  return index * (cardWidth + gap) + cardWidth * fraction
}

export function reelKeyframes(start: number, destination: number, cardWidth: number): Keyframe[] {
  const transform = (offset: number) => `translate3d(${-offset}px, 0, 0)`
  // Reserve the last two seconds for a continuous, slow approach to the same result.
  const approach = Math.max(start, destination - cardWidth * 0.85)
  const slope = (destination - approach) * 2 * 0.62 / (0.38 * Math.max(1, approach - start))
  const controlY = Math.max(0.55, 1 - 0.75 * slope)
  return [
    { transform: transform(start), offset: 0, easing: `cubic-bezier(0.12, 0.55, 0.25, ${controlY})` },
    { transform: transform(approach), offset: 0.62, easing: 'cubic-bezier(0.2, 0.4, 0.55, 1)' },
    { transform: transform(destination), offset: 1 },
  ]
}

export function availablePrizes(prizes: LotteryPrize[]) {
  return prizes.filter(p => p.available && p.enabled && p.stock !== 0 && p.weight > 0)
}

export function createReel(prizes: LotteryPrize[], winner?: ReelPrize, random: () => number = Math.random): ReelPrize[] {
  const available = availablePrizes(prizes)
  if (!available.length) return winner ? Array.from({ length: REEL_LENGTH }, () => winner) : []
  const total = available.reduce((sum, p) => sum + p.weight, 0)
  const pick = () => {
    let cursor = random() * total
    return available.find(p => (cursor -= p.weight) < 0) ?? available[available.length - 1]
  }
  const cards: ReelPrize[] = Array.from({ length: REEL_LENGTH }, pick)
  if (winner) {
    cards[WINNER_INDEX] = winner
    // Neighbour placement is presentation only, never a prize selection input.
    const highlights = available.filter(p => p.kind === 'balance' && p.id !== winner.id)
      .sort((a, b) => b.amount - a.amount)
    if (highlights.length) {
      cards[WINNER_INDEX - 1] = highlights[0]
      cards[WINNER_INDEX + 1] = highlights[1] ?? highlights[0]
    }
  }
  return cards
}

export function prizeColor(prize: ReelPrize, prizes: LotteryPrize[]) {
  if (prize.kind === 'subscription') return '#7c3aed'
  if (prize.kind === 'text') return '#db2777'
  const amounts = [...new Set(prizes.filter(p => p.kind === 'balance').map(p => p.amount))].sort((a, b) => a - b)
  if (amounts.length > 1 && prize.amount >= amounts[amounts.length - 1]) return '#b77908'
  return ['#0d9488', '#2563eb', '#7c3aed'][Math.max(0, amounts.indexOf(prize.amount)) % 3]
}
