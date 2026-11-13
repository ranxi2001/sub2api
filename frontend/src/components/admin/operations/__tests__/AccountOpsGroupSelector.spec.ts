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
  { id: 'manual-beta', name: 'Beta site', default_name: 'Beta site', provider: 'newapi', site: 'https://beta.example', mode: 'manual', account_ids: [2] },
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
    await picker.get('input:not([type])').setValue('#125')
    await picker.get('[data-testid="quality-select-all"]').trigger('click')
    await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
    await flushPromises()
    const saved = vi.mocked(saveAccountOpsGroups).mock.calls[0]![0]
    expect(saved[0]!.account_ids).toEqual(expect.arrayContaining([1, 2, 121, 125, 999]))
    expect(saved[0]!.account_ids).not.toContain(100)
    expect(saved[1]!.account_ids).toEqual([])
    expect(groups[0]!.account_ids).toEqual([121, 999])
    expect(groups[1]!.account_ids).toEqual([2])
    wrapper.unmount()
  })
})
