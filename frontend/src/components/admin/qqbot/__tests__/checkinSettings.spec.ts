import { beforeEach, describe, expect, it, vi } from 'vitest'
import { loadCheckinConfig, saveCheckinSettings, saveGrowthSettings, validCheckinDistribution } from '../checkinSettings'
import type { QQCheckinConfig } from '@/api/qqbot'
const api = vi.hoisted(() => ({ checkinConfig: vi.fn(), saveCheckin: vi.fn() }))
vi.mock('@/api/qqbot', () => ({ qqbotAPI: api }))

describe('check-in draft revision protection', () => {
  const draft = () => ({ enabled: true, min_amount: 0.1, max_amount: 0.2, min_recharge_total: 0, min_recharge_30d: 0, recharge_window_days: 30, streak_days: 7, streak_chances: 0, streak_reward: { type: 'chances', amount: 0, quantity: 0, prize_name: '', claim_instructions: '' }, growth: { tasks: [], periodic_tasks: [] } }) as QQCheckinConfig
  beforeEach(() => {
    vi.resetAllMocks()
    api.checkinConfig.mockResolvedValue({ ...draft(), _version: 'fresh-whole-config', _sections: { checkin: 'fresh-checkin' } })
    api.saveCheckin.mockImplementation(async value => value)
  })
  it('persists base coupons and preserves them while saving growth tasks', async () => {
    api.checkinConfig.mockResolvedValue({ ...draft(), coupon_template_id: 12, coupon_quantity: 2 })
    const config = await loadCheckinConfig()
    await saveGrowthSettings(config)
    expect(api.saveCheckin.mock.lastCall![0]).toEqual(expect.objectContaining({ coupon_template_id: 12, coupon_quantity: 2 }))
    config.coupon_quantity = 3
    await saveCheckinSettings(config)
    expect(api.saveCheckin.mock.lastCall![0]).toEqual(expect.objectContaining({ coupon_template_id: 12, coupon_quantity: 3 }))
  })
  it('keeps the original section revision while refreshing unrelated fields', async () => {
    await saveCheckinSettings({ ...draft(), _version: 'old-whole-config', _sections: { checkin: 'old-checkin' } })
    expect(api.saveCheckin).toHaveBeenCalledWith(expect.objectContaining({ _version: 'old-checkin', _section: 'checkin' }))
  })
  it('defaults older configurations and saves edited probabilities with the draft revision', async () => {
    const config = await loadCheckinConfig()
    expect(config.milestones).toEqual([])
    expect(config.reward_distribution).toEqual({ lower_half_bps: 7000, next_quarter_bps: 2000, upper_quarter_bps: 1000 })
    config.reward_distribution = { lower_half_bps: 1234, next_quarter_bps: 4321, upper_quarter_bps: 4445 }
    await saveCheckinSettings(config)
    expect(api.saveCheckin).toHaveBeenCalledWith(expect.objectContaining({ reward_distribution: config.reward_distribution, _version: 'fresh-checkin' }))
  })
  it('preserves current probabilities while saving unrelated growth tasks', async () => {
    const reward_distribution = { lower_half_bps: 0, next_quarter_bps: 0, upper_quarter_bps: 10000 }
    api.checkinConfig.mockResolvedValue({ ...draft(), reward_distribution })
    await saveGrowthSettings(draft())
    expect(api.saveCheckin).toHaveBeenCalledWith(expect.objectContaining({ reward_distribution }))
  })
  it('validates integer basis points, zero weights and total probability', () => {
    expect(validCheckinDistribution({ lower_half_bps: 0, next_quarter_bps: 0, upper_quarter_bps: 10000 })).toBe(true)
    expect(validCheckinDistribution({ lower_half_bps: 1234, next_quarter_bps: 4321, upper_quarter_bps: 4445 })).toBe(true)
    for (const lower_half_bps of [-1, NaN, Infinity, 7000.1, 10001, 6999]) {
      expect(validCheckinDistribution({ lower_half_bps, next_quarter_bps: 2000, upper_quarter_bps: 1000 })).toBe(false)
    }
    expect(validCheckinDistribution(undefined)).toBe(false)
  })
  it('never upgrades an unversioned draft to the freshly fetched revision', async () => {
    await saveCheckinSettings(draft())
    expect(api.saveCheckin.mock.calls[0]![0]._version).toBeUndefined()
    expect(api.saveCheckin.mock.calls[0]![0]._section).toBeUndefined()
  })
  it('keeps milestone drafts scoped to check-in while growth saves preserve current milestones', async () => {
    const milestone = { id: 'seven_days', name: 'Seven days', kind: 'checkin_total', days: 7, enabled: true, reward: { type: 'chances', amount: 0, quantity: 2, prize_name: '', claim_instructions: '' } }
    api.checkinConfig.mockResolvedValue({ ...draft(), milestones: [milestone], _sections: { tasks: 'tasks-original', checkin: 'checkin-original' } })
    const config = await loadCheckinConfig()
    config.milestones[0]!.days = 30
    await saveGrowthSettings(config)
    expect(api.saveCheckin.mock.lastCall![0]).toEqual(expect.objectContaining({ milestones: [milestone], _section: 'tasks', _version: 'tasks-original' }))
    await saveCheckinSettings(config)
    expect(api.saveCheckin.mock.lastCall![0]).toEqual(expect.objectContaining({ milestones: [expect.objectContaining({ days: 30 })], _section: 'checkin', _version: 'checkin-original' }))
  })

  it('normalizes an omitted check-in metric to cumulative days', async () => {
    api.checkinConfig.mockResolvedValue({ ...draft(), milestones: [{ id: 'seven_days', name: 'Seven days', days: 7, enabled: false, reward: draft().streak_reward }] })
    expect((await loadCheckinConfig()).milestones[0]!.kind).toBe('checkin_total')
  })

})
