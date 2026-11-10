import { describe, expect, it } from 'vitest'
import { buildQualityRulePatch, copyQualityRuleConfig, qualityActionPayload, qualityBPSError, qualityBPSForm, qualityBPSPayload, qualityJudgeConfigured, qualityRuleFieldValue, type QualityRuleDraft } from '../qualityRulePatch'
import type { QualityBPSPolicy, ScheduledTestPlan } from '@/types'

const draft = (): QualityRuleDraft => ({
  model_id: 'new-model', cron_expression: '0 * * * *', enabled: false,
  pelican_config: { question_kind: 'candy', prompt: 'New question', reasoning_effort: 'high', parallel_count: 3,
    quality: { expected_answer: '42', action: 'remove_groups', remove_group_ids: [21], auto_restore: true,
      judge: { group_id: 21, model_id: 'new-judge', prompt: 'Grade the answer' } } },
})
const plan = (): ScheduledTestPlan => ({
  id: 1, account_id: 10, model_id: 'old-model', cron_expression: '*/30 * * * *', enabled: true,
  max_results: 150, auto_recover: false, last_run_at: null, next_run_at: null, created_at: '', updated_at: '',
  pelican_config: { question_kind: 'candy', prompt: 'Own question', reasoning_effort: 'low', parallel_count: 2,
    quality: { expected_answer: '7', action: 'remove_groups', remove_group_ids: [99], auto_restore: false,
      judge: { group_id: 99, model_id: 'own-judge', prompt: 'Own grading instructions' } } },
})
const bps = (): QualityBPSPolicy => ({
  failure_threshold: 2, usage_percent: 0, require_all: false, all_models: false, models: ['gpt-6-astra'],
  omit_unsupported_tools: true, ignore_encrypted_content: true, auto_disable_on_403: false,
  auto_move_on_403: false, target_group_id: -1, session_proxy: false, proxy_source: 'mihomo', cache_creation_as_input: false,
  pass_threshold: 2, hold_on_usage: true, recovery_interval_minutes: 60,
})
const probePlan = () => {
  const rule = plan(), config = rule.pelican_config!
  config.question_kind = 'state_probe'; config.prompt = ''; config.parallel_count = 1
  config.quality!.expected_answer = ''; delete config.quality!.judge
  return rule
}

