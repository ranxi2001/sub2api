import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AIOpsControls from '../AIOpsControls.vue'
import AIOpsRecoveryProbes from '../AIOpsRecoveryProbes.vue'
import type { AIOpsRun } from '@/api/admin/aiOps'
import type { AIOpsActionRecord, AIOpsFrozenProposal, AIOpsPolicy } from '@/api/admin/aiOpsControl'
import en from '@/i18n/locales/en/aiOpsControl'
import zh from '@/i18n/locales/zh/aiOpsControl'
import enOps from '@/i18n/locales/en/aiOps'
import zhOps from '@/i18n/locales/zh/aiOps'

const { api, auth } = vi.hoisted(() => ({
  api: { policy: vi.fn(), savePolicy: vi.fn(), actions: vi.fn(), halt: vi.fn(), preview: vi.fn(), cancel: vi.fn(), apply: vi.fn(), rollback: vi.fn() },
  auth: { isAdmin: true, user: { id: 41 } },
}))
vi.mock('@/api/admin/aiOpsControl', () => ({ aiOpsControlAPI: api }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))

const policy = (): AIOpsPolicy => ({
  control_enabled: true, revision: 5, mode: 'approve_each', halted: false, group_id: 2,
  accounts: [{ account_id: 3, capacity_domain: 'provider-a' }], min_healthy_accounts: 1, min_total_concurrency: 1,
  min_concurrency: 1, max_concurrency: 100, max_step_percent: 10, daily_action_limit: 10,
  cooldown_seconds: 300, observation_seconds: 300, evidence_max_age_seconds: 120, min_samples: 20,
  error_rate_threshold: 0.2, rollback_error_rate_delta: 0.05, auto_actions: ['set_concurrency'], max_pause_seconds: 900,
})
const run = (id = 'run-a'): AIOpsRun => ({
  id, status: 'completed', group_id: 2, created_at: '2026-11-05T00:00:00Z',
  window: { start: '2026-11-04T23:45:00Z', end: '2026-11-05T00:00:00Z' }, evidence: [],
  result: { summary: 'Capacity diagnosis', findings: [], limitations: [], proposals: [{ action: 'set_concurrency', account_id: 3, proposed_value: 9, reason: 'Reduce pressure', evidence_ids: ['capacity'], executable: false }] },
})
const frozen = (): AIOpsFrozenProposal => ({
  id: 'proposal-1', run_id: 'run-a', proposal_index: 0, action: 'set_concurrency', account_id: 3, proposed_value: 9, before_value: 10,
  policy_revision: 5, target_revision: 8, digest: 'd'.repeat(64), expires_at: '2026-11-05T00:02:00Z', created_at: '2026-11-05T00:00:00Z', status: 'ready', blocked_reasons: [], impact_group_ids: [2],
  evidence: { observed_at: '2026-11-05T00:00:00Z', window_start: '2026-11-04T23:45:00Z', window_end: '2026-11-05T00:00:00Z', samples: 80, error_rate: 0.2, inflight: 3, eligible_accounts: 2, total_concurrency: 30, model_coverage_complete: true, probe_passed: false },
})
const action = (): AIOpsActionRecord => ({
  id: 'action-1', proposal_id: 'proposal-1', run_id: 'run-a', action: 'set_concurrency', account_id: 3, before_value: 10, after_value: 9,
  status: 'propagation_pending', created_at: '2026-11-05T00:00:00Z', updated_at: '2026-11-05T00:00:00Z', observe_until: '2026-11-05T00:05:00Z', owner_revision: 9, target_revision: 8, reason_code: '', auto: false,
})
const mountControls = (locale: 'en' | 'zh' = 'en', compact = false) => mount(AIOpsControls, { props: { run: run(), compact }, global: {
  plugins: [createI18n({ legacy: false, locale, messages: { en: { aiOpsControl: en, aiOps: enOps }, zh: { aiOpsControl: zh, aiOps: zhOps } } })],
  stubs: { NewFeatureBadge: true, AIOpsRecoveryProbes: true, BaseDialog: { props: ['show'], template: '<div v-if="show" role="dialog"><slot /><slot name="footer" /></div>' } },
} })
let wrapper: ReturnType<typeof mountControls> | undefined
beforeEach(() => {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2026-11-05T00:00:00Z'))
  auth.isAdmin = true
  for (const fn of Object.values(api)) fn.mockReset()
  api.policy.mockResolvedValue(policy())
  api.actions.mockResolvedValue({ items: [], total: 0 })
  api.savePolicy.mockImplementation(async (value: AIOpsPolicy) => ({ ...value, revision: value.revision + 1 }))
  api.preview.mockResolvedValue(frozen())
  api.cancel.mockResolvedValue({ ...frozen(), status: 'cancelled' })
  api.apply.mockResolvedValue(action())
  api.rollback.mockResolvedValue({ ...action(), status: 'rolled_back' })
  api.halt.mockResolvedValue({ ...policy(), halted: true, revision: 6 })
})
afterEach(() => { wrapper?.unmount(); wrapper = undefined; vi.useRealTimers() })

