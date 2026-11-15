import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import QQBotRewardCodes from '../QQBotRewardCodes.vue'

const api = vi.hoisted(() => ({ rewardCodes: vi.fn(), updateRewardCode: vi.fn() }))
vi.mock('@/api/qqbot', () => ({ qqbotAPI: api }))
function setup(locale = 'zh') {
  const i18n = createI18n({ legacy: false, locale, messages: { zh, en } })
  return { wrapper: mount(QQBotRewardCodes, { global: { plugins: [i18n] } }), t: i18n.global.t }
}

describe('QQ reward code management', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    api.rewardCodes.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 })
    api.updateRewardCode.mockResolvedValue({ success: true })
  })
  it.each(['zh', 'en'])('%s shows reward records without a generation form', async locale => {
    const { wrapper: w, t } = setup(locale); await flushPromises()
    expect(w.text()).toContain(t('qqbot.redemption.title'))
    expect(w.find('[data-testid="reward-generate"]').exists()).toBe(false)
    expect(w.find('[data-testid="reward-batch"]').exists()).toBe(false)
    expect(w.text()).not.toContain('/福利')
    w.unmount()
  })
  it.each(['zh', 'en'])('%s identifies automatic growth and streak prizes and fulfills them', async locale => {
    api.rewardCodes.mockResolvedValue({ items: ['growth', 'streak', 'ranking'].map((source_kind, index) => ({ id: 10 + index, source_kind, app_id: 'app', code_hint: '', batch_name: 'Activity reward', reward_type: 'physical', amount: 0, quantity: 1, prize_name: 'Cup', claim_instructions: 'Contact admin', status: 'pending', email: 'test@example.com', user_id: 8, member_id: 'member', expires_at: null, target_user_id: null, redeemed_at: '2026-10-21T00:00:00Z', fulfilled_at: null, fulfillment_note: '' })), total: 3, page: 1, page_size: 20 })
    const { wrapper: w, t } = setup(locale); await flushPromises()
    expect(w.text()).toContain(t('qqbot.growth.sources.growth'))
    expect(w.text()).toContain(t('qqbot.growth.sources.streak'))
    expect(w.text()).toContain(t('qqbot.growth.sources.ranking'))
    expect(w.get('tbody').text()).not.toContain('…')
    await w.get('tbody button').trigger('click')
    await w.findAll('form')[1]!.trigger('submit'); await flushPromises()
    expect(api.updateRewardCode).toHaveBeenCalledWith(10, 'fulfill', '')
    w.unmount()
  })
  it.each([[0.00000001, '0.00000001'], [0.12345678, '0.12345678'], [10, '10.00']] as const)('displays balance reward %s without losing precision', async (amount, expected) => {
    api.rewardCodes.mockResolvedValue({ items: [{ id: 9, app_id: 'app', source_kind: 'growth', code_hint: '', batch_name: 'Growth reward', reward_type: 'balance', amount, quantity: 0, prize_name: '', status: 'fulfilled', email: 'test@example.com', user_id: 8, expires_at: null }], total: 1, page: 1, page_size: 20 })
    const { wrapper: w } = setup(); await flushPromises()
    expect(w.get('tbody').text()).toContain(`USD ${expected}`)
    w.unmount()
  })
  it('requires explicit fulfillment confirmation and preserves the note after failure', async () => {
    api.rewardCodes.mockResolvedValue({ items: [{ id: 7, app_id: 'app', code_hint: 'ABCD23', batch_name: 'launch', reward_type: 'physical', amount: 0, quantity: 1, prize_name: 'Cup', status: 'pending', email: 'test@example.com', user_id: 8, member_id: 'member', expires_at: null, target_user_id: null, redeemed_at: '2026-10-17T00:00:00Z', fulfilled_at: null, fulfillment_note: '' }], total: 1, page: 1, page_size: 20 })
    const { wrapper: w, t } = setup(); await flushPromises()
    await w.get('tbody button').trigger('click')
    expect(api.updateRewardCode).not.toHaveBeenCalled()
    const confirmation = w.findAll('form')[1]!
    await confirmation.get('textarea').setValue('Delivered in person')
    api.updateRewardCode.mockRejectedValueOnce(new Error('Try again'))
    await confirmation.trigger('submit'); await flushPromises()
    expect(api.updateRewardCode).toHaveBeenCalledWith(7, 'fulfill', 'Delivered in person')
    expect(confirmation.get('textarea').element.value).toBe('Delivered in person')
    await confirmation.trigger('submit'); await flushPromises()
    expect(w.text()).toContain(t('common.saved'))
    expect(w.findAll('form')).toHaveLength(1)
    w.unmount()
  })
})