describe('OAuth 401 rule cleanup settings', () => {
  it('copies the explicit limit and keeps legacy rules disabled instead of applying new-rule defaults', () => {
    const source = draft().pelican_config
    expect(copyQualityRuleConfig(source).quality.oauth_401_max_rounds).toBe(0)
    source.quality.oauth_401_max_rounds = 9
    expect(copyQualityRuleConfig(source).quality.oauth_401_max_rounds).toBe(9)
    source.quality.oauth_401_max_rounds = 0
    expect(copyQualityRuleConfig(source).quality.oauth_401_max_rounds).toBe(0)
  })

  it('changes only the cleanup limit when its independent bulk checkbox is selected', () => {
    const current = plan(), source = draft()
    current.pelican_config!.quality!.oauth_401_max_rounds = 12
    source.pelican_config.quality.oauth_401_max_rounds = 6
    const original = JSON.stringify(current)
    const changed = buildQualityRulePatch(current, source, ['oauth401'])
    expect(changed.pelican_config!.quality).toEqual({ ...current.pelican_config!.quality, oauth_401_max_rounds: 6 })
    expect(changed).not.toHaveProperty('model_id')
    expect(JSON.stringify(current)).toBe(original)
    expect(buildQualityRulePatch(current, source, ['restore']).pelican_config!.quality!.oauth_401_max_rounds).toBe(12)
    expect(buildQualityRulePatch(current, source, ['action']).pelican_config!.quality!.oauth_401_max_rounds).toBe(12)
    expect(buildQualityRulePatch(current, source, ['enabled'])).toEqual({ enabled: false })
    source.pelican_config.quality.oauth_401_max_rounds = 0
    expect(buildQualityRulePatch(current, source, ['oauth401']).pelican_config!.quality!.oauth_401_max_rounds).toBe(0)
    delete source.pelican_config.quality.oauth_401_max_rounds
    expect(buildQualityRulePatch(current, source, ['oauth401']).pelican_config!.quality!.oauth_401_max_rounds).toBe(0)
  })

  it('compares cleanup independently of the failure action and treats a missing value as disabled', () => {
    const current = plan(), changed = plan()
    changed.pelican_config!.quality!.oauth_401_max_rounds = 6
    expect(qualityRuleFieldValue(current, 'oauth401')).toBe(0)
    expect(qualityRuleFieldValue(changed, 'oauth401')).toBe(6)
    expect(qualityRuleFieldValue(current, 'action')).toEqual(qualityRuleFieldValue(changed, 'action'))
  })

  it.each([0, 1, 6, 100])('accepts a cleanup limit of %s rounds', limit => {
    const source = draft()
    source.pelican_config.quality.oauth_401_max_rounds = limit
    expect(qualityActionPayload(source.pelican_config.quality).oauth_401_max_rounds).toBe(limit)
    expect(buildQualityRulePatch(plan(), source, ['oauth401']).pelican_config!.quality!.oauth_401_max_rounds).toBe(limit)
  })

  it.each([-1, 1.5, 101, NaN, Infinity])('rejects an invalid cleanup limit of %s in create and bulk payloads', limit => {
    const source = draft()
    source.pelican_config.quality.oauth_401_max_rounds = limit
    expect(() => qualityActionPayload(source.pelican_config.quality)).toThrow('qualityOps.invalidOAuth401MaxRounds')
    expect(() => buildQualityRulePatch(plan(), source, ['oauth401'])).toThrow('qualityOps.invalidOAuth401MaxRounds')
    // An unchecked cleanup field cannot block an unrelated top-level edit.
    expect(buildQualityRulePatch(plan(), source, ['enabled'])).toEqual({ enabled: false })
  })
})

