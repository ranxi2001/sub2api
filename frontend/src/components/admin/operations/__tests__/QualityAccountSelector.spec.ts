import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import Selector from '../QualityAccountSelector.vue'
import type { AccountListItem } from '@/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const props = {
  modelValue: [99], search: '', group: '', type: '', statuses: [], scope: 'manual' as const,
  accounts: [{ id: 1, name: 'Available' }, { id: 2, name: 'Already used' }] as AccountListItem[],
  groups: [{ id: 7, name: 'Existing quality group' }], accountsLoading: false, selectingAccounts: false, accountsError: '',
  accountPage: 1, accountPages: 2, accountsTotal: 51, bulk: false,
  disabledReason: (account: AccountListItem) => account.id === 2 ? 'qualityOps.accountHasRule' : '',
}

describe('quality selector existing contract', () => {
  it('retains quality type/status/scope filters and disabled reasons by default', async () => {
    const wrapper = mount(Selector, { props })
    expect(wrapper.find('[data-testid="quality-account-status-active"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="quality-account-type"]').find('option[value="setup-token"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="quality-account-scope-all"]').exists()).toBe(true)
    expect(wrapper.get('[data-account-id="2"] input').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-account-id="2"]').text()).toContain('qualityOps.accountHasRule')
    await wrapper.get('[data-account-id="1"] input').setValue(true)
    expect(wrapper.emitted('update:modelValue')).toEqual([[[99, 1]]])
    await wrapper.get('button[aria-label="qualityOps.nextAccountPage"]').trigger('click')
    expect(wrapper.emitted('search')).toEqual([[2]])
    wrapper.unmount()
  })

  it('keeps all-scope accounts unselectable and optional hidden filters hidden', () => {
    const wrapper = mount(Selector, { props: { ...props, scope: 'all', hideTypeFilter: true, hideStatusFilter: true, hideScope: true } })
    expect(wrapper.get('[data-account-id="1"] input').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="quality-select-page"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="quality-account-type"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="quality-account-status-active"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
