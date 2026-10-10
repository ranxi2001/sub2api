import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import RuleList from '../AccountOpsRuleList.vue'
import type { AccountOpsConfig, AccountOpsGroup, AccountOpsThresholdAccount } from '@/api/admin/accountOps'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, values?: Record<string, unknown>) => values ? `${key}:${JSON.stringify(values)}` : key }) }))

const config: AccountOpsConfig = {
  enabled: false, recipient: '', balance_low: true, weekly_quota: true, cooldown_minutes: 60,
  balance_thresholds: [{ account_id: 1, enabled: true, threshold: 5, unit: 'USD' }],
  quota_thresholds: [{ account_id: 2, enabled: true, threshold_percent: 80, window: 'weekly' }]
}
const accounts: AccountOpsThresholdAccount[] = [
  { account_id: 1, account_name: 'Alpha key', platform: 'openai', type: 'apikey', balance: 3, unit: 'USD', balance_status: 'ok', received_at: null, usage_windows: [] },
  { account_id: 2, account_name: 'Beta oauth', platform: 'openai', type: 'oauth', balance: null, unit: 'USD', balance_status: 'unsupported', received_at: null, usage_windows: [{ window: 'weekly', label: 'Weekly', used_percent: 50, status: 'ok' }] },
  { account_id: 3, account_name: 'Gamma key', platform: 'openai', type: 'apikey', balance: 10, unit: 'USD', balance_status: 'ok', received_at: null, usage_windows: [] }
]
const groups: AccountOpsGroup[] = [
  { id: 'manual-primary', name: 'Primary site', default_name: 'Primary site', provider: 'sub2api', site: 'https://example.com', mode: 'manual', account_ids: [1, 2] },
  { id: 'manual-secondary', name: 'Secondary site', default_name: 'Secondary site', provider: 'sub2api', site: 'https://other.example.com', mode: 'manual', account_ids: [3] }
]

function create() {
  return mount(RuleList, { props: { accounts, groups, config, loading: false, ready: true, error: '', busy: false } })
}

describe('group account rule selection', () => {
  it('selects visible groups and emits all of their members for bulk editing', async () => {
    const wrapper = create()
    expect(wrapper.get('[data-testid="select-visible-groups"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="select-visible-groups"]').setValue(true)
    await wrapper.get('[data-testid="batch-edit-rules"]').trigger('click')
    expect(wrapper.emitted('batch-edit')).toEqual([[accounts]])
    await wrapper.get('[data-testid="select-group-manual-primary"]').setValue(false)
    await wrapper.get('[data-testid="batch-edit-rules"]').trigger('click')
    expect(wrapper.emitted('batch-edit')?.[1]).toEqual([[accounts[2]]])
    wrapper.unmount()
  })

  it('filters by group and prunes selection after filtering', async () => {
    const wrapper = create()
    await wrapper.get('[data-testid="select-group-manual-primary"]').setValue(true)
    await wrapper.get('input[aria-label="accountOps.search"]').setValue('Secondary')
    expect((wrapper.get('[data-testid="select-group-manual-secondary"]').element as HTMLInputElement).checked).toBe(false)
    await wrapper.get('[data-testid="select-group-manual-secondary"]').setValue(true)
    await wrapper.get('[data-testid="batch-edit-rules"]').trigger('click')
    expect(wrapper.emitted('batch-edit')?.[0]).toEqual([[accounts[2]]])
    wrapper.unmount()
  })

  it('allows an empty manual group and blocks edits while saving', async () => {
    const empty: AccountOpsGroup = { id: 'manual-empty', name: 'Empty', default_name: 'Empty', provider: 'sub2api', site: 'https://empty.example.com', mode: 'manual', account_ids: [] }
    const wrapper = mount(RuleList, { props: { accounts, groups: [...groups, empty], config, loading: false, ready: true, error: '', busy: false } })
    expect(wrapper.get('[data-testid="rule-row-manual-empty"]').exists()).toBe(true)
    await wrapper.get('[data-testid="select-group-manual-secondary"]').setValue(true)
    await wrapper.setProps({ busy: true })
    expect(wrapper.get('[data-testid="batch-edit-rules"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="select-group-manual-secondary"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('opens group editing when an empty group row is edited', async () => {
    const empty: AccountOpsGroup = { id: 'manual-empty', name: 'Empty', default_name: 'Empty', provider: 'sub2api', site: 'https://empty.example.com', mode: 'manual', account_ids: [] }
    const wrapper = mount(RuleList, { props: { accounts, groups: [...groups, empty], config, loading: false, ready: true, error: '', busy: false } })
    await wrapper.get('[data-testid="edit-rule-manual-empty"]').trigger('click')
    expect(wrapper.emitted('edit-group')).toEqual([[empty]])
    wrapper.unmount()
  })
})

describe('saved threshold currency', () => {
  it('shows the saved rule unit separately from a changed sample unit', () => {
    const account = { ...accounts[0], balance: 36, unit: 'CNY' }
    const group = { ...groups[0], account_ids: [1] }
    const wrapper = mount(RuleList, { props: { accounts: [account], groups: [group], config, loading: false, ready: true, error: '' } })
    expect(wrapper.text()).toContain('36.00 CNY')
    expect(wrapper.text()).toContain('≤ 5.00 USD')
    expect(wrapper.text()).toContain('accountOps.ruleStates.unknown')
    wrapper.unmount()
  })
})