describe('quality rule copy inputs', () => {
  it('preserves quality rest in copies and changes it only with the bulk action field', () => {
    const current = plan(), source = draft()
    current.pelican_config!.quality!.retest_rest_minutes = 30
    source.pelican_config.quality.retest_rest_minutes = 90
    expect(copyQualityRuleConfig(source.pelican_config).quality.retest_rest_minutes).toBe(90)
    expect(buildQualityRulePatch(current, source, ['restore']).pelican_config!.quality!.retest_rest_minutes).toBe(30)
    expect(buildQualityRulePatch(current, source, ['action']).pelican_config!.quality!.retest_rest_minutes).toBe(90)
    expect(qualityRuleFieldValue(current, 'action')).not.toEqual(qualityRuleFieldValue({ ...current, pelican_config: source.pelican_config }, 'action'))
    expect(qualityActionPayload({ ...source.pelican_config.quality, action: 'observe_only' }).retest_rest_minutes).toBeUndefined()
    expect(qualityActionPayload({ ...source.pelican_config.quality, retest_rest_minutes: 0 }).retest_rest_minutes).toBeUndefined()
    for (const value of [-1, 1.5, 10081, NaN]) {
      expect(() => qualityActionPayload({ ...source.pelican_config.quality, retest_rest_minutes: value })).toThrow('qualityOps.invalidRetestRest')
    }
  })
  it('retains Jev choice in copy and bulk patches without importing arbitrary fields', () => {
    const source = draft()
    source.pelican_config.quality.judge = { kind: 'jev', group_id: 0, model_id: '', prompt: 'Compare values' }
    Object.assign(source.pelican_config.quality.judge, { api_key: 'synthetic-secret', base_url: 'https://example.com?key=secret' })
    const copy = copyQualityRuleConfig(source.pelican_config)
    expect(copy.quality.judge).toEqual({ kind: 'jev', group_id: 0, model_id: '', prompt: 'Compare values' })
    expect(qualityJudgeConfigured(copy.quality.judge)).toBe(true)
    const patch = buildQualityRulePatch(plan(), source, ['test'])
    expect(patch.pelican_config!.quality!.judge).toEqual(copy.quality.judge)
    expect(JSON.stringify(patch)).not.toContain('synthetic-secret')
    expect(qualityRuleFieldValue({ ...plan(), pelican_config: copy }, 'test')).not.toEqual(qualityRuleFieldValue(plan(), 'test'))
    expect(qualityJudgeConfigured({ kind: 'jev', group_id: 21, model_id: 'chat', prompt: 'grade' })).toBe(false)
    expect(qualityJudgeConfigured({ group_id: 21, model_id: 'chat', prompt: 'grade' })).toBe(true)
    expect(qualityJudgeConfigured({ kind: 'llm', group_id: 0, model_id: '', prompt: 'grade' })).toBe(false)
  })
  it('accepts null group and BPS model arrays returned by normalized observation and BPS rules', () => {
    const source = plan().pelican_config!
    source.quality!.action = 'enable_bps'
    Object.assign(source.quality!, { remove_group_ids: null, bps: { ...bps(), all_models: true, models: null } })
    const copy = copyQualityRuleConfig(source)
    expect(copy.quality.remove_group_ids).toEqual([])
    expect(copy.quality.bps!.models).toEqual([])
  })

  it('copies editable answer and cooldown settings with independent nested values', () => {
    const source = plan().pelican_config!
    source.model_ids = ['first', 'second']
    source.quality!.action = 'remove_models'
    source.quality!.remove_models = ['first']
    source.quality!.recovery_concurrency = 9
    const original = JSON.stringify(source)
    const copy = copyQualityRuleConfig(source)
    expect(copy).toMatchObject({ prompt: source.prompt, reasoning_effort: 'low', parallel_count: 2, model_ids: ['first', 'second'],
      quality: { action: 'remove_models', remove_models: ['first'], recovery_concurrency: 9, judge: source.quality!.judge } })
    copy.model_ids!.push('third')
    copy.quality.remove_group_ids.push(100)
    copy.quality.remove_models!.push('second')
    copy.quality.judge!.prompt = 'Changed'
    expect(JSON.stringify(source)).toBe(original)
  })

  it('drops generated questions, runtime state and unknown nested fields while preserving reusable random-test inputs', () => {
    const source = plan().pelican_config!
    source.question_kind = 'random_reasoning'
    source.random_question = { id: 'previous-question', family: 'paths', version: 'v1' }
    Object.assign(source, { trigger_source: 'upstream_5xx', model_id: 'result-model', quality_round_id: 'round',
      quality_model_actions: { first: 'cooled' }, quality_model_outcomes: { first: 'failed' },
      quality_restore_diagnostic: { ownership_retained: true }, credentials: 'synthetic-secret' })
    Object.assign(source.quality!, { admission: { enabled: true }, owner_plan_id: 1, failure_count: 3 })
    Object.assign(source.quality!.judge!, { credentials: 'synthetic-secret' })
    const copy = copyQualityRuleConfig(source)
    expect(copy.prompt).toBe('')
    expect(copy.quality.expected_answer).toBe('')
    expect(copy.quality.judge).toEqual({ group_id: 99, model_id: 'own-judge', prompt: 'Own grading instructions' })
    expect(Object.keys(copy).sort()).toEqual(['model_ids', 'parallel_count', 'prompt', 'quality', 'question_kind', 'reasoning_effort', 'rule_name', 'test_channel'])
    for (const key of ['admission', 'owner_plan_id', 'failure_count']) expect(copy.quality).not.toHaveProperty(key)
  })
})

