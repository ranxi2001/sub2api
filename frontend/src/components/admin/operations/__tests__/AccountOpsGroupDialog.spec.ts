import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import GroupDialog from '../AccountOpsGroupDialog.vue'
import { saveAccountOpsGroups } from '@/api/admin/accountOps'
import type { AccountOpsConfig, AccountOpsGroup, AccountOpsThresholdAccount } from '@/api/admin/accountOps'

vi.mock('@/api/admin/accountOps', () => ({ saveAccountOpsGroups: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const config: AccountOpsConfig = {
  enabled: false,
  recipient: '',
  balance_low: true,
  weekly_quota: true,
  cooldown_minutes: 60,
  groups: []
}
const groups: AccountOpsGroup[] = [
  { id: 'manual-primary', name: 'Primary', default_name: 'Primary', provider: 'sub2api', site: 'https://example.com', mode: 'manual', account_ids: [1] },
  { id: 'manual-empty', name: 'Empty', default_name: 'Empty', provider: 'sub2api', site: 'https://empty.example.com', mode: 'manual', account_ids: [] }
]
const accounts: AccountOpsThresholdAccount[] = [
  { account_id: 1, account_name: 'Alpha', platform: 'openai', type: 'apikey', balance: 3, unit: 'USD', balance_status: 'ok', received_at: null, usage_windows: [] }
]

function create() {
  return mount(GroupDialog, {
    props: { show: true, groups, accounts },
    global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' } } }
  })
}

beforeEach(() => {
  vi.mocked(saveAccountOpsGroups).mockReset().mockResolvedValue(config)
})

describe('account group editing', () => {
  it('removes a group from the saved payload', async () => {
    const wrapper = create()
    await wrapper.get('[data-testid="delete-account-group-manual-empty"]').trigger('click')
    expect(wrapper.find('[data-testid="account-group-name-manual-empty"]').exists()).toBe(false)
    await wrapper.get('[data-testid="save-account-groups"]').trigger('click')
    await flushPromises()
    expect(saveAccountOpsGroups).toHaveBeenCalledWith([{ id: 'manual-primary', name: 'Primary', provider: 'sub2api', site: 'https://example.com', account_ids: [1] }])
    expect(wrapper.emitted('saved')).toEqual([[config]])
    wrapper.unmount()
  })
})
