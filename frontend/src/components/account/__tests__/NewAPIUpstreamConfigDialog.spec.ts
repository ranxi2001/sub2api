import { createPinia } from 'pinia'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import type { Account, NewAPIUpstreamConfig, NewAPIUpstreamPreview } from '@/types'
import NewAPIUpstreamConfigDialog from '../NewAPIUpstreamConfigDialog.vue'

const api = vi.hoisted(() => ({ get: vi.fn(), preview: vi.fn(), save: vi.fn(), remove: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({
  getNewAPIUpstreamConfig: api.get, previewNewAPIUpstreamConfig: api.preview,
  saveNewAPIUpstreamConfig: api.save, deleteNewAPIUpstreamConfig: api.remove
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 42 } }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${Object.values(params).join(',')}` : key }) }))

const account = { id: 7, name: 'current' } as Account
const config: NewAPIUpstreamConfig = {
  account_id: 7, site_url: 'https://upstream.example/tenant', configured: true, user_id: 42,
  encryption_key_configured: true, accounts: [
    { account_id: 7, name: 'current', configured_user_id: 42, cn_provider: false, cost_multiplier_scale: 7 },
    { account_id: 8, name: 'same user', configured_user_id: 42, cn_provider: true, cost_multiplier_scale: 1 },
    { account_id: 9, name: 'other user', configured_user_id: 55 },
    { account_id: 10, name: 'unconfigured' }
  ]
}
const preview: NewAPIUpstreamPreview = {
  site_url: config.site_url, user_id: 42, wallet: { amount: 12.34, unit: 'USD' },
  accounts: [
    { account_id: 7, name: 'current', matched: true, token_id: 101, group: 'auto', rate: 0.5, cn_provider: false, cost_multiplier_scale: 7, token_options: [] },
    { account_id: 8, name: 'same user', matched: false, cn_provider: true, cost_multiplier_scale: 1, error: 'ambiguous_token', token_options: [
      { token_id: 102, name: 'candidate one', group: 'default', masked_key: 'ab***yz' },
      { token_id: 103, name: 'candidate two', group: 'premium', masked_key: 'ab***yz' }
    ] }
  ]
}
let wrappers: VueWrapper[] = []
const open = async (props = {}, pinia = createPinia(), realBadges = false) => {
  const wrapper = mount(NewAPIUpstreamConfigDialog, { props: { show: true, account, ...props }, global: { plugins: [pinia], stubs: { Teleport: true, NewFeatureBadge: !realBadges } } })
  wrappers.push(wrapper)
  await flushPromises()
  return wrapper
}
const checkbox = (wrapper: VueWrapper, id: number) => wrapper.get<HTMLInputElement>(`[data-testid="new-api-account-${id}"]`)

beforeEach(() => {
  vi.resetAllMocks()
  api.get.mockResolvedValue(config)
  api.preview.mockResolvedValue(preview)
  api.save.mockResolvedValue({ ...preview, accounts: preview.accounts.map(row => ({ ...row, matched: true })) })
  api.remove.mockResolvedValue({ account_id: 7, configured: false })
})
afterEach(() => { wrappers.forEach(wrapper => wrapper.unmount()); wrappers = [] })

describe('New API shared authorization dialog', () => {
  it('loads full same-site candidates without reading a secret or selecting another user', async () => {
    const wrapper = await open()
    expect(wrapper.text()).toContain(config.site_url)
    expect(wrapper.get<HTMLInputElement>('[data-testid="new-api-user-id"]').element.value).toBe('42')
    expect(wrapper.get<HTMLInputElement>('[data-testid="new-api-access-token"]').element.type).toBe('password')
    expect(wrapper.get<HTMLInputElement>('[data-testid="new-api-access-token"]').element.value).toBe('')
    expect(checkbox(wrapper, 7).element.checked).toBe(true)
    expect(checkbox(wrapper, 8).element.checked).toBe(true)
    expect(checkbox(wrapper, 9).element.checked).toBe(false)
    expect(checkbox(wrapper, 10).element.checked).toBe(false)
    await checkbox(wrapper, 10).setValue(true)
    await wrapper.get('[data-testid="new-api-preview"]').trigger('click')
    expect(api.preview).toHaveBeenCalledWith(7, { user_id: 42, account_ids: [7, 8, 10] })
  })

  it('derives ×7 and ×1 from account platforms without asking for a factor', async () => {
    api.preview.mockResolvedValue({ ...preview, accounts: [
      { ...preview.accounts[0], group: 'foreign', rate: 0.1 },
      { ...preview.accounts[1], matched: true, group: 'domestic', rate: 0.7 }
    ] })
    const wrapper = await open()
    expect(wrapper.get<HTMLInputElement>('[data-testid="new-api-apply-usd-rate-7"]').element.checked).toBe(true)
    await wrapper.get('[data-testid="new-api-apply-usd-rate-7"]').setValue(true)
    await wrapper.get('[data-testid="new-api-apply-usd-rate-8"]').setValue(true)
    await wrapper.get('[data-testid="new-api-preview"]').trigger('click')
    await flushPromises()
    expect(api.preview).toHaveBeenCalledWith(7, { user_id: 42, account_ids: [7, 8] })
    expect(wrapper.get('[data-testid="new-api-cost-preview-7"]').text()).toContain('0.1,7,0.7')
    expect(wrapper.get('[data-testid="new-api-cost-preview-8"]').text()).toContain('0.7,1,0.7')
    await wrapper.get('[data-testid="new-api-save"]').trigger('click')
    expect(api.save).toHaveBeenCalledWith(7, { user_id: 42, account_ids: [7, 8] })
  })

  it('keeps cost sync at x1 when USD conversion is unchecked', async () => {
    api.preview.mockResolvedValue({ ...preview, accounts: [{ ...preview.accounts[0], group: 'foreign', rate: 0.1 }] })
    const wrapper = await open()
    await wrapper.get('[data-testid="new-api-apply-usd-rate-7"]').setValue(false)
    await wrapper.get('[data-testid="new-api-preview"]').trigger('click')
    await flushPromises()
    expect(api.preview).toHaveBeenCalledWith(7, { user_id: 42, account_ids: [7, 8], apply_usd_rate: { '7': false } })
    expect(wrapper.get('[data-testid="new-api-cost-preview-7"]').text()).toContain('0.1,1,0.1')
  })

  it('preserves the independent manual-cost setting in the preview', async () => {
    api.preview.mockResolvedValue({ ...preview, accounts: [{ ...preview.accounts[0], group: 'foreign', rate: 0.1, cost_auto_sync_enabled: false }] })
    const wrapper = await open()
    await wrapper.get('[data-testid="new-api-preview"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="new-api-cost-preview-7"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.upstreamBilling.newAPI.costSyncPaused')
  })

  it('loads USD conversion switches, sends explicit opt-out, and excludes unchecked peers', async () => {
    api.get.mockResolvedValue({ ...config, accounts: config.accounts.map(row => ({ ...row, apply_usd_rate: row.account_id === 7 })) })
    const wrapper = await open()
    expect(wrapper.get<HTMLInputElement>('[data-testid="new-api-apply-usd-rate-7"]').element.checked).toBe(true)
    await wrapper.get('[data-testid="new-api-apply-usd-rate-7"]').setValue(false)
    await wrapper.get('[data-testid="new-api-apply-usd-rate-8"]').setValue(true)
    await checkbox(wrapper, 8).setValue(false)
    await wrapper.get('[data-testid="new-api-preview"]').trigger('click')
    expect(api.preview).toHaveBeenCalledWith(7, { user_id: 42, account_ids: [7], apply_usd_rate: { '7': false } })
  })

  it('does not derive a cost for an automatic routing group', async () => {
    const wrapper = await open()
    await wrapper.get('[data-testid="new-api-apply-usd-rate-7"]').setValue(true)
    await wrapper.get('[data-testid="new-api-preview"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="new-api-cost-preview-7"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.upstreamBilling.newAPI.costUnavailable')
  })

  it('announces cost conversion independently and shares persistent dismissals', async () => {
    localStorage.clear()
    const pinia = createPinia()
    const store = useNewFeatureNoticeStore(pinia)
    store.dismiss(42, 'previous-feature')
    const wrapper = await open({}, pinia, true)
    const badge = wrapper.getComponent(NewFeatureBadge)
    expect(badge.props('noticeId')).toBe(NEW_FEATURE_NOTICES.newAPICostConversion)
    expect(badge.find('button').exists()).toBe(true)
    const sibling = mount(NewFeatureBadge, { props: { userId: 42, noticeId: NEW_FEATURE_NOTICES.newAPICostConversion }, global: { plugins: [pinia] } })
    wrappers.push(sibling)
    await sibling.get('button').trigger('click')
    expect(badge.find('button').exists()).toBe(false)
    expect(sibling.find('button').exists()).toBe(false)
    const reloaded = await open({}, createPinia(), true)
    expect(reloaded.getComponent(NewFeatureBadge).find('button').exists()).toBe(false)
    const persisted = useNewFeatureNoticeStore(createPinia())
    expect(persisted.isDismissed(42, 'previous-feature')).toBe(true)
    expect(persisted.isDismissed(42, 'future-feature')).toBe(false)
    expect(persisted.isDismissed(43, NEW_FEATURE_NOTICES.newAPICostConversion)).toBe(false)
    localStorage.clear()
  })

  it('requires a non-whitespace access token for a new upstream user', async () => {
    const wrapper = await open()
    await wrapper.get('[data-testid="new-api-user-id"]').setValue('99')
    await wrapper.get('[data-testid="new-api-access-token"]').setValue('   ')
    await wrapper.get('[data-testid="new-api-save"]').trigger('click')
    expect(wrapper.get('[role="alert"]').text()).toContain('admin.accounts.upstreamBilling.newAPI.tokenRequired')
    expect(api.save).not.toHaveBeenCalled()
    expect(checkbox(wrapper, 8).element.checked).toBe(false)
    expect(checkbox(wrapper, 9).element.checked).toBe(false)
  })

  it('shows wallet-only automatic groups and sends manually selected candidates for revalidation', async () => {
    const wrapper = await open()
    await wrapper.get('[data-testid="new-api-preview"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="new-api-wallet"]').text()).toContain('$12.34 USD')
    expect(wrapper.get('[data-testid="new-api-result-7"]').text()).toContain('auto')
    expect(wrapper.get('[data-testid="new-api-result-7"]').text()).toContain('admin.accounts.upstreamBilling.newAPI.noFixedRatio')
    const select = wrapper.get('[data-testid="new-api-token-8"]')
    expect(select.findAll('option')).toHaveLength(3)
    await select.setValue('103')
    await wrapper.get('[data-testid="new-api-save"]').trigger('click')
    await flushPromises()
    expect(api.save).toHaveBeenCalledWith(7, { user_id: 42, account_ids: [7, 8], token_selections: { '8': 103 } })
    expect(wrapper.emitted('saved')).toHaveLength(1)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('shows verification failure and keeps the dialog open without success', async () => {
    api.preview.mockRejectedValueOnce({ message: 'User cannot read token inventory' })
    api.save.mockRejectedValueOnce({ message: 'Selected key does not belong to this user' })
    const wrapper = await open()
    await wrapper.get('[data-testid="new-api-preview"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('User cannot read token inventory')
    await wrapper.get('[data-testid="new-api-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('Selected key does not belong to this user')
    expect(wrapper.emitted('saved')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it('clears entered secrets on close and after save', async () => {
    const wrapper = await open()
    await wrapper.get('[data-testid="new-api-access-token"]').setValue(' replacement ')
    await wrapper.get('[data-testid="new-api-save"]').trigger('click')
    await flushPromises()
    expect(api.save).toHaveBeenCalledWith(7, { user_id: 42, access_token: 'replacement', account_ids: [7, 8] })
    expect(wrapper.get<HTMLInputElement>('[data-testid="new-api-access-token"]').element.value).toBe('')
    await wrapper.get('[data-testid="new-api-access-token"]').setValue('temporary')
    await wrapper.get('[data-testid="new-api-close"]').trigger('click')
    expect(wrapper.get<HTMLInputElement>('[data-testid="new-api-access-token"]').element.value).toBe('')
  })

  it('ignores an old config response after switching accounts', async () => {
    let resolveOld!: (value: NewAPIUpstreamConfig) => void
    api.get.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    api.get.mockResolvedValueOnce({ ...config, account_id: 9, user_id: 55 })
    const wrapper = await open()
    await wrapper.setProps({ account: { id: 9, name: 'other user' } as Account })
    await flushPromises()
    resolveOld(config)
    await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="new-api-user-id"]').element.value).toBe('55')
    expect(wrapper.get<HTMLInputElement>('[data-testid="new-api-access-token"]').element.value).toBe('')
    expect(checkbox(wrapper, 7).element.checked).toBe(false)
    expect(checkbox(wrapper, 9).element.checked).toBe(true)
  })

  it('hides a selector for an already matched unique token and explains failed ownership with candidates', async () => {
    api.preview.mockResolvedValue({ ...preview, accounts: [
      { ...preview.accounts[0], token_options: [{ token_id: 101, name: 'unique', group: 'auto', masked_key: 'ab***yz' }] },
      { ...preview.accounts[1], error: 'selected_key_verification_failed' }
    ] })
    const wrapper = await open()
    await wrapper.get('[data-testid="new-api-preview"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="new-api-token-7"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="new-api-result-8"]').text()).toContain('admin.accounts.upstreamBilling.newAPI.selectedKeyVerificationFailed')
    expect(wrapper.get('[data-testid="new-api-result-8"]').text()).not.toContain('selected_key_verification_failed')
  })

  it('turns safe backend error codes into actionable localized guidance', async () => {
    api.save.mockRejectedValue({ code: 'NEW_API_VERIFICATION_FAILED', message: 'New API verification failed: invalid_inventory_pagination' })
    const wrapper = await open()
    await wrapper.get('[data-testid="new-api-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('admin.accounts.upstreamBilling.newAPI.upstreamVerificationFailed')
    expect(wrapper.get('[role="alert"]').text()).not.toContain('invalid_inventory_pagination')
  })

  it('requires confirmation before removing only the current binding', async () => {
    const wrapper = await open()
    await wrapper.get('[data-testid="new-api-remove"]').trigger('click')
    expect(api.remove).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="new-api-confirm-remove"]').trigger('click')
    await flushPromises()
    expect(api.remove).toHaveBeenCalledWith(7)
    expect(wrapper.emitted('saved')).toHaveLength(1)
  })

  it('blocks saving when encryption is unavailable', async () => {
    api.get.mockResolvedValue({ ...config, encryption_key_configured: false })
    const wrapper = await open()
    expect(wrapper.get('[role="alert"]').text()).toContain('admin.accounts.upstreamBilling.newAPI.encryptionRequired')
    expect(wrapper.get<HTMLButtonElement>('[data-testid="new-api-save"]').element.disabled).toBe(true)
  })
})
