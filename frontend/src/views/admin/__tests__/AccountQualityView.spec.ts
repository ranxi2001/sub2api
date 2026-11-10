import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import AccountQualityView from '../AccountQualityView.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import * as accountsAPI from '@/api/admin/accounts'
import scheduledTests from '@/api/admin/scheduledTests'
import { updateQualityPreset, previewQualityOwnership, restoreQualityOwnership, removeQualityOwnedModel, createQualityPreset, listQualityPresets, type QualityRulePreset, createQualityTemplate, deleteQualityTemplate, listQualityPlans, listQualityOperations, listQualityTemplates, runQualityPlan, updateQualityTemplate, type QualityRuleTemplate } from '@/api/admin/accountQuality'
import { useAccountQualityStore } from '@/stores/accountQuality'
import { useAuthStore } from '@/stores/auth'
import type { ScheduledTestPlan } from '@/types'
import { defaultQualityBPS } from '@/utils/qualityRulePatch'
vi.mock('@/components/admin/operations/SmartOpsNav.vue', () => ({ default: { template: '<nav />' } }))
vi.mock('@/stores/auth', async () => {
  const { reactive } = await import('vue')
  const auth = reactive({ user: { id: 1, role: 'admin' } })
  return { useAuthStore: () => auth }
})
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key, te: () => true }) }))
vi.mock('@/api/admin/accountQuality', () => ({ listQualityPlans: vi.fn(), runQualityPlan: vi.fn(), listQualityOperations: vi.fn().mockResolvedValue({items:[],next_cursor:0}),
  previewQualityOwnership: vi.fn().mockResolvedValue({ plan_id: 1, models: [], group_ids: [], entries: [], action: '', ownership_retained: false, relinquishable_count: 0 }), reconcileQualityOwnership: vi.fn(), restoreQualityOwnership: vi.fn(), removeQualityOwnedModel: vi.fn(), listQualityPresets: vi.fn(), createQualityPreset: vi.fn(), updateQualityPreset: vi.fn(), listQualityTemplates: vi.fn().mockResolvedValue([]), createQualityTemplate: vi.fn(), updateQualityTemplate: vi.fn(), deleteQualityTemplate: vi.fn() }))
