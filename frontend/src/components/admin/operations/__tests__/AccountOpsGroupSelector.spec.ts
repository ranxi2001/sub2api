import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import GroupDialog from '../AccountOpsGroupDialog.vue'
import QualityAccountSelector from '../QualityAccountSelector.vue'
import { saveAccountOpsGroups } from '@/api/admin/accountOps'
import type { AccountOpsGroup, AccountOpsThresholdAccount } from '@/api/admin/accountOps'

vi.mock('@/api/admin/accountOps', () => ({ saveAccountOpsGroups: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const groups: AccountOpsGroup[] = [
  { id: 'manual-alpha', name: 'Alpha site', default_name: 'Alpha site', provider: 'sub2api', site: 'https://alpha.example', mode: 'manual', account_ids: [121, 999] },
  { id: 'manual-beta', name: 'Beta site', default_name: 'Beta site', provider: 'newapi', site: 'https://beta.example', mode: 'manual', account_ids: [2, 60, 998] },
]
const accounts: AccountOpsThresholdAccount[] = Array.from({ length: 125 }, (_, i) => ({
  account_id: i + 1, account_name: `Demo account ${i + 1}`, platform: 'openai', type: i % 2 ? 'oauth' : 'apikey',
  balance: null, unit: 'USD', balance_status: 'unknown', received_at: null, usage_windows: [],
}))
function create() {
  return mount(GroupDialog, {
    props: { show: true, groups, accounts },
    global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' } } },
  })
}
beforeEach(() => { vi.mocked(saveAccountOpsGroups).mockReset().mockResolvedValue({ enabled: false, recipient: '', balance_low: true, weekly_quota: true, cooldown_minutes: 60 }) })

describe('account ops shared selector', () => {
  it('reuses the real quality selector and searches name or global ID immediately', async () => {
    const wrapper = create()
    expect(wrapper.findComponent(QualityAccountSelector).exists()).toBe(true)
    const picker = wrapper.getComponent(QualityAccountSelector)
    expect(picker.findAll('[data-account-id]').length).toBeLessThan(125)
    await picker.get('input:not([type])').setValue('DEMO account 125')
    expect(picker.findAll('[data-account-id]').map(row => row.attributes('data-account-id'))).toEqual(['125'])
    await picker.get('input:not([type])').setValue('#124')
    expect(picker.findAll('[data-account-id]').map(row => row.attributes('data-account-id'))).toEqual(['124'])
    wrapper.unmount()
  })

  it('saves hidden selections and moves only explicitly selected accounts between sites', async () => {
    const wrapper = create()
    expect(wrapper.findComponent(QualityAccountSelector).exists()).toBe(true)
    const picker = wrapper.getComponent(QualityAccountSelector)
    await picker.get('[data-testid="quality-select-page"]').trigger('click')
    await picker.get('button[aria-label="qualityOps.nextAccountPage"]').trigger('click')
    expect((picker.get('[data-account-id="60"] input').element as HTMLInputElement).checked).toBe(false)
    await picker.get('input:not([type])').setValue('#125')
    await picker.get('[data-testid="quality-select-all"]').trigger('click')
    await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
    await flushPromises()
    const saved = vi.mocked(saveAccountOpsGroups).mock.calls[0]![0]
    expect(saved[0]!.account_ids).toEqual(expect.arrayContaining([1, 2, 121, 125, 999]))
    expect(saved[0]!.account_ids).not.toContain(100)
    expect(saved[1]!.account_ids).toEqual([60, 998])
    expect(saved[0]!.account_ids).not.toContain(60)
    expect(saved[0]!.account_ids).not.toContain(998)
    expect(groups[0]!.account_ids).toEqual([121, 999])
    expect(groups[1]!.account_ids).toEqual([2, 60, 998])
    wrapper.unmount()
  })
})

describe('account ops selection boundaries', () => {
  it('filters actual account types and draft membership, including ungrouped accounts', async () => {
    const wrapper = create()
    const picker = wrapper.getComponent(QualityAccountSelector)
    expect(picker.find('[data-testid="quality-account-status-active"]').exists()).toBe(false)
    expect(picker.get('[data-testid="quality-account-type"]').findAll('option').map(option => option.attributes('value'))).toEqual(['', 'oauth', 'apikey'])
    await picker.get('[data-testid="quality-account-group"]').setValue('manual-beta')
    expect(picker.findAll('[data-account-id]').map(row => row.attributes('data-account-id'))).toEqual(['2', '60'])
    await picker.get('[data-testid="quality-account-type"]').setValue('apikey')
    expect(picker.findAll('[data-account-id]')).toHaveLength(0)
    await picker.get('[data-testid="quality-account-type"]').setValue('')
    await picker.get('[data-account-id="2"] input').setValue(true)
    expect(picker.findAll('[data-account-id]').map(row => row.attributes('data-account-id'))).toEqual(['60'])
    await picker.get('[data-testid="quality-account-group"]').setValue('ungrouped')
    expect(picker.find('[data-account-id="1"]').exists()).toBe(true)
    expect(picker.find('[data-account-id="2"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('selects all matching pages and clears only the active group', async () => {
    const wrapper = create()
    const picker = wrapper.getComponent(QualityAccountSelector)
    await picker.get('[data-testid="quality-account-type"]').setValue('apikey')
    await picker.get('[data-testid="quality-select-all"]').trigger('click')
    await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
    await flushPromises()
    const selected = vi.mocked(saveAccountOpsGroups).mock.calls[0]![0]
    expect(selected[0]!.account_ids).toHaveLength(64) // 63 API keys + pre-existing hidden ID 999.
    expect(selected[0]!.account_ids).toContain(125)
    expect(selected[1]!.account_ids).toEqual([2, 60, 998])
    await picker.get('[data-testid="quality-clear-selection"]').trigger('click')
    await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
    await flushPromises()
    expect(vi.mocked(saveAccountOpsGroups).mock.calls[1]![0].map(group => group.account_ids)).toEqual([[], [2, 60, 998]])
    wrapper.unmount()
  })

  it('discards draft changes on cancel and reopening without writing', async () => {
    const wrapper = create()
    await wrapper.getComponent(QualityAccountSelector).get('[data-testid="quality-select-page"]').trigger('click')
    await wrapper.get('[data-testid="cancel-account-groups"]').trigger('click')
    expect(saveAccountOpsGroups).not.toHaveBeenCalled()
    expect(wrapper.emitted('close')).toHaveLength(1)
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    expect(wrapper.getComponent(QualityAccountSelector).props('modelValue')).toEqual([121, 999])
    wrapper.unmount()
  })

  it('keeps draft selection and in-flight save fenced during the parent 30-second refresh', async () => {
    let resolveSave!: (value: Awaited<ReturnType<typeof saveAccountOpsGroups>>) => void
    vi.mocked(saveAccountOpsGroups).mockImplementationOnce(() => new Promise(resolve => { resolveSave = resolve }))
    const wrapper = create()
    await wrapper.getComponent(QualityAccountSelector).get('[data-testid="quality-select-page"]').trigger('click')
    await wrapper.get('[data-testid="account-group-name-manual-alpha"]').setValue('Draft name')
    await wrapper.setProps({ groups: structuredClone(groups) })
    expect((wrapper.get('[data-testid="account-group-name-manual-alpha"]').element as HTMLInputElement).value).toBe('Draft name')
    expect(wrapper.getComponent(QualityAccountSelector).props('modelValue')).toContain(1)
    await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
    await wrapper.setProps({ groups: structuredClone(groups) })
    expect(wrapper.get('[data-testid="save-account-groups"]').attributes('disabled')).toBeDefined()
    resolveSave({ enabled: false, recipient: '', balance_low: true, weekly_quota: true, cooldown_minutes: 60 })
    await flushPromises()
    expect(wrapper.emitted('saved')).toHaveLength(1)
    wrapper.unmount()
  })

  it('ignores an old save response after reopening and keeps the new save busy', async () => {
    let resolveOld!: (value: Awaited<ReturnType<typeof saveAccountOpsGroups>>) => void
    let resolveNew!: (value: Awaited<ReturnType<typeof saveAccountOpsGroups>>) => void
    vi.mocked(saveAccountOpsGroups).mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
      .mockImplementationOnce(() => new Promise(resolve => { resolveNew = resolve }))
    const wrapper = create()
    await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
    expect(wrapper.getComponent(QualityAccountSelector).get('[data-testid="quality-select-page"]').attributes('disabled')).toBeDefined()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
    resolveOld({ enabled: false, recipient: 'old', balance_low: true, weekly_quota: true, cooldown_minutes: 60 })
    await flushPromises()
    expect(wrapper.emitted('saved')).toBeUndefined()
    expect(wrapper.get('[data-testid="save-account-groups"]').attributes('disabled')).toBeDefined()
    resolveNew({ enabled: false, recipient: 'new', balance_low: true, weekly_quota: true, cooldown_minutes: 60 })
    await flushPromises()
    expect(wrapper.emitted('saved')).toHaveLength(1)
    expect(wrapper.get('[data-testid="save-account-groups"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('resets pagination on filtering and bounds rendering for a large collection', async () => {
    const wrapper = create()
    await wrapper.setProps({ accounts: Array.from({ length: 10000 }, (_, i) => ({ ...accounts[0]!, account_id: i + 1, account_name: `Large ${i + 1}` })) })
    const picker = wrapper.getComponent(QualityAccountSelector)
    expect(picker.findAll('[data-account-id]')).toHaveLength(50)
    await picker.get('button[aria-label="qualityOps.nextAccountPage"]').trigger('click')
    expect(picker.props('accountPage')).toBe(2)
    await picker.get('input:not([type])').setValue('#9999')
    expect(picker.props('accountPage')).toBe(1)
    expect(picker.findAll('[data-account-id]')).toHaveLength(1)
    await picker.get('input:not([type])').setValue('')
    await picker.get('[data-testid="quality-select-all"]').trigger('click')
    expect(wrapper.get('[role="alert"]').text()).toBe('accountOps.groupMemberLimit')
    expect(picker.props('modelValue')).toEqual([121, 999])
    wrapper.unmount()
  })
})