describe('quality rule partial updates', () => {
  it('bulk switches to generated questions without requiring fixed inputs or changing the judge', () => {
    const input = draft()
    input.pelican_config.question_kind = 'random_reasoning'
    input.pelican_config.prompt = ''
    input.pelican_config.quality.expected_answer = ''
    input.pelican_config.quality.action = 'isolate_models'
    const updated = buildQualityRulePatch(plan(), input, ['test', 'action'])
    expect(updated.pelican_config).toMatchObject({ question_kind: 'random_reasoning', prompt: '', parallel_count: 3,
      quality: { expected_answer: '', action: 'isolate_models', judge: input.pelican_config.quality.judge } })
    expect(updated.pelican_config).not.toHaveProperty('random_question')
  })

  it('sends only selected top-level fields, including an explicit false value', () => {
    expect(buildQualityRulePatch(plan(), draft(), ['model', 'schedule', 'enabled'])).toEqual({
      model_id: 'new-model', cron_expression: '0 * * * *', enabled: false,
    })
  })

  it('replaces the full model selection without overwriting the rule’s grading policy', () => {
    const source = draft()
    Object.assign(source.pelican_config, { model_ids: ['second-model', 'second-model'] })
    const updated = buildQualityRulePatch(plan(), source, ['model'])
    expect(updated.model_id).toBe('new-model')
    expect(updated.pelican_config).toMatchObject({ model_ids: ['new-model', 'second-model'],
      quality: { expected_answer: '7', judge: { model_id: 'own-judge' } } })
  })

  it('preserves different questions, judges and target groups when changing restoration', () => {
    const first = plan(), second = plan()
    second.pelican_config!.quality!.remove_group_ids = [100]
    second.pelican_config!.quality!.judge!.model_id = 'another-judge'
    const before = JSON.stringify([first, second])
    for (const rule of [first, second]) {
      expect(buildQualityRulePatch(rule, draft(), ['restore'])).toEqual({
        pelican_config: { ...rule.pelican_config, quality: { ...rule.pelican_config!.quality, auto_restore: true } },
      })
    }
    expect(JSON.stringify([first, second])).toBe(before)
  })

  it('switches to a probe while preserving each rule’s own action and restoration policy', () => {
    const input = draft(); input.pelican_config.question_kind = 'state_probe'
    const updated = buildQualityRulePatch(plan(), input, ['test'])
    expect(updated.pelican_config).toMatchObject({ question_kind: 'state_probe', prompt: '', parallel_count: 1,
      quality: { expected_answer: '', action: 'remove_groups', remove_group_ids: [99], auto_restore: false } })
    expect(updated.pelican_config!.quality).not.toHaveProperty('judge')
  })

  it('does not convert probe rules when editing only failure actions', () => {
    const rule = plan()
    rule.pelican_config!.question_kind = 'state_probe'
    rule.pelican_config!.prompt = ''; rule.pelican_config!.parallel_count = 1
    rule.pelican_config!.quality!.expected_answer = ''; delete rule.pelican_config!.quality!.judge
    expect(buildQualityRulePatch(rule, draft(), ['action']).pelican_config).toEqual({
      ...rule.pelican_config, quality: { ...rule.pelican_config!.quality, remove_group_ids: [21] },
    })
  })

  it('validates selected fields without requiring untouched grading configuration', () => {
    const input = draft(); delete input.pelican_config.quality.judge
    input.pelican_config.quality.remove_group_ids = []
    expect(buildQualityRulePatch(plan(), input, ['enabled'])).toEqual({ enabled: false })
    expect(() => buildQualityRulePatch(plan(), input, ['test'])).toThrow('qualityOps.configureJudge')
    expect(() => buildQualityRulePatch(plan(), input, ['action'])).toThrow('qualityOps.selectGroups')
    expect(() => buildQualityRulePatch(plan(), input, [])).toThrow('qualityOps.selectBulkFields')
  })

  it('copies and normalizes the BPS policy when enabling BPS on probe rules', () => {
    const input = draft(); input.pelican_config.quality.action = 'enable_bps'
    input.pelican_config.quality.bps = { ...bps(), all_models: true, session_proxy: true, proxy_source: '' }
    const updated = buildQualityRulePatch(probePlan(), input, ['action']).pelican_config!.quality!
    expect(updated).toMatchObject({ action: 'enable_bps', remove_group_ids: [] })
    expect(updated.bps).toEqual({ ...bps(), all_models: true, models: [], target_group_id: 0, session_proxy: true, proxy_source: 'mihomo' })
    expect(input.pelican_config.quality.bps!.models).toEqual(['gpt-6-astra'])
  })

  it('applies the BPS turn-off conditions only together with automatic restoration', () => {
    const own = probePlan(), quality = own.pelican_config!.quality!
    quality.action = 'enable_bps'; quality.bps = { ...bps(), usage_percent: 70, pass_threshold: 4, hold_on_usage: false }
    const input = draft(); input.pelican_config.question_kind = 'state_probe'; input.pelican_config.quality.action = 'enable_bps'
    input.pelican_config.quality.bps = { ...bps(), failure_threshold: 3, pass_threshold: 6, hold_on_usage: true }
    // 只改处理方式：各规则保留自己的关闭条件，原本不开 BPS 的规则用默认值。
    expect(buildQualityRulePatch(own, input, ['action']).pelican_config!.quality!.bps).toMatchObject({ failure_threshold: 3, usage_percent: 0, pass_threshold: 4, hold_on_usage: false })
    expect(buildQualityRulePatch(probePlan(), input, ['action']).pelican_config!.quality!.bps).toMatchObject({ failure_threshold: 3, pass_threshold: 2, hold_on_usage: true })
    expect(buildQualityRulePatch(own, input, ['action', 'restore']).pelican_config!.quality!).toMatchObject({ auto_restore: true, bps: { failure_threshold: 3, pass_threshold: 6, hold_on_usage: true } })
    // 只改自动恢复：只动这两项，开启条件和其它选项不变。
    expect(buildQualityRulePatch(own, input, ['restore']).pelican_config!.quality!).toEqual({ ...quality, auto_restore: true, bps: { ...quality.bps, pass_threshold: 6, hold_on_usage: true } })
    // 自动恢复关掉时次数输入框是隐藏的，不拿它覆盖各规则自己的值，也不因它拦截保存。
    input.pelican_config.quality.auto_restore = false; input.pelican_config.quality.bps.pass_threshold = 0
    expect(buildQualityRulePatch(own, input, ['restore']).pelican_config!.quality!).toEqual({ ...quality, auto_restore: false })
    input.pelican_config.quality.auto_restore = true
    expect(() => buildQualityRulePatch(own, input, ['restore'])).toThrow('qualityOps.bpsPassCountInvalid')
    expect(buildQualityRulePatch(plan(), input, ['restore']).pelican_config!.quality!).not.toHaveProperty('bps')
  })

  it('reads the turn-off count back from saved rules', () => {
    expect(qualityBPSForm(undefined)).toMatchObject({ pass_threshold: 2, hold_on_usage: true })
    expect(qualityBPSForm({ ...bps(), pass_threshold: 5, hold_on_usage: false })).toMatchObject({ pass_threshold: 5, hold_on_usage: false })
    expect(qualityBPSForm({ ...bps(), pass_threshold: 0 }).pass_threshold).toBe(1)
  })

  it('drops BPS settings when changing to another action', () => {
    const rule = probePlan(); rule.pelican_config!.quality!.action = 'enable_bps'; rule.pelican_config!.quality!.bps = bps()
    const input = draft(); input.pelican_config.quality.action = 'disable_scheduling'
    const updated = buildQualityRulePatch(rule, input, ['action']).pelican_config!.quality!
    expect(updated).toMatchObject({ action: 'disable_scheduling', remove_group_ids: [] })
    expect(updated).not.toHaveProperty('bps')
  })

  it('never leaves a candy rule with the enable BPS action', () => {
    const input = draft(); input.pelican_config.quality.action = 'enable_bps'; input.pelican_config.quality.bps = bps()
    expect(() => buildQualityRulePatch(plan(), input, ['action'])).toThrow('qualityOps.bpsRequiresProbe')
    const rule = probePlan(); rule.pelican_config!.quality!.action = 'enable_bps'; rule.pelican_config!.quality!.bps = bps()
    expect(() => buildQualityRulePatch(rule, draft(), ['test'])).toThrow('qualityOps.bpsRequiresProbe')
    input.pelican_config.question_kind = 'state_probe'; input.pelican_config.quality.bps = { ...bps(), failure_threshold: 0 }
    expect(() => buildQualityRulePatch(probePlan(), input, ['action'])).toThrow('qualityOps.bpsTriggerRequired')
  })

  it('converts explicit BPS candy observations without retaining automatic BPS or restoration', () => {
    const rule = probePlan()
    rule.pelican_config!.quality = { ...rule.pelican_config!.quality!, action: 'enable_bps', auto_restore: true, bps: bps() }
    const input = draft(); input.pelican_config.test_channel = 'bps'
    const patch = buildQualityRulePatch(rule, input, ['test', 'restore'])
    expect(patch.pelican_config).toMatchObject({ question_kind: 'candy', test_channel: 'bps', prompt: 'New question',
      quality: { action: 'observe_only', auto_restore: false, remove_group_ids: [], expected_answer: '42', judge: input.pelican_config.quality.judge } })
    expect(patch.pelican_config!.quality).not.toHaveProperty('bps')
    expect(rule.pelican_config!.quality!.action).toBe('enable_bps')
  })

  it('reports the first invalid BPS setting', () => {
    expect(qualityBPSError(bps())).toBe('')
    expect(qualityBPSError({ ...bps(), failure_threshold: 0, usage_percent: 80 })).toBe('')
    expect(qualityBPSError({ ...bps(), failure_threshold: 1.5 })).toBe('qualityOps.bpsCountInvalid')
    expect(qualityBPSError({ ...bps(), failure_threshold: 101 })).toBe('qualityOps.bpsCountInvalid')
    expect(qualityBPSError({ ...bps(), usage_percent: -1 })).toBe('qualityOps.bpsUsageInvalid')
    expect(qualityBPSError({ ...bps(), usage_percent: Number.NaN })).toBe('qualityOps.bpsUsageInvalid')
    expect(qualityBPSError({ ...bps(), failure_threshold: 0 })).toBe('qualityOps.bpsTriggerRequired')
    expect(qualityBPSError({ ...bps(), models: [' '] })).toBe('qualityOps.bpsModelsRequired')
    expect(qualityBPSError({ ...bps(), models: [], all_models: true })).toBe('')
    expect(qualityBPSError({ ...bps(), auto_move_on_403: true })).toBe('qualityOps.bpsTargetGroupRequired')
    expect(qualityBPSError({ ...bps(), auto_move_on_403: true, target_group_id: 0 })).toBe('')
    expect(qualityBPSError({ ...bps(), pass_threshold: 1 })).toBe('')
    expect(qualityBPSError({ ...bps(), pass_threshold: 100 })).toBe('')
    for (const pass_threshold of [0, 101, 1.5, Number.NaN]) expect(qualityBPSError({ ...bps(), pass_threshold })).toBe('qualityOps.bpsPassCountInvalid')
  })
})