describe('AI operations controlled execution', () => {
  it.each(['en', 'zh'] as const)('renders %s controls with no writes until an explicit action', async locale => {
    wrapper = mountControls(locale); await flushPromises()
    expect(wrapper.get('h2').text()).toBe((locale === 'zh' ? zh : en).title)
    expect(wrapper.get('[data-testid="ai-control-advanced"]').element.tagName).toBe('DIV')
    expect(api.policy).toHaveBeenCalledOnce(); expect(api.actions).toHaveBeenCalledOnce()
    for (const name of ['savePolicy', 'halt', 'preview', 'apply', 'rollback'] as const) expect(api[name]).not.toHaveBeenCalled()
  })

  it('does not load or render controls for an observer', async () => {
    auth.isAdmin = false
    wrapper = mountControls(); await flushPromises()
    expect(wrapper.find('[data-testid="ai-control"]').exists()).toBe(false)
    for (const fn of Object.values(api)) expect(fn).not.toHaveBeenCalled()
  })

  it('keeps execution blocked when the deployment gate is off', async () => {
    api.policy.mockResolvedValue({ ...policy(), control_enabled: false })
    wrapper = mountControls(); await flushPromises()
    expect(wrapper.text()).toContain(en.deploymentDisabled)
    expect(wrapper.get('[data-testid="ai-control-preview-0"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="ai-policy-save"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="ai-control-halt"]').attributes('disabled')).toBeUndefined()
  })

  it.each(['en', 'zh'] as const)('keeps a compact read-only deployment separate from diagnostic readiness in %s', async locale => {
    api.policy.mockResolvedValue({ ...policy(), control_enabled: false, mode: 'disabled', halted: true })
    wrapper = mountControls(locale, true); await flushPromises()
    const messages = locale === 'zh' ? zh : en
    expect(wrapper.get('[data-testid="ai-control-disabled"]').text()).toBe(messages.readOnly)
    expect(wrapper.find('[data-testid="ai-control-halt"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="ai-control-halted"]').exists()).toBe(false)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    const advanced = wrapper.get('[data-testid="ai-control-advanced"]')
    expect(advanced.element.tagName).toBe('DETAILS')
    expect((advanced.element as HTMLDetailsElement).open).toBe(false)
    expect(advanced.get('summary').text()).toBe(messages.advancedDetails)
    expect(advanced.get('[data-testid="ai-policy-save"]').attributes('disabled')).toBeDefined()
    expect(wrapper.findComponent(AIOpsRecoveryProbes).exists()).toBe(true)
    for (const name of ['savePolicy', 'halt', 'preview', 'apply', 'rollback'] as const) expect(api[name]).not.toHaveBeenCalled()
  })

  it('preserves the frozen approval and mounted recovery controls when advanced controls are folded', async () => {
    wrapper = mountControls('en', true); await flushPromises()
    const advanced = wrapper.get('[data-testid="ai-control-advanced"]')
    const details = advanced.element as HTMLDetailsElement
    expect(details.open).toBe(false)
    expect(advanced.find('[data-testid="ai-control-halt"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="ai-control-halt"]').attributes('disabled')).toBeUndefined()
    const recovery = wrapper.getComponent(AIOpsRecoveryProbes).element
    details.open = true
    await advanced.trigger('toggle')
    await wrapper.get('[data-testid="ai-control-preview-0"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="ai-control-apply-consent"]').setValue(true)
    const preview = wrapper.get('[data-testid="ai-control-frozen"]').element
    details.open = false
    await advanced.trigger('toggle')
    expect(wrapper.getComponent(AIOpsRecoveryProbes).element).toBe(recovery)
    expect(wrapper.get('[data-testid="ai-control-frozen"]').element).toBe(preview)
    expect((wrapper.get('[data-testid="ai-control-apply-consent"]').element as HTMLInputElement).checked).toBe(true)
    details.open = true
    await advanced.trigger('toggle')
    expect(wrapper.get('[data-testid="ai-control-apply"]').attributes('disabled')).toBeUndefined()
    expect(api.policy).toHaveBeenCalledOnce(); expect(api.actions).toHaveBeenCalledOnce()
    expect(api.preview).toHaveBeenCalledOnce()
    for (const name of ['savePolicy', 'halt', 'apply', 'rollback'] as const) expect(api[name]).not.toHaveBeenCalled()
  })

  it('keeps compact errors and emergency stop outside the disclosure when execution state is unknown', async () => {
    api.policy.mockRejectedValue(new Error('private service address'))
    wrapper = mountControls('en', true); await flushPromises()
    const advanced = wrapper.get('[data-testid="ai-control-advanced"]')
    expect(advanced.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toBe(en.loadFailed)
    expect(wrapper.text()).not.toContain('private service address')
    expect(advanced.find('[data-testid="ai-control-halt"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="ai-control-halt"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="ai-control-halt"]').trigger('click'); await flushPromises()
    expect(api.halt).toHaveBeenCalledOnce()
    expect(wrapper.get('[data-testid="ai-control-halted"]').text()).toBe(en.halted)
  })

  it('requires a frozen preview and separate approval before executing its digest', async () => {
    wrapper = mountControls(); await flushPromises()
    await wrapper.get('[data-testid="ai-control-preview-0"]').trigger('click'); await flushPromises()
    expect(api.preview).toHaveBeenCalledWith('run-a', 0, expect.any(AbortSignal))
    expect(api.apply).not.toHaveBeenCalled()
    const preview = wrapper.get('[data-testid="ai-control-frozen"]').text()
    expect(preview).toContain(en.before); expect(preview).toContain(en.after)
    expect(preview).toContain('10'); expect(preview).toContain('9'); expect(preview).toContain('#2')
    expect(wrapper.get('[data-testid="ai-control-apply"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="ai-control-apply-consent"]').setValue(true)
    await wrapper.get('[data-testid="ai-control-apply"]').trigger('click'); await flushPromises()
    expect(api.apply).toHaveBeenCalledOnce()
    expect(api.apply).toHaveBeenCalledWith('proposal-1', 'd'.repeat(64), expect.any(AbortSignal))
    expect(wrapper.find('[data-testid="ai-control-frozen"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="ai-control-action"]').text()).toContain(en.status.propagation_pending)
  })

  it.each(['en', 'zh'] as const)('cancels the exact frozen proposal and blocks later approval in %s', async locale => {
    wrapper = mountControls(locale); await flushPromises()
    await wrapper.get('[data-testid="ai-control-preview-0"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="ai-control-apply-consent"]').setValue(true)
    await wrapper.get('[data-testid="ai-control-cancel"]').trigger('click'); await flushPromises()
    expect(api.cancel).toHaveBeenCalledWith('proposal-1', 'd'.repeat(64), expect.any(AbortSignal))
    expect(api.cancel).toHaveBeenCalledOnce()
    expect(api.apply).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="ai-control-cancel"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="ai-control-apply"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain((locale === 'zh' ? zh : en).proposalCancelled)
  })

  it('does not replay cancellation when its response is unknown', async () => {
    api.cancel.mockRejectedValue(new Error('unknown outcome'))
    wrapper = mountControls(); await flushPromises()
    await wrapper.get('[data-testid="ai-control-preview-0"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="ai-control-cancel"]').trigger('click'); await flushPromises()
    expect(api.cancel).toHaveBeenCalledOnce()
    expect(wrapper.find('[data-testid="ai-control-frozen"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="ai-control-preview-0"]').attributes('disabled')).toBeDefined()
    expect(api.apply).not.toHaveBeenCalled()
  })

  it('expires a preview and clears it when the selected diagnostic changes', async () => {
    api.preview.mockResolvedValue({ ...frozen(), expires_at: '2026-11-05T00:00:01Z' })
    wrapper = mountControls(); await flushPromises()
    await wrapper.get('[data-testid="ai-control-preview-0"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="ai-control-apply-consent"]').setValue(true)
    await vi.advanceTimersByTimeAsync(1000)
    expect(wrapper.text()).toContain(en.expired)
    expect(wrapper.get('[data-testid="ai-control-apply"]').attributes('disabled')).toBeDefined()
    await wrapper.setProps({ run: run('run-b') })
    expect(wrapper.find('[data-testid="ai-control-frozen"]').exists()).toBe(false)
    expect(api.apply).not.toHaveBeenCalled()
  })

  it('displays safety rejection reasons and never turns a blocked preview into approval', async () => {
    api.preview.mockResolvedValue({ ...frozen(), status: 'blocked', blocked_reasons: ['insufficient_samples', 'shared_account'] })
    wrapper = mountControls(); await flushPromises()
    await wrapper.get('[data-testid="ai-control-preview-0"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="ai-control-blocked"]').text()).toContain(en.reasons.insufficient_samples)
    expect(wrapper.get('[data-testid="ai-control-blocked"]').text()).toContain(en.reasons.shared_account)
    expect(wrapper.get('[data-testid="ai-control-apply-consent"]').attributes('disabled')).toBeDefined()
    expect(api.apply).not.toHaveBeenCalled()
  })

  it('labels unavailable inflight as unknown and shows the frozen model scope', async () => {
    const candidate = frozen()
    candidate.evidence = { ...candidate.evidence, inflight: -1, model: 'synthetic-model', effort: 'high', protocol: 'native_http' }
    api.preview.mockResolvedValue(candidate)
    wrapper = mountControls(); await flushPromises()
    await wrapper.get('[data-testid="ai-control-preview-0"]').trigger('click'); await flushPromises()
    const detail = wrapper.get('[data-testid="ai-control-frozen"]').text()
    expect(detail).toContain(en.unknown)
    expect(detail).not.toContain('In flight：-1')
    expect(detail).toContain('synthetic-model')
    expect(detail).toContain('native_http')
    expect(detail).toContain('high')
  })

  it('invalidates an approval on conflict and requires a refresh without retrying the write', async () => {
    api.apply.mockRejectedValue({ status: 409, reason: 'AI_OPS_CONFLICT', message: 'internal endpoint must stay private' })
    wrapper = mountControls(); await flushPromises()
    await wrapper.get('[data-testid="ai-control-preview-0"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="ai-control-apply-consent"]').setValue(true)
    await wrapper.get('[data-testid="ai-control-apply"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain(en.conflict)
    expect(wrapper.text()).not.toContain('internal endpoint must stay private')
    expect(wrapper.find('[data-testid="ai-control-frozen"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="ai-control-preview-0"]').attributes('disabled')).toBeDefined()
    expect(api.apply).toHaveBeenCalledOnce()
    await wrapper.get('[data-testid="ai-control-refresh"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[data-testid="ai-control-preview-0"]').attributes('disabled')).toBeUndefined()
  })

  it('requires explicit automatic execution authorization, resetting consent after any policy edit', async () => {
    wrapper = mountControls(); await flushPromises()
    await wrapper.get('[data-testid="ai-policy-mode"]').setValue('bounded_auto')
    expect(wrapper.get('[data-testid="ai-policy-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="ai-policy-consent"]').setValue(true)
    await wrapper.get('[data-testid="ai-policy-auto-consent"]').setValue(true)
    expect(wrapper.get('[data-testid="ai-policy-save"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="ai-policy-max_step_percent"]').setValue(12)
    expect((wrapper.get('[data-testid="ai-policy-auto-consent"]').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.get('[data-testid="ai-policy-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="ai-policy-consent"]').setValue(true)
    await wrapper.get('[data-testid="ai-policy-auto-consent"]').setValue(true)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(api.savePolicy).toHaveBeenCalledWith(expect.objectContaining({ revision: 5, mode: 'bounded_auto', max_step_percent: 12, auto_actions: ['set_concurrency'] }), expect.any(AbortSignal))
    expect(wrapper.text()).toContain(en.saved)
    expect(api.apply).not.toHaveBeenCalled()
  })

  it('blocks zero concurrency, duplicate accounts, missing domains and dirty previews', async () => {
    wrapper = mountControls(); await flushPromises()
    await wrapper.get('[data-testid="ai-policy-min_concurrency"]').setValue(0)
    await wrapper.get('[data-testid="ai-policy-consent"]').setValue(true)
    expect(wrapper.get('[data-testid="ai-policy-save"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="ai-control-preview-0"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="ai-policy-min_concurrency"]').setValue(1)
    await wrapper.get('[data-testid="ai-policy-add-account"]').trigger('click')
    await wrapper.get('[data-testid="ai-policy-account-1"]').setValue(3)
    await wrapper.get('[data-testid="ai-policy-domain-1"]').setValue('provider-a')
    await wrapper.get('[data-testid="ai-policy-consent"]').setValue(true)
    expect(wrapper.get('[data-testid="ai-policy-save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="ai-policy-account-1"]').setValue(4)
    await wrapper.get('[data-testid="ai-policy-domain-1"]').setValue('')
    await wrapper.get('[data-testid="ai-policy-consent"]').setValue(true)
    expect(wrapper.get('[data-testid="ai-policy-save"]').attributes('disabled')).toBeDefined()
    expect(api.savePolicy).not.toHaveBeenCalled()
  })

  it.each([false, true])('keeps emergency stop usable during an outstanding write and invalidates local approval (compact=%s)', async compact => {
    let resolveApply!: (value: AIOpsActionRecord) => void
    api.apply.mockImplementation(() => new Promise<AIOpsActionRecord>(resolve => { resolveApply = resolve }))
    wrapper = mountControls('en', compact); await flushPromises()
    await wrapper.get('[data-testid="ai-control-preview-0"]').trigger('click'); await flushPromises()
    await wrapper.get('[data-testid="ai-control-apply-consent"]').setValue(true)
    await wrapper.get('[data-testid="ai-control-apply"]').trigger('click')
    const signal = api.apply.mock.calls[0][2] as AbortSignal
    expect(wrapper.get('[data-testid="ai-control-halt"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="ai-control-halt"]').trigger('click'); await flushPromises()
    expect(signal.aborted).toBe(true)
    expect(api.halt).toHaveBeenCalledOnce()
    expect(wrapper.text()).toContain(en.halted)
    resolveApply(action()); await flushPromises()
    expect(wrapper.find('[data-testid="ai-control-frozen"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="ai-control-preview-0"]').attributes('disabled')).toBeDefined()
  })

  it('requires a separate rollback confirmation and sends the recorded owner revision', async () => {
    api.actions.mockResolvedValue({ items: [action()], total: 1 })
    wrapper = mountControls(); await flushPromises()
    await wrapper.get('[data-testid="ai-control-rollback-action-1"]').trigger('click')
    expect(wrapper.get('[data-testid="ai-control-rollback-confirm"]').attributes('disabled')).toBeDefined()
    expect(api.rollback).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="ai-control-rollback-consent"]').setValue(true)
    await wrapper.get('[data-testid="ai-control-rollback-confirm"]').trigger('click'); await flushPromises()
    expect(api.rollback).toHaveBeenCalledWith('action-1', 9, expect.any(AbortSignal))
    expect(wrapper.get('[data-testid="ai-control-action"]').text()).toContain(en.status.rolled_back)
    expect(wrapper.find('[data-testid="ai-control-rollback-action-1"]').exists()).toBe(false)
  })

  it('shows a rollback conflict without retrying or reporting success', async () => {
    api.actions.mockResolvedValue({ items: [action()], total: 1 })
    api.rollback.mockRejectedValue({ status: 409, reason: 'AI_OPS_CONFLICT' })
    wrapper = mountControls(); await flushPromises()
    await wrapper.get('[data-testid="ai-control-rollback-action-1"]').trigger('click')
    await wrapper.get('[data-testid="ai-control-rollback-consent"]').setValue(true)
    await wrapper.get('[data-testid="ai-control-rollback-confirm"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain(en.conflict)
    expect(wrapper.text()).not.toContain(en.rollbackDone)
    expect(api.rollback).toHaveBeenCalledOnce()
  })

  it('treats a conflict action returned with HTTP success as a conflict', async () => {
    api.actions.mockResolvedValue({ items: [action()], total: 1 })
    api.rollback.mockResolvedValue({ ...action(), status: 'conflict', reason_code: 'ownership_changed' })
    wrapper = mountControls(); await flushPromises()
    await wrapper.get('[data-testid="ai-control-rollback-action-1"]').trigger('click')
    await wrapper.get('[data-testid="ai-control-rollback-consent"]').setValue(true)
    await wrapper.get('[data-testid="ai-control-rollback-confirm"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain(en.conflict)
    expect(wrapper.text()).not.toContain(en.rollbackDone)
    expect(wrapper.get('[data-testid="ai-control-action"]').text()).toContain(en.status.conflict)
  })

  it('shows a pause as an independent AI restriction rather than a null value', async () => {
    const data = run()
    data.result!.proposals[0] = { ...data.result!.proposals[0], action: 'pause_account', proposed_value: null }
    api.preview.mockResolvedValue({ ...frozen(), action: 'pause_account', before_value: 0, proposed_value: null })
    wrapper = mountControls(); await wrapper.setProps({ run: data }); await flushPromises()
    await wrapper.get('[data-testid="ai-control-preview-0"]').trigger('click'); await flushPromises()
    const preview = wrapper.get('[data-testid="ai-control-frozen"]').text()
    expect(preview).toContain(en.aiPauseSet)
    expect(preview).toContain(en.aiPauseClear)
    expect(api.apply).not.toHaveBeenCalled()
  })

  it('renders recommendation reasons as text and keeps the stop button available after load failure', async () => {
    const data = run(), hostile = '<img src=x onerror="alert(1)">'
    data.result!.proposals[0].reason = hostile
    wrapper = mountControls(); await wrapper.setProps({ run: data }); await flushPromises()
    expect(wrapper.text()).toContain(hostile)
    expect(wrapper.find('img').exists()).toBe(false)
    api.policy.mockRejectedValue(new Error('internal endpoint'))
    await wrapper.get('[data-testid="ai-control-refresh"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain(en.loadFailed)
    expect(wrapper.text()).not.toContain('internal endpoint')
    expect(wrapper.get('[data-testid="ai-control-halt"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-testid="ai-control-preview-0"]').attributes('disabled')).toBeDefined()
  })

  it('aborts an obsolete preview when the run changes and ignores its late response', async () => {
    let resolveOld!: (value: AIOpsFrozenProposal) => void
    api.preview.mockImplementationOnce(() => new Promise<AIOpsFrozenProposal>(resolve => { resolveOld = resolve }))
    wrapper = mountControls(); await flushPromises()
    await wrapper.get('[data-testid="ai-control-preview-0"]').trigger('click')
    const signal = api.preview.mock.calls[0][2] as AbortSignal
    await wrapper.setProps({ run: run('run-b') })
    expect(signal.aborted).toBe(true)
    resolveOld(frozen()); await flushPromises()
    expect(wrapper.find('[data-testid="ai-control-frozen"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="ai-control-preview-0"]').attributes('disabled')).toBeUndefined()
  })
})