vi.mock('@/api/admin/scheduledTests', () => ({ default: { create: vi.fn(), update: vi.fn(), delete: vi.fn(), listResults: vi.fn(), getResult: vi.fn() } }))
vi.mock('@/api/admin/accounts', () => ({ list: vi.fn().mockResolvedValue({ items: [{ id: 1, name: 'Test account', platform: 'openai', type: 'oauth' }], total: 1 }) }))
vi.mock('@/api/admin/groups', () => ({ getModelAllowlistCandidates: vi.fn().mockResolvedValue(["test-judge"]), getAllIncludingInactive: vi.fn().mockResolvedValue([{ id: 21, name: 'Quality pool', status:'active', platform: 'openai' }]) }))
vi.mock('@/components/account/ModelWhitelistSelector.vue', () => ({ default: { props: ['modelValue'], template: '<div data-testid="model-selector">{{ modelValue.join(",") }}</div>' } }))
const mountView = () => mount(AccountQualityView, { global: { plugins: [createPinia()], stubs: { Teleport: true, AppLayout: { template: '<main><slot /></main>' } } } })
describe('quality operations', () => {
  beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); useAuthStore().user!.id = 1; useAuthStore().user!.role = 'admin'; vi.mocked(scheduledTests.create).mockReset(); vi.mocked(scheduledTests.delete).mockReset().mockResolvedValue(undefined); vi.mocked(scheduledTests.update).mockReset(); vi.mocked(accountsAPI.list).mockReset().mockResolvedValue({ items: [{ id: 1, name: 'Test account', platform: 'openai', type: 'oauth' }], total: 1 } as any); vi.mocked(listQualityPlans).mockResolvedValue([]); vi.mocked(listQualityOperations).mockResolvedValue({items:[],next_cursor:0})
    vi.mocked(listQualityTemplates).mockReset().mockResolvedValue([])
    vi.mocked(listQualityPresets).mockReset().mockResolvedValue([])
    vi.mocked(createQualityPreset).mockReset(); vi.mocked(updateQualityPreset).mockReset()
    vi.mocked(createQualityTemplate).mockReset().mockResolvedValue({ template: groupRule(), created: 0 })
    vi.mocked(updateQualityTemplate).mockReset().mockResolvedValue({ template: groupRule(), updated: 0, failed: 0, created: 0 })
    vi.mocked(deleteQualityTemplate).mockReset().mockResolvedValue({ deleted: 0 }) })
  const groupRule = (): QualityRuleTemplate => ({
    id: 7, account_filter: { group: '21', statuses: ['active'] }, model_id: 'group-model', cron_expression: '*/30 * * * *', enabled: true, max_results: 100,
    pelican_config: { question_kind: 'state_probe', prompt: '', reasoning_effort: 'high', parallel_count: 1,
      quality: { expected_answer: '', action: 'disable_scheduling', remove_group_ids: [], auto_restore: true } },
    plan_ids: [1], last_synced_at: null, created_at: '', updated_at: '',
  })
  const rules = (): ScheduledTestPlan[] => [1, 2, 3].map(id => ({
    id, account_id: id, account_name: `Account ${id}`, model_id: `model-${id}`, cron_expression: '*/30 * * * *', enabled: true,
    max_results: 100, auto_recover: false, last_run_at: null, next_run_at: null, created_at: '', updated_at: '',
    pelican_config: { question_kind: 'state_probe', prompt: '', reasoning_effort: 'high', parallel_count: 1,
      quality: { expected_answer: '', action: 'remove_groups', remove_group_ids: [id], auto_restore: false } },
  }))
  const preset = (): QualityRulePreset => ({ id: 17, name: 'Saved probe', model_id: 'preset-model', cron_expression: '0 * * * *', enabled: false, max_results: 75,
    pelican_config: { question_kind: 'state_probe', test_channel: 'account', prompt: '', reasoning_effort: 'high', parallel_count: 1,
      quality: { action: 'observe_only', expected_answer: '', remove_group_ids: [], auto_restore: false } }, created_at: '', updated_at: '' })

  it('defaults new OAuth cleanup to six rounds and saves a custom limit on a new rule', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises(); vm.selectedAccounts = [1]
    const input = wrapper.get<HTMLInputElement>('[data-testid="quality-oauth-401-max-rounds"]')
    expect(input.element.value).toBe('6')
    expect(input.attributes()).toMatchObject({ min: '0', max: '100', step: '1', 'aria-describedby': 'quality-oauth-401-hint' })
    expect(wrapper.get('[data-testid="quality-oauth-401-setting"]').findComponent(NewFeatureBadge).props('noticeId')).toBe(NEW_FEATURE_NOTICES.qualityOAuth401Cleanup)
    expect(wrapper.get('#quality-oauth-401-hint').text()).toBe('qualityOps.oauth401MaxRoundsHint')
    await wrapper.get('[data-testid="quality-question-kind"]').setValue('state_probe')
    await wrapper.get('[data-testid="quality-action-observe-only"]').setValue(true)
    await input.setValue('8')
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.create).toHaveBeenCalledWith(expect.objectContaining({ account_id: 1,
      pelican_config: expect.objectContaining({ quality: expect.objectContaining({ oauth_401_max_rounds: 8 }) }) }))
    wrapper.unmount()
  })

  it('keeps legacy account and group rules disabled until the cleanup limit is explicitly edited', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue([rules()[0]])
    vi.mocked(listQualityTemplates).mockResolvedValue([groupRule()])
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-plan-id="1"] .rule-actions button:nth-child(2)').trigger('click'); await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="quality-oauth-401-max-rounds"]').element.value).toBe('0')
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(vi.mocked(scheduledTests.update).mock.calls[0][1].pelican_config!.quality!.oauth_401_max_rounds).toBe(0)
    await wrapper.get('[data-testid="quality-template-edit"]').trigger('click'); await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="quality-oauth-401-max-rounds"]').element.value).toBe('0')
    await wrapper.get('[data-testid="quality-oauth-401-max-rounds"]').setValue('12')
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(vi.mocked(updateQualityTemplate).mock.calls[0][1].pelican_config!.quality!.oauth_401_max_rounds).toBe(12)
    wrapper.unmount()
  })

  it('shows cleanup as an independent bulk field and preserves each rules other settings', async () => {
    const targets = rules().slice(0, 2)
    targets[0].pelican_config!.quality!.oauth_401_max_rounds = 9
    targets[1].pelican_config!.quality!.oauth_401_max_rounds = 12
    vi.mocked(listQualityPlans).mockResolvedValue(targets)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.selectedRuleIds = [1, 2]; vm.editSelectedRules(); await flushPromises()
    expect(wrapper.find('[data-testid="quality-oauth-401-max-rounds"]').exists()).toBe(false)
    await wrapper.get('[data-testid="quality-bulk-field-oauth401"]').setValue(true)
    await wrapper.get('[data-testid="quality-oauth-401-max-rounds"]').setValue('6')
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.update).toHaveBeenCalledTimes(2)
    for (const [index, [id, body]] of vi.mocked(scheduledTests.update).mock.calls.entries()) {
      expect(id).toBe(index + 1)
      expect(body.pelican_config!.quality).toEqual({ ...targets[index].pelican_config!.quality, oauth_401_max_rounds: 6 })
      expect(body).not.toHaveProperty('enabled')
    }
    wrapper.unmount()
  })

  it('retains preset limits and copies legacy missing limits as disabled', async () => {
    const saved = preset(); saved.pelican_config.quality!.oauth_401_max_rounds = 15
    vi.mocked(listQualityPresets).mockResolvedValue([saved])
    vi.mocked(listQualityPlans).mockResolvedValue([rules()[0]])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); await flushPromises()
    await wrapper.get('[data-testid="quality-preset-select"]').setValue('17')
    await wrapper.get('[data-testid="quality-apply-preset"]').trigger('click'); await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="quality-oauth-401-max-rounds"]').element.value).toBe('15')
    vm.copyRule(rules()[0]); await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="quality-oauth-401-max-rounds"]').element.value).toBe('0')
    wrapper.unmount()
  })

  it('shares the cleanup notice dismissal across account and template editors', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); await flushPromises()
    await wrapper.get('[data-testid="quality-oauth-401-setting"] [data-testid="new-feature-badge"]').trigger('click')
    expect(wrapper.find('[data-testid="quality-oauth-401-setting"] [data-testid="new-feature-badge"]').exists()).toBe(false)
    vm.editTemplate(groupRule()); await flushPromises()
    expect(wrapper.find('[data-testid="quality-oauth-401-setting"] [data-testid="new-feature-badge"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it.each([-1, 1.5, 101])('rejects the invalid cleanup limit %s before creating a rule', async limit => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises(); vm.selectedAccounts = [1]
    await wrapper.get('[data-testid="quality-question-kind"]').setValue('state_probe')
    await wrapper.get('[data-testid="quality-action-observe-only"]').setValue(true)
    await wrapper.get('[data-testid="quality-oauth-401-max-rounds"]').setValue(String(limit))
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.create).not.toHaveBeenCalled()
    expect(wrapper.get('.editor-error').text()).toContain('qualityOps.invalidOAuth401MaxRounds')
    wrapper.unmount()
  })

  it('edits quality rest on group rules and keeps legacy account rules at zero', async () => {
    const plan = rules()[0]
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    vi.mocked(listQualityTemplates).mockResolvedValue([groupRule()])
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-plan-id="1"] .rule-actions button:nth-child(2)').trigger('click'); await flushPromises()
    expect(wrapper.get<HTMLInputElement>('[data-testid="quality-retest-rest"]').element.value).toBe('0')
    await wrapper.get('[data-testid="quality-retest-rest"]').setValue('90')
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(vi.mocked(scheduledTests.update).mock.calls[0][1].pelican_config!.quality!.retest_rest_minutes).toBe(90)
    await wrapper.get('[data-testid="quality-template-edit"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-retest-rest"]').setValue('120')
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(vi.mocked(updateQualityTemplate).mock.calls[0][1].pelican_config!.quality!.retest_rest_minutes).toBe(120)
    wrapper.unmount()
  })

  it.each([',', '，'])('preserves typed %s delimiters and saves distinct cooldown targets', async separator => {
    const plan = rules()[0]
    plan.pelican_config!.quality!.action = 'remove_models'
    plan.pelican_config!.quality!.remove_models = ['gpt-6.1-sol']
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-plan-id="1"] .rule-actions button:nth-child(2)').trigger('click'); await flushPromises()
    const input = wrapper.get<HTMLInputElement>('[data-testid="quality-remove-models-input"]')
    expect(input.element.value).toBe('gpt-6.1-sol')
    const targets = ['gpt-6.1-sol', 'gpt-6-astra', 'gpt-6.1-terra', 'gpt-5.6-terra', 'gpt-6-luna', 'gpt-5.6-sol']
    // Use the rendered value for each successive keystroke: the old handler
    // erased a comma before the next model could be entered.
    const suffix = separator + ' ' + targets.slice(1).join(separator + ' ')
    let expected = input.element.value
    for (const character of suffix) {
      expected += character
      await input.setValue(input.element.value + character)
      expect(input.element.value).toBe(expected)
    }
    await input.setValue(expected + separator + ' gpt-6.1-sol' + separator)
    expect(input.element.value.endsWith(separator)).toBe(true)
    await input.setValue(input.element.value.replace('gpt-6-astra', ' gpt-6-astra '))
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.update).toHaveBeenCalledTimes(1)
    expect(vi.mocked(scheduledTests.update).mock.calls[0][1].pelican_config!.quality!.remove_models).toEqual(targets)
    wrapper.unmount()
  })

  it('tracks raw delimiter edits as unsaved and resets the input when reopening a rule', async () => {
    const plan = rules()[0]
    plan.pelican_config!.quality!.action = 'remove_models'
    plan.pelican_config!.quality!.remove_models = ['gpt-6.1-sol', 'gpt-6-astra']
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const wrapper = mountView(); await flushPromises()
    const vm = wrapper.vm as any
    vm.edit(plan); await flushPromises()
    const input = wrapper.get<HTMLInputElement>('[data-testid="quality-remove-models-input"]')
    await input.setValue(input.element.value + ',')
    vm.closeForm(); await flushPromises()
    expect(vm.discardPrompt).toBe(true)
    vm.edit(plan); await flushPromises()
    expect(vm.removeModelsInput).toBe('gpt-6.1-sol, gpt-6-astra')
    expect(wrapper.get<HTMLInputElement>('[data-testid="quality-remove-models-input"]').element.value).toBe('gpt-6.1-sol, gpt-6-astra')
    wrapper.unmount()
  })

  const reuseCases = (['new', 'edit', 'bulk'] as const).flatMap(mode => (['preset', 'account'] as const).map(origin => ({ mode, origin })))
  async function openReuseCase({ mode, origin }: typeof reuseCases[number]) {
    const targets = rules()
    for (const plan of targets) {
      plan.pelican_config!.rule_name = `Target rule ${plan.id}`
      plan.last_run_at = '2026-01-01T00:00:00Z'
      plan.running_until = '2099-01-01T00:00:00Z'
    }
    const source = { ...rules()[0], id: 10, account_id: 90, account_name: 'Source account', model_id: 'chosen-source-model',
      enabled: false, cron_expression: '0 */2 * * *', max_results: 75 }
    source.pelican_config!.quality = { action: 'observe_only', expected_answer: '', remove_group_ids: [], auto_restore: false, trigger_on_upstream_5xx: true }
    Object.assign(source.pelican_config!, { rule_name: 'Source name', model_id: 'previous-result-model', trigger_source: 'upstream_5xx',
      quality_model_outcomes: { stale: 'failed' }, quality_model_actions: { stale: 'cooled' },
      quality_restore_diagnostic: { reason: 'group_missing', ownership_retained: true },
      credentials: { token: 'synthetic-secret' }, quality_round_id: 'old-round', statistics: { failed: 9 } })
    const alternative = { ...rules()[0], id: 9, account_id: 90, account_name: 'Source account', model_id: 'wrong-source-model' }
    const saved = { ...preset(), model_id: source.model_id, cron_expression: source.cron_expression, pelican_config: source.pelican_config! }
    const originals = JSON.stringify([targets, source, alternative, saved])
    vi.mocked(listQualityPlans).mockResolvedValue([...targets, alternative, source])
    vi.mocked(listQualityPresets).mockResolvedValue([saved])
    vi.mocked(accountsAPI.list).mockResolvedValue({ items: [1, 2, 3, 4, 5, 90].map(id => ({ id, name: `Account ${id}`, platform: 'openai', type: 'oauth' })), total: 6 } as any)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    if (mode === 'new') {
      await wrapper.get('[data-testid="quality-create-rule"]').trigger('click'); await flushPromises()
      await wrapper.get('[data-testid="quality-account-scope-manual"]').setValue(true)
      await wrapper.get('[data-testid="quality-account-picker"] input[value="4"]').setValue(true)
      await wrapper.get('[data-testid="quality-account-picker"] input[value="5"]').setValue(true)
    } else if (mode === 'edit') {
      await wrapper.get('[data-plan-id="1"] .rule-actions button:nth-child(2)').trigger('click'); await flushPromises()
    } else {
      await wrapper.get('[data-plan-id="1"] .rule-checkbox input').setValue(true)
      await wrapper.get('[data-plan-id="2"] .rule-checkbox input').setValue(true)
      await wrapper.get('[data-testid="quality-bulk-edit"]').trigger('click'); await flushPromises()
    }
    // Both actual source controls must be present in every editor mode.
    expect(wrapper.find('[data-testid="quality-preset-select"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="quality-source-account"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="quality-source-rule"]').exists()).toBe(true)
    if (origin === 'preset') {
      await wrapper.get('[data-testid="quality-preset-select"]').setValue('17')
      expect(vm.form.model_id).not.toBe(source.model_id)
      await wrapper.get('[data-testid="quality-apply-preset"]').trigger('click')
    } else {
      await wrapper.get('[data-testid="quality-source-account"]').setValue('90')
      expect(wrapper.get('[data-testid="quality-source-rule"]').text()).toContain('wrong-source-model')
      expect(wrapper.get('[data-testid="quality-apply-source"]').attributes('disabled')).toBeDefined()
      expect(vm.form.model_id).not.toBe(source.model_id)
      await wrapper.get('[data-testid="quality-source-rule"]').setValue('10')
      await wrapper.get('[data-testid="quality-apply-source"]').trigger('click')
    }
    await flushPromises()
    expect(vm.form.model_id).toBe(source.model_id)
    expect(wrapper.get('[data-testid="quality-reuse-preview"]').text()).toContain('qualityOps.reusePreview')
    expect(wrapper.get('[data-reuse-field="schedule"]').text()).toContain(source.cron_expression)
    expect(wrapper.get('[data-reuse-field="enabled"]').text()).toContain('qualityOps.off')
    if (mode === 'new') expect(vm.selectedAccounts).toEqual([4, 5])
    else {
      expect(vm.copying).toBe(false)
      if (mode === 'edit') { expect(vm.editing).toBe(1); expect(vm.form.pelican_config.rule_name).toBe('Target rule 1') }
      else { expect(vm.bulkRuleIds).toEqual([1, 2]); expect(vm.bulkFields).toEqual([]); expect(vm.bulkEditing).toBe(true) }
    }
    const assertUnchanged = () => expect(JSON.stringify([targets, source, alternative, saved])).toBe(originals)
    const assertNoWrite = () => {
      expect(scheduledTests.create).not.toHaveBeenCalled(); expect(scheduledTests.update).not.toHaveBeenCalled()
      expect(scheduledTests.delete).not.toHaveBeenCalled(); expect(runQualityPlan).not.toHaveBeenCalled()
      expect(createQualityPreset).not.toHaveBeenCalled(); expect(createQualityTemplate).not.toHaveBeenCalled(); expect(updateQualityTemplate).not.toHaveBeenCalled()
    }
    assertNoWrite(); assertUnchanged()
    return { wrapper, vm, targets, source, saved, assertUnchanged, assertNoWrite }
  }
  async function confirmReuseFields(wrapper: ReturnType<typeof mountView>) {
    for (const field of ['model', 'schedule', 'enabled']) await wrapper.get(`[data-testid="quality-bulk-field-${field}"]`).setValue(true)
    await wrapper.get('[data-testid="quality-reuse-confirm"]').setValue(true)
  }

  it.each(reuseCases)('prefills $mode from $origin and saves only the intended targets and fields', async testCase => {
    const { wrapper, vm, source, assertUnchanged, assertNoWrite } = await openReuseCase(testCase)
    if (testCase.mode === 'bulk') {
      await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
      assertNoWrite()
      expect(wrapper.get('.editor-error').text()).toContain('qualityOps.reuseConfirmRequired')
      await confirmReuseFields(wrapper)
      expect(wrapper.findAll('[data-reuse-target]')).toHaveLength(2)
    }
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    if (testCase.mode === 'new') {
      expect(vi.mocked(scheduledTests.create).mock.calls.map(([body]) => body.account_id)).toEqual([4, 5])
      expect(scheduledTests.update).not.toHaveBeenCalled()
      for (const [body] of vi.mocked(scheduledTests.create).mock.calls) {
        expect(body).toMatchObject({ model_id: source.model_id, cron_expression: source.cron_expression, enabled: false })
        for (const key of ['id', 'running_until', 'last_run_at', 'created_at']) expect(body).not.toHaveProperty(key)
        for (const key of ['trigger_source', 'quality_model_actions', 'quality_model_outcomes', 'quality_restore_diagnostic', 'credentials', 'quality_round_id', 'statistics']) expect(body.pelican_config).not.toHaveProperty(key)
      }
    } else if (testCase.mode === 'edit') {
      expect(scheduledTests.create).not.toHaveBeenCalled()
      expect(scheduledTests.update).toHaveBeenCalledTimes(1)
      const [id, body] = vi.mocked(scheduledTests.update).mock.calls[0]
      expect(id).toBe(1)
      expect(body).toMatchObject({ model_id: source.model_id, cron_expression: source.cron_expression, enabled: false,
        preserve_quality_state: true,
        pelican_config: { rule_name: 'Target rule 1', quality: { action: 'observe_only' } } })
      for (const key of ['account_id', 'id', 'running_until', 'last_run_at', 'created_at']) expect(body).not.toHaveProperty(key)
      for (const key of ['trigger_source', 'quality_model_actions', 'quality_model_outcomes', 'quality_restore_diagnostic', 'credentials', 'quality_round_id', 'statistics']) expect(body.pelican_config).not.toHaveProperty(key)
    } else {
      expect(scheduledTests.create).not.toHaveBeenCalled()
      expect(vi.mocked(scheduledTests.update).mock.calls).toEqual([1, 2].map(id => [id, { model_id: source.model_id, cron_expression: source.cron_expression, enabled: false, preserve_quality_state: true }]))
    }
    expect(vm.showForm).toBe(false)
    expect(runQualityPlan).not.toHaveBeenCalled(); expect(createQualityPreset).not.toHaveBeenCalled(); expect(createQualityTemplate).not.toHaveBeenCalled()
    assertUnchanged(); wrapper.unmount()
  })

  it.each(reuseCases)('cancels $mode after applying $origin without any write or source mutation', async testCase => {
    const { wrapper, vm, assertUnchanged, assertNoWrite } = await openReuseCase(testCase)
    if (testCase.mode === 'bulk') await confirmReuseFields(wrapper)
    await wrapper.get('.editor-footer .btn-secondary').trigger('click'); await flushPromises()
    expect(vm.discardPrompt).toBe(true)
    await wrapper.findAll('button').find(button => button.text() === 'qualityOps.discard')!.trigger('click'); await flushPromises()
    expect(vm.showForm).toBe(false)
    assertNoWrite(); assertUnchanged(); wrapper.unmount()
  })

  it.each(reuseCases)('retains failed $mode targets from $origin and retries only unfinished writes', async testCase => {
    const { wrapper, vm, targets, assertUnchanged } = await openReuseCase(testCase)
    if (testCase.mode === 'new') {
      vi.mocked(scheduledTests.create).mockResolvedValueOnce({ ...targets[0], id: 91, account_id: 4 }).mockRejectedValueOnce(new Error('Target save failed')).mockResolvedValueOnce({ ...targets[0], id: 92, account_id: 5 })
    } else if (testCase.mode === 'edit') {
      vi.mocked(scheduledTests.update).mockRejectedValueOnce(new Error('Target save failed')).mockResolvedValueOnce(targets[0])
    } else {
      await confirmReuseFields(wrapper)
      vi.mocked(scheduledTests.update).mockResolvedValueOnce(targets[0]).mockRejectedValueOnce(new Error('Target save failed')).mockResolvedValueOnce(targets[1])
    }
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(vm.showForm).toBe(true)
    expect(wrapper.get('.editor-error').text()).toContain('Target save failed')
    if (testCase.mode === 'new') expect(vm.selectedAccounts).toEqual([5])
    else if (testCase.mode === 'edit') expect(vm.editing).toBe(1)
    else {
      expect(vm.bulkRuleIds).toEqual([2])
      expect(wrapper.get('.editor-error').text()).toContain('qualityOps.bulkPartial')
      expect(vm.reuseConfirmed).toBe(false)
      await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
      expect(scheduledTests.update).toHaveBeenCalledTimes(2)
      await wrapper.get('[data-testid="quality-reuse-confirm"]').setValue(true)
    }
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(vm.showForm).toBe(false)
    if (testCase.mode === 'new') {
      expect(vi.mocked(scheduledTests.create).mock.calls.map(([body]) => body.account_id)).toEqual([4, 5, 5])
      expect(scheduledTests.update).not.toHaveBeenCalled()
    } else {
      expect(vi.mocked(scheduledTests.update).mock.calls.map(([id]) => id)).toEqual(testCase.mode === 'edit' ? [1, 1] : [1, 2, 2])
      expect(scheduledTests.create).not.toHaveBeenCalled()
    }
    expect(runQualityPlan).not.toHaveBeenCalled(); expect(createQualityPreset).not.toHaveBeenCalled(); expect(createQualityTemplate).not.toHaveBeenCalled()
    assertUnchanged(); wrapper.unmount()
  })

  it.each(['preset', 'account'] as const)('requires explicit dependent fields before bulk policy reuse from %s', async origin => {
    const targets = rules().slice(0, 2)
    targets.forEach((plan, index) => { plan.pelican_config!.quality!.auto_restore = true; plan.pelican_config!.quality!.trigger_on_upstream_5xx = index === 0 })
    const source = { ...rules()[0], id: 10, account_id: 90, model_id: 'source-model' }
    source.pelican_config!.quality = { action: 'observe_only', expected_answer: '', remove_group_ids: [], auto_restore: false, trigger_on_upstream_5xx: true }
    const original = JSON.stringify([targets, source])
    vi.mocked(listQualityPlans).mockResolvedValue([...targets, source])
    vi.mocked(listQualityPresets).mockResolvedValue([{ ...preset(), pelican_config: source.pelican_config! }])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    for (const id of [1, 2]) await wrapper.get(`[data-plan-id="${id}"] .rule-checkbox input`).setValue(true)
    await wrapper.get('[data-testid="quality-bulk-edit"]').trigger('click'); await flushPromises()
    if (origin === 'preset') {
      await wrapper.get('[data-testid="quality-preset-select"]').setValue('17')
      await wrapper.get('[data-testid="quality-apply-preset"]').trigger('click')
    } else {
      await wrapper.get('[data-testid="quality-source-account"]').setValue('90')
      await wrapper.get('[data-testid="quality-source-rule"]').setValue('10')
      await wrapper.get('[data-testid="quality-apply-source"]').trigger('click')
    }
    await wrapper.get('[data-testid="quality-bulk-field-action"]').setValue(true); await flushPromises()
    expect(wrapper.get('[data-reuse-target="1"]').text()).toContain('qualityOps.reuseDependentField')
    expect(wrapper.get('[data-testid="quality-reuse-confirm"]').attributes('disabled')).toBeDefined()
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.update).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="quality-bulk-field-restore"]').setValue(true)
    await wrapper.get('[data-testid="quality-reuse-confirm"]').setValue(true)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(vi.mocked(scheduledTests.update).mock.calls.map(([id]) => id)).toEqual([1, 2])
    for (const [id, body] of vi.mocked(scheduledTests.update).mock.calls) {
      expect(body).not.toHaveProperty('model_id'); expect(body).not.toHaveProperty('cron_expression'); expect(body).not.toHaveProperty('enabled')
      expect(body.pelican_config).toMatchObject({ quality: { action: 'observe_only', auto_restore: false, trigger_on_upstream_5xx: id === 1 } })
    }
    expect(vm.showForm).toBe(false)
    expect(scheduledTests.create).not.toHaveBeenCalled(); expect(runQualityPlan).not.toHaveBeenCalled()
    expect(JSON.stringify([targets, source])).toBe(original)
    wrapper.unmount()
  })

  it.each(['preset', 'account'] as const)('invalidates bulk confirmation after field or refreshed target changes for %s', async origin => {
    const { wrapper, vm, targets, source, assertNoWrite, assertUnchanged } = await openReuseCase({ mode: 'bulk', origin })
    await confirmReuseFields(wrapper)
    expect(vm.reuseConfirmed).toBe(true)
    await wrapper.get('[data-testid="quality-bulk-field-schedule"]').setValue(false)
    expect(vm.reuseConfirmed).toBe(false)
    await wrapper.get(`[data-testid="${origin === 'preset' ? 'quality-apply-preset' : 'quality-apply-source'}"]`).trigger('click'); await flushPromises()
    expect(vm.bulkFields).toEqual(['model', 'enabled'])
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    assertNoWrite()
    await wrapper.get('[data-testid="quality-reuse-confirm"]').setValue(true)
    const refreshed = targets.map(plan => plan.id === 1 ? { ...plan, cron_expression: '15 * * * *' } : plan)
    vi.mocked(listQualityPlans).mockResolvedValue([...refreshed, source])
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(wrapper.get('.editor-error').text()).toContain('qualityOps.reuseConfirmRequired')
    assertNoWrite()
    await wrapper.get('[data-testid="quality-reuse-confirm"]').setValue(true)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(vi.mocked(scheduledTests.update).mock.calls).toEqual([1, 2].map(id => [id, { model_id: source.model_id, enabled: false, preserve_quality_state: true }]))
    assertUnchanged(); wrapper.unmount()
  })

  it('rejects a source that is an edit target and prevents later bulk target selection from updating the source', async () => {
    const { wrapper, vm, assertUnchanged, assertNoWrite } = await openReuseCase({ mode: 'bulk', origin: 'account' })
    await confirmReuseFields(wrapper)
    await wrapper.get('[data-testid="quality-account-picker"] input[value="90"]').setValue(true)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(wrapper.get('.editor-error').text()).toContain('qualityOps.reuseSourceIsTarget')
    assertNoWrite()
    vm.showForm = false; await flushPromises()
    await wrapper.get('[data-plan-id="1"] .rule-actions button:nth-child(2)').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-source-account"]').setValue('1')
    await wrapper.get('[data-testid="quality-source-rule"]').setValue('1')
    await wrapper.get('[data-testid="quality-apply-source"]').trigger('click'); await flushPromises()
    expect(wrapper.get('.editor-error').text()).toContain('qualityOps.reuseSourceIsTarget')
    assertNoWrite(); assertUnchanged(); wrapper.unmount()
  })

  it('does not implicitly enlarge target history retention outside the bulk field scope', async () => {
    const targets = rules().slice(0, 2)
    for (const plan of targets) {
      plan.max_results = 1
      plan.pelican_config = { question_kind: 'candy', prompt: 'Own question', reasoning_effort: 'high', parallel_count: 1,
        quality: { action: 'disable_scheduling', expected_answer: '1', remove_group_ids: [], auto_restore: false, judge: { group_id: 21, model_id: 'test-judge', prompt: 'Compare' } } }
    }
    const saved = { ...preset(), pelican_config: { ...targets[0].pelican_config!, prompt: 'New question', parallel_count: 2 } }
    const original = JSON.stringify([targets, saved])
    vi.mocked(listQualityPlans).mockResolvedValue(targets)
    vi.mocked(listQualityPresets).mockResolvedValue([saved])
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="quality-select-rules"]').setValue(true)
    await wrapper.get('[data-testid="quality-bulk-edit"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-preset-select"]').setValue('17')
    await wrapper.get('[data-testid="quality-apply-preset"]').trigger('click')
    await wrapper.get('[data-testid="quality-bulk-field-test"]').setValue(true); await flushPromises()
    expect(wrapper.get('[data-reuse-target="1"]').text()).toContain('qualityOps.reuseRetention')
    expect(wrapper.get('[data-testid="quality-reuse-confirm"]').attributes('disabled')).toBeDefined()
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.create).not.toHaveBeenCalled(); expect(scheduledTests.update).not.toHaveBeenCalled()
    expect(JSON.stringify([targets, saved])).toBe(original)
    wrapper.unmount()
  })

  it('offers separate row actions and saves a named configuration template without creating account rules', async () => {
    const plan = rules()[0], original = JSON.stringify(plan)
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    vi.mocked(createQualityPreset).mockImplementationOnce(async body => {
      const saved = { ...body, id: 17, created_at: '', updated_at: '' }
      vi.mocked(listQualityPresets).mockResolvedValue([saved])
      return saved
    })
    let wrapper = mountView(); await flushPromises(); let vm = wrapper.vm as any
    const row = wrapper.get('[data-plan-id="1"]')
    expect(row.get('[data-testid="quality-rule-copy"]').text()).toBe('qualityOps.copy')
    expect(row.get('[data-testid="quality-rule-save-preset"]').text()).toBe('qualityOps.savePreset')
    await row.get('[data-testid="quality-rule-save-preset"]').trigger('click'); await flushPromises()
    expect(vm.savingPreset).toBe(true)
    expect(wrapper.find('[data-testid="quality-account-picker"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="quality-copy-hint"]').exists()).toBe(false)
    expect(vm.form.model_id).toBe('model-1')
    expect(vm.form.pelican_config.quality.remove_group_ids).toEqual([1])
    expect(createQualityPreset).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="quality-preset-name"]').setValue('Independent check template')
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(createQualityPreset).toHaveBeenCalledTimes(1)
    const body = vi.mocked(createQualityPreset).mock.calls[0][0]
    expect(body).toMatchObject({ name: 'Independent check template', model_id: 'model-1', cron_expression: plan.cron_expression, enabled: true })
    expect(body).not.toHaveProperty('account_id')
    expect(body).not.toHaveProperty('account_filter')
    expect(body.pelican_config).not.toHaveProperty('rule_name')
    expect(vm.showForm).toBe(false)
    expect(scheduledTests.create).not.toHaveBeenCalled()
    expect(scheduledTests.update).not.toHaveBeenCalled()
    expect(createQualityTemplate).not.toHaveBeenCalled()
    expect(updateQualityTemplate).not.toHaveBeenCalled()
    expect(runQualityPlan).not.toHaveBeenCalled()
    expect(JSON.stringify(plan)).toBe(original)
    wrapper.unmount()
    // A new page/store must load the template through the read API.
    wrapper = mountView(); await flushPromises(); vm = wrapper.vm as any
    vm.newPlan(); await flushPromises()
    expect(wrapper.get('[data-testid="quality-preset-select"]').text()).toContain('Independent check template')
    expect(listQualityPresets).toHaveBeenCalledTimes(3) // first load, post-save reload, remount
    wrapper.unmount()
  })

  it('validates template naming, cancels without writes, and retains the template draft after save errors', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue([rules()[0]])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    await wrapper.get('[data-testid="quality-rule-save-preset"]').trigger('click'); await flushPromises()
    await wrapper.get('.editor-footer .btn-secondary').trigger('click'); await flushPromises()
    expect(vm.showForm).toBe(false)
    expect(createQualityPreset).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="quality-rule-save-preset"]').trigger('click'); await flushPromises()
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(wrapper.get('.editor-error').text()).toContain('qualityOps.presetNameRequired')
    expect(createQualityPreset).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="quality-preset-name"]').setValue('Retry template')
    vi.mocked(createQualityPreset).mockRejectedValueOnce(new Error('storage unavailable')).mockResolvedValueOnce(preset())
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(wrapper.get('.editor-error').text()).toContain('qualityOps.presetSaveFailed')
    expect(wrapper.get('.editor-error').text()).toContain('storage unavailable')
    expect(vm.showForm).toBe(true)
    expect(vm.presetName).toBe('Retry template')
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(createQualityPreset).toHaveBeenCalledTimes(2)
    expect(scheduledTests.create).not.toHaveBeenCalled()
    expect(scheduledTests.update).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('selects a source account and rule while retaining the chosen target, and creates without a required rule name', async () => {
    const source = rules()[0], second = { ...rules()[1], account_id: 1 }, original = JSON.stringify([source, second])
    vi.mocked(listQualityPlans).mockResolvedValue([source, second])
    vi.mocked(accountsAPI.list).mockResolvedValue({ items: [{ id: 4, name: 'Target', platform: 'openai', type: 'oauth' }], total: 1 } as any)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); await flushPromises()
    await wrapper.get('[data-testid="quality-account-scope-manual"]').setValue(true)
    await wrapper.get('[data-testid="quality-account-picker"] input[value="4"]').setValue(true)
    await wrapper.get('[data-testid="quality-source-account"]').setValue('1')
    await wrapper.get('[data-testid="quality-source-rule"]').setValue('2')
    await wrapper.get('[data-testid="quality-apply-source"]').trigger('click'); await flushPromises()
    expect(vm.selectedAccounts).toEqual([4])
    expect(vm.form.model_id).toBe('model-2')
    expect(wrapper.get('[data-testid="quality-rule-name-input"]').attributes('required')).toBeUndefined()
    expect(vm.form.pelican_config.rule_name).toBe('')
    expect(scheduledTests.create).not.toHaveBeenCalled()
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.create).toHaveBeenCalledWith(expect.objectContaining({ account_id: 4, model_id: 'model-2', pelican_config: expect.objectContaining({ rule_name: '' }) }))
    expect(scheduledTests.update).not.toHaveBeenCalled()
    expect(createQualityPreset).not.toHaveBeenCalled()
    expect(createQualityTemplate).not.toHaveBeenCalled()
    expect(runQualityPlan).not.toHaveBeenCalled()
    expect(JSON.stringify([source, second])).toBe(original)
    wrapper.unmount()
  })

  it('reports a template reload failure after a successful save without retrying the write', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue([rules()[0]])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    await wrapper.get('[data-testid="quality-rule-save-preset"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-preset-name"]').setValue('Saved but reload failed')
    vi.mocked(createQualityPreset).mockResolvedValueOnce(preset())
    vi.mocked(listQualityPresets).mockRejectedValueOnce(new Error('read unavailable'))
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(vm.showForm).toBe(false)
    expect(wrapper.get('.workspace-message').text()).toContain('qualityOps.presetsLoadFailed')
    expect(createQualityPreset).toHaveBeenCalledTimes(1)
    vm.newPlan(); await flushPromises()
    expect(wrapper.get('[data-testid="quality-reuse-controls"]').text()).toContain('qualityOps.presetsLoadFailed')
    await vm.store.refreshPresets(); await flushPromises()
    expect(vm.store.presetsError).toBe('')
    expect(createQualityPreset).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('uses a persisted template in new and existing rule forms only after applying it, keeping targets and optional names', async () => {
    const saved = preset(), original = JSON.stringify(saved), plan = rules()[0]
    vi.mocked(listQualityPresets).mockResolvedValue([saved])
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); await flushPromises()
    await wrapper.get('[data-testid="quality-account-scope-manual"]').setValue(true)
    await wrapper.get('[data-testid="quality-rule-name-input"]').setValue('Optional local name')
    await wrapper.get('[data-testid="quality-action-observe-only"]').setValue(true)
    await wrapper.get('[data-testid="quality-account-picker"] input[value="1"]').setValue(true)
    await wrapper.get('[data-testid="quality-preset-select"]').setValue('17')
    expect(vm.form.model_id).not.toBe('preset-model')
    await wrapper.get('[data-testid="quality-apply-preset"]').trigger('click'); await flushPromises()
    expect(vm.selectedAccounts).toEqual([1])
    expect(vm.form.model_id).toBe('preset-model')
    expect(vm.form.pelican_config.rule_name).toBe('Optional local name')
    expect(scheduledTests.create).not.toHaveBeenCalled()
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.create).toHaveBeenCalledWith(expect.objectContaining({ account_id: 1, model_id: 'preset-model', enabled: false }))
    await wrapper.get('[data-plan-id="1"] .rule-actions button:nth-child(2)').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-preset-select"]').setValue('17')
    await wrapper.get('[data-testid="quality-apply-preset"]').trigger('click'); await flushPromises()
    expect(scheduledTests.update).not.toHaveBeenCalled()
    expect(vm.editing).toBe(1)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.update).toHaveBeenCalledWith(1, expect.objectContaining({ model_id: 'preset-model' }))
    expect(JSON.stringify(saved)).toBe(original)
    wrapper.unmount()
  })

  it('opens a prefilled create editor from the real rule row and cancels without writing', async () => {
    const plan = rules()[0]
    plan.pelican_config!.rule_name = 'Hourly quality'
    plan.pelican_config!.question_kind = 'candy'
    plan.pelican_config!.prompt = 'Configured question'
    plan.pelican_config!.model_ids = ['model-1', 'second-model']
    plan.pelican_config!.parallel_count = 3
    plan.pelican_config!.quality!.expected_answer = '42'
    plan.pelican_config!.quality!.judge = { group_id: 21, model_id: 'test-judge', prompt: 'Compare values' }
    plan.pelican_config!.quality!.auto_restore = true
    const original = JSON.stringify(plan)
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    vi.mocked(listQualityTemplates).mockResolvedValue([groupRule()])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    const row = wrapper.get('[data-plan-id="1"]')
    expect(row.text()).toContain('Hourly quality')
    expect(row.findAll('.rule-actions button')).toHaveLength(5)
    expect(wrapper.find('[data-testid="quality-bulk-edit"]').exists()).toBe(true)
    await row.get('[data-testid="quality-rule-copy"]').trigger('click'); await flushPromises()
    expect(vm.editing).toBeNull()
    expect(vm.editingTemplate).toBeNull()
    expect(vm.bulkEditing).toBe(false)
    expect(vm.selectedAccounts).toEqual([1])
    expect(vm.accountScope).toBe('manual')
    expect(wrapper.find('[data-testid="quality-account-scope-all"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="quality-plan-template-hint"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="quality-editor-delete"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="quality-rule-name-input"]').element).toHaveProperty('value', 'Hourly quality')
    expect(wrapper.get('#quality-prompt').element).toHaveProperty('value', 'Configured question')
    expect(wrapper.get('[data-testid="quality-extra-model"]').element).toHaveProperty('value', 'second-model')
    expect(wrapper.get('[data-testid="quality-account-picker"] input[value="1"]').element).toMatchObject({ checked: true, disabled: false })
    expect(vm.form).toMatchObject({ model_id: plan.model_id, cron_expression: plan.cron_expression, max_results: 100, enabled: true,
      pelican_config: { parallel_count: 3, quality: { expected_answer: '42', remove_group_ids: [1], auto_restore: true,
        judge: { group_id: 21, model_id: 'test-judge', prompt: 'Compare values' } } } })
    expect(vm.initialForm).toBe(vm.formSnapshot())
    await wrapper.get('.editor-footer .btn-secondary').trigger('click'); await flushPromises()
    expect(vm.showForm).toBe(false)
    expect(scheduledTests.create).not.toHaveBeenCalled()
    expect(scheduledTests.update).not.toHaveBeenCalled()
    expect(createQualityTemplate).not.toHaveBeenCalled()
    expect(updateQualityTemplate).not.toHaveBeenCalled()
    expect(runQualityPlan).not.toHaveBeenCalled()
    expect(JSON.stringify(plan)).toBe(original)
    wrapper.unmount()
  })

  it('creates an independently named rule on a changed target without copying runtime data or mutating the source', async () => {
    const plan = rules()[0]
    plan.running_until = '2099-01-01T00:00:00Z'
    plan.last_run_at = '2026-01-01T00:00:00Z'
    plan.next_run_at = '2026-02-01T00:00:00Z'
    Object.assign(plan.pelican_config!, { rule_name: 'Probe', model_id: 'result-model', trigger_source: 'upstream_5xx',
      quality_model_outcomes: { 'model-1': 'failed' }, quality_model_actions: { 'model-1': 'cooled' },
      quality_restore_diagnostic: { reason: 'group_missing', disposition: 'manual_required', scope: 'remove_groups', ownership_retained: true },
      credentials: { token: 'synthetic-secret' }, quality_round_id: 'previous-round', statistics: { failed: 9 } })
    const original = JSON.stringify(plan)
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    vi.mocked(accountsAPI.list).mockResolvedValue({ items: [1, 4].map(id => ({ id, name: `Account ${id}`, platform: 'openai', type: 'oauth' })), total: 2 } as any)
    vi.mocked(scheduledTests.create).mockImplementationOnce(async body => {
      const created = { ...body, id: 91, enabled: body.enabled ?? true, max_results: body.max_results ?? 100,
        auto_recover: false, last_run_at: null, next_run_at: null, created_at: '', updated_at: '' }
      vi.mocked(listQualityPlans).mockResolvedValue([plan, created])
      return created
    })
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    // A running source still has reusable configuration; its execution is not copied.
    await wrapper.get('[data-plan-id="1"] [data-testid="quality-rule-copy"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-rule-name-input"]').setValue('Independent probe')
    await wrapper.get('[data-testid="quality-account-picker"] input[value="1"]').setValue(false)
    await wrapper.get('[data-testid="quality-account-picker"] input[value="4"]').setValue(true)
    await wrapper.get('[data-testid="quality-copy-max-results"]').setValue(75)
    await wrapper.get('[data-testid="quality-trigger-5xx"]').setValue(true)
    await wrapper.get('[data-testid="quality-rule-enabled"]').setValue(false)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.create).toHaveBeenCalledTimes(1)
    const body = vi.mocked(scheduledTests.create).mock.calls[0][0]
    // Copying a legacy rule preserves disabled cleanup instead of the new-rule default.
    expect(body).toEqual({ account_id: 4, model_id: 'model-1', cron_expression: '*/30 * * * *', enabled: false, max_results: 75, auto_recover: false,
      pelican_config: { rule_name: 'Independent probe', question_kind: 'state_probe', test_channel: 'account', prompt: '', reasoning_effort: 'high', parallel_count: 1,
        model_ids: ['model-1'], quality: { trigger_on_upstream_5xx: true, oauth_401_max_rounds: 0, expected_answer: '', action: 'remove_groups', remove_group_ids: [1], auto_restore: false } } })
    expect(vm.plans.map((item: ScheduledTestPlan) => item.id)).toEqual([1, 91])
    expect(vm.showForm).toBe(false)
    expect(JSON.stringify(plan)).toBe(original)
    expect(scheduledTests.update).not.toHaveBeenCalled()
    expect(scheduledTests.delete).not.toHaveBeenCalled()
    expect(createQualityTemplate).not.toHaveBeenCalled()
    expect(updateQualityTemplate).not.toHaveBeenCalled()
    expect(runQualityPlan).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('keeps creation validation for duplicate policy scopes and saves on the original account after changing policy type', async () => {
    const plan = rules()[0]
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    await wrapper.get('[data-testid="quality-rule-copy"]').trigger('click'); await flushPromises()
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(wrapper.get('.editor-error').text()).toContain('qualityOps.copyAccountConflict')
    expect(scheduledTests.create).not.toHaveBeenCalled()
    expect(vm.showForm).toBe(true)
    await wrapper.get('[data-testid="quality-action-observe-only"]').setValue(true)
    expect(vm.selectedAccounts).toEqual([1])
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.create).toHaveBeenCalledWith(expect.objectContaining({ account_id: 1, pelican_config: expect.objectContaining({ quality: expect.objectContaining({ action: 'observe_only' }) }) }))
    expect(scheduledTests.update).not.toHaveBeenCalled()
    expect(plan.pelican_config!.quality!.action).toBe('remove_groups')
    wrapper.unmount()
  })

  it('preserves BPS thresholds, restoration options and mutable arrays without inheriting state', async () => {
    const plan = rules()[0]
    plan.pelican_config!.quality = { ...plan.pelican_config!.quality!, action: 'enable_bps', remove_group_ids: [], auto_restore: false,
      bps: { ...defaultQualityBPS(), failure_threshold: 4, usage_percent: 85, require_all: true, models: ['bps-model'],
        auto_recover_on_403: true, recovery_interval_minutes: 95, auto_move_on_403: true, target_group_id: 21,
        session_proxy: true, proxy_source: 'ip_pool', pass_threshold: 6, hold_on_usage: false } }
    Object.assign(plan.pelican_config!.quality.bps!, { failure_count: 3, pass_count: 5, owner_plan_id: 1, credentials: 'synthetic-secret' })
    const original = JSON.stringify(plan)
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    await wrapper.get('[data-testid="quality-rule-copy"]').trigger('click'); await flushPromises()
    expect(vm.form.pelican_config.quality.auto_restore).toBe(false)
    expect(vm.form.pelican_config.quality.bps).toEqual({ ...defaultQualityBPS(), failure_threshold: 4, usage_percent: 85, require_all: true, models: ['bps-model'],
      auto_recover_on_403: true, recovery_interval_minutes: 95, auto_move_on_403: true, target_group_id: 21,
      session_proxy: true, proxy_source: 'ip_pool', pass_threshold: 6, hold_on_usage: false })
    await wrapper.get('[data-testid="quality-bps-threshold"]').setValue(7)
    vm.form.pelican_config.quality.bps.models.push('another-model')
    expect(JSON.stringify(plan)).toBe(original)
    await wrapper.get('.editor-footer .btn-secondary').trigger('click'); await flushPromises()
    expect(vm.discardPrompt).toBe(true)
    const discard = wrapper.findAll('button').find(button => button.text() === 'qualityOps.discard')!
    await discard.trigger('click'); await flushPromises()
    expect(vm.showForm).toBe(false)
    expect(scheduledTests.create).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('retains a copied target outside the picker page and leaves its eligibility to the create API', async () => {
    const plan = { ...rules()[0], account_id: 72, account_name: 'Later page account' }
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    await wrapper.get('[data-testid="quality-rule-copy"]').trigger('click'); await flushPromises()
    expect(vm.selectedAccounts).toEqual([72])
    await wrapper.get('[data-testid="quality-action-observe-only"]').setValue(true)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.create).toHaveBeenCalledWith(expect.objectContaining({ account_id: 72 }))
    expect(scheduledTests.update).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('keeps the copy editor and original unchanged after a create error so the user can retry', async () => {
    const plan = rules()[0]
    const original = JSON.stringify(plan)
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    vi.mocked(scheduledTests.create).mockRejectedValueOnce({ response: { status: 400, data: { message: 'invalid test schedule' } } }).mockResolvedValueOnce({ ...plan, id: 91 })
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    await wrapper.get('[data-testid="quality-rule-copy"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-rule-name-input"]').setValue('')
    await wrapper.get('[data-testid="quality-account-picker"] input[value="1"]').setValue(false)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(wrapper.get('.editor-error').text()).toContain('qualityOps.copyAccountRequired')
    expect(scheduledTests.create).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="quality-account-picker"] input[value="1"]').setValue(true)
    await wrapper.get('[data-testid="quality-action-observe-only"]').setValue(true)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(wrapper.get('.editor-error').text()).toContain('qualityOps.copyFailed')
    expect(wrapper.get('.editor-error').text()).toContain('invalid test schedule')
    expect(vm.showForm).toBe(true)
    expect(vm.form.pelican_config.rule_name).toBe('')
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.create).toHaveBeenCalledTimes(2)
    expect(scheduledTests.update).not.toHaveBeenCalled()
    expect(JSON.stringify(plan)).toBe(original)
    wrapper.unmount()
  })

  it('disables copying without admin permission or while pending and rejects import-managed admission rules', async () => {
    const plan = rules()[0]
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    const button = () => wrapper.get('[data-testid="quality-rule-copy"]')
    vm.pending[1] = 'toggle'; await flushPromises()
    expect(button().attributes('disabled')).toBeDefined()
    vm.pending = {}; vm.busy = true; await flushPromises()
    expect(button().attributes('disabled')).toBeDefined()
    vm.busy = false
    useAuthStore().user!.role = 'observer'; await flushPromises()
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    await useAccountQualityStore().refreshRules(); await flushPromises()
    expect(button().attributes('disabled')).toBeDefined()
    expect(button().attributes('title')).toBe('qualityOps.copyPermission')
    expect(wrapper.get('[data-testid="quality-rule-save-preset"]').attributes('disabled')).toBeDefined()
    vm.saveRuleAsPreset(plan); await flushPromises()
    expect(vm.error).toBe('qualityOps.reusePermission')
    expect(createQualityPreset).not.toHaveBeenCalled()
    vm.copyRule(plan); await flushPromises()
    expect(vm.error).toBe('qualityOps.copyPermission')
    expect(vm.showForm).toBe(false)
    useAuthStore().user!.role = 'admin'; await flushPromises()
    const admission = { ...plan, pelican_config: { ...plan.pelican_config!, quality: { ...plan.pelican_config!.quality!, action: 'openai_two_fa_admission' as const } } }
    vi.mocked(listQualityPlans).mockResolvedValue([admission])
    await useAccountQualityStore().refreshRules(); await flushPromises()
    expect(button().attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="quality-rule-save-preset"]').attributes('disabled')).toBeDefined()
    vm.copyRule(admission); await flushPromises()
    expect(vm.error).toBe('qualityOps.copyUnsupported')
    expect(vm.showForm).toBe(false)
    expect(scheduledTests.create).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('adds built-in shortcuts without duplicates, reuses blank rows and saves the selected models', async () => {
    const plan = rules()[0]
    plan.model_id = 'gpt-6-astra'
    plan.pelican_config!.question_kind = 'candy'
    plan.pelican_config!.prompt = 'question'
    plan.pelican_config!.quality!.expected_answer = '21'
    plan.pelican_config!.quality!.judge = { group_id: 21, model_id: 'test-judge', prompt: 'Grade' }
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('.rule-actions button:nth-child(2)').trigger('click'); await flushPromises()
    const shortcuts = () => wrapper.findAll('[data-testid="quality-model-shortcut"]')
    expect(shortcuts().map(button => button.text())).toEqual(['gpt-6-astra', 'gpt-6.1-sol', 'gpt-5.6-sol'])
    expect(shortcuts()[0].attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="quality-add-model"]').trigger('click')
    await shortcuts()[1].trigger('click')
    await shortcuts()[1].trigger('click')
    await shortcuts()[2].trigger('click')
    expect(wrapper.findAll('[data-testid="quality-extra-model"]')).toHaveLength(2)
    expect((wrapper.vm as any).form.pelican_config.model_ids).toEqual(['gpt-6.1-sol', 'gpt-5.6-sol'])
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.update).toHaveBeenCalledWith(1, expect.objectContaining({
      model_id: 'gpt-6-astra', pelican_config: expect.objectContaining({ model_ids: ['gpt-6-astra', 'gpt-6.1-sol', 'gpt-5.6-sol'] }),
    }))
    wrapper.unmount()
  })

  it('persists batch custom shortcuts for later editing and removes labels independently of selected models', async () => {
    let wrapper = mountView(); await flushPromises()
    ;(wrapper.vm as any).newPlan(); await flushPromises()
    await wrapper.get('[data-testid="quality-model-shortcuts-input"]').setValue(' custom-a,custom-b\ncustom-a；gpt-6-astra，custom-c ')
    await wrapper.get('[data-testid="quality-save-model-shortcuts"]').trigger('click')
    expect(JSON.parse(localStorage.getItem('sub2api-quality-model-shortcuts:1')!)).toEqual(['custom-a', 'custom-b', 'custom-c'])
    expect((wrapper.vm as any).form.pelican_config.model_ids).toEqual([])
    wrapper.unmount()

    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    wrapper = mountView(); await flushPromises()
    await wrapper.get('.rule-actions button:nth-child(2)').trigger('click'); await flushPromises()
    const custom = wrapper.findAll('[data-testid="quality-model-shortcut"]').find(button => button.text() === 'custom-a')!
    await custom.trigger('click')
    expect((wrapper.vm as any).form.model_id).toBe('custom-a')
    await wrapper.findAll('[data-testid="quality-remove-model-shortcut"]')[0].trigger('click')
    expect((wrapper.vm as any).form.model_id).toBe('custom-a')
    expect(JSON.parse(localStorage.getItem('sub2api-quality-model-shortcuts:1')!)).toEqual(['custom-b', 'custom-c'])
    wrapper.unmount()
  })

  it('keeps custom shortcuts scoped to the current administrator', async () => {
    localStorage.setItem('sub2api-quality-model-shortcuts:1', JSON.stringify(['private-model']))
    const wrapper = mountView(); await flushPromises()
    ;(wrapper.vm as any).newPlan(); await flushPromises()
    expect(wrapper.get('[data-testid="quality-model-shortcuts"]').text()).toContain('private-model')
    useAuthStore().user!.id = 2; await flushPromises()
    // Changing identity closes the editor; reopen it as the new administrator.
    expect(wrapper.find('[data-testid="quality-model-shortcuts"]').exists()).toBe(false)
    ;(wrapper.vm as any).newPlan(); await flushPromises()
    expect(wrapper.get('[data-testid="quality-model-shortcuts"]').text()).not.toContain('private-model')
    expect(wrapper.findAll('[data-testid="quality-model-shortcut"]')).toHaveLength(3)
    wrapper.unmount()
  })

  it('replaces the only model for state probes when a shortcut is clicked', async () => {
    const wrapper = mountView(); await flushPromises()
    ;(wrapper.vm as any).newPlan(); await flushPromises()
    await wrapper.get('[data-testid="quality-question-kind"]').setValue('state_probe')
    await wrapper.findAll('[data-testid="quality-model-shortcut"]')[1].trigger('click')
    expect((wrapper.vm as any).form.model_id).toBe('gpt-6.1-sol')
    expect((wrapper.vm as any).form.pelican_config.model_ids).toEqual([])
    expect(wrapper.find('[data-testid="quality-extra-model"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('enforces the 50-model limit while allowing empty model slots to be filled', async () => {
    const wrapper = mountView(); await flushPromises()
    const vm = wrapper.vm as any
    vm.newPlan(); await flushPromises()
    vm.form.pelican_config.model_ids = Array.from({ length: 49 }, (_, index) => `model-${index}`)
    await flushPromises()
    expect(wrapper.findAll('[data-testid="quality-model-shortcut"]').every(button => button.attributes('disabled') !== undefined)).toBe(true)
    vm.form.pelican_config.model_ids[20] = ' '
    await flushPromises()
    await wrapper.findAll('[data-testid="quality-model-shortcut"]')[1].trigger('click')
    expect(vm.form.pelican_config.model_ids).toHaveLength(49)
    expect(vm.form.pelican_config.model_ids[20]).toBe('gpt-6.1-sol')
    vm.form.model_id = ''; await flushPromises()
    await wrapper.findAll('[data-testid="quality-model-shortcut"]')[0].trigger('click')
    expect(vm.form.model_id).toBe('gpt-6-astra')
    expect(vm.form.pelican_config.model_ids).toHaveLength(49)
    wrapper.unmount()
  })

  it('rejects overlong custom model names and tolerates invalid saved shortcut data', async () => {
    localStorage.setItem('sub2api-quality-model-shortcuts:1', '{invalid json')
    const wrapper = mountView(); await flushPromises()
    ;(wrapper.vm as any).newPlan(); await flushPromises()
    expect(wrapper.findAll('[data-testid="quality-model-shortcut"]')).toHaveLength(3)
    expect(wrapper.get('[data-testid="quality-model-shortcuts-error"]').text()).toBe('qualityOps.modelShortcutsLoadFailed')
    await wrapper.get('[data-testid="quality-model-shortcuts-input"]').setValue(`valid-model,${'x'.repeat(101)}`)
    await wrapper.get('[data-testid="quality-save-model-shortcuts"]').trigger('click')
    expect(wrapper.get('[data-testid="quality-model-shortcuts-error"]').text()).toBe('qualityOps.modelShortcutTooLong')
    expect(wrapper.findAll('[data-testid="quality-model-shortcut"]')).toHaveLength(3)
    wrapper.unmount()
  })

  it('switches to generated reasoning without saving a fixed question or answer', async () => {
    const plan = rules()[0]
    plan.pelican_config!.question_kind = 'candy'
    plan.pelican_config!.prompt = 'Existing operator question'
    plan.pelican_config!.quality!.expected_answer = 'Existing operator reference'
    plan.pelican_config!.quality!.judge = { group_id: 21, model_id: 'test-judge', prompt: 'Compare values' }
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('.rule-actions button:nth-child(2)').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-question-kind"]').setValue('random_reasoning')
    expect(wrapper.find('[data-testid="quality-random-hint"]').exists()).toBe(true)
    expect(wrapper.find('#quality-prompt').exists()).toBe(false)
    expect(wrapper.find('#quality-judge-models').exists()).toBe(true)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.update).toHaveBeenCalledWith(1, expect.objectContaining({
      pelican_config: expect.objectContaining({ question_kind: 'random_reasoning', prompt: '',
        quality: expect.objectContaining({ expected_answer: '', judge: { group_id: 21, model_id: 'test-judge', prompt: 'Compare values' } }) }),
    }))
    wrapper.unmount()
  })

  it('saves composed isolation through the shared policy payload', async () => {
    const plan = rules()[0]
    plan.pelican_config!.question_kind = 'candy'
    plan.pelican_config!.prompt = 'question'
    plan.pelican_config!.quality!.expected_answer = '21'
    plan.pelican_config!.quality!.judge = { group_id: 21, model_id: 'test-judge', prompt: 'Grade' }
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('.rule-actions button:nth-child(2)').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-action-isolate-models"]').setValue(true)
    expect(wrapper.get('[data-testid="quality-isolation-help"]').text()).toContain('qualityOps.isolationHelp')
    await wrapper.get('[data-testid="quality-add-model"]').trigger('click')
    await wrapper.get('[data-testid="quality-extra-model"]').setValue('astra')
    await wrapper.get('[data-testid="quality-auto-restore"]').setValue(true)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    const payload = vi.mocked(scheduledTests.update).mock.calls[0][1]
    expect(payload.pelican_config).toMatchObject({ model_ids: ['model-1', 'astra'], quality: {
      action: 'isolate_models', remove_group_ids: [1], auto_restore: true,
    } })
    expect(payload.pelican_config!.quality).not.toHaveProperty('remove_models')
    expect(payload.pelican_config!.quality).not.toHaveProperty('recovery_concurrency')
    wrapper.unmount()
  })

  it('saves multiple quality models and displays them on the rule card', async () => {
    const plan = rules()[0]
    plan.pelican_config!.question_kind = 'candy'
    plan.pelican_config!.prompt = 'question'
    plan.pelican_config!.quality!.expected_answer = '21'
    plan.pelican_config!.quality!.judge = { group_id: 21, model_id: 'test-judge', prompt: 'Grade' }
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('.rule-actions button:nth-child(2)').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-add-model"]').trigger('click')
    await wrapper.get('[data-testid="quality-extra-model"]').setValue('gpt-6-sol')
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.update).toHaveBeenCalledWith(1, expect.objectContaining({
      model_id: 'model-1', pelican_config: expect.objectContaining({ model_ids: ['model-1', 'gpt-6-sol'] }),
    }))
    wrapper.unmount()
    plan.pelican_config = { ...plan.pelican_config!, model_ids: ['model-1', 'gpt-6-sol'] } as any
    const saved = mountView(); await flushPromises()
    expect(saved.get('[data-plan-id="1"] .rule-model').text()).toContain('gpt-6-sol')
    saved.unmount()
  })

  it.each(['state_probe', 'candy'] as const)('preserves the saved 5xx trigger when editing a %s rule', async questionKind => {
    const plan = rules()[0]
    plan.pelican_config!.question_kind = questionKind
    plan.pelican_config!.quality!.trigger_on_upstream_5xx = true
    plan.pelican_config!.quality!.judge = { group_id: 21, model_id: 'test-judge', prompt: 'Compare answers' }
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('.rule-actions button:nth-child(2)').trigger('click'); await flushPromises()
    expect((wrapper.get('[data-testid="quality-trigger-5xx"]').element as HTMLInputElement).checked).toBe(true)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.update).toHaveBeenCalledWith(1, expect.objectContaining({
      pelican_config: expect.objectContaining({ quality: expect.objectContaining({ trigger_on_upstream_5xx: true }) }),
    }))
    wrapper.unmount()
  })

  it('preserves each rule 5xx setting when bulk editing account actions', async () => {
    const plans = rules().slice(0, 2)
    plans[0].pelican_config!.quality!.trigger_on_upstream_5xx = true
    plans[1].pelican_config!.quality!.trigger_on_upstream_5xx = false
    vi.mocked(listQualityPlans).mockResolvedValue(plans)
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="quality-select-rules"]').setValue(true)
    await wrapper.get('[data-testid="quality-bulk-edit"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="quality-trigger-5xx"]').exists()).toBe(false)
    await wrapper.get('[data-testid="quality-bulk-field-action"]').setValue(true)
    await wrapper.get('input[value="disable_scheduling"]').setValue(true)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    for (const [id, trigger] of [[1, true], [2, false]]) {
      expect(scheduledTests.update).toHaveBeenCalledWith(id, expect.objectContaining({
        pelican_config: expect.objectContaining({ quality: expect.objectContaining({ action: 'disable_scheduling', trigger_on_upstream_5xx: trigger }) }),
      }))
    }
    wrapper.unmount()
  })

  it('allows disabling an existing 5xx trigger', async () => {
    const plan = rules()[0]
    plan.pelican_config!.quality!.trigger_on_upstream_5xx = true
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('.rule-actions button:nth-child(2)').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-trigger-5xx"]').setValue(false)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.update).toHaveBeenCalledWith(1, expect.objectContaining({
      pelican_config: expect.objectContaining({ quality: expect.objectContaining({ trigger_on_upstream_5xx: false }) }),
    }))
    wrapper.unmount()
  })

  it.each([false, true])('creates rules with an explicit 5xx trigger value of %s', async trigger => {
    const wrapper = mountView(); await flushPromises()
    const vm = wrapper.vm as any
    vm.newPlan(); await flushPromises()
    expect((wrapper.get('[data-testid="quality-trigger-5xx"]').element as HTMLInputElement).checked).toBe(false)
    await wrapper.get('[data-testid="quality-trigger-5xx"]').setValue(trigger)
    await wrapper.get('[data-testid="quality-question-kind"]').setValue('state_probe')
    vm.form.pelican_config.quality.action = 'disable_scheduling'
    vm.accountScope = 'selected'
    vm.selectedAccounts = [1]
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.create).toHaveBeenCalledWith(expect.objectContaining({
      account_id: 1,
      pelican_config: expect.objectContaining({ quality: expect.objectContaining({ trigger_on_upstream_5xx: trigger }) }),
    }))
    wrapper.unmount()
  })

  it('confirms and deletes checked rules including hidden search selections', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    const wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-testid="quality-bulk-delete"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-plan-id="1"] .rule-checkbox input').setValue(true)
    await wrapper.get('.rule-search input').setValue('Account 2')
    await wrapper.get('[data-testid="quality-select-rules"]').setValue(true)
    await wrapper.get('[data-testid="quality-bulk-delete"]').trigger('click')
    const targets = wrapper.get('[data-testid="quality-delete-targets"]')
    expect(targets.text()).toContain('Account 1')
    expect(targets.text()).toContain('Account 2')
    expect(targets.text()).not.toContain('Account 3')
    expect(scheduledTests.delete).not.toHaveBeenCalled()
    vi.mocked(listQualityPlans).mockResolvedValue([rules()[2]])
    await wrapper.get('[data-testid="quality-confirm-delete"]').trigger('click'); await flushPromises()
    expect(vi.mocked(scheduledTests.delete).mock.calls).toEqual([[1], [2]])
    expect(wrapper.find('[data-testid="quality-delete-targets"]').exists()).toBe(false)
    expect((wrapper.vm as any).selectedRuleIds).toEqual([])
    expect(listQualityOperations).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('deletes rules selected in the bulk account picker without checking edit fields', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    vi.mocked(accountsAPI.list).mockResolvedValue({ items: [{ id: 2, name: 'Account 2' }, { id: 3, name: 'Account 3' }], total: 2 } as any)
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="quality-bulk-edit"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="quality-editor-delete"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="quality-select-page"]').trigger('click')
    expect(wrapper.get('button[form="quality-rule-form"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="quality-editor-delete"]').trigger('click')
    vi.mocked(listQualityPlans).mockResolvedValue([rules()[0]])
    await wrapper.get('[data-testid="quality-confirm-delete"]').trigger('click'); await flushPromises()
    expect(vi.mocked(scheduledTests.delete).mock.calls).toEqual([[2], [3]])
    expect(scheduledTests.update).not.toHaveBeenCalled()
    expect(wrapper.find('#quality-rule-form').exists()).toBe(false)
    wrapper.unmount()
  })

  it('continues batch deletion after an error and retries only failed rules', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    vi.mocked(scheduledTests.delete).mockResolvedValueOnce(undefined).mockRejectedValueOnce(new Error('delete denied')).mockResolvedValueOnce(undefined)
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="quality-select-rules"]').setValue(true)
    await wrapper.get('[data-testid="quality-bulk-delete"]').trigger('click')
    vi.mocked(listQualityPlans).mockResolvedValue([rules()[1]])
    await wrapper.get('[data-testid="quality-confirm-delete"]').trigger('click'); await flushPromises()
    expect(vi.mocked(scheduledTests.delete).mock.calls).toEqual([[1], [2], [3]])
    expect(wrapper.get('[data-testid="quality-delete-error"]').text()).toContain('#2: delete denied')
    expect((wrapper.vm as any).selectedRuleIds).toEqual([2])
    expect(wrapper.get('[data-testid="quality-delete-targets"]').findAll('li')).toHaveLength(1)
    vi.mocked(listQualityPlans).mockResolvedValue([])
    await wrapper.get('[data-testid="quality-confirm-delete"]').trigger('click'); await flushPromises()
    expect(vi.mocked(scheduledTests.delete).mock.calls).toEqual([[1], [2], [3], [2]])
    expect(wrapper.find('[data-testid="quality-delete-targets"]').exists()).toBe(false)
    wrapper.unmount()
  })

  const templateCases = (['observe_only', 'remove_groups', 'isolate_models', 'disable_scheduling'] as const).flatMap(action =>
    (['new', 'edit', 'bulk'] as const).flatMap(mode => (['preset', 'account'] as const).map(origin => ({ action, mode, origin }))))
  it.each(templateCases)('complete template roundtrip $action / $mode / $origin', async ({ action, mode, origin }) => {
    const targets = rules()
    const source = { ...rules()[0], id: 10, account_id: 90, model_id: 'model-a', enabled: false, max_results: 145, cron_expression: '7 */2 * * *' }
    source.pelican_config = { question_kind: 'candy', test_channel: 'account', prompt: 'Configured question', reasoning_effort: 'xhigh', parallel_count: 3, model_ids: ['model-a', 'model-b'],
      quality: { action, expected_answer: '42', remove_group_ids: action === 'remove_groups' || action === 'isolate_models' ? [21] : [], auto_restore: action !== 'observe_only', trigger_on_upstream_5xx: true,
        judge: { kind: 'jev', group_id: 0, model_id: '', prompt: 'Compare values' } } }
    Object.assign(source, { running_until: '2099-01-01T00:00:00Z', last_run_at: '2026-01-01T00:00:00Z', credentials: 'synthetic-no-copy' })
    Object.assign(source.pelican_config, { quality_model_actions: { 'model-a': 'isolated' }, trigger_source: 'old-worker' })
    const original = JSON.stringify(source)
    const saved = { ...preset(), id: 17, name: 'Complete saved check', model_id: source.model_id, cron_expression: source.cron_expression, enabled: source.enabled, max_results: source.max_results, pelican_config: source.pelican_config }
    vi.mocked(createQualityPreset).mockImplementation(async body => ({ ...JSON.parse(JSON.stringify(body)), id: 17, created_at: '', updated_at: '' }))
    vi.mocked(listQualityPresets).mockResolvedValueOnce([]).mockResolvedValue([saved])
    vi.mocked(listQualityPlans).mockResolvedValue([...targets, source])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    await wrapper.get('[data-plan-id="10"] [data-testid="quality-rule-save-preset"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="quality-action-observe-only"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="quality-action-remove-groups"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="quality-action-isolate-models"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="quality-action-disable-scheduling"]').exists()).toBe(true)
    if (action === 'remove_groups' || action === 'isolate_models') expect((wrapper.get('[data-testid="quality-target-group-21"]').element as HTMLInputElement).checked).toBe(true)
    await wrapper.get('[data-testid="quality-preset-name"]').setValue(saved.name)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(createQualityPreset).toHaveBeenCalledWith(expect.objectContaining({ name: saved.name, max_results: 145, enabled: false, cron_expression: source.cron_expression, pelican_config: expect.objectContaining({ model_ids: ['model-a', 'model-b'], question_kind: 'candy', parallel_count: 3, reasoning_effort: 'xhigh',
      quality: expect.objectContaining({ action, remove_group_ids: saved.pelican_config.quality!.remove_group_ids, auto_restore: action !== 'observe_only', trigger_on_upstream_5xx: true, judge: { kind: 'jev', group_id: 0, model_id: '', prompt: 'Compare values' } }) }) }))
    const stored = vi.mocked(createQualityPreset).mock.calls[0][0]
    expect(JSON.stringify(stored)).not.toContain('old-worker'); expect(JSON.stringify(stored)).not.toContain('quality_model_actions'); expect(JSON.stringify(stored)).not.toContain('synthetic-no-copy')
    expect(scheduledTests.create).not.toHaveBeenCalled(); expect(scheduledTests.update).not.toHaveBeenCalled(); expect(runQualityPlan).not.toHaveBeenCalled()
    if (mode === 'new') { vm.newPlan(); await flushPromises(); vm.selectedAccounts = [4]; vm.accountScope = 'manual' }
    else if (mode === 'edit') vm.edit(targets[0])
    else { vm.selectedRuleIds = [1, 2]; vm.editSelectedRules() }
    await flushPromises()
    if (origin === 'preset') { await wrapper.get('[data-testid="quality-preset-select"]').setValue('17'); await wrapper.get('[data-testid="quality-apply-preset"]').trigger('click') }
    else { await wrapper.get('[data-testid="quality-source-account"]').setValue('90'); await wrapper.get('[data-testid="quality-source-rule"]').setValue('10'); await wrapper.get('[data-testid="quality-apply-source"]').trigger('click') }
    await flushPromises()
    expect(vm.form.pelican_config.quality.action).toBe(action)
    expect(vm.form.pelican_config.quality.remove_group_ids).toEqual(saved.pelican_config.quality!.remove_group_ids)
    expect(scheduledTests.create).not.toHaveBeenCalled(); expect(scheduledTests.update).not.toHaveBeenCalled()
    if (mode === 'bulk') {
      for (const field of ['model', 'schedule', 'test', 'action', 'restore', 'retention', 'trigger', 'enabled']) await wrapper.get(`[data-testid="quality-bulk-field-${field}"]`).setValue(true)
      await wrapper.get('[data-testid="quality-reuse-confirm"]').setValue(true)
    }
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    const expected = expect.objectContaining({ model_id: 'model-a', cron_expression: source.cron_expression, enabled: false, max_results: 145,
      pelican_config: expect.objectContaining({ model_ids: ['model-a', 'model-b'], parallel_count: 3, quality: expect.objectContaining({ action, remove_group_ids: saved.pelican_config.quality!.remove_group_ids, auto_restore: action !== 'observe_only', trigger_on_upstream_5xx: true }) }) })
    if (mode === 'new') expect(scheduledTests.create).toHaveBeenCalledWith(expected)
    else { expect(scheduledTests.update).toHaveBeenCalledWith(1, expected); if (mode === 'bulk') expect(scheduledTests.update).toHaveBeenCalledWith(2, expected) }
    expect(JSON.stringify(source)).toBe(original); expect(runQualityPlan).not.toHaveBeenCalled(); wrapper.unmount()
  })

  it('edits passive presets with the full policy without distributing or saving a rule', async () => {
    const saved = preset(); saved.pelican_config.question_kind = 'candy'; saved.pelican_config.prompt = 'Question'; saved.pelican_config.parallel_count = 2
    saved.pelican_config.quality = { action: 'remove_groups', expected_answer: '42', remove_group_ids: [21], auto_restore: true, judge: { kind: 'jev', group_id: 0, model_id: '', prompt: 'Compare' } }
    vi.mocked(listQualityPresets).mockResolvedValue([saved]); vi.mocked(updateQualityPreset).mockResolvedValue(saved)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any; vm.newPlan(); await flushPromises()
    await wrapper.get('[data-testid="quality-preset-select"]').setValue('17'); await wrapper.get('[data-testid="quality-edit-preset"]').trigger('click'); await flushPromises()
    expect(vm.editingPreset).toBe(17); expect(wrapper.find('[data-testid="quality-account-picker"]').exists()).toBe(false)
    await wrapper.get('[data-testid="quality-action-isolate-models"]').setValue(true)
    await wrapper.get('[data-testid="quality-copy-max-results"]').setValue(147)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(updateQualityPreset).toHaveBeenCalledWith(17, expect.objectContaining({ max_results: 147, pelican_config: expect.objectContaining({ quality: expect.objectContaining({ action: 'isolate_models', remove_group_ids: [21], auto_restore: true }) }) }))
    expect(createQualityPreset).not.toHaveBeenCalled(); expect(scheduledTests.create).not.toHaveBeenCalled(); expect(scheduledTests.update).not.toHaveBeenCalled(); expect(createQualityTemplate).not.toHaveBeenCalled(); expect(runQualityPlan).not.toHaveBeenCalled(); wrapper.unmount()
  })

  it('normalizes legacy distribution-template editing without inheriting a punitive policy or result data', async () => {
    const tpl = groupRule()
    delete (tpl.pelican_config.quality as any).action
    tpl.pelican_config.quality!.remove_group_ids = [21]; tpl.pelican_config.quality!.auto_restore = true
    Object.assign(tpl.pelican_config, { quality_model_actions: { stale: 'isolated' }, trigger_source: 'old-worker' })
    vi.mocked(listQualityTemplates).mockResolvedValue([tpl])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.editTemplate(tpl); await flushPromises()
    expect(vm.form.pelican_config.quality.action).toBe('observe_only')
    expect(vm.form.pelican_config.quality.remove_group_ids).toEqual([])
    expect(vm.form.pelican_config.quality.auto_restore).toBe(false)
    expect(vm.form.pelican_config).not.toHaveProperty('quality_model_actions'); expect(vm.form.pelican_config).not.toHaveProperty('trigger_source')
    expect(updateQualityTemplate).not.toHaveBeenCalled(); expect(scheduledTests.update).not.toHaveBeenCalled(); wrapper.unmount()
  })

  it('defaults a legacy preset without action to safe observation and switches without stale policy fields', async () => {
    const punitive = { ...preset(), id: 18, pelican_config: { question_kind: 'candy' as const, prompt: 'Question', parallel_count: 2, reasoning_effort: 'high', quality: { action: 'isolate_models' as const, remove_group_ids: [21], auto_restore: true, expected_answer: '42', judge: { kind: 'jev' as const, group_id: 0, model_id: '', prompt: 'Grade' } } } }
    const legacy = structuredClone(punitive); legacy.id = 17; delete (legacy.pelican_config.quality as any).action
    vi.mocked(listQualityPresets).mockResolvedValue([punitive, legacy])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any; vm.newPlan(); await flushPromises()
    for (const id of ['18', '17']) { await wrapper.get('[data-testid="quality-preset-select"]').setValue(id); await wrapper.get('[data-testid="quality-apply-preset"]').trigger('click'); await flushPromises() }
    expect(vm.form.pelican_config.quality.action).toBe('observe_only'); expect(vm.form.pelican_config.quality.remove_group_ids).toEqual([]); expect(vm.form.pelican_config.quality.auto_restore).toBe(false)
    expect(scheduledTests.create).not.toHaveBeenCalled(); expect(scheduledTests.update).not.toHaveBeenCalled(); wrapper.unmount()
  })

  it('saves reduced scope after targeted orphan release while retaining changed peer and draft settings', async () => {
    const plan = rules()[0]
    plan.model_id = 'sol'; plan.pelican_config!.model_ids = ['sol', 'astra', 'missing']; plan.pelican_config!.quality!.action = 'isolate_models'
    plan.pelican_config!.question_kind = 'candy'; plan.pelican_config!.prompt = 'Own question'; plan.pelican_config!.quality!.expected_answer = '21'
    plan.pelican_config!.quality!.judge = { group_id: 21, model_id: 'test-judge', prompt: 'Grade' }
    const p = { plan_id: 1, account_id: 1, rule_version: 'v1', account_version: 'v1', fence: 'a'.repeat(64), action: 'isolate_models', models: ['sol', 'astra', 'missing'], group_ids: plan.pelican_config!.quality!.remove_group_ids, entries: [], relinquishable_count: 0, ownership_retained: true, restrictions_changed: false,
      model_removals: [{ model: 'sol', isolation_model: 'sol', ready: true, effect: 'remove_orphan_marker_only' }, { model: 'astra', isolation_model: 'astra', ready: false, effect: 'remove_target_only', blocked_reason: 'changed_same_owner' }] }
    vi.mocked(previewQualityOwnership).mockResolvedValueOnce(p).mockResolvedValue({ ...p, models: ['astra', 'missing'], model_removals: p.model_removals.slice(1) })
    vi.mocked(removeQualityOwnedModel).mockResolvedValueOnce({ ...p, fence: '', restrictions_changed: true })
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.edit(plan); await flushPromises()
    vm.form.pelican_config.rule_name = 'keep my draft'
    await wrapper.get('[data-testid="quality-removal-model"]').setValue('sol')
    await wrapper.get('[data-testid="quality-removal-reason"]').setValue('orphan_marker_recovery')
    await wrapper.get('[data-testid="quality-removal-confirm"]').setValue(true)
    await wrapper.get('[data-testid="quality-remove-model"]').trigger('click'); await flushPromises()
    expect(vm.form.model_id).toBe('astra'); expect(vm.form.pelican_config.model_ids).toEqual(['astra', 'missing'])
    expect(vm.form.pelican_config.rule_name).toBe('keep my draft')
    expect(vm.ownershipBlocked).toBe(false)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.update).toHaveBeenCalledWith(1, expect.objectContaining({ model_id: 'astra', pelican_config: expect.objectContaining({ model_ids: ['astra', 'missing'], rule_name: 'keep my draft', quality: expect.objectContaining({ action: 'isolate_models', remove_group_ids: p.group_ids }) }) }))
    expect(removeQualityOwnedModel).toHaveBeenCalledTimes(1); wrapper.unmount()
  })

  it('restores isolated ownership before deletion and retries deletion without restoring twice', async () => {
    const plan = rules()[0]; plan.pelican_config!.quality!.action = 'isolate_models'
    const owned = { plan_id: 1, account_id: 1, rule_version: 'v1', account_version: 'v1', fence: 'a'.repeat(64), action: 'isolate_models', models: ['model-1'], group_ids: [1], entries: [], relinquishable_count: 0, ownership_retained: true, restrictions_changed: false, restore_ready: true }
    vi.mocked(previewQualityOwnership).mockResolvedValueOnce(owned).mockResolvedValue({ ...owned, action: '', entries: [] })
    vi.mocked(restoreQualityOwnership).mockResolvedValueOnce({ ...owned, restrictions_changed: true })
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    vi.mocked(scheduledTests.delete).mockRejectedValueOnce({ status: 503, message: 'temporary delete failure' }).mockResolvedValueOnce(undefined)
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="quality-select-rules"]').setValue(true)
    await wrapper.get('[data-testid="quality-bulk-delete"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="quality-confirm-delete"]').attributes('disabled')).toBeDefined()
    expect(scheduledTests.delete).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="quality-restore-confirm"]').setValue(true)
    await wrapper.get('[data-testid="quality-restore-owned"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="quality-confirm-delete"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="quality-confirm-delete"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="quality-delete-error"]').text()).toContain('temporary delete failure')
    expect(wrapper.get('[data-testid="quality-delete-targets"]').text()).toContain('#1')
    vi.mocked(listQualityPlans).mockResolvedValue([])
    await wrapper.get('[data-testid="quality-confirm-delete"]').trigger('click'); await flushPromises()
    expect(restoreQualityOwnership).toHaveBeenCalledTimes(1); expect(vi.mocked(scheduledTests.delete).mock.calls).toEqual([[1, { fence: 'a'.repeat(64), confirm: true }], [1, { fence: 'a'.repeat(64), confirm: true }]])
    expect(wrapper.find('[data-testid="quality-delete-targets"]').exists()).toBe(false); wrapper.unmount()
  })

  it('can cancel deletion and still delete a single rule from its editor', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    const wrapper = mountView(); await flushPromises()
    const vm = wrapper.vm as any
    vm.edit(rules()[0]); await flushPromises()
    await wrapper.get('[data-testid="quality-editor-delete"]').trigger('click')
    await wrapper.get('[data-testid="quality-cancel-delete"]').trigger('click')
    expect(scheduledTests.delete).not.toHaveBeenCalled()
    expect(wrapper.find('#quality-rule-form').exists()).toBe(true)
    expect(wrapper.find('[data-testid="quality-delete-targets"]').exists()).toBe(false)
    await wrapper.get('[data-testid="quality-editor-delete"]').trigger('click')
    vi.mocked(listQualityPlans).mockRejectedValueOnce(new Error('refresh failed'))
    await wrapper.get('[data-testid="quality-confirm-delete"]').trigger('click'); await flushPromises()
    expect(vi.mocked(scheduledTests.delete).mock.calls).toEqual([[1]])
    expect(useAccountQualityStore().plans.map(plan => plan.id)).toEqual([2, 3])
    expect(wrapper.find('#quality-rule-form').exists()).toBe(false)
    wrapper.unmount()
  })

  it('keeps the confirmed rule IDs fixed if selection changes', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-plan-id="1"] .rule-checkbox input').setValue(true)
    await wrapper.get('[data-testid="quality-bulk-delete"]').trigger('click')
    const vm = wrapper.vm as any
    vm.selectedRuleIds = [2, 3]
    vi.mocked(listQualityPlans).mockResolvedValue(rules().slice(1))
    await wrapper.get('[data-testid="quality-confirm-delete"]').trigger('click'); await flushPromises()
    expect(vi.mocked(scheduledTests.delete).mock.calls).toEqual([[1]])
    expect(vm.selectedRuleIds).toEqual([2, 3])
    wrapper.unmount()
  })

  it.each(['unmount', 'identity'])('prevents duplicate submissions and stops deletion after %s changes', async (change) => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    let finish!: () => void
    vi.mocked(scheduledTests.delete).mockImplementationOnce(() => new Promise<void>(resolve => { finish = resolve }))
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="quality-select-rules"]').setValue(true)
    await wrapper.get('[data-testid="quality-bulk-delete"]').trigger('click')
    await wrapper.get('[data-testid="quality-confirm-delete"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="quality-confirm-delete"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="quality-cancel-delete"]').attributes('disabled')).toBeDefined()
    await (wrapper.vm as any).confirmDelete()
    expect(scheduledTests.delete).toHaveBeenCalledTimes(1)
    if (change === 'unmount') wrapper.unmount()
    else { useAuthStore().user!.id = 2; await flushPromises() }
    finish(); await flushPromises()
    expect(vi.mocked(scheduledTests.delete).mock.calls).toEqual([[1]])
    expect(listQualityOperations).toHaveBeenCalledTimes(1)
    if (change === 'identity') {
      expect(wrapper.find('[data-testid="quality-delete-targets"]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="quality-delete-error"]').exists()).toBe(false)
      wrapper.unmount()
    }
  })

  it('selects search matches independently of the history filter and preserves hidden selections', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-plan-id="1"] .rule-checkbox input').setValue(true)
    await wrapper.get('[data-plan-id="2"] .rule-select').trigger('click')
    expect(useAccountQualityStore().selectedPlanId).toBe(2)
    await wrapper.get('.rule-search input').setValue('Account 2')
    await wrapper.get('[data-testid="quality-select-rules"]').setValue(true)
    await wrapper.get('.rule-search input').setValue('')
    expect(wrapper.get('[data-testid="quality-select-rules"]').element).toHaveProperty('indeterminate', true)
    expect(wrapper.get('[data-plan-id="1"] .rule-checkbox input').element).toHaveProperty('checked', true)
    expect(wrapper.get('[data-plan-id="2"] .rule-checkbox input').element).toHaveProperty('checked', true)
    expect(wrapper.get('[data-plan-id="3"] .rule-checkbox input').element).toHaveProperty('checked', false)
    await wrapper.get('[data-testid="quality-clear-rules"]').trigger('click')
    expect(wrapper.get('[data-testid="quality-bulk-edit"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('opens bulk editing without preselection and selects only matching accounts with rules across pages', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    vi.mocked(accountsAPI.list).mockResolvedValue({ items: [{ id: 2, name: 'Account 2' }, { id: 4, name: 'No rule' }], total: 51 } as any)
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="quality-bulk-edit"]').trigger('click'); await flushPromises()
    expect(wrapper.get('button[form="quality-rule-form"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('#quality-rule-form input[type="checkbox"][value="4"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="quality-account-group"]').setValue('21')
    await wrapper.get('[data-testid="quality-account-type"]').setValue('apikey')
    await wrapper.get('#quality-account-search').setValue('matching')
    await wrapper.get('#quality-account-search').trigger('keydown', { key: 'Enter' }); await flushPromises()
    vi.mocked(accountsAPI.list).mockClear()
      .mockResolvedValueOnce({ items: [{ id: 2 }, { id: 4 }], total: 51 } as any)
      .mockResolvedValueOnce({ items: [{ id: 3 }, { id: 2 }], total: 51 } as any)
    await wrapper.get('[data-testid="quality-select-all"]').trigger('click'); await flushPromises()
    expect(accountsAPI.list).toHaveBeenLastCalledWith(2, 50, expect.objectContaining({ group: '21', type: 'apikey', search: 'matching' }))
    expect((wrapper.vm as any).bulkRuleIds).toEqual([2, 3])
    await wrapper.get('[data-testid="quality-account-type"]').setValue('oauth'); await flushPromises()
    expect((wrapper.vm as any).bulkRuleIds).toEqual([2, 3])
    await wrapper.get('[data-testid="quality-bulk-field-model"]').setValue(true)
    await wrapper.get('input[placeholder="gpt-6-astra"]').setValue('new-model')
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(vi.mocked(scheduledTests.update).mock.calls).toEqual([[2, { model_id: 'new-model' }], [3, { model_id: 'new-model' }]])
    expect(scheduledTests.create).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('removes unsupported selections when switching to probes and excludes them from select all', async () => {
    const accounts = [
      { id: 1, name: 'OAuth', platform: 'openai', type: 'oauth' },
      { id: 2, name: 'Key', platform: 'openai', type: 'apikey' },
      { id: 3, name: 'Claude', platform: 'anthropic', type: 'oauth' },
      { id: 4, name: 'Setup', platform: 'openai', type: 'setup-token' },
    ]
    vi.mocked(accountsAPI.list).mockResolvedValue({ items: accounts, total: 4 } as any)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises()
    await wrapper.get('[data-testid="quality-select-page"]').trigger('click')
    expect(vm.selectedAccounts).toEqual([1, 2, 3, 4])
    await wrapper.get('[data-testid="quality-question-kind"]').setValue('state_probe')
    expect(vm.selectedAccounts).toEqual([1, 4])
    expect(wrapper.get('#quality-rule-form input[type="checkbox"][value="2"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="quality-select-all"]').trigger('click'); await flushPromises()
    expect(vm.selectedAccounts).toEqual([1, 4])
    vm.form.pelican_config.quality.action = 'disable_scheduling'
    await vm.save()
    expect(vi.mocked(scheduledTests.create).mock.calls.map(([request]) => request.account_id)).toEqual([1, 4])
    wrapper.unmount()
  })

  it('prunes deleted selections after a refresh', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="quality-select-rules"]').setValue(true)
    vi.mocked(listQualityPlans).mockResolvedValue([])
    await useAccountQualityStore().refreshRules(true); await flushPromises()
    expect(wrapper.get('[data-testid="quality-bulk-edit"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('applies only checked fields, continues after a failure and retries only failed rules', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    vi.mocked(scheduledTests.update).mockResolvedValueOnce(rules()[0]).mockRejectedValueOnce(new Error('Temporary failure')).mockResolvedValue(rules()[2])
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="quality-select-rules"]').setValue(true)
    await wrapper.get('[data-testid="quality-bulk-edit"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="quality-account-group"]').exists()).toBe(true)
    expect(wrapper.get('button[form="quality-rule-form"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="quality-bulk-field-model"]').setValue(true)
    await wrapper.get('input[placeholder="gpt-6-astra"]').setValue('new-model')
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(vi.mocked(scheduledTests.update).mock.calls).toEqual([[1, { model_id: 'new-model' }], [2, { model_id: 'new-model' }], [3, { model_id: 'new-model' }]])
    expect(wrapper.text()).toContain('Temporary failure')
    expect(wrapper.find('#quality-rule-form').exists()).toBe(true)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(vi.mocked(scheduledTests.update).mock.calls.map(([id]) => id)).toEqual([1, 2, 3, 2])
    expect(wrapper.find('#quality-rule-form').exists()).toBe(false)
    wrapper.unmount()
  })

  it('identifies a non-probe rule in a 13-rule BPS batch and saves after explicitly changing the test method', async () => {
    const selected = Array.from({ length: 13 }, (_, index) => ({
      ...rules()[0], id: index + 1, account_id: index + 1, account_name: `Account ${index + 1}`,
      model_id: `model-${index + 1}`,
    }))
    selected[12].pelican_config = { question_kind: 'candy', prompt: 'Own question', reasoning_effort: 'low', parallel_count: 3,
      quality: { expected_answer: '7', action: 'disable_scheduling', remove_group_ids: [], auto_restore: false,
        judge: { group_id: 21, model_id: 'own-judge', prompt: 'Own grading instructions' } } }
    const before = JSON.stringify(selected)
    vi.mocked(listQualityPlans).mockResolvedValue(selected)
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="quality-select-rules"]').setValue(true)
    await wrapper.get('[data-testid="quality-bulk-edit"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-bulk-field-action"]').setValue(true)
    await wrapper.get('[data-testid="quality-action-enable-bps"]').setValue(true)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('qualityOps.rule #13: qualityOps.bpsRequiresProbe')
    expect(scheduledTests.update).not.toHaveBeenCalled()
    expect(JSON.stringify(selected)).toBe(before)
    expect((wrapper.vm as any).bulkRuleIds).toHaveLength(13)

    // The test method changes only after the user explicitly includes it.
    await wrapper.get('[data-testid="quality-bulk-field-test"]').setValue(true)
    await wrapper.get('[data-testid="quality-question-kind"]').setValue('state_probe')
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.update).toHaveBeenCalledTimes(13)
    for (const [index, [id, body]] of vi.mocked(scheduledTests.update).mock.calls.entries()) {
      expect(id).toBe(index + 1)
      expect(body).not.toHaveProperty('model_id')
      expect(body).not.toHaveProperty('cron_expression')
      expect(body.pelican_config).toMatchObject({ question_kind: 'state_probe', prompt: '', parallel_count: 1,
        quality: { action: 'enable_bps', expected_answer: '', remove_group_ids: [], auto_restore: false } })
      expect(body.pelican_config!.quality).not.toHaveProperty('judge')
    }
    expect(wrapper.find('#quality-rule-form').exists()).toBe(false)
    wrapper.unmount()
  })

  it('merges nested changes into refreshed per-rule settings', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="quality-select-rules"]').setValue(true)
    await wrapper.get('[data-testid="quality-bulk-edit"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-bulk-field-restore"]').setValue(true)
    const vm = wrapper.vm as any
    vm.form.pelican_config.quality.auto_restore = true
    const refreshed = rules(); refreshed[1].pelican_config!.quality!.remove_group_ids = [99]
    vi.mocked(listQualityPlans).mockResolvedValue(refreshed)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    const bodies = vi.mocked(scheduledTests.update).mock.calls.map(([, body]) => body)
    expect(bodies.map(body => body.pelican_config!.quality!.remove_group_ids)).toEqual([[1], [99], [3]])
    expect(bodies.every(body => body.pelican_config!.quality!.auto_restore)).toBe(true)
    expect(bodies.every(body => !('model_id' in body) && !('enabled' in body))).toBe(true)
    wrapper.unmount()
  })

  it('stops before any write if a selected rule disappeared or refreshing fails', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="quality-select-rules"]').setValue(true)
    await wrapper.get('[data-testid="quality-bulk-edit"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-bulk-field-enabled"]').setValue(true)
    vi.mocked(listQualityPlans).mockRejectedValueOnce(new Error('Refresh failed'))
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('Refresh failed')
    expect(scheduledTests.update).not.toHaveBeenCalled()
    vi.mocked(listQualityPlans).mockResolvedValue(rules().slice(0, 2))
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('qualityOps.ruleUnavailable')
    expect(scheduledTests.update).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('blocks duplicate saves and stops a running batch on unmount', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    let resolve!: (value: ScheduledTestPlan) => void
    vi.mocked(scheduledTests.update).mockImplementationOnce(() => new Promise(done => { resolve = done }))
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="quality-select-rules"]').setValue(true)
    await wrapper.get('[data-testid="quality-bulk-edit"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-bulk-field-enabled"]').setValue(true)
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.update).toHaveBeenCalledTimes(1)
    wrapper.unmount(); resolve(rules()[0]); await flushPromises()
    expect(scheduledTests.update).toHaveBeenCalledTimes(1)
  })
  it.each(['oauth', 'apikey'])('combines %s type, group and search across pages', async (type) => {
    vi.mocked(accountsAPI.list).mockResolvedValue({ items: [{ id: 2, name: 'Matching account' }], total: 51 } as any)
    const wrapper = mountView(); await flushPromises()
    const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises()
    await wrapper.get('[data-testid="quality-account-type"]').setValue(type)
    await wrapper.get('[data-testid="quality-account-group"]').setValue('21')
    await wrapper.get('#quality-account-search').setValue('  matching  ')
    await wrapper.get('#quality-account-search').trigger('keydown', { key: 'Enter' }); await flushPromises()
    expect(accountsAPI.list).toHaveBeenLastCalledWith(1, 50, expect.objectContaining({ type, group: '21', search: 'matching', lite: 'true' }))
    await wrapper.get('button[aria-label="qualityOps.nextAccountPage"]').trigger('click'); await flushPromises()
    expect(accountsAPI.list).toHaveBeenLastCalledWith(2, 50, expect.objectContaining({ type, group: '21', search: 'matching' }))
    await wrapper.get('[data-testid="quality-account-type"]').setValue(''); await flushPromises()
    expect(accountsAPI.list).toHaveBeenLastCalledWith(1, 50, expect.objectContaining({ type: undefined, group: '21', search: 'matching' }))
    wrapper.unmount()
  })
  it('selects only eligible accounts on this page and keeps selections across filters', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue([{ id: 4, account_id: 1 }] as any)
    vi.mocked(accountsAPI.list).mockResolvedValue({ items: [{ id: 1, name: 'Rule exists' }, { id: 2, name: 'New account' }], total: 51 } as any)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises(); vm.selectedAccounts = [99]
    await wrapper.get('[data-testid="quality-select-page"]').trigger('click')
    await wrapper.get('[data-testid="quality-select-page"]').trigger('click')
    expect(vm.selectedAccounts).toEqual([99, 2])
    expect(wrapper.get('input[type="checkbox"][value="1"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="quality-account-group"]').setValue('ungrouped'); await flushPromises()
    expect(accountsAPI.list).toHaveBeenLastCalledWith(1, 50, expect.objectContaining({ group: 'ungrouped' }))
    expect(vm.selectedAccounts).toEqual([99, 2])
    await wrapper.get('[data-testid="quality-clear-selection"]').trigger('click')
    expect(vm.selectedAccounts).toEqual([])
    wrapper.unmount()
  })
  it('selects accounts by rule type and drops conflicting selections when switching type', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue([
      { id: 4, account_id: 1, enabled: false, pelican_config: { quality: { action: 'remove_groups', remove_group_ids: [21] } } },
      { id: 5, account_id: 2, enabled: false, pelican_config: { quality: { action: 'enable_bps', remove_group_ids: [] } } }
    ] as any)
    vi.mocked(accountsAPI.list).mockResolvedValue({ items: [
      { id: 1, name: 'Group rule', platform: 'openai', type: 'oauth', extra: {} },
      { id: 2, name: 'BPS rule', platform: 'openai', type: 'oauth', extra: {} },
      { id: 3, name: 'New', platform: 'openai', type: 'oauth', extra: {} }
    ], total: 3 } as any)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises()
    await wrapper.get('[data-testid="quality-select-page"]').trigger('click')
    expect(vm.selectedAccounts).toEqual([2, 3])
    vm.form.pelican_config.question_kind = 'state_probe'
    vm.form.pelican_config.quality.action = 'enable_bps'
    await flushPromises()
    expect(vm.selectedAccounts).toEqual([3])
    expect(wrapper.get('input[type="checkbox"][value="1"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('input[type="checkbox"][value="2"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="quality-select-all"]').trigger('click'); await flushPromises()
    expect(vm.selectedAccounts).toEqual([3, 1])
    await vm.save()
    expect(vi.mocked(scheduledTests.create).mock.calls.map(([request]) => request.account_id)).toEqual([3, 1])
    wrapper.unmount()
  })
  it('selects all filtered pages without duplicate IDs or accounts with rules', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue([{ id: 4, account_id: 1 }] as any)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises()
    await wrapper.get('[data-testid="quality-account-type"]').setValue('oauth')
    await wrapper.get('[data-testid="quality-account-group"]').setValue('21'); await flushPromises()
    await wrapper.get('#quality-account-search').setValue('demo')
    await wrapper.get('#quality-account-search').trigger('keydown', { key: 'Enter' }); await flushPromises()
    vm.selectedAccounts = [99, 2]
    vi.mocked(accountsAPI.list).mockClear()
      .mockResolvedValueOnce({ items: [{ id: 1 }, { id: 2 }], total: 51 } as any)
      .mockResolvedValueOnce({ items: [{ id: 2 }, { id: 3 }], total: 51 } as any)
    await wrapper.get('[data-testid="quality-select-all"]').trigger('click'); await flushPromises()
    expect(accountsAPI.list).toHaveBeenNthCalledWith(1, 1, 50, expect.objectContaining({ type: 'oauth', group: '21', search: 'demo', sort_by: 'id', sort_order: 'asc' }))
    expect(accountsAPI.list).toHaveBeenNthCalledWith(2, 2, 50, expect.objectContaining({ type: 'oauth', group: '21', search: 'demo' }))
    expect(vm.selectedAccounts).toEqual([99, 2, 3])
    vm.form.pelican_config.quality.judge = { group_id: 21, model_id: 'test-judge', prompt: 'grade' }
    vm.form.pelican_config.quality.action = 'disable_scheduling'
    await vm.save()
    expect(vi.mocked(scheduledTests.create).mock.calls.map(([request]) => request.account_id)).toEqual([99, 2, 3])
    wrapper.unmount()
  })
  it('keeps the original selection if a later bulk page fails and allows retry', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises(); vm.selectedAccounts = [99]
    vi.mocked(accountsAPI.list).mockResolvedValueOnce({ items: [{ id: 2 }], total: 51 } as any).mockRejectedValueOnce(new Error('Page failed'))
    await wrapper.get('[data-testid="quality-select-all"]').trigger('click'); await flushPromises()
    expect(vm.selectedAccounts).toEqual([99]); expect(wrapper.text()).toContain('Page failed')
    await wrapper.get('[data-testid="quality-select-all"]').trigger('click'); await flushPromises()
    expect(vm.selectedAccounts).toEqual([99, 1]); expect(wrapper.text()).not.toContain('Page failed')
    wrapper.unmount()
  })
  it.each(['filter', 'clear', 'close'])('discards an in-flight bulk selection after %s', async (action) => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises()
    let resolve!: (value: any) => void
    vi.mocked(accountsAPI.list).mockImplementationOnce(() => new Promise(done => { resolve = done }))
    await wrapper.get('[data-testid="quality-select-all"]').trigger('click')
    expect(wrapper.get('button[form="quality-rule-form"]').attributes('disabled')).toBeDefined()
    if (action === 'filter') await wrapper.get('[data-testid="quality-account-type"]').setValue('apikey')
    else if (action === 'clear') await wrapper.get('[data-testid="quality-clear-selection"]').trigger('click')
    else { vm.closeForm(); vm.newPlan(); vm.accountScope = 'manual' }
    resolve({ items: [{ id: 77 }], total: 51 }); await flushPromises()
    expect(vm.selectedAccounts).toEqual([])
    expect(vm.selectingAccounts).toBe(false)
    expect(accountsAPI.list).not.toHaveBeenCalledWith(2, 50, expect.anything())
    wrapper.unmount()
  })
  it('ignores old list responses after filters change and clears failed results', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises()
    let resolve!: (value: any) => void
    vi.mocked(accountsAPI.list).mockImplementationOnce(() => new Promise(done => { resolve = done }))
    await wrapper.get('[data-testid="quality-account-type"]').setValue('oauth')
    vi.mocked(accountsAPI.list).mockResolvedValueOnce({ items: [{ id: 2, name: 'API result' }], total: 1 } as any)
    await wrapper.get('[data-testid="quality-account-type"]').setValue('apikey'); await flushPromises()
    resolve({ items: [{ id: 77, name: 'Stale OAuth result' }], total: 1 }); await flushPromises()
    expect(vm.accounts.map((account: any) => account.id)).toEqual([2])
    vi.mocked(accountsAPI.list).mockRejectedValueOnce(new Error('Search failed'))
    await wrapper.get('[data-testid="quality-account-group"]').setValue('21'); await flushPromises()
    expect(vm.accounts).toEqual([])
    expect(wrapper.text()).toContain('Search failed')
    expect(wrapper.get('[data-testid="quality-select-all"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
  it('prefills the test model and submits it for a new rule', async () => {
    const wrapper = mountView(); await flushPromises()
    const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises()
    expect(wrapper.find('input[placeholder="gpt-6-astra"]').element).toHaveProperty('value', 'gpt-6-astra')
    vm.selectedAccounts = [1]
    vm.form.pelican_config.quality.judge = { group_id: 21, model_id: 'test-judge', prompt: 'grade semantically' }
    vm.form.pelican_config.quality.remove_group_ids = [21]
    await vm.save()
    expect(scheduledTests.create).toHaveBeenCalledWith(expect.objectContaining({ account_id: 1, model_id: 'gpt-6-astra' }))
    wrapper.unmount()
  })
  it('requires explicit group selection and keeps automatic restoration opt-in', async () => {
    const wrapper = mountView(); await flushPromises()
    const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises(); vm.selectedAccounts = [1]; vm.form.pelican_config.quality.judge = {group_id:21,model_id:'test-judge',prompt:'grade semantically'}; vm.form.model_id = 'test-model'
    await vm.save()
    expect(scheduledTests.create).not.toHaveBeenCalled()
    expect(wrapper.find('[role="alert"]').text()).toContain('qualityOps.selectGroups')
    vm.form.pelican_config.quality.remove_group_ids = [21]
    await vm.save()
    // Fresh rules default cleanup to six rounds; automatic restoration stays opt-in.
    expect(scheduledTests.create).toHaveBeenCalledWith(expect.objectContaining({ account_id: 1, auto_recover: false, pelican_config: expect.objectContaining({ quality: { trigger_on_upstream_5xx: false, oauth_401_max_rounds: 6, expected_answer: '21', action: 'remove_groups', remove_group_ids: [21], auto_restore: false, judge: {group_id:21,model_id:'test-judge',prompt:'grade semantically'} } }) }))
    wrapper.unmount()
  })
  it('retries only accounts that were not created before a partial batch failure', async () => {
    vi.mocked(scheduledTests.create).mockResolvedValueOnce({ id: 1 } as any).mockRejectedValueOnce(new Error('failed')).mockResolvedValueOnce({ id: 2 } as any)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; vm.selectedAccounts = [1, 2]; vm.form.pelican_config.quality.judge = {group_id:21,model_id:'test-judge',prompt:'grade semantically'}; vm.form.model_id = 'test-model'; vm.form.pelican_config.quality.action = 'disable_scheduling'
    await vm.save(); expect(vm.selectedAccounts).toEqual([2]); expect(vm.showForm).toBe(true)
    await vm.save()
    expect(vi.mocked(scheduledTests.create).mock.calls.map(([request]) => request.account_id)).toEqual([1, 2, 2])
    wrapper.unmount()
  })
  it.each([false, true])('keeps wrong answers separate from missing shared controls (legacy=%s)', async legacy => {
    const batch = { id: 'synthetic-batch', started_at: '2026-01-01T00:00:00Z', start_deadline: '2026-01-01T00:02:00Z', closes_at: '2026-01-01T00:12:00Z', decision: 'no_passing_peer', peer_accounts: 2, peer_passed: 0, peer_failed: 2, peer_inconclusive: 0, comparison_key: 'synthetic', route: 'synthetic', mapped_model: 'test-model' }
    const cfg = { question_kind: 'random_reasoning', model_id: 'test-model', prompt: 'Synthetic question', reasoning_effort: 'medium', parallel_count: 1, random_question: {id: 'synthetic', family: 'paths', version: 'fixture'}, quality: { action: 'disable_scheduling', expected_answer: '42', auto_restore: false, remove_group_ids: [] }, quality_batch: legacy ? undefined : batch }
    const operation = { id: 101, plan_id: 4, account_id: 1, account_name: 'Synthetic account', status: 'failed', error_message: 'answer_mismatch', quality_action: 'inconclusive', passed_count: 0, total_count: 1, result_ids: [101], pelican_config: cfg }
    vi.mocked(listQualityOperations).mockResolvedValueOnce({ items: [operation] as any, next_cursor: 0 })
    vi.mocked(scheduledTests.getResult).mockResolvedValue({ ...operation, response_text: '41', quality_judgment: {verdict: 'incorrect', reason: 'Different answer'} } as any)
    const wrapper = mountView(); await flushPromises()
    await (wrapper.vm as any).operationDetails(operation); await flushPromises()
    expect(wrapper.get('[data-testid="quality-result-badge"]').text()).toBe('qualityOps.wrongAnswer')
    expect(wrapper.get('[data-testid="quality-batch-evidence"]').text()).toContain(legacy ? 'qualityOps.batchLegacy' : 'qualityOps.batchDecisions.no_passing_peer')
    expect(wrapper.text()).not.toContain('qualityOps.outcomes.groups_removed')
    wrapper.unmount()
  })
  it('shows one operation row per round and loads response bodies only on demand', async () => {
    const operation = { id: 10, plan_id: 4, account_id: 1, account_name: 'Test account', status: 'success', error_message: '', quality_action: 'restored', passed_count: 2, total_count: 2, result_ids: [10, 9], pelican_config: { quality: { action: 'remove_groups', remove_group_ids: [21] } } }
    vi.mocked(listQualityOperations).mockResolvedValueOnce({ items: [operation] as any, next_cursor: 10 })
    vi.mocked(scheduledTests.getResult).mockResolvedValue({ id: 10, status: 'success', error_message: '', response_text: '21个。', quality_judgment: { verdict: 'correct', reason: 'same answer', model_id: 'custom-judge' } } as any)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    expect(wrapper.text()).toContain('qualityOps.operations')
    expect(wrapper.text()).toContain('2 / 2')
    expect(wrapper.text()).toContain('qualityOps.groupsRestored')
    expect(scheduledTests.getResult).not.toHaveBeenCalled()
    await vm.operationDetails(operation)
    expect(scheduledTests.getResult).toHaveBeenCalledWith(4, 10)
    expect(scheduledTests.getResult).not.toHaveBeenCalledWith(4, 9)
    await vm.selectResult(vm.results[1])
    expect(scheduledTests.getResult).toHaveBeenCalledWith(4, 9)
    expect(wrapper.text()).toContain('same answer')
    await vm.moreOperations(); expect(listQualityOperations).toHaveBeenLastCalledWith(10)
    wrapper.unmount()
  })
  it('requires the operator to select a judge model and group', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; vm.selectedAccounts = [1]; vm.form.model_id = 'test-model'
    await vm.save(); expect(scheduledTests.create).not.toHaveBeenCalled()
    expect(wrapper.find('[role="alert"]').text()).toContain('qualityOps.configureJudge')
    wrapper.unmount()
  })
  it('explicitly selects Jev, clears ordinary routing and saves without loading audit secrets', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; vm.selectedAccounts = [1]; vm.form.model_id = 'test-model'
    vm.form.pelican_config.quality.action = 'disable_scheduling'
    vm.form.pelican_config.quality.judge = { group_id: 21, model_id: 'test-judge', prompt: 'Compare values' }
    await flushPromises()
    expect(wrapper.get('[data-testid="quality-judge-kind"]').element).toHaveProperty('value', 'llm')
    await wrapper.get('[data-testid="quality-judge-kind"]').setValue('jev')
    expect(wrapper.get('[data-testid="quality-jev-hint"]').text()).toContain('qualityOps.judgeJevHint')
    expect(wrapper.find('#quality-judge-models').exists()).toBe(false)
    expect(vm.form.pelican_config.quality.judge).toEqual({ kind: 'jev', group_id: 0, model_id: '', prompt: 'Compare values' })
    vi.mocked(scheduledTests.create).mockResolvedValue({ id: 8 } as any)
    await vm.save()
    expect(scheduledTests.create).toHaveBeenCalledWith(expect.objectContaining({ pelican_config: expect.objectContaining({ quality: expect.objectContaining({ judge: { kind: 'jev', group_id: 0, model_id: '', prompt: 'Compare values' } }) }) }))
    wrapper.unmount()
  })
  it('renders Jev safe reason codes and separate latency without showing an upstream reason', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    const result = { quality_judgment: { kind: 'jev', verdict: 'unknown', reason: 'judge_jev_credentials_rejected' } }
    expect(vm.judgmentReason(result)).toBe('qualityOps.jevReasons.credentials_rejected')
    result.quality_judgment.reason = 'SYNTHETIC_PROVIDER_SECRET'
    expect(vm.judgmentReason(result)).toBe('qualityOps.jevReasons.uncertain')
    wrapper.unmount()
  })
  it('saves a state probe rule without a question, reference answer, judge or parallel runs', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises()
    vm.selectedAccounts = [1]; vm.form.pelican_config.parallel_count = 3
    await wrapper.find('[data-testid="quality-question-kind"]').setValue('state_probe')
    expect(wrapper.find('[data-testid="quality-probe-hint"]').exists()).toBe(true)
    expect(wrapper.find('#quality-prompt').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('qualityOps.judgeTitle')
    vm.form.pelican_config.quality.remove_group_ids = [21]
    await vm.save()
    const request = vi.mocked(scheduledTests.create).mock.calls[0][0] as any
    expect(request).toMatchObject({ account_id: 1, model_id: 'gpt-6-astra', pelican_config: { question_kind: 'state_probe', prompt: '', parallel_count: 1, quality: { expected_answer: '', action: 'remove_groups', remove_group_ids: [21], auto_restore: false } } })
    expect(request.pelican_config.quality).not.toHaveProperty('judge')
    wrapper.unmount()
  })
  it('switching a probe rule back to candy restores the question and judge fields', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.edit({ id: 5, account_id: 1, model_id: 'gpt-6-astra', cron_expression: '*/30 * * * *', enabled: true, max_results: 100, pelican_config: { question_kind: 'state_probe', prompt: '', reasoning_effort: 'high', parallel_count: 1, quality: { expected_answer: '', action: 'disable_scheduling', remove_group_ids: [], auto_restore: true } } })
    await flushPromises()
    expect(wrapper.find('#quality-prompt').exists()).toBe(false)
    await wrapper.find('[data-testid="quality-question-kind"]').setValue('candy')
    expect(wrapper.find('#quality-prompt').exists()).toBe(true)
    expect(vm.form.pelican_config.quality.expected_answer).toBe('21')
    expect(vm.form.pelican_config.quality.judge).toEqual({ group_id: 0, model_id: '', prompt: 'qualityOps.defaultJudgePrompt' })
    await vm.save()
    expect(scheduledTests.update).not.toHaveBeenCalled()
    expect(wrapper.find('[role="alert"]').text()).toContain('qualityOps.configureJudge')
    wrapper.unmount()
  })
  it.each(['state_probe', 'candy'] as const)('shows unsupported %s models as neutral skips without a fabricated answer', async kind => {
    const plan = rules()[0]
    plan.pelican_config!.question_kind = kind
    if (kind === 'candy') plan.pelican_config!.quality!.judge = { group_id: 21, model_id: 'test-judge', prompt: 'Compare answers' }
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    const skipped = { id: 9, status: 'skipped', error_message: 'model_unsupported', response_text: '', pelican_config: plan.pelican_config }
    vi.mocked(scheduledTests.listResults).mockResolvedValue([skipped] as any)
    vi.mocked(scheduledTests.getResult).mockResolvedValue(skipped as any)
    const wrapper = mountView(); await flushPromises()
    const vm = wrapper.vm as any
    await vm.history(vm.plans[0]); await flushPromises()
    expect(wrapper.get('[data-testid="quality-result-badge"]').text()).toBe('qualityOps.modelUnsupportedSkipped')
    expect(wrapper.get('[data-testid="quality-result-badge"]').classes()).toContain('tone-neutral')
    expect(wrapper.get('.result-navigation button span:last-child').classes()).not.toContain('text-rose-600')
    expect(wrapper.get('.response-content').text()).not.toContain('model_unsupported')
    expect(wrapper.find('.answer-reference').exists()).toBe(false)
    vm.closeDetails()
    vm.edit(vm.plans[0]); await flushPromises()
    vi.mocked(scheduledTests.update).mockRejectedValueOnce({ response: { data: { message: '账号 #1 不支持检测模型：model-A' } } })
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('账号 #1 不支持检测模型：model-A')
    expect(wrapper.find('#quality-rule-form').exists()).toBe(true)
    wrapper.unmount()
  })

  it('distinguishes actual model cooldowns from configured targets in a round', async () => {
    const plan = rules()[0]
    const cfg = { ...plan.pelican_config!, model_ids: ['gpt-6-astra', 'gpt-6.1-sol'],
      quality: { ...plan.pelican_config!.quality!, action: 'remove_models', remove_models: ['gpt-6-astra', 'gpt-5.6-sol', 'gpt-6.1-sol'] },
      quality_model_outcomes: { 'gpt-6-astra': 'passed', 'gpt-6.1-sol': 'failed', 'gpt-5.6-sol': 'skipped' },
      quality_model_actions: { 'gpt-6-astra': 'passed', 'gpt-6.1-sol': 'models_cooled' } }
    const op = { id: 11, plan_id: plan.id, account_id: 1, account_name: 'Account 1', passed_count: 1, total_count: 2,
      status: 'failed', quality_action: 'models_cooled', result_ids: [], pelican_config: cfg }
    vi.mocked(listQualityPlans).mockResolvedValue([plan])
    vi.mocked(listQualityOperations).mockResolvedValue({ items: [op as any], next_cursor: 0 })
    const wrapper = mountView(); await flushPromises()
    await (wrapper.vm as any).operationDetails(op); await flushPromises()
    const rows = wrapper.findAll('[data-testid="quality-model-summary"] tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('gpt-6-astra')
    expect(rows[0].text()).toContain('qualityOps.noNewCooldown')
    expect(rows[1].text()).toContain('gpt-6.1-sol')
    expect(rows[1].text()).toContain('qualityOps.cooldownApplied')
    expect(wrapper.get('.detail-policy dt').text()).toBe('qualityOps.cooldownTargets')
    wrapper.unmount()
  })

  it('shows an uncertain action result without claiming an old record or successful cooldown', async () => {
    const cfg = { ...rules()[0].pelican_config!, model_ids: ['gpt-6-astra'],
      quality: { ...rules()[0].pelican_config!.quality!, action: 'remove_models', remove_models: ['gpt-6-astra'] },
      quality_model_outcomes: { 'gpt-6-astra': 'failed' } }
    const op = { id: 12, plan_id: 1, account_id: 1, account_name: 'Account 1', passed_count: 0, total_count: 1,
      status: 'failed', quality_action: 'action_error', result_ids: [], pelican_config: cfg }
    const wrapper = mountView(); await flushPromises()
    await (wrapper.vm as any).operationDetails(op); await flushPromises()
    const summary = wrapper.get('[data-testid="quality-model-summary"]').text()
    expect(summary).toContain('qualityOps.cooldownUnconfirmed')
    expect(summary).not.toContain('qualityOps.cooldownNotRecorded')
    expect(summary).not.toContain('qualityOps.cooldownApplied')
    wrapper.unmount()
  })

  it('tags probe rules and labels probe results as full capability, degraded or inconclusive', async () => {
    const probe = { question_kind: 'state_probe', prompt: '', reasoning_effort: 'high', parallel_count: 1, quality: { expected_answer: '', action: 'disable_scheduling', remove_group_ids: [], auto_restore: false } }
    vi.mocked(listQualityPlans).mockResolvedValue([{ id: 1, account_id: 1, account_name: 'Probe account', model_id: 'gpt-6-astra', cron_expression: '*/30 * * * *', enabled: true, max_results: 100, pelican_config: probe }] as any)
    vi.mocked(scheduledTests.listResults).mockResolvedValue([
      { id: 7, status: 'failed', error_message: 'state_degraded', pelican_config: probe },
      { id: 6, status: 'failed', error_message: 'state_probe_inconclusive: network', pelican_config: probe },
      { id: 5, status: 'success', error_message: '', pelican_config: probe }
    ] as any)
    vi.mocked(scheduledTests.getResult).mockResolvedValue({ id: 7, status: 'failed', error_message: 'state_degraded', response_text: '判定：降智', pelican_config: probe, quality_judgment: { verdict: 'incorrect', reason: 'new ticket' } } as any)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    expect(wrapper.find('[data-testid="quality-probe-tag"]').exists()).toBe(true)
    expect(wrapper.find('.rule-warning').exists()).toBe(false)
    await vm.history(vm.plans[0]); await flushPromises()
    const labels = wrapper.findAll('.result-navigation button').map(button => button.text())
    expect(labels[0]).toContain('qualityOps.probeDegraded')
    expect(labels[1]).toContain('qualityOps.probeInconclusive')
    expect(labels[2]).toContain('qualityOps.probeHealthy')
    expect(wrapper.find('[data-testid="quality-result-badge"]').text()).toBe('qualityOps.probeDegraded')
    expect(wrapper.find('pre').text()).toContain('判定：降智')
    expect(wrapper.find('.answer-reference').exists()).toBe(false)
    expect(wrapper.find('.judge-reason').exists()).toBe(false)
    wrapper.unmount()
  })
  it('offers enable BPS only for probe rules and saves the normalized BPS policy', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises(); vm.selectedAccounts = [1]
    expect(wrapper.find('[data-testid="quality-action-enable-bps"]').exists()).toBe(false)
    await wrapper.find('[data-testid="quality-question-kind"]').setValue('state_probe')
    expect((wrapper.find('[data-testid="quality-auto-restore"]').element as HTMLInputElement).checked).toBe(false)
    await wrapper.find('[data-testid="quality-action-enable-bps"]').setValue(true)
    expect(wrapper.find('[data-testid="quality-bps-settings"]').exists()).toBe(true)
    expect((wrapper.find('[data-testid="quality-bps-auto-disable"]').element as HTMLInputElement).checked).toBe(true)
    expect(wrapper.find('[data-testid="quality-auto-restore"]').exists()).toBe(false)
    const checked = (id: string) => (wrapper.find(`[data-testid="quality-bps-${id}"]`).element as HTMLInputElement).checked
    expect([checked('omit_unsupported_tools'), checked('ignore_encrypted_content'), checked('auto_disable_on_403'), checked('cache_creation_as_input')]).toEqual([false, true, true, true])
    expect(checked('auto_recover_on_403')).toBe(false)
    expect((wrapper.find('[data-testid="quality-bps-auto_recover_on_403"]').element as HTMLInputElement).disabled).toBe(false)
    expect(wrapper.get<HTMLSelectElement>('[data-testid="quality-probe-interval"]').element.value).toBe('*/2 * * * *')
    await wrapper.get('[data-testid="quality-probe-interval"]').setValue('*/10 * * * *')
    expect(wrapper.find('[data-testid="model-selector"]').text()).toBe('gpt-6-astra,gpt-5.6-sol,gpt-5.6-terra')
    expect(wrapper.find('[data-testid="quality-bps-require-all"]').exists()).toBe(false)
    expect((wrapper.find('[data-testid="quality-bps-pass-threshold"]').element as HTMLInputElement).value).toBe('2')
    expect(wrapper.find('[data-testid="quality-bps-hold-on-usage"]').exists()).toBe(false)
    await wrapper.find('[data-testid="quality-bps-threshold"]').setValue('3')
    await wrapper.find('[data-testid="quality-bps-usage"]').setValue('80')
    expect(checked('hold-on-usage')).toBe(true)
    await wrapper.find('[data-testid="quality-bps-hold-on-usage"]').setValue(false)
    await wrapper.find('[data-testid="quality-bps-pass-threshold"]').setValue('3')
    await wrapper.find('[data-testid="quality-bps-require-all"]').setValue(true)
    expect(wrapper.find('[data-testid="quality-bps-hold-on-usage"]').exists()).toBe(false)
    await wrapper.find('[data-testid="quality-bps-astra-only"]').trigger('click')
    await wrapper.find('[data-testid="quality-bps-auto_move_on_403"]').setValue(true)
    await vm.save()
    expect(scheduledTests.create).not.toHaveBeenCalled()
    expect(wrapper.find('[role="alert"]').text()).toContain('qualityOps.bpsTargetGroupRequired')
    await wrapper.find('[data-testid="quality-bps-target-group"]').setValue('21')
    await wrapper.find('[data-testid="quality-bps-auto_disable_on_403"]').setValue(true)
    await wrapper.find('[data-testid="quality-bps-auto_recover_on_403"]').setValue(true)
    const interval = wrapper.get<HTMLInputElement>('[data-testid="quality-bps-recovery-interval"]')
    expect(interval.element.value).toBe('60')
    await interval.setValue('0')
    await vm.save()
    expect(scheduledTests.create).not.toHaveBeenCalled()
    expect(wrapper.find('[role="alert"]').text()).toContain('admin.accounts.openai.excelBPS403RecoveryIntervalInvalid')
    await interval.setValue('360')
    vm.form.pelican_config.quality.remove_group_ids = [21]
    await vm.save()
    const request = vi.mocked(scheduledTests.create).mock.calls[0][0] as any
    expect(request.cron_expression).toBe('*/10 * * * *')
    expect(request.pelican_config.quality).toEqual({ trigger_on_upstream_5xx: false, oauth_401_max_rounds: 6, expected_answer: '', action: 'enable_bps', remove_group_ids: [], auto_restore: true, bps: {
      failure_threshold: 3, usage_percent: 80, require_all: true, all_models: false, models: ['gpt-6-astra'],
      omit_unsupported_tools: false, ignore_encrypted_content: true, auto_disable_on_403: true,
      auto_recover_on_403: true, recovery_interval_minutes: 360,
      auto_move_on_403: true, target_group_id: 21, session_proxy: false, proxy_source: '', cache_creation_as_input: true,
      pass_threshold: 3, hold_on_usage: false } })
    wrapper.unmount()
  })
  it('validates BPS triggers and switches candy tests to BPS observation', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises(); vm.selectedAccounts = [1]
    await wrapper.find('[data-testid="quality-question-kind"]').setValue('state_probe')
    await wrapper.find('[data-testid="quality-action-enable-bps"]').setValue(true)
    const alert = async () => { await vm.save(); return wrapper.find('[role="alert"]').text() }
    await wrapper.find('[data-testid="quality-bps-threshold"]').setValue('0')
    expect(await alert()).toContain('qualityOps.bpsTriggerRequired')
    await wrapper.find('[data-testid="quality-bps-threshold"]').setValue('1.5')
    expect(await alert()).toContain('qualityOps.bpsCountInvalid')
    await wrapper.find('[data-testid="quality-bps-threshold"]').setValue('2')
    await wrapper.find('[data-testid="quality-bps-usage"]').setValue('101')
    expect(await alert()).toContain('qualityOps.bpsUsageInvalid')
    await wrapper.find('[data-testid="quality-bps-usage"]').setValue('0')
    await wrapper.find('[data-testid="quality-bps-pass-threshold"]').setValue('0')
    expect(await alert()).toContain('qualityOps.bpsPassCountInvalid')
    await wrapper.find('[data-testid="quality-bps-pass-threshold"]').setValue('101')
    expect(await alert()).toContain('qualityOps.bpsPassCountInvalid')
    await wrapper.find('[data-testid="quality-bps-pass-threshold"]').setValue('1')
    vm.form.pelican_config.quality.bps.models = []
    expect(await alert()).toContain('qualityOps.bpsModelsRequired')
    await wrapper.find('[data-testid="quality-bps-all-models"]').setValue(true)
    expect(wrapper.find('[data-testid="quality-bps-models"]').exists()).toBe(false)
    expect(scheduledTests.create).not.toHaveBeenCalled()
    await wrapper.find('[data-testid="quality-question-kind"]').setValue('candy')
    expect(vm.form.pelican_config.quality.action).toBe('observe_only')
    expect(vm.form.pelican_config.test_channel).toBe('bps')
    expect(wrapper.find('[data-testid="quality-action-enable-bps"]').exists()).toBe(false)
    vm.form.pelican_config.quality.judge = { group_id: 21, model_id: 'test-judge', prompt: 'grade semantically' }
    vm.form.pelican_config.quality.remove_group_ids = [21]
    await vm.save()
    const request = vi.mocked(scheduledTests.create).mock.calls[0][0] as any
    expect(request.pelican_config).toMatchObject({ question_kind: 'candy', test_channel: 'bps' })
    expect(request.pelican_config.quality).toMatchObject({ action: 'observe_only', remove_group_ids: [], auto_restore: false })
    expect(request.pelican_config.quality.bps).toBeUndefined()
    wrapper.unmount()
  })

  it('saves 13 BPS rules as candy observations without retaining BPS lifecycle actions', async () => {
    const selected = Array.from({ length: 13 }, (_, index) => {
      const rule = rules()[0]
      return { ...rule, id: index + 1, account_id: index + 1, model_id: `model-${index + 1}`,
        pelican_config: { ...rule.pelican_config!, quality: { ...rule.pelican_config!.quality!,
          action: 'enable_bps' as const, auto_restore: true, bps: defaultQualityBPS() } } }
    })
    const before = JSON.stringify(selected)
    vi.mocked(listQualityPlans).mockResolvedValue(selected)
    const wrapper = mountView(); await flushPromises()
    await wrapper.get('[data-testid="quality-select-rules"]').setValue(true)
    await wrapper.get('[data-testid="quality-bulk-edit"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="quality-bulk-field-test"]').setValue(true)
    await wrapper.get('[data-testid="quality-question-kind"]').setValue('candy')
    expect(wrapper.get<HTMLSelectElement>('[data-testid="quality-test-channel"]').element.value).toBe('bps')
    expect(wrapper.get('[data-testid="quality-bps-observation-hint"]').text()).toContain('qualityOps.bpsObservationHint')
    const vm = wrapper.vm as any
    vm.form.pelican_config.quality.judge = { group_id: 21, model_id: 'judge', prompt: 'Grade the answer' }
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.update).toHaveBeenCalledTimes(13)
    for (const [index, [id, body]] of vi.mocked(scheduledTests.update).mock.calls.entries()) {
      expect(id).toBe(index + 1)
      expect(body).not.toHaveProperty('model_id')
      expect(body).not.toHaveProperty('cron_expression')
      expect(body.pelican_config).toMatchObject({ question_kind: 'candy', test_channel: 'bps',
        quality: { action: 'observe_only', expected_answer: '21', remove_group_ids: [], auto_restore: false,
          judge: { group_id: 21, model_id: 'judge', prompt: 'Grade the answer' } } })
      expect(body.pelican_config!.quality).not.toHaveProperty('bps')
    }
    expect(JSON.stringify(selected)).toBe(before)
    expect(wrapper.find('#quality-rule-form').exists()).toBe(false)
    wrapper.unmount()
  })

  it('creates a candy BPS observation without group or lifecycle settings', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises(); vm.selectedAccounts = [1]
    await wrapper.get('[data-testid="quality-test-channel"]').setValue('bps')
    expect(wrapper.find('[data-testid="quality-action-enable-bps"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="quality-auto-restore"]').exists()).toBe(false)
    vm.form.pelican_config.quality.judge = { group_id: 21, model_id: 'judge', prompt: 'Grade the answer' }
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(scheduledTests.create).toHaveBeenCalledWith(expect.objectContaining({ pelican_config: expect.objectContaining({
      question_kind: 'candy', test_channel: 'bps', quality: expect.objectContaining({ action: 'observe_only', auto_restore: false, remove_group_ids: [] }),
    }) }))
    wrapper.unmount()
  })

  it('allows observation beside existing account policies while excluding duplicate observations', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue([
      { ...rules()[0], account_id: 1 },
      { ...rules()[1], account_id: 2, pelican_config: { ...rules()[1].pelican_config!, quality: { ...rules()[1].pelican_config!.quality!, action: 'enable_bps' } } },
      { ...rules()[2], account_id: 3, pelican_config: { ...rules()[2].pelican_config!, quality: { ...rules()[2].pelican_config!.quality!, action: 'observe_only' } } },
    ])
    vi.mocked(accountsAPI.list).mockResolvedValue({ items: [1, 2, 3].map(id => ({ id, name: 'Account', platform: 'openai', type: 'oauth' })), total: 3 } as any)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises()
    await wrapper.get('[data-testid="quality-test-channel"]').setValue('bps')
    await wrapper.get('[data-testid="quality-select-page"]').trigger('click')
    expect(vm.selectedAccounts).toEqual([1, 2])
    expect(wrapper.get('#quality-rule-form input[type="checkbox"][value="3"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
  it('loads saved BPS rules, summarizes their trigger and labels counted failures', async () => {
    const probe = { question_kind: 'state_probe', prompt: '', reasoning_effort: 'high', parallel_count: 1, quality: { expected_answer: '', action: 'enable_bps', remove_group_ids: [], auto_restore: true,
      bps: { failure_threshold: 2, usage_percent: 80, require_all: false, all_models: true, models: null, omit_unsupported_tools: false, ignore_encrypted_content: false,
        auto_disable_on_403: true, auto_move_on_403: false, target_group_id: 0, session_proxy: true, proxy_source: 'ip_pool', cache_creation_as_input: true } } }
    vi.mocked(listQualityPlans).mockResolvedValue([{ id: 8, account_id: 1, account_name: 'BPS account', model_id: 'gpt-6-astra', cron_expression: '*/30 * * * *', enabled: true, max_results: 100, pelican_config: probe }] as any)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    expect(wrapper.find('.rule-target').text()).toBe('qualityOps.enableBPSShort（qualityOps.bpsTriggerCount / qualityOps.bpsTriggerUsage）')
    expect(vm.actionLabel('failure_counted:1/2')).toBe('qualityOps.outcomes.failure_counted_n')
    expect(vm.actionLabel('failure_counted:2/2')).toBe('qualityOps.outcomes.failure_counted_usage')
    expect([vm.tone('failure_counted:1/2'), vm.tone('bps_enabled'), vm.tone('bps_blocked_403')]).toEqual(['tone-muted', 'tone-success', 'tone-warning'])
    expect(vm.actionLabel('restore_counted:1/3')).toBe('qualityOps.outcomes.restore_counted_n')
    expect(vm.tone('restore_counted:1/3')).toBe('tone-muted')
    expect(vm.actionExplanation('restore_counted:1/3')).toBe('qualityOps.actionHelp.restore_counted')
    vm.edit(vm.plans[0]); await flushPromises()
    // 早先保存、没有满血关闭次数的规则后端按 1 次处理。
    expect(vm.form.pelican_config.quality.bps).toMatchObject({ all_models: true, models: ['gpt-6-astra', 'gpt-5.6-sol', 'gpt-5.6-terra'], target_group_id: -1, proxy_source: 'ip_pool', pass_threshold: 1 })
    expect((wrapper.find('[data-testid="quality-bps-pass-threshold"]').element as HTMLInputElement).value).toBe('1')
    expect(wrapper.find('[data-testid="quality-bps-models"]').exists()).toBe(false)
    await wrapper.find('[data-testid="quality-bps-all-models"]').setValue(false)
    expect(wrapper.find('[data-testid="quality-bps-models"]').exists()).toBe(true)
    expect((wrapper.find('[data-testid="quality-bps-auto-disable"]').element as HTMLInputElement).checked).toBe(true)
    await wrapper.find('[data-testid="quality-bps-auto-disable"]').setValue(false)
    await vm.save()
    const body = vi.mocked(scheduledTests.update).mock.calls[0][1] as any
    expect(vi.mocked(scheduledTests.update).mock.calls[0][0]).toBe(8)
    expect(body.pelican_config.quality).toMatchObject({ action: 'enable_bps', auto_restore: false, remove_group_ids: [], bps: {
      all_models: false, models: ['gpt-6-astra', 'gpt-5.6-sol', 'gpt-5.6-terra'], target_group_id: 0, session_proxy: true, proxy_source: 'ip_pool', cache_creation_as_input: true } })
    wrapper.unmount()
  })
  it('defaults BPS auto-disable only for new rules and places the toggle where the BPS settings are', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); vm.accountScope = 'manual'; await flushPromises()
    await wrapper.find('[data-testid="quality-question-kind"]').setValue('state_probe')
    await wrapper.find('[data-testid="quality-action-enable-bps"]').setValue(true)
    expect(vm.form.pelican_config.quality.auto_restore).toBe(true)
    await wrapper.find('input[type="radio"][value="remove_groups"]').setValue(true)
    expect(vm.form.pelican_config.quality.auto_restore).toBe(false)
    expect(wrapper.find('[data-testid="quality-bps-auto-disable"]').exists()).toBe(false)
    wrapper.unmount()

    const saved = rules(); saved[0].pelican_config!.quality!.action = 'enable_bps'
    vi.mocked(listQualityPlans).mockResolvedValue(saved)
    const view = mountView(); await flushPromises(); const page = view.vm as any
    page.edit(page.plans.find((plan: ScheduledTestPlan) => plan.id === 2)); await flushPromises()
    await view.find('[data-testid="quality-action-enable-bps"]').setValue(true)
    expect((view.find('[data-testid="quality-bps-auto-disable"]').element as HTMLInputElement).checked).toBe(false)
    expect(page.form.pelican_config.quality.auto_restore).toBe(false)
    page.edit(page.plans.find((plan: ScheduledTestPlan) => plan.id === 1)); await flushPromises()
    expect(page.initialForm).toBe(page.formSnapshot())
    page.showForm = false; await flushPromises()

    page.selectedRuleIds = [1]; await flushPromises()
    await view.get('[data-testid="quality-bulk-edit"]').trigger('click'); await flushPromises()
    // 只改「自动恢复」：BPS 规则给出满血关闭条件；各规则开启条件不同，始终给出「用量仍高时先不关」。
    await view.get('[data-testid="quality-bulk-field-restore"]').setValue(true)
    expect(view.find('[data-testid="quality-auto-restore"]').exists()).toBe(false)
    expect(view.find('[data-testid="quality-bps-settings"]').exists()).toBe(false)
    expect((view.find('[data-testid="quality-bps-auto-disable"]').element as HTMLInputElement).checked).toBe(false)
    expect(view.find('[data-testid="quality-bps-restore-options"]').exists()).toBe(false)
    await view.find('[data-testid="quality-bps-auto-disable"]').setValue(true)
    expect(view.find('[data-testid="quality-bps-hold-on-usage"]').exists()).toBe(true)
    await view.find('[data-testid="quality-bps-pass-threshold"]').setValue('4')
    await view.get('[data-testid="quality-bulk-field-action"]').setValue(true)
    expect(view.find('[data-testid="quality-bps-settings"]').exists()).toBe(true)
    expect(view.findAll('[data-testid="quality-bps-auto-disable"]')).toHaveLength(1)
    expect((view.find('[data-testid="quality-bps-auto-disable"]').element as HTMLInputElement).checked).toBe(true)
    expect(view.find('[data-testid="quality-bps-hold-on-usage"]').exists()).toBe(false)
    await view.get('#quality-rule-form').trigger('submit'); await flushPromises()
    expect(vi.mocked(scheduledTests.update)).toHaveBeenCalledTimes(1)
    const [id, body] = vi.mocked(scheduledTests.update).mock.calls[0] as any
    expect(id).toBe(1)
    expect(body.pelican_config.quality).toMatchObject({ action: 'enable_bps', auto_restore: true, bps: { pass_threshold: 4, hold_on_usage: true } })
    view.unmount()
  })
  it('defaults new rules to normal accounts and saves the matching filter as a group rule', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    vi.mocked(accountsAPI.list).mockImplementation(async (page: number) => (page === 1
      ? { items: [{ id: 1, name: 'Has rule', platform: 'openai', type: 'oauth' }, { id: 4, name: 'OAuth', platform: 'openai', type: 'oauth' }, { id: 5, name: 'Key', platform: 'openai', type: 'apikey' }], total: 51 }
      : { items: [{ id: 6, name: 'Setup', platform: 'openai', type: 'setup-token' }, { id: 4, name: 'OAuth', platform: 'openai', type: 'oauth' }], total: 51 }) as any)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); await flushPromises()
    expect(accountsAPI.list).toHaveBeenLastCalledWith(1, 50, expect.objectContaining({ status: 'active' }))
    expect((wrapper.get('[data-testid="quality-account-status-active"]').element as HTMLInputElement).checked).toBe(true)
    expect((wrapper.get('[data-testid="quality-account-scope-all"]').element as HTMLInputElement).checked).toBe(true)
    expect(wrapper.find('[data-testid="quality-select-all"]').exists()).toBe(false)
    expect(wrapper.get('#quality-rule-form input[type="checkbox"][value="4"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="quality-account-count"]').text()).toBe('qualityOps.accountsMatched')
    expect(vm.accountsTotal).toBe(51)
    expect(wrapper.get('button[form="quality-rule-form"]').attributes('disabled')).toBeUndefined()

    await wrapper.get('[data-testid="quality-account-scope-manual"]').setValue(true)
    expect(wrapper.find('[data-testid="quality-select-all"]').exists()).toBe(true)
    expect(wrapper.get('#quality-rule-form input[type="checkbox"][value="4"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-testid="quality-account-count"]').text()).toBe('qualityOps.selected')
    expect(wrapper.get('button[form="quality-rule-form"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="quality-account-scope-all"]').setValue(true)

    await wrapper.get('[data-testid="quality-question-kind"]').setValue('state_probe')
    vm.form.pelican_config.quality.action = 'disable_scheduling'; await flushPromises()
    vi.mocked(accountsAPI.list).mockClear()
    vi.mocked(createQualityTemplate).mockResolvedValueOnce({ template: groupRule(), created: 2 })
    await wrapper.get('#quality-rule-form').trigger('submit'); await flushPromises()
    // 由后端按筛选给现在和之后匹配的账户建规则，前端不再翻页逐个创建。
    expect(accountsAPI.list).not.toHaveBeenCalledWith(2, 50, expect.anything())
    expect(scheduledTests.create).not.toHaveBeenCalled()
    expect(createQualityTemplate).toHaveBeenCalledTimes(1)
    const body = vi.mocked(createQualityTemplate).mock.calls[0][0]
    expect(body.account_filter).toEqual({ group: undefined, type: undefined, search: undefined, statuses: ['active'] })
    expect(body.pelican_config).toMatchObject({ question_kind: 'state_probe', quality: { action: 'disable_scheduling' } })
    expect(body.cron_expression).toBeTruthy()
    expect(wrapper.get('.workspace-message[role="status"]').text()).toContain('qualityOps.templateCreated')
    expect(vm.showForm).toBe(false)
    wrapper.unmount()
  })
  it('filters by any checked account status and by every status when none is checked', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); await flushPromises()
    await wrapper.get('[data-testid="quality-account-status-rate_limited"]').setValue(true); await flushPromises()
    expect(vm.accountStatuses).toEqual(['active', 'rate_limited'])
    expect(accountsAPI.list).toHaveBeenLastCalledWith(1, 50, expect.objectContaining({ status: 'active,rate_limited' }))
    await wrapper.get('[data-testid="quality-account-status-active"]').setValue(false)
    await wrapper.get('[data-testid="quality-account-status-rate_limited"]').setValue(false); await flushPromises()
    expect(vi.mocked(accountsAPI.list).mock.lastCall![2]).toHaveProperty('status', undefined)
    vm.closeForm(); vm.newPlan(); await flushPromises()
    expect(vm.accountStatuses).toEqual(['active'])
    expect(vm.accountScope).toBe('all')
    wrapper.unmount()
  })
  it('saves a group rule even when no account matches yet so later accounts get it', async () => {
    vi.mocked(accountsAPI.list).mockResolvedValue({ items: [], total: 0 } as any)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); await flushPromises()
    await wrapper.get('[data-testid="quality-account-group"]').setValue('21'); await flushPromises()
    expect(wrapper.get('button[form="quality-rule-form"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="quality-question-kind"]').setValue('state_probe')
    vm.form.pelican_config.quality.action = 'disable_scheduling'
    await vm.save(); await flushPromises()
    expect(createQualityTemplate).toHaveBeenCalledWith(expect.objectContaining({ account_filter: expect.objectContaining({ group: '21', statuses: ['active'] }) }))
    expect(scheduledTests.create).not.toHaveBeenCalled()
    expect(wrapper.get('.workspace-message[role="status"]').text()).toContain('qualityOps.templateCreatedNone')
    expect(vm.showForm).toBe(false)
    wrapper.unmount()
  })
  it('does not page through accounts when the form itself is incomplete', async () => {
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); await flushPromises()
    vi.mocked(accountsAPI.list).mockClear()
    await vm.save()
    expect(wrapper.find('[role="alert"]').text()).toContain('qualityOps.configureJudge')
    expect(accountsAPI.list).not.toHaveBeenCalled()
    expect(createQualityTemplate).not.toHaveBeenCalled()
    expect(vm.selectedAccounts).toEqual([])
    wrapper.unmount()
  })
  it('keeps the form open when saving a group rule fails and saves on retry', async () => {
    vi.mocked(createQualityTemplate).mockRejectedValueOnce(new Error('failed')).mockResolvedValueOnce({ template: groupRule(), created: 3 })
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.newPlan(); await flushPromises()
    await wrapper.get('[data-testid="quality-question-kind"]').setValue('state_probe')
    vm.form.pelican_config.quality.action = 'disable_scheduling'
    await vm.save()
    expect(wrapper.find('[role="alert"]').text()).toContain('failed')
    expect(vm.showForm).toBe(true)
    await vm.save()
    expect(createQualityTemplate).toHaveBeenCalledTimes(2)
    expect(scheduledTests.create).not.toHaveBeenCalled()
    expect(vm.showForm).toBe(false)
    wrapper.unmount()
  })
  it('lists group rules and tags the account rules they created', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    vi.mocked(listQualityTemplates).mockResolvedValue([groupRule()])
    const wrapper = mountView(); await flushPromises()
    const card = wrapper.get('[data-template-id="7"]')
    expect(card.get('[data-testid="quality-template-filter"]').text()).toBe('Quality pool · qualityOps.allAccountTypes · admin.accounts.status.active')
    expect(card.text()).toContain('group-model')
    expect(wrapper.find('[data-plan-id="1"] [data-testid="quality-template-tag"]').exists()).toBe(true)
    expect(wrapper.find('[data-plan-id="2"] [data-testid="quality-template-tag"]').exists()).toBe(false)

    await card.get('[data-testid="quality-template-toggle"]').trigger('click'); await flushPromises()
    expect(updateQualityTemplate).toHaveBeenCalledWith(7, { enabled: false })
    expect(scheduledTests.update).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('shows group rules even before any account rule exists', async () => {
    vi.mocked(listQualityTemplates).mockResolvedValue([{ ...groupRule(), plan_ids: [] }])
    const wrapper = mountView(); await flushPromises()
    expect(wrapper.find('[data-template-id="7"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="rules-scroll"] .panel-empty').exists()).toBe(false)
    wrapper.unmount()
  })
  it('edits a group rule through its filter and pushes the change to every rule it created', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    vi.mocked(listQualityTemplates).mockResolvedValue([groupRule()])
    vi.mocked(updateQualityTemplate).mockResolvedValueOnce({ template: groupRule(), updated: 1, failed: 0, created: 0 })
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    await wrapper.get('[data-template-id="7"] [data-testid="quality-template-edit"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="quality-template-edit-hint"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="quality-account-scope-all"]').exists()).toBe(false)
    expect(vm.accountGroup).toBe('21')
    expect(vm.accountStatuses).toEqual(['active'])
    expect(vm.form.model_id).toBe('group-model')
    expect(vm.initialForm).toBe(vm.formSnapshot())
    vm.form.model_id = 'new-model'
    await vm.save(); await flushPromises()
    expect(updateQualityTemplate).toHaveBeenCalledWith(7, expect.objectContaining({ model_id: 'new-model', account_filter: expect.objectContaining({ group: '21', statuses: ['active'] }) }))
    expect(scheduledTests.create).not.toHaveBeenCalled()
    expect(scheduledTests.update).not.toHaveBeenCalled()
    expect(wrapper.get('.workspace-message[role="status"]').text()).toContain('qualityOps.templateUpdated')
    expect(vm.showForm).toBe(false)
    wrapper.unmount()
  })
  it('keeps a group rule edit open when some of its rules could not be updated', async () => {
    vi.mocked(listQualityTemplates).mockResolvedValue([groupRule()])
    vi.mocked(updateQualityTemplate).mockResolvedValueOnce({ template: groupRule(), updated: 1, failed: 2, created: 0 })
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.editTemplate(vm.templates[0]); await flushPromises()
    await vm.save(); await flushPromises()
    expect(wrapper.find('[role="alert"]').text()).toContain('qualityOps.templateUpdatePartial')
    expect(vm.showForm).toBe(true)
    expect(vm.initialForm).toBe(vm.formSnapshot())
    wrapper.unmount()
  })
  it('deletes a group rule with or without the rules it created', async () => {
    for (const keep of [false, true]) {
      vi.mocked(listQualityPlans).mockResolvedValue(rules())
      vi.mocked(listQualityTemplates).mockResolvedValue([groupRule()])
      vi.mocked(deleteQualityTemplate).mockClear().mockResolvedValueOnce({ deleted: keep ? 0 : 1 })
      const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
      vm.editTemplate(vm.templates[0]); await flushPromises()
      await wrapper.get('[data-testid="quality-delete-template"]').trigger('click'); await flushPromises()
      const checkbox = wrapper.get('[data-testid="quality-delete-template-plans"]')
      expect((checkbox.element as HTMLInputElement).checked).toBe(true)
      if (keep) await checkbox.setValue(false)
      await wrapper.get('[data-testid="quality-confirm-delete-template"]').trigger('click'); await flushPromises()
      expect(deleteQualityTemplate).toHaveBeenCalledWith(7, !keep)
      expect(wrapper.get('.workspace-message[role="status"]').text()).toContain(keep ? 'qualityOps.templateDeletedKept' : 'qualityOps.templateDeleted')
      expect(vm.showForm).toBe(false)
      wrapper.unmount()
    }
  })
  it('explains that an account rule belongs to a group rule when editing it', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    vi.mocked(listQualityTemplates).mockResolvedValue([groupRule()])
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    vm.edit(vm.plans[0]); await flushPromises()
    expect(wrapper.find('[data-testid="quality-plan-template-hint"]').exists()).toBe(true)
    vm.showForm = false; await flushPromises()
    vm.edit(vm.plans[1]); await flushPromises()
    expect(wrapper.find('[data-testid="quality-plan-template-hint"]').exists()).toBe(false)
    wrapper.unmount()
  })
  it('hides the scope switch in bulk editing and lists rules on accounts in any status', async () => {
    vi.mocked(listQualityPlans).mockResolvedValue(rules())
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    await wrapper.get('[data-plan-id="1"] .rule-checkbox input').setValue(true)
    await wrapper.get('[data-testid="quality-bulk-edit"]').trigger('click'); await flushPromises()
    expect(wrapper.find('[data-testid="quality-account-scope-all"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="quality-select-all"]').exists()).toBe(true)
    expect(vm.accountStatuses).toEqual([])
    expect(vi.mocked(accountsAPI.list).mock.lastCall![2]).toHaveProperty('status', undefined)
    wrapper.unmount()
  })
  it('renders returned model content as text', async () => {
    vi.mocked(scheduledTests.listResults).mockResolvedValue([{ id: 4, status: 'failed', error_message: 'answer_mismatch', quality_action: 'groups_removed' }] as any)
    vi.mocked(scheduledTests.getResult).mockResolvedValue({ id: 4, status: 'failed', error_message: 'answer_mismatch', response_text: '<img src=x onerror=alert(1)>', quality_action: 'groups_removed' } as any)
    const wrapper = mountView(); await flushPromises(); const vm = wrapper.vm as any
    await vm.history({ id: 1 })
    expect(wrapper.find('pre').text()).toContain('<img')
    expect(wrapper.find('pre img').exists()).toBe(false)
    wrapper.unmount()
  })
})
