import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia } from 'pinia'
import { reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import AccountOpsView from '../AccountOpsView.vue'
import AccountOpsGroupDialog from '@/components/admin/operations/AccountOpsGroupDialog.vue'
import type { AccountOpsGroup, AccountOpsThresholdAccount } from '@/api/admin/accountOps'
import { useAppStore } from '@/stores/app'
import { getAccountOpsSettings, getAccountOpsEvents, getAccountOpsThresholdAccounts, getAccountOpsThresholdGroups, saveAccountOpsGroups, saveAccountOpsNotificationSettings, saveAccountOpsRulesBatch, saveAccountOpsRuleGroups, testAccountOpsWebhook } from '@/api/admin/accountOps'
const state = vi.hoisted(() => ({ auth: null as any }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/accountOps', () => ({ getAccountOpsSettings: vi.fn(), getAccountOpsEvents: vi.fn(), getAccountOpsThresholdAccounts: vi.fn(), getAccountOpsThresholdGroups: vi.fn(), saveAccountOpsNotificationSettings: vi.fn(), saveAccountOpsRulesBatch: vi.fn(), saveAccountOpsRuleGroups: vi.fn(), saveAccountOpsGroups: vi.fn(), deleteAccountOpsRule: vi.fn(), saveAccountOpsWebhook: vi.fn(), deleteAccountOpsWebhook: vi.fn(), testAccountOpsWebhook: vi.fn() }))
const config = { enabled: false, recipient: '', balance_low: true, weekly_quota: true, cooldown_minutes: 60, webhooks: [], balance_thresholds: [], quota_thresholds: [] }
const settings = () => ({ config: { ...config }, smtp_configured: true, dropped_signals: 0, storage_failures: 0, encryption_key_configured: true })
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(yes => { resolve = yes }); return { promise, resolve } }
const create = () => mount(AccountOpsView, { global: { plugins: [createPinia()], stubs: { teleport: true } } })
const pollingGroups = (): AccountOpsGroup[] => [
  { id: 'manual-alpha', name: 'Alpha', default_name: 'Alpha', provider: 'sub2api', site: 'https://alpha.example', mode: 'manual', account_ids: [1] },
  { id: 'manual-beta', name: 'Beta', default_name: 'Beta', provider: 'newapi', site: 'https://beta.example', mode: 'manual', account_ids: [2, 60, 998] },
]
const pollingAccounts = (): AccountOpsThresholdAccount[] => [1, 2, 60].map(id => ({
  account_id: id, account_name: `Demo ${id}`, platform: 'openai', type: 'apikey', balance: null, unit: 'USD', balance_status: 'unknown', received_at: null, usage_windows: [],
}))
beforeEach(() => { vi.resetAllMocks(); state.auth = reactive({ user: { id: 1, role: 'admin' } }); vi.mocked(getAccountOpsSettings).mockResolvedValue(settings()); vi.mocked(getAccountOpsEvents).mockResolvedValue({ items: [], has_more: false }); vi.mocked(getAccountOpsThresholdAccounts).mockResolvedValue([]); vi.mocked(getAccountOpsThresholdGroups).mockResolvedValue([]); vi.mocked(saveAccountOpsGroups).mockResolvedValue(config); vi.mocked(saveAccountOpsRulesBatch).mockResolvedValue(config); vi.mocked(saveAccountOpsRuleGroups).mockResolvedValue(config); vi.mocked(testAccountOpsWebhook).mockResolvedValue({ ok: true }); vi.mocked(saveAccountOpsNotificationSettings).mockImplementation(async c => ({ ...config, ...c })) })
describe('account operations tabs and independent saves', () => {
  it('reopens from saved memberships before the post-save groups read completes', async () => {
    vi.mocked(getAccountOpsThresholdGroups).mockImplementation(async () => pollingGroups())
    vi.mocked(getAccountOpsThresholdAccounts).mockImplementation(async () => pollingAccounts())
    const savedGroups = pollingGroups()
    savedGroups[0]!.account_ids = [1, 2]
    savedGroups[1]!.account_ids = [60, 998]
    const saved = { ...config, groups: savedGroups }
    vi.mocked(saveAccountOpsGroups).mockResolvedValue(saved)
    const wrapper = create()
    try {
      await flushPromises()
      await wrapper.get('[data-testid="edit-account-groups"]').trigger('click')
      await wrapper.get('[data-account-id="2"] input').setValue(true)
      const postSaveRead = deferred<AccountOpsGroup[]>()
      vi.mocked(getAccountOpsThresholdGroups).mockReturnValueOnce(postSaveRead.promise)
      await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
      await flushPromises()
      expect(getAccountOpsThresholdGroups).toHaveBeenCalledTimes(2)
      await wrapper.get('[data-testid="edit-account-groups"]').trigger('click')
      expect((wrapper.get('[data-account-id="2"] input').element as HTMLInputElement).checked).toBe(true)
      postSaveRead.resolve(savedGroups)
      await flushPromises()
      await wrapper.get('[data-testid="account-group-name-manual-alpha"]').setValue('Rename only')
      await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
      await flushPromises()
      expect(vi.mocked(saveAccountOpsGroups).mock.calls[1]![0]).toEqual([
        expect.objectContaining({ id: 'manual-alpha', name: 'Rename only', account_ids: [1, 2] }),
        expect.objectContaining({ id: 'manual-beta', account_ids: [60, 998] }),
      ])
    } finally { wrapper.unmount() }
  })

  it('fences old post-save group reads and account polls after a newer save succeeds', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    vi.mocked(getAccountOpsThresholdGroups).mockImplementation(async () => pollingGroups())
    vi.mocked(getAccountOpsThresholdAccounts).mockImplementation(async () => pollingAccounts())
    const firstGroups = pollingGroups()
    firstGroups[0]!.account_ids = [1, 2]; firstGroups[1]!.account_ids = [60, 998]
    const latestGroups = pollingGroups()
    latestGroups[0]!.name = 'Latest saved'; latestGroups[0]!.account_ids = [1, 2, 60]; latestGroups[1]!.account_ids = [998]
    vi.mocked(saveAccountOpsGroups).mockResolvedValueOnce({ ...config, groups: firstGroups }).mockResolvedValueOnce({ ...config, groups: latestGroups })
    const wrapper = create()
    try {
      await flushPromises()
      await wrapper.get('[data-testid="edit-account-groups"]').trigger('click')
      await wrapper.get('[data-account-id="2"] input').setValue(true)
      const firstRead = deferred<AccountOpsGroup[]>()
      vi.mocked(getAccountOpsThresholdGroups).mockReturnValueOnce(firstRead.promise)
      await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
      await flushPromises()
      await wrapper.get('[data-testid="edit-account-groups"]').trigger('click')
      await wrapper.get('[data-account-id="60"] input').setValue(true)
      await wrapper.get('[data-testid="account-group-name-manual-alpha"]').setValue('Latest saved')
      const oldPoll = deferred<AccountOpsGroup[]>()
      const oldAccounts = deferred<AccountOpsThresholdAccount[]>()
      vi.mocked(getAccountOpsThresholdGroups).mockReturnValueOnce(oldPoll.promise)
      vi.mocked(getAccountOpsThresholdAccounts).mockReturnValueOnce(oldAccounts.promise)
      await vi.advanceTimersByTimeAsync(30000)
      expect(getAccountOpsThresholdGroups).toHaveBeenCalledTimes(3)
      const latestRead = deferred<AccountOpsGroup[]>()
      vi.mocked(getAccountOpsThresholdGroups).mockReturnValueOnce(latestRead.promise)
      await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
      await flushPromises()
      expect(getAccountOpsThresholdGroups).toHaveBeenCalledTimes(4)
      oldPoll.resolve(pollingGroups()); oldAccounts.resolve(pollingAccounts())
      await flushPromises()
      firstRead.resolve(firstGroups)
      await flushPromises()
      await wrapper.get('[data-testid="edit-account-groups"]').trigger('click')
      expect((wrapper.get('[data-testid="account-group-name-manual-alpha"]').element as HTMLInputElement).value).toBe('Latest saved')
      expect((wrapper.get('[data-account-id="60"] input').element as HTMLInputElement).checked).toBe(true)
      expect(wrapper.getComponent(AccountOpsGroupDialog).props('groups').map(group => group.account_ids)).toEqual([[1, 2, 60], [998]])
      latestRead.resolve(latestGroups)
      await flushPromises()
    } finally {
      wrapper.unmount(); visibility.mockRestore(); vi.useRealTimers()
    }
  })

  it('preserves group drafts and save lock when real parent polling reads complete', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    vi.mocked(getAccountOpsThresholdGroups).mockImplementation(async () => pollingGroups())
    vi.mocked(getAccountOpsThresholdAccounts).mockImplementation(async () => pollingAccounts())
    const wrapper = create()
    try {
      await flushPromises()
      await wrapper.get('[data-testid="edit-account-groups"]').trigger('click')
      const dialog = wrapper.getComponent(AccountOpsGroupDialog)
      await wrapper.get('[data-testid="account-group-name-manual-alpha"]').setValue('Draft name')
      await wrapper.get('[data-testid="account-ops-member-picker"] [data-account-id="2"] input').setValue(true)

      const editingRead = deferred<AccountOpsGroup[]>()
      vi.mocked(getAccountOpsThresholdGroups).mockReturnValueOnce(editingRead.promise)
      await vi.advanceTimersByTimeAsync(30000)
      expect(getAccountOpsThresholdGroups).toHaveBeenCalledTimes(2)
      expect(getAccountOpsThresholdAccounts).toHaveBeenCalledTimes(2)
      editingRead.resolve(pollingGroups())
      await flushPromises()
      expect((wrapper.get('[data-testid="account-group-name-manual-alpha"]').element as HTMLInputElement).value).toBe('Draft name')
      expect((wrapper.get('[data-account-id="2"] input').element as HTMLInputElement).checked).toBe(true)

      const saving = deferred<Awaited<ReturnType<typeof saveAccountOpsGroups>>>()
      vi.mocked(saveAccountOpsGroups).mockReturnValueOnce(saving.promise)
      await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
      const savingRead = deferred<AccountOpsGroup[]>()
      vi.mocked(getAccountOpsThresholdGroups).mockReturnValueOnce(savingRead.promise)
      await vi.advanceTimersByTimeAsync(30000)
      expect(getAccountOpsThresholdGroups).toHaveBeenCalledTimes(3)
      expect(getAccountOpsThresholdAccounts).toHaveBeenCalledTimes(3)
      savingRead.resolve(pollingGroups())
      await flushPromises()
      expect((wrapper.get('[data-testid="account-group-name-manual-alpha"]').element as HTMLInputElement).value).toBe('Draft name')
      expect(wrapper.get('[data-testid="save-account-groups"]').attributes('disabled')).toBeDefined()
      await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
      expect(saveAccountOpsGroups).toHaveBeenCalledTimes(1)
      expect(saveAccountOpsGroups).toHaveBeenCalledWith([
        expect.objectContaining({ id: 'manual-alpha', name: 'Draft name', account_ids: [1, 2] }),
        expect.objectContaining({ id: 'manual-beta', account_ids: [60, 998] }),
      ])
      expect(dialog.emitted('saved')).toBeUndefined()
      saving.resolve(config)
      await flushPromises()
      expect(dialog.emitted('saved')).toHaveLength(1)
      expect(wrapper.find('[data-testid="save-account-groups"]').exists()).toBe(false)
      expect(useAppStore().toasts.filter(toast => toast.type === 'success')).toHaveLength(1)
    } finally {
      wrapper.unmount(); visibility.mockRestore(); vi.useRealTimers()
    }
  })

  it('isolates a late save reply after parent polling and closing then reopening the dialog', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    vi.mocked(getAccountOpsThresholdGroups).mockImplementation(async () => pollingGroups())
    vi.mocked(getAccountOpsThresholdAccounts).mockImplementation(async () => pollingAccounts())
    const oldSave = deferred<Awaited<ReturnType<typeof saveAccountOpsGroups>>>()
    const newSave = deferred<Awaited<ReturnType<typeof saveAccountOpsGroups>>>()
    vi.mocked(saveAccountOpsGroups).mockReturnValueOnce(oldSave.promise).mockReturnValueOnce(newSave.promise)
    const wrapper = create()
    try {
      await flushPromises()
      await wrapper.get('[data-testid="edit-account-groups"]').trigger('click')
      const dialog = wrapper.getComponent(AccountOpsGroupDialog)
      await wrapper.get('[data-testid="account-group-name-manual-alpha"]').setValue('Closed draft')
      await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
      const lateRead = deferred<AccountOpsGroup[]>()
      vi.mocked(getAccountOpsThresholdGroups).mockReturnValueOnce(lateRead.promise)
      await vi.advanceTimersByTimeAsync(30000)
      expect(getAccountOpsThresholdGroups).toHaveBeenCalledTimes(2)
      await wrapper.get('[data-testid="cancel-account-groups"]').trigger('click')
      const refreshed = pollingGroups()
      refreshed[0]!.name = 'Refreshed name'
      lateRead.resolve(refreshed)
      await flushPromises()
      await wrapper.get('[data-testid="edit-account-groups"]').trigger('click')
      expect((wrapper.get('[data-testid="account-group-name-manual-alpha"]').element as HTMLInputElement).value).toBe('Refreshed name')
      await wrapper.get('[data-testid="account-group-name-manual-alpha"]').setValue('New draft')
      await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
      oldSave.resolve(config)
      await flushPromises()
      expect(dialog.emitted('saved')).toBeUndefined()
      expect(wrapper.get('[data-testid="save-account-groups"]').attributes('disabled')).toBeDefined()
      expect((wrapper.get('[data-testid="account-group-name-manual-alpha"]').element as HTMLInputElement).value).toBe('New draft')
      await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
      expect(saveAccountOpsGroups).toHaveBeenCalledTimes(2)
      newSave.resolve(config)
      await flushPromises()
      expect(dialog.emitted('saved')).toHaveLength(1)
      expect(wrapper.find('[data-testid="save-account-groups"]').exists()).toBe(false)
      expect(useAppStore().toasts.filter(toast => toast.type === 'success')).toHaveLength(1)
    } finally {
      wrapper.unmount(); visibility.mockRestore(); vi.useRealTimers()
    }
  })

  it('initializes a dynamically opened dialog from saved recovery-only flags', async () => {
    vi.mocked(getAccountOpsSettings).mockResolvedValue({ ...settings(), config: { ...config, balance_thresholds: [{ account_id: 1, enabled: true, threshold: 5, unit: 'USD', notify_alert: false, notify_recovery: true }] } } as any)
    vi.mocked(getAccountOpsThresholdAccounts).mockResolvedValue([{ account_id: 1, account_name: 'key', type: 'apikey', platform: 'openai', balance: 3, unit: 'USD', balance_status: 'ok', received_at: null, usage_windows: [] }])
    vi.mocked(getAccountOpsThresholdGroups).mockResolvedValue([{ id: 'manual-recovery', name: 'Recovery', default_name: 'Recovery', provider: 'sub2api', site: 'https://example.com', mode: 'manual', account_ids: [1] }])
    const wrapper = create(); await flushPromises()
    await wrapper.get('[data-testid="account-ops-tab-settings"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="edit-rule-manual-recovery"]').trigger('click'); await flushPromises()
    expect((wrapper.get('[data-testid="batch-rule-notify-alert"]').element as HTMLInputElement).checked).toBe(false)
    expect((wrapper.get('[data-testid="batch-rule-threshold"]').element as HTMLInputElement).value).toBe('5')
    wrapper.unmount()
  })

  it('releases pagination when Refresh invalidates an in-flight page response', async () => {
    vi.mocked(getAccountOpsEvents).mockResolvedValue({ items: [], has_more: true })
    const wrapper = create(); await flushPromises()
    const old = deferred<{ items: []; has_more: boolean }>(); vi.mocked(getAccountOpsEvents).mockReturnValueOnce(old.promise)
    const page = (wrapper.vm as any).more()
    await (wrapper.vm as any).load()
    old.resolve({ items: [], has_more: false }); await page
    expect((wrapper.vm as any).loadingMore).toBe(false)
    wrapper.unmount()
  })

  it('loads rules on first entry and keeps the master switch usable from either tab', async () => {
    const wrapper = create(); await flushPromises()
    expect(wrapper.findAll('[role="tab"]')[0].attributes('id')).toBe('ops-tab-settings')
    expect(wrapper.get('[data-testid="account-ops-tab-settings"]').attributes('aria-selected')).toBe('true')
    expect(getAccountOpsThresholdAccounts).toHaveBeenCalledOnce()
    expect(wrapper.find('[data-testid="account-ops-rules"]').exists()).toBe(true)
    await wrapper.get('[data-testid="account-ops-tab-records"]').trigger('click')
    await wrapper.get('[data-testid="notifications-enabled"]').trigger('click'); await flushPromises()
    expect(saveAccountOpsNotificationSettings).toHaveBeenLastCalledWith({ enabled: true })
    await wrapper.get('[data-testid="account-ops-tab-settings"]').trigger('click')
    expect(wrapper.get('[data-testid="notifications-enabled"]').attributes('aria-checked')).toBe('true')
    await wrapper.get('[data-testid="account-ops-tab-settings"]').trigger('keydown', { key: 'End' })
    expect(wrapper.get('[data-testid="account-ops-tab-records"]').attributes('aria-selected')).toBe('true')
    wrapper.unmount()
  })
  it('renames an automatic group and creates a manual membership group', async () => {
    vi.mocked(getAccountOpsThresholdAccounts).mockResolvedValue([{ account_id: 1, account_name: 'key', type: 'apikey', platform: 'openai', balance: 3, unit: 'USD', balance_status: 'ok', received_at: null, usage_windows: [] }])
    vi.mocked(getAccountOpsThresholdGroups).mockResolvedValue([{ id: 'manual-primary', name: 'example.com', default_name: 'example.com', provider: 'sub2api', site: 'https://example.com', mode: 'manual', account_ids: [1] }])
    const wrapper = create(); await flushPromises()
    await wrapper.get('[data-testid="edit-account-groups"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="account-group-name-manual-primary"]').setValue('主站余额')
    await wrapper.get('[data-testid="add-account-group"]').trigger('click')
    const manual = wrapper.findAll('[data-testid^="account-group-name-manual-"]').at(-1)!
    await manual.setValue('手动组')
    const member = wrapper.get('[data-testid="account-ops-member-picker"] [data-account-id="1"] input')
    await member.setValue(true)
    await wrapper.get('[data-testid="save-account-groups"]').trigger('click'); await flushPromises()
    expect(saveAccountOpsGroups).toHaveBeenCalledWith(expect.arrayContaining([
      { id: 'manual-primary', name: '主站余额', provider: 'sub2api', site: 'https://example.com', account_ids: [] },
      expect.objectContaining({ name: '手动组', account_ids: [1] })
    ]))
    wrapper.unmount()
  })
  it('keeps empty groups editable when the API returns a null member list', async () => {
    vi.mocked(getAccountOpsThresholdAccounts).mockResolvedValue([{ account_id: 1, account_name: 'key', type: 'apikey', platform: 'openai', balance: 3, unit: 'USD', balance_status: 'ok', received_at: null, usage_windows: [] }])
    vi.mocked(getAccountOpsThresholdGroups).mockResolvedValue([{ id: 'manual-empty', name: '空组', default_name: '空组', provider: 'sub2api', site: '', mode: 'manual', account_ids: null }] as any)
    const wrapper = create(); await flushPromises()
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
    await wrapper.get('[data-testid="edit-account-groups"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="account-group-name-manual-empty"]').exists()).toBe(true)
    expect((wrapper.get('[data-testid="account-ops-member-picker"] [data-account-id="1"] input').element as HTMLInputElement).checked).toBe(false)
    wrapper.unmount()
  })
  it('adds and removes an email channel without posting the global switch or failure rules', async () => {
    const wrapper = create(); await flushPromises()
    await wrapper.get('[data-testid="account-ops-tab-settings"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="account-ops-email-channel"]').exists()).toBe(false)
    await wrapper.get('[data-testid="account-ops-add-channel"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="add-channel-email"]').trigger('click'); await flushPromises()
    await wrapper.get('#account-ops-email-name').setValue('运营邮箱')
    await wrapper.get('#account-ops-email').setValue('ops@example.com')
    await wrapper.get('#account-ops-email-form').trigger('submit'); await flushPromises()
    expect(saveAccountOpsNotificationSettings).toHaveBeenCalledWith({ recipient: 'ops@example.com', email_name: '运营邮箱' })
    expect(wrapper.find('#account-ops-email').exists()).toBe(false)
    expect(wrapper.get('[data-testid="account-ops-email-channel"]').text()).toContain('ops@example.com')
    expect(wrapper.get('[data-testid="account-ops-email-channel"]').text()).toContain('运营邮箱')
    await wrapper.get('[data-testid="delete-email-channel"]').trigger('click'); await flushPromises()
    expect(saveAccountOpsNotificationSettings).toHaveBeenLastCalledWith({ recipient: '', email_name: '' })
    expect(wrapper.find('[data-testid="account-ops-email-channel"]').exists()).toBe(false)
    wrapper.unmount()
  })
  it('keeps email addition available at the robot limit without an encryption key', async () => {
    vi.mocked(getAccountOpsSettings).mockResolvedValue({ ...settings(), encryption_key_configured: false, config: { ...config, webhooks: Array.from({ length: 5 }, (_, i) => ({ id: `bot-${i}`, provider: 'wecom' as const, enabled: true, url_configured: true })) } })
    const wrapper = create(); await flushPromises()
    await wrapper.get('[data-testid="account-ops-tab-settings"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="account-ops-add-channel"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="add-channel-email"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-testid="add-channel-webhook"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="add-channel-email"]').trigger('click'); await flushPromises()
    expect(wrapper.find('#account-ops-email').exists()).toBe(true)
    wrapper.unmount()
  })
  it('offers only email and Webhook and opens an automatic Webhook editor', async () => {
    const wrapper = create(); await flushPromises()
    await wrapper.get('[data-testid="account-ops-add-channel"]').trigger('click'); await flushPromises()
    expect(wrapper.findAll('[data-testid^="add-channel-"]')).toHaveLength(2)
    await wrapper.get('[data-testid="add-channel-webhook"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid^="account-ops-webhook-provider-"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid^="account-ops-webhook-url-"]').exists()).toBe(true)
    wrapper.unmount()
  })
  it('saves the master switch directly and restores its state when saving fails', async () => {
    vi.mocked(saveAccountOpsNotificationSettings).mockRejectedValueOnce(new Error('offline'))
    const wrapper = create(); await flushPromises()
    await wrapper.get('[data-testid="account-ops-tab-settings"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="edit-notification-settings"]').exists()).toBe(false)
    const toggle = wrapper.get('[data-testid="notifications-enabled"]')
    await toggle.trigger('click'); await flushPromises()
    expect(saveAccountOpsNotificationSettings).toHaveBeenLastCalledWith({ enabled: true })
    expect(toggle.attributes('aria-checked')).toBe('false')
    expect(useAppStore().toasts.at(-1)).toMatchObject({ type: 'error' })
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    await toggle.trigger('click'); await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('true')
    expect(useAppStore().toasts.at(-1)).toMatchObject({ type: 'success' })
    expect(wrapper.text()).not.toContain('qualityOps.saved')
    wrapper.unmount()
  })
  it('keeps unsaved global rules during refresh and saves without affecting channels', async () => {
    vi.mocked(saveAccountOpsNotificationSettings).mockRejectedValueOnce(new Error('offline'))
    const wrapper = create(); await flushPromises()
    await wrapper.get('[data-testid="account-ops-tab-settings"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="notifications-enabled"]').trigger('click'); await flushPromises()
    expect(useAppStore().toasts.at(-1)).toMatchObject({ type: 'error' })
    const rules = wrapper.get('[data-testid="account-ops-rules"]')
    await rules.get('[data-testid="global-balance-low"]').setValue(false)
    await rules.get('[data-testid="global-cooldown"]').setValue(30)
    await (wrapper.vm as any).load(); await flushPromises()
    expect((rules.get('[data-testid="global-cooldown"]').element as HTMLInputElement).value).toBe('30')
    await rules.get('#account-ops-global-form').trigger('submit'); await flushPromises()
    expect(saveAccountOpsNotificationSettings).toHaveBeenLastCalledWith({ balance_low: false, weekly_quota: true, cooldown_minutes: 30 })
    expect(useAppStore().toasts.at(-1)).toMatchObject({ type: 'success' })
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    wrapper.unmount()
  })
  it('opens a rule dialog with amount and two independent notification switches', async () => {
    vi.mocked(getAccountOpsThresholdAccounts).mockResolvedValue([{ account_id: 1, account_name: 'key', type: 'apikey', platform: 'openai', balance: 3, unit: 'USD', balance_status: 'ok', received_at: null, usage_windows: [] }])
    vi.mocked(getAccountOpsThresholdGroups).mockResolvedValue([{ id: 'manual-rule', name: 'Rule group', default_name: 'Rule group', provider: 'sub2api', site: 'https://example.com', mode: 'manual', account_ids: [1] }])
    const wrapper = create(); await flushPromises()
    await wrapper.get('[data-testid="account-ops-tab-settings"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="edit-rule-manual-rule"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="batch-rule-threshold"]').setValue('5')
    await wrapper.get('[data-testid="batch-rule-notify-alert"]').setValue(false)
    await wrapper.get('#account-ops-batch-rule-form').trigger('submit'); await flushPromises()
    expect(saveAccountOpsRulesBatch).toHaveBeenCalledWith([1], { metric: 'balance', enabled: true, threshold: 5, unit: 'USD', notify_alert: false, notify_recovery: true })
    wrapper.unmount()
  })
  it('toggles a mixed-unit group without replacing each rule value', async () => {
    vi.mocked(getAccountOpsSettings).mockResolvedValue({ ...settings(), config: {
      ...config,
      balance_thresholds: [
        { account_id: 1, enabled: true, threshold: 5, unit: 'USD', notify_alert: false, notify_recovery: true },
        { account_id: 2, enabled: true, threshold: 3, unit: 'CNY', notify_alert: true, notify_recovery: false },
      ],
    } } as any)
    vi.mocked(getAccountOpsThresholdAccounts).mockResolvedValue([
      { account_id: 1, account_name: 'usd', type: 'apikey', platform: 'openai', balance: 4, unit: 'USD', balance_status: 'ok', received_at: null, usage_windows: [] },
      { account_id: 2, account_name: 'cny', type: 'apikey', platform: 'openai', balance: 2, unit: 'CNY', balance_status: 'ok', received_at: null, usage_windows: [] },
    ])
    vi.mocked(getAccountOpsThresholdGroups).mockResolvedValue([{ id: 'manual-mixed', name: 'Mixed', default_name: 'Mixed', provider: 'sub2api', site: 'https://example.com', mode: 'manual', account_ids: [1, 2] }])
    const wrapper = create(); await flushPromises()
    const inputs = wrapper.get('[data-testid="rule-row-manual-mixed"]').findAll('input')
    await inputs[1]!.setValue(false); await flushPromises()
    expect(saveAccountOpsRuleGroups).toHaveBeenCalledWith([{ account_ids: [1, 2], rule: { metric: 'balance', enabled: false, enabled_only: true } }])
    wrapper.unmount()
  })
  it('renders alert and recovery as different historical rows', async () => {
    const base = { account_id: 1, account_name: 'key', kind: 'balance_threshold' as const, signal: 'balance_threshold', http_status: 0, first_seen: '2026-10-07T12:00:00Z', last_seen: '2026-10-07T12:00:00Z', occurrences: 1, state: 'sent' as const, last_sent_at: '2026-10-07T12:00:00Z', next_send_at: '', attempts: 1 }
    vi.mocked(getAccountOpsEvents).mockResolvedValue({ items: [{ ...base, id: '1', phase: 'alert' }, { ...base, id: '2', phase: 'recovery' }], has_more: false })
    const wrapper = create(); await flushPromises()
    await wrapper.get('[data-testid="account-ops-tab-records"]').trigger('click')
    expect(wrapper.findAll('[data-testid="notification-record"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('accountOps.phases.alert')
    expect(wrapper.text()).toContain('accountOps.phases.recovery')
    wrapper.unmount()
  })
  it('requires a new amount if a selected account changes currency while the bulk dialog is open', async () => {
    const account = { account_id: 1, account_name: 'key', type: 'apikey' as const, platform: 'openai', balance: 3, unit: 'USD', balance_status: 'ok' as const, received_at: null, usage_windows: [] }
    vi.mocked(getAccountOpsThresholdAccounts).mockResolvedValue([account])
    vi.mocked(getAccountOpsThresholdGroups).mockResolvedValue([{ id: 'manual-currency', name: 'Currency', default_name: 'Currency', provider: 'sub2api', site: 'https://example.com', mode: 'manual', account_ids: [1] }])
    const wrapper = create(); await flushPromises()
    await wrapper.get('[data-testid="select-group-manual-currency"]').setValue(true)
    await wrapper.get('[data-testid="batch-edit-rules"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="batch-rule-threshold"]').setValue('5')
    vi.mocked(getAccountOpsThresholdAccounts).mockResolvedValue([{ ...account, unit: 'CNY' }])
    await (wrapper.vm as any).loadAccounts(); await flushPromises()
    expect((wrapper.get('[data-testid="batch-rule-threshold"]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.get('#account-ops-batch-rule-form').text()).toContain('CNY')
    wrapper.unmount()
  })
  it('shows custom channel names in cards and keeps the recorded delivery name', async () => {
    vi.mocked(getAccountOpsSettings).mockResolvedValue({ ...settings(), config: { ...config, webhooks: [{ id: 'named', name: '夜班通知', provider: 'custom', enabled: true, url_configured: true }] } } as any)
    const event = { id: 'named-event', account_id: 1, account_name: 'key', kind: 'balance_threshold', signal: 'balance_threshold', http_status: 0, first_seen: '2026-10-07T12:00:00Z', last_seen: '2026-10-07T12:00:00Z', occurrences: 1, state: 'sent', last_sent_at: null, next_send_at: '', attempts: 1, deliveries: { 'robot:named:revision': { provider: 'custom', name: '当时的通知群', status: 'sent', attempts: 1 } } }
    vi.mocked(getAccountOpsEvents).mockResolvedValue({ items: [event], has_more: false } as any)
    const wrapper = create(); await flushPromises()
    expect(wrapper.text()).toContain('夜班通知')
    await wrapper.get('[data-testid="account-ops-tab-records"]').trigger('click')
    expect(wrapper.get('[data-testid="delivery-details-list"]').text()).toContain('当时的通知群')
    wrapper.unmount()
  })
  it('ignores a stale configuration response after a scoped save', async () => {
    const wrapper = create(); await flushPromises()
    const old = deferred<any>(); vi.mocked(getAccountOpsSettings).mockReturnValueOnce(old.promise)
    const refresh = (wrapper.vm as any).load()
    ;(wrapper.vm as any).acceptConfig({ ...config, recipient: 'new@example.com' })
    old.resolve(settings()); await refresh
    expect((wrapper.vm as any).remote.config.recipient).toBe('new@example.com')
    wrapper.unmount()
  })
  it('does not restore old admin configuration after logout', async () => {
    const old = deferred<any>(); vi.mocked(getAccountOpsSettings).mockReturnValueOnce(old.promise)
    const wrapper = create(); state.auth.user = null; old.resolve(settings()); await flushPromises()
    expect(wrapper.find('[data-testid="account-ops-rules"]').exists()).toBe(false)
    expect(wrapper.find('#account-ops-email').exists()).toBe(false)
    wrapper.unmount()
  })
})