describe('BPS recovery interval in quality rules', () => {
  it('defaults legacy rules to 60 minutes and persists custom intervals', () => {
    expect(qualityBPSForm({}).recovery_interval_minutes).toBe(60)
    expect(qualityBPSPayload({ ...bps(), recovery_interval_minutes: undefined }).recovery_interval_minutes).toBe(60)
    for (const minutes of [1, 30, 360, 10080]) {
      const form = qualityBPSForm({ ...bps(), recovery_interval_minutes: minutes })
      expect(qualityBPSError(form)).toBe('')
      expect(qualityBPSPayload(form).recovery_interval_minutes).toBe(minutes)
    }
  })
  it.each([0, -1, 1.5, 10081, NaN, Infinity])('rejects invalid interval %s', (minutes) => {
    expect(qualityBPSError({ ...bps(), recovery_interval_minutes: minutes })).toBe('admin.accounts.openai.excelBPS403RecoveryIntervalInvalid')
  })
})

describe('composed model isolation', () => {
  it('uses tested models and selected groups without legacy cooldown or concurrency fields', () => {
    const input = draft()
    input.pelican_config.quality.action = 'isolate_models'
    input.pelican_config.quality.remove_models = ['stale-target']
    input.pelican_config.quality.recovery_concurrency = 2
    input.pelican_config.model_ids = ['sol', 'astra']
    const patch = buildQualityRulePatch(plan(), input, ['action', 'model'])
    expect(patch.pelican_config).toMatchObject({ model_ids: ['new-model', 'sol', 'astra'], quality: {
      action: 'isolate_models', remove_group_ids: [21], expected_answer: '7', auto_restore: false,
    } })
    expect(patch.pelican_config!.quality).not.toHaveProperty('remove_models')
    expect(patch.pelican_config!.quality).not.toHaveProperty('recovery_concurrency')
    expect(patch.pelican_config!.quality).not.toHaveProperty('bps')
  })

  it('requires explicit groups and per-model answer tests for bulk changes', () => {
    const input = draft()
    input.pelican_config.quality.action = 'isolate_models'
    input.pelican_config.quality.remove_group_ids = []
    expect(() => buildQualityRulePatch(plan(), input, ['action'])).toThrow('qualityOps.selectGroups')
    input.pelican_config.quality.remove_group_ids = [21]
    expect(() => buildQualityRulePatch(probePlan(), input, ['action'])).toThrow('qualityOps.isolationRequiresCandy')
  })
})


