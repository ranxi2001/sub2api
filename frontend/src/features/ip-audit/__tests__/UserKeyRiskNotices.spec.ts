import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import UserKeyRiskNotices from '../UserKeyRiskNotices.vue'
import { userKeyRiskApi, type UserKeyRiskNotice } from '../keyRiskApi'
import en from '@/i18n/locales/en/ipAudit'
import zh from '@/i18n/locales/zh/ipAudit'
enableAutoUnmount(afterEach)
afterEach(() => { document.body.replaceChildren() })
vi.mock('../keyRiskApi', () => ({ userKeyRiskApi: { list: vi.fn(), acknowledge: vi.fn() } }))
const notice = (): UserKeyRiskNotice => ({ event_id: 4, generation: 2, rule: 'cross_user_ip', state: 'pending', key_ids: [11], keys: [{ id: 11, name: '<Production key>' }], first_seen: '2026-11-07T01:00:00Z', last_seen: '2026-11-07T01:01:00Z', attempts: 1, max_reminders: 3, acknowledged_at: null })
function render(locale = 'en') {
  // jsdom invalidates computed-style caches only for nodes attached to its document.
  return mount(UserKeyRiskNotices, { attachTo: document.body, global: { plugins: [createI18n({ legacy: false, locale, messages: { en: { ipAudit: en, common: { refresh: 'Refresh', cancel: 'Cancel' } }, zh: { ipAudit: zh, common: { refresh: '刷新', cancel: '取消' } } } })] } })
}
beforeEach(() => { vi.resetAllMocks(); vi.mocked(userKeyRiskApi.list).mockResolvedValue({ items: [notice()] }); vi.mocked(userKeyRiskApi.acknowledge).mockResolvedValue() })
describe('Customer risk acknowledgement', () => {
  it.each(['en', 'zh'])('renders the risk and confirmation boundary in %s', async locale => {
    const w = render(locale); await flushPromises()
    expect(w.get('[data-test=toggle-risk-notices]').attributes('aria-expanded')).toBe('false')
    const details = w.get('[data-test=risk-notices-details]')
    expect(details.isVisible()).toBe(false)
    expect(w.get('[data-test=toggle-risk-notices]').attributes('aria-controls')).toBe(details.attributes('id'))
    expect(w.get('[data-test=toggle-risk-notices]').text()).not.toContain('<Production key>')
    await w.get('[data-test=toggle-risk-notices]').trigger('click')
    expect(details.isVisible()).toBe(true)
    expect(w.text()).not.toContain('ipAudit.')
    expect(w.text()).toContain('<Production key>#11')
    expect(w.find('production').exists()).toBe(false)
    expect(userKeyRiskApi.acknowledge).not.toHaveBeenCalled()
    w.unmount()
  })
  it('requires confirmation and displays the server acknowledgement', async () => {
    const w = render(); await flushPromises()
    await w.get('[data-test=toggle-risk-notices]').trigger('click')
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
    await w.get('[data-test=toggle-risk-notices]').trigger('click')
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
    await w.get('[data-test=toggle-risk-notices]').trigger('click')
    expect(w.text()).toContain(en.keyRisk.limitReached)
    expect(w.findAll('button').some(b => b.text() === en.keyRisk.acknowledge)).toBe(true)
    w.unmount()
  })
  it('collapses details without acknowledging or fetching the notices again', async () => {
    const w = render(); await flushPromises()
    const toggle = w.get('[data-test=toggle-risk-notices]')
    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('true')
    expect(w.get('[data-test=risk-notices-details]').text()).toContain('<Production key>#11')
    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(w.get('[data-test=risk-notices-details]').isVisible()).toBe(false)
    expect(toggle.attributes('aria-controls')).toBe(w.get('[data-test=risk-notices-details]').attributes('id'))
    expect(userKeyRiskApi.acknowledge).not.toHaveBeenCalled()
    expect(userKeyRiskApi.list).toHaveBeenCalledTimes(1)
    w.unmount()
    const reopened = render(); await flushPromises()
    expect(reopened.get('[data-test=toggle-risk-notices]').attributes('aria-expanded')).toBe('false')
    expect(reopened.get('[data-test=risk-notices-details]').isVisible()).toBe(false)
    reopened.unmount()
  })
  it('keeps each disclosure linked to its own mounted panel', async () => {
    const w = mount({ components: { UserKeyRiskNotices }, template: '<div><UserKeyRiskNotices /><UserKeyRiskNotices /></div>' }, {
      attachTo: document.body,
      global: { plugins: [createI18n({ legacy: false, locale: 'en', messages: { en: { ipAudit: en, common: { refresh: 'Refresh', cancel: 'Cancel' } } } })] }
    })
    await flushPromises()
    const panels = w.findAll('[data-test=risk-notices-details]')
    const toggles = w.findAll('[data-test=toggle-risk-notices]')
    expect(panels).toHaveLength(2)
    expect(panels[0]!.attributes('id')).not.toBe(panels[1]!.attributes('id'))
    expect(toggles.map(toggle => toggle.attributes('aria-controls'))).toEqual(panels.map(panel => panel.attributes('id')))
    await toggles[0]!.trigger('click')
    expect(panels[0]!.isVisible()).toBe(true)
    expect(panels[1]!.isVisible()).toBe(false)
    const labelledSections = w.findAll('section')
    expect(labelledSections.map(section => section.attributes('aria-labelledby'))).toEqual(w.findAll('h2').map(title => title.attributes('id')))
    w.unmount()
  })
})
