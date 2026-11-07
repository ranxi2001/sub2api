import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import UserKeyRiskNotices from '../UserKeyRiskNotices.vue'
import { userKeyRiskApi, type UserKeyRiskNotice } from '../keyRiskApi'
import en from '@/i18n/locales/en/ipAudit'
import zh from '@/i18n/locales/zh/ipAudit'
vi.mock('../keyRiskApi', () => ({ userKeyRiskApi: { list: vi.fn(), acknowledge: vi.fn() } }))
const notice = (): UserKeyRiskNotice => ({ event_id: 4, generation: 2, rule: 'cross_user_ip', state: 'pending', key_ids: [11], keys: [{ id: 11, name: '<Production key>' }], first_seen: '2026-11-07T01:00:00Z', last_seen: '2026-11-07T01:01:00Z', attempts: 1, max_reminders: 3, acknowledged_at: null })
function render(locale = 'en') {
  return mount(UserKeyRiskNotices, { global: { plugins: [createI18n({ legacy: false, locale, messages: { en: { ipAudit: en, common: { refresh: 'Refresh', cancel: 'Cancel' } }, zh: { ipAudit: zh, common: { refresh: '刷新', cancel: '取消' } } } })] } })
}
beforeEach(() => { vi.resetAllMocks(); vi.mocked(userKeyRiskApi.list).mockResolvedValue({ items: [notice()] }); vi.mocked(userKeyRiskApi.acknowledge).mockResolvedValue() })
describe('Customer risk acknowledgement', () => {
  it.each(['en', 'zh'])('renders the risk and confirmation boundary in %s', async locale => {
    const w = render(locale); await flushPromises()
    expect(w.text()).not.toContain('ipAudit.')
    expect(w.text()).toContain('<Production key>#11')
    expect(w.find('production').exists()).toBe(false)
    expect(userKeyRiskApi.acknowledge).not.toHaveBeenCalled()
    w.unmount()
  })
  it('requires confirmation and displays the server acknowledgement', async () => {
    const w = render(); await flushPromises()
    await w.findAll('button').find(b => b.text() === en.keyRisk.acknowledge)!.trigger('click')
    expect(userKeyRiskApi.acknowledge).not.toHaveBeenCalled()
    expect(w.get('[role=group]').text()).toContain(en.keyRisk.ackConfirm)
    vi.mocked(userKeyRiskApi.list).mockResolvedValue({ items: [{ ...notice(), acknowledged_at: '2026-11-07T02:00:00Z' }] })
    await w.get('[data-test=confirm-risk]').trigger('click'); await flushPromises()
    expect(userKeyRiskApi.acknowledge).toHaveBeenCalledWith(notice())
    expect(w.text()).toContain(en.keyRisk.acknowledged)
    expect(w.findAll('button').some(b => b.text() === en.keyRisk.acknowledge)).toBe(false)
    w.unmount()
  })
  it('keeps an unsuccessful confirmation actionable', async () => {
    vi.mocked(userKeyRiskApi.acknowledge).mockRejectedValue(new Error('stale episode'))
    const w = render(); await flushPromises()
    await w.findAll('button').find(b => b.text() === en.keyRisk.acknowledge)!.trigger('click')
    await w.get('[data-test=confirm-risk]').trigger('click'); await flushPromises()
    expect(w.get('[role=alert]').text()).toBe(en.keyRisk.error)
    expect(w.find('[data-test=confirm-risk]').exists()).toBe(true)
    expect(w.find('[role=status]').exists()).toBe(false)
    w.unmount()
  })
  it('permits acknowledgement after the reminder cap is reached', async () => {
    vi.mocked(userKeyRiskApi.list).mockResolvedValue({ items: [{ ...notice(), attempts: 3 }] })
    const w = render(); await flushPromises()
    expect(w.text()).toContain(en.keyRisk.limitReached)
    expect(w.findAll('button').some(b => b.text() === en.keyRisk.acknowledge)).toBe(true)
    w.unmount()
  })
})
