import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import EmailDialog from '../AccountOpsEmailDialog.vue'
import Lifecycle from '../AccountOpsLifecycleSelector.vue'
import RuleList from '../AccountOpsRuleList.vue'
import Balance from '../AccountOpsBalanceStatus.vue'
import { saveAccountOpsNotificationSettings, setAccountOpsOAuthLifecycle, type AccountOpsConfig, type AccountOpsThresholdAccount } from '@/api/admin/accountOps'
vi.mock('vue-i18n', async original => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin/accountOps', () => ({ saveAccountOpsNotificationSettings: vi.fn(), setAccountOpsOAuthLifecycle: vi.fn() }))
const config: AccountOpsConfig = { enabled: true, recipient: 'one@example.test, two@example.test', balance_low: true, weekly_quota: true, cooldown_minutes: 60 }
const account: AccountOpsThresholdAccount = { account_id: 1, account_name: 'Synthetic', platform: 'openai', type: 'oauth', balance: null, unit: '%', received_at: null, balance_status: 'unknown', usage_windows: [] }
beforeEach(() => vi.resetAllMocks())
describe('account operations team settings', () => {
 it('edits multiple recipients without replacing other notification settings', async () => {
  vi.mocked(saveAccountOpsNotificationSettings).mockResolvedValue(config)
  const wrapper = mount(EmailDialog, { props: { show: true, config, smtpConfigured: true }, global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' } } } })
  expect((wrapper.get('#account-ops-email').element as HTMLTextAreaElement).value).toBe('one@example.test\ntwo@example.test')
  await wrapper.get('#account-ops-email').setValue('one@example.test\nthree@example.test')
  await wrapper.get('form').trigger('submit'); await flushPromises()
  expect(saveAccountOpsNotificationSettings).toHaveBeenCalledWith({ recipient: 'one@example.test\nthree@example.test', email_name: '' })
  expect(wrapper.emitted('saved')).toHaveLength(1)
 })
 it('defaults existing OAuth to temporary and persists a long-term override', async () => {
  vi.mocked(setAccountOpsOAuthLifecycle).mockResolvedValue(undefined)
  const wrapper = mount(Lifecycle, { props: { account } })
  expect((wrapper.get('select').element as HTMLSelectElement).value).toBe('temporary')
  await wrapper.get('select').setValue('long_term'); await flushPromises()
  expect(setAccountOpsOAuthLifecycle).toHaveBeenCalledWith(1, 'long_term')
  expect(wrapper.emitted('updated')).toHaveLength(1)
 })
 it('displays one shared balance and keeps all keys in batch edits', async () => {
  const accounts: AccountOpsThresholdAccount[] = [1, 2].map(id => ({ ...account, account_id: id, type: 'apikey', balance_status: id === 1 ? 'ok' : 'unknown', balance: id === 1 ? 100 : null, unit: 'USD' }))
  const groups = [{ id: 'manual-shared123', name: 'Shared', default_name: 'Shared', provider: 'manual', site: '', mode: 'manual' as const, account_ids: [1, 2] }]
  const wrapper = mount(RuleList, { props: { accounts, groups, config: { ...config, balance_thresholds: [{ account_id: 1, enabled: true, threshold: 50, unit: 'USD' }] }, loading: false, ready: true, error: '' } })
  expect(wrapper.findComponent(Balance).props('accounts')).toEqual([accounts[0]])
  expect(wrapper.get('[data-testid="rule-row-manual-shared123"]').text()).toContain('accountOps.ruleStates.normal')
  await wrapper.get('[data-testid="select-group-manual-shared123"]').setValue(true)
  await wrapper.get('[data-testid="batch-edit-rules"]').trigger('click')
  expect(wrapper.emitted('batch-edit')).toEqual([[accounts]])
 })
})
