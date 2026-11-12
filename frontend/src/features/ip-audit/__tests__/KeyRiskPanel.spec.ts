import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import KeyRiskPanel from '../KeyRiskPanel.vue'
import { keyRiskApi } from '../keyRiskApi'
import en from '@/i18n/locales/en/ipAudit'
import zh from '@/i18n/locales/zh/ipAudit'
vi.mock('@/components/common/NewFeatureBadge.vue', () => ({ default: { template: '<span />' } }))
vi.mock('../keyRiskApi', () => ({ keyRiskApi: { get: vi.fn(), save: vi.fn(), action: vi.fn() } }))
const page = () => ({ config: { enabled: true, auto_email: false, age_email: false, max_reminders: 3, age_days: 90, window_minutes: 30, ips_per_key: 2, trusted_networks: [] }, items: [{ id: 1, generation: 1, users: [{ id: 1, name: 'Customer One' }, { id: 2, name: 'two@example.invalid' }], keys: [{ id: 11, name: 'Production key' }], recipients: [], rule: 'key_ip_spread', subject: '11', state: 'pending', first_seen: '2026-11-05T01:00:00Z', last_seen: '2026-11-05T01:01:00Z', evidence: { ips: ['192.0.2.1', '192.0.2.2'], user_ids: [1, 2, 3], key_ids: [11, 22, 33], requests: 3, window_minutes: 30, first: '', last: '' } }], last_scan_at: '2026-11-05T01:01:00Z', coverage_issue: '', pending: 0, sent: 0, uncertain: 0 })
function render(locale = 'en') { return mount(KeyRiskPanel, { global: { plugins: [createI18n({ legacy: false, locale, messages: { en: { ipAudit: en, common: { save: 'Save', refresh: 'Refresh', actions: 'Actions', cancel: 'Cancel' } }, zh: { ipAudit: zh, common: { save: '保存', refresh: '刷新', actions: '操作', cancel: '取消' } } } })] } }) }
beforeEach(() => { vi.resetAllMocks(); vi.mocked(keyRiskApi.get).mockResolvedValue(page()); vi.mocked(keyRiskApi.save).mockResolvedValue(); vi.mocked(keyRiskApi.action).mockResolvedValue({queued:true,recipients:3}) })
describe('Key risk controls', () => {
  it('shows names before muted IDs and retains missing identities', async () => {
    const w = render(); await flushPromises()
    expect(w.text()).toContain('Customer One#1')
    expect(w.text()).toContain('two@example.invalid#2')
    expect(w.text()).toContain('Production key#11')
    expect(w.text()).toContain(`${en.keyRisk.unnamed}#33`)
    expect(w.text()).not.toContain('UID:')
    w.unmount()
  })
  it('reports suppression instead of claiming a notice was queued', async () => {
    vi.mocked(keyRiskApi.action).mockResolvedValue({ queued: false, recipients: 0 })
    const w = render(); await flushPromises()
    await w.findAll('button').find(b => b.text() === en.keyRisk.notify)!.trigger('click')
    await w.get('[role=dialog] button').trigger('click'); await flushPromises()
    expect(w.get('[role=status]').text()).toBe(en.keyRisk.suppressed)
    w.unmount()
  })
  it.each(['en', 'zh'])('renders translated controls in %s and keeps emails opt-in', async locale => { const w = render(locale); await flushPromises(); expect(w.text()).not.toContain('ipAudit.'); const boxes = w.findAll<HTMLInputElement>('input[type=checkbox]'); expect(boxes[1]!.element.checked).toBe(false); expect(boxes[2]!.element.checked).toBe(false); w.unmount() })
  it('confirms notices before queuing and normalizes trusted networks', async () => { const w = render(); await flushPromises(); const notify = w.findAll('button').find(b => b.text() === en.keyRisk.notify)!; await notify.trigger('click'); expect(keyRiskApi.action).not.toHaveBeenCalled(); await w.get('[role=dialog] button').trigger('click'); await flushPromises(); expect(keyRiskApi.action).toHaveBeenCalledWith(1, 'notify'); await w.get('textarea').setValue(' 192.0.2.0/24\n\n198.51.100.1 '); await w.get('form').trigger('submit'); await flushPromises(); expect(keyRiskApi.save).toHaveBeenCalledWith(expect.objectContaining({ trusted_networks: ['192.0.2.0/24', '198.51.100.1'] })); w.unmount() })
  it.each(['en', 'zh'])('confirms per-key source restrictions separately in %s', async locale => {
    const messages = locale === 'zh' ? zh : en
    const w = render(locale); await flushPromises()
    await w.findAll('button').find(b => b.text() === messages.keyRisk.restrict)!.trigger('click')
    expect(w.get('[role=dialog]').text()).toContain(messages.keyRisk.restrictConfirm)
    expect(w.get('[role=dialog] button').text()).toBe(messages.keyRisk.confirmAction)
    expect(w.get('[role=dialog] button').attributes('disabled')).toBeDefined()
    expect(keyRiskApi.action).not.toHaveBeenCalled()
    await w.get('[data-testid=allowed-networks]').setValue(' 192.0.2.1\n\n198.51.100.0/24 ')
    await w.get('[role=dialog] button').trigger('click'); await flushPromises()
    expect(keyRiskApi.action).toHaveBeenCalledWith(1, 'restrict', { generation: 1, confirm: true, allowed_networks: ['192.0.2.1', '198.51.100.0/24'] })
    expect(w.get('[role=status]').text()).toBe(messages.keyRisk.restrictSaved)
    w.unmount()
  })
  it('keeps failed restrictions open without claiming success', async () => {
    vi.mocked(keyRiskApi.action).mockRejectedValue(new Error('stale generation'))
    const w = render(); await flushPromises()
    await w.findAll('button').find(b => b.text() === en.keyRisk.restrict)!.trigger('click')
    await w.get('[data-testid=allowed-networks]').setValue('192.0.2.1')
    await w.get('[role=dialog] button').trigger('click'); await flushPromises()
    expect(w.find('[role=dialog]').exists()).toBe(true)
    expect(w.get('[role=alert]').text()).toBe(en.keyRisk.error)
    expect(w.find('[role=status]').exists()).toBe(false)
    w.unmount()
  })
  it('retains historical shared-exit records without notify, reopen or block actions', async () => {
    const snapshot = page()
    snapshot.items[0]!.rule = 'cross_user_ip'; snapshot.items[0]!.state = 'resolved'; snapshot.items[0]!.subject = '192.0.2.1'
    vi.mocked(keyRiskApi.get).mockResolvedValue(snapshot)
    const w = render(); await flushPromises()
    expect(w.text()).toContain(en.keyRisk.retired)
    const labels = w.findAll('button').map(button => button.text())
    for (const label of [en.keyRisk.notify, en.keyRisk.reopen, en.keyRisk.block, en.keyRisk.restrict]) expect(labels).not.toContain(label)
    expect(w.text()).not.toContain(en.keyRisk.users_per_ip)
    w.unmount()
  })
})