describe('complete template selected fields', () => {
  it('imports retention and 5xx trigger only with their explicit bulk checkboxes', () => {
    const source = { ...draft(), max_results: 173 }
    source.pelican_config.quality.trigger_on_upstream_5xx = true
    const untouched = buildQualityRulePatch(plan(), source, ['enabled'])
    expect(untouched).toEqual({ enabled: false })
    const retention = buildQualityRulePatch(plan(), source, ['retention'])
    expect(retention).toEqual({ max_results: 173 })
    const triggered = buildQualityRulePatch(plan(), source, ['trigger'])
    expect(triggered.pelican_config!.quality!.trigger_on_upstream_5xx).toBe(true)
    expect(triggered.pelican_config!.quality!.action).toBe(plan().pelican_config!.quality!.action)
    expect(triggered).not.toHaveProperty('max_results')
  })
  it('rejects retention too small for the targets existing samples when only retention is selected', () => {
    const target = plan(); target.pelican_config!.model_ids = ['first', 'second']; target.pelican_config!.parallel_count = 3
    expect(() => buildQualityRulePatch(target, { ...draft(), max_results: 5 }, ['retention'])).toThrow('qualityOps.retentionTooSmall')
    expect(buildQualityRulePatch(target, { ...draft(), max_results: 6 }, ['retention'])).toEqual({ max_results: 6 })
  })
  it('never inherits a punitive action for old presets and removes hidden restrictions', () => {
    const source = draft().pelican_config
    delete (source.quality as any).action
    const copied = copyQualityRuleConfig(source)
    expect(copied.quality.action).toBe('observe_only'); expect(copied.quality.remove_group_ids).toEqual([]); expect(copied.quality.auto_restore).toBe(false)
    expect(copied.quality.remove_models).toBeUndefined(); expect(copied.quality.bps).toBeUndefined()
  })
})
