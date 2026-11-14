import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import QQBotRankingSettings from '../QQBotRankingSettings.vue'
import type { QQRankingConfig } from '@/api/qqbot'

const api = vi.hoisted(() => ({ rankingConfig: vi.fn(), saveRankings: vi.fn() }))
vi.mock('@/api/qqbot', () => ({ qqbotAPI: api }))
function config(): QQRankingConfig {
  const prizes = () => [1, 2, 3].map(rank => ({ rank, reward: { type: 'chances' as const, amount: 0, quantity: 0, prize_name: '', claim_instructions: '' } }))
  return { enabled: true, rewards_enabled: false, effective_day: '', token_rewards: prizes(), amount_rewards: prizes() }
}
function setup(locale = 'zh') {
  const i18n = createI18n({ legacy: false, locale, messages: { zh, en } })
  return { wrapper: mount(QQBotRankingSettings, { global: { plugins: [i18n] } }), t: i18n.global.t }
}
describe('QQ daily ranking prizes', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    let saved = config()
    api.rankingConfig.mockImplementation(async () => structuredClone(saved))
    api.saveRankings.mockImplementation(async data => { saved = structuredClone(data); saved.effective_day = '2026-10-22'; return structuredClone(saved) })
  })
  it.each(['zh', 'en'])('%s saves independent board prizes and reloads exact reward types', async locale => {
    const { wrapper: w, t } = setup(locale); await flushPromises()
    expect(w.text()).toContain('今日排行榜')
    expect(w.text()).toContain(t('qqbot.rankings.help'))
    expect(w.get('[data-testid="token_rewards-rank-0"]').findAll('option')).toHaveLength(10)
    expect((w.get('[data-testid="rankings-rewards-enabled"]').element as HTMLInputElement).checked).toBe(false)
    await w.get('[data-testid="token_rewards-reward-type-0"]').setValue('balance')
    await w.get('[data-testid="token_rewards-reward-amount-0"]').setValue('1.00000001')
    await w.get('[data-testid="amount_rewards-reward-type-0"]').setValue('physical')
    await w.get('[data-testid="amount_rewards-prize-name-0"]').setValue('Cup')
    await w.get('[data-testid="amount_rewards-instructions-0"]').setValue('Contact admin')
    await w.get('[data-testid="rankings-rewards-enabled"]').setValue(true)
    await w.get('form').trigger('submit'); await flushPromises()
    expect(api.saveRankings).toHaveBeenCalledTimes(1)
    const sent = api.saveRankings.mock.calls[0]![0]
    expect(sent.token_rewards[0]).toEqual({ rank: 1, reward: { type: 'balance', amount: 1.00000001, quantity: 0, prize_name: '', claim_instructions: '' } })
    expect(sent.amount_rewards[0]).toEqual({ rank: 1, reward: { type: 'physical', amount: 0, quantity: 1, prize_name: 'Cup', claim_instructions: 'Contact admin' } })
    expect(w.get('[role="status"]').text()).toBe(t('common.saved'))
    expect(w.text()).toContain('2026-10-22')
    w.unmount()
    const reloaded = setup(locale).wrapper; await flushPromises()
    expect((reloaded.get('[data-testid="token_rewards-reward-amount-0"]').element as HTMLInputElement).value).toBe('1.00000001')
    expect((reloaded.get('[data-testid="amount_rewards-prize-name-0"]').element as HTMLInputElement).value).toBe('Cup')
    reloaded.unmount()
  })
  it('rejects duplicate ranks, zero enabled rewards and invalid prize information', async () => {
    const { wrapper: w } = setup(); await flushPromises()
    await w.get('[data-testid="rankings-rewards-enabled"]').setValue(true)
    await w.get('form').trigger('submit')
    expect(api.saveRankings).not.toHaveBeenCalled()
    await w.get('[data-testid="token_rewards-chances-0"]').setValue('3')
    await w.get('[data-testid="token_rewards-rank-0"]').setValue('2')
    await w.get('form').trigger('submit')
    expect(api.saveRankings).not.toHaveBeenCalled()
    await w.get('[data-testid="token_rewards-rank-0"]').setValue('1')
    await w.get('[data-testid="token_rewards-reward-type-0"]').setValue('other')
    await w.get('form').trigger('submit')
    expect(api.saveRankings).not.toHaveBeenCalled()
    await w.get('[data-testid="token_rewards-prize-name-0"]').setValue('Voucher')
    await w.get('form').trigger('submit'); await flushPromises()
    expect(api.saveRankings).toHaveBeenCalledTimes(1)
    w.unmount()
  })
  it('limits prizes to ten unique places and preserves an empty board', async () => {
    const { wrapper: w } = setup(); await flushPromises()
    for (let i = 3; i < 10; i++) await w.get('[data-testid="token_rewards-add"]').trigger('click')
    expect(w.get('[data-testid="token_rewards-add"]').attributes('disabled')).toBeDefined()
    expect(w.findAll('[data-testid^="token_rewards-rank-"]')).toHaveLength(10)
    await w.get('[data-testid="token_rewards-remove-4"]').trigger('click')
    await w.get('[data-testid="token_rewards-add"]').trigger('click')
    expect((w.get('[data-testid="token_rewards-rank-9"]').element as HTMLSelectElement).value).toBe('5')
    for (let i = 0; i < 3; i++) await w.get('[data-testid="amount_rewards-remove-0"]').trigger('click')
    await w.get('form').trigger('submit'); await flushPromises()
    expect(api.saveRankings.mock.calls[0]![0].amount_rewards).toEqual([])
    w.unmount()
  })
  it('retains unsaved prizes on failure and allows retrying', async () => {
    const { wrapper: w } = setup(); await flushPromises()
    await w.get('[data-testid="token_rewards-chances-0"]').setValue('4')
    api.saveRankings.mockRejectedValueOnce(new Error('Save failed'))
    await w.get('form').trigger('submit'); await flushPromises()
    expect(w.get('[role="alert"]').text()).toBe('Save failed')
    expect((w.get('[data-testid="token_rewards-chances-0"]').element as HTMLInputElement).value).toBe('4')
    await w.get('form').trigger('submit'); await flushPromises()
    expect(api.saveRankings).toHaveBeenCalledTimes(2)
    expect(w.find('[role="alert"]').exists()).toBe(false)
    w.unmount()
  })
  it('reports loading failure without displaying an editable empty configuration', async () => {
    api.rankingConfig.mockRejectedValueOnce(new Error('Load failed'))
    const { wrapper: w } = setup(); await flushPromises()
    expect(w.get('[role="alert"]').text()).toBe('Load failed')
    expect(w.find('form').exists()).toBe(false)
    w.unmount()
  })
})
