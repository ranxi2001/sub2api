import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UserAutoCreditModal from '../UserAutoCreditModal.vue'
import en from '@/i18n/locales/en/autoCredit'
import zh from '@/i18n/locales/zh/autoCredit'
import type { AutoCreditState } from '@/api/admin/autoCredit'
import type { AdminUser } from '@/types'
const mocks = vi.hoisted(() => ({ getAutoCredit: vi.fn(), saveAutoCredit: vi.fn(), settleAutoCredit: vi.fn(), role: 'admin' }))
vi.mock('@/api/admin/autoCredit', () => mocks)
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isAdmin: mocks.role === 'admin', user: { id: 41 } }) }))
enableAutoUnmount(afterEach)
function state(): AutoCreditState {
  return { config: { enabled: true, version: 3, threshold: '1.00000001', amount: '5.00000001', cap: '10.00000001' }, outstanding: '5.00000001', cap_blocked: true, has_more: true, next_before_id: 9,
    entries: [{ id: 9, user_id: 2, epoch: 0, source: 'auto_credit', trigger: 'initial-admission', amount: '5.00000001', balance_before: '0.00000000', balance_after: '5.00000001', note: '自动充值需索取费用', status: 'pending', created_at: '2026-11-04T00:00:00Z', settled_at: null, settled_by: null }] }
}
function render(locale = 'en') {
  return mount(UserAutoCreditModal, { props: { show: true, user: { id: 2, email: 'fixture@example.invalid' } as AdminUser },
    global: { plugins: [createI18n({ legacy: false, locale, messages: { en: { autoCredit: en }, zh: { autoCredit: zh } } })], stubs: { BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' }, NewFeatureBadge: true } } })
}
beforeEach(() => { vi.clearAllMocks(); mocks.role = 'admin'; mocks.getAutoCredit.mockResolvedValue(state()); mocks.saveAutoCredit.mockResolvedValue(state()); mocks.settleAutoCredit.mockResolvedValue({ ...state(), entries: [], outstanding: '0.00000000' }) })
describe('VIP auto credit', () => {
  it.each(['en', 'zh'])('defaults legacy notifications off and persists explicit true/false in %s', async locale => {
    const w = render(locale); await flushPromises()
    expect((w.get('[name="notify_email"]').element as HTMLInputElement).checked).toBe(false)
    mocks.saveAutoCredit.mockImplementation(async (_id, config) => ({ ...state(), config }))
    await w.get('[name="notify_email"]').setValue(true)
    await w.get('form').trigger('submit'); await flushPromises()
    expect(mocks.saveAutoCredit).toHaveBeenLastCalledWith(2, expect.objectContaining({ notify_email: true, threshold: '1.00000001', amount: '5.00000001', cap: '10.00000001' }))
    expect((w.get('[name="notify_email"]').element as HTMLInputElement).checked).toBe(true)
    mocks.getAutoCredit.mockResolvedValue({ ...state(), config: { ...state().config, notify_email: true } })
    await w.setProps({ show: false }); await w.setProps({ show: true }); await flushPromises()
    expect((w.get('[name="notify_email"]').element as HTMLInputElement).checked).toBe(true)
    await w.get('[name="notify_email"]').setValue(false)
    await w.get('form').trigger('submit'); await flushPromises()
    expect(mocks.saveAutoCredit).toHaveBeenLastCalledWith(2, expect.objectContaining({ notify_email: false }))
  })
  it.each(['en', 'zh'])('loads and saves exact strings with version in %s', async locale => {
    const w = render(locale); await flushPromises()
    expect(w.get('[data-testid="outstanding"]').text()).toContain('$5.00')
    expect(w.get('article').text()).toContain('$5.00')
    expect(w.get('article').text()).toContain('$0.00 → $5.00')
    expect(w.text()).not.toContain('5.00000001')
    expect((w.get('[name="threshold"]').element as HTMLInputElement).value).toBe('1.00000001')
    await w.get('[name="amount"]').setValue('5.12345678')
    await w.get('form').trigger('submit'); await flushPromises()
    expect(mocks.saveAutoCredit).toHaveBeenCalledWith(2, expect.objectContaining({ version: 3, amount: '5.12345678' }))
    expect(w.emitted('success')).toBeTruthy()
  })
  it('requires a second full-collection confirmation and supports older history', async () => {
    const w = render(); await flushPromises()
    await w.get('[data-testid="settle-9"]').trigger('click')
    expect(mocks.settleAutoCredit).not.toHaveBeenCalled()
    expect(w.get('[role="alertdialog"]').text()).toContain('$5.00')
    expect(w.get('[role="alertdialog"]').text()).not.toContain('5.00000001')
    await w.get('[data-testid="confirm-settlement"]').trigger('click'); await flushPromises()
    expect(mocks.settleAutoCredit).toHaveBeenCalledWith(2, 9)
    await w.get('[data-testid="older-history"]').trigger('click'); await flushPromises()
    expect(mocks.getAutoCredit).toHaveBeenCalledWith(2, 'all', 9)
  })
  it('preserves input and shows a stale-save error', async () => {
    mocks.saveAutoCredit.mockRejectedValue({ message: 'Configuration changed; reload' })
    const w = render(); await flushPromises(); await w.get('form').trigger('submit'); await flushPromises()
    expect(w.get('[role="alert"]').text()).toContain('Configuration changed; reload')
    expect(w.emitted('close')).toBeUndefined()
  })
  it('does not fetch or mutate for a non-administrator', async () => {
    mocks.role = 'user'; const w = render(); await flushPromises(); expect(mocks.getAutoCredit).not.toHaveBeenCalled(); expect(w.find('form').exists()).toBe(false)
  })
})
