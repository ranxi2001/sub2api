import type { PelicanTestConfig, QualityBPSPolicy, QualityJudgeConfig, QualityPolicy, ScheduledTestPlan, UpdateScheduledTestPlanRequest } from '@/types'
import { DEFAULT_EXCEL_BPS_MODELS } from '@/constants/account'
import { STATE_PROBE_QUESTION } from './intelligenceTest'
import { DEFAULT_BPS_RECOVERY_INTERVAL_MINUTES, isValidBPSRecoveryInterval } from './excelBPSRecovery'

export function qualityJudgeConfigured(judge?: QualityJudgeConfig): judge is QualityJudgeConfig {
  if (!judge?.prompt.trim()) return false
  if (judge.kind === 'jev') return !judge.group_id && !judge.model_id
  return (!judge.kind || judge.kind === 'llm') && judge.group_id > 0 && !!judge.model_id.trim()
}

// 新建「降智开 BPS」时的默认勾选；target_group_id = -1 表示还没选 403 后的目标分组。
export function defaultQualityBPS(): QualityBPSPolicy {
  return { failure_threshold: 2, usage_percent: 0, require_all: false, all_models: false, models: [...DEFAULT_EXCEL_BPS_MODELS],
    omit_unsupported_tools: false, ignore_encrypted_content: true, auto_disable_on_403: true, auto_recover_on_403: false, recovery_interval_minutes: DEFAULT_BPS_RECOVERY_INTERVAL_MINUTES, auto_move_on_403: false,
    target_group_id: -1, session_proxy: false, proxy_source: 'mihomo', cache_creation_as_input: true, pass_threshold: 2, hold_on_usage: true }
}

// 已保存的 BPS 设置盖在默认值上；「全部模型」时后端不存模型列表，切回按模型时给默认候选。
// 早先保存、没有满血关闭次数的规则后端按 1 次处理，表单也显示 1。
export function qualityBPSForm(saved?: Partial<QualityBPSPolicy> | null): QualityBPSPolicy {
  const value = saved || {}
  return { ...defaultQualityBPS(), ...value,
    models: value.models?.length ? [...value.models] : [...DEFAULT_EXCEL_BPS_MODELS],
    target_group_id: value.auto_move_on_403 ? value.target_group_id ?? -1 : -1,
    proxy_source: value.proxy_source || 'mihomo',
    pass_threshold: saved ? saved.pass_threshold || 1 : defaultQualityBPS().pass_threshold }
}

// Copy reusable inputs only. Result snapshots and future server-only fields must
// never enter a create request through a spread of the source configuration.
export function copyQualityRuleConfig(source: PelicanTestConfig): PelicanTestConfig & { quality: QualityPolicy } {
  const policy: QualityPolicy = source.quality || { action: 'observe_only', expected_answer: '', remove_group_ids: [], auto_restore: false }
  if (policy.action === 'openai_two_fa_admission') throw new Error('qualityOps.copyUnsupported')
  const action = policy.action || 'observe_only'
  const bps = action === 'enable_bps' ? policy.bps : undefined
  return {
    rule_name: source.rule_name,
    question_kind: source.question_kind,
    test_channel: source.test_channel,
    prompt: source.question_kind === 'random_reasoning' ? '' : source.prompt,
    reasoning_effort: source.reasoning_effort,
    parallel_count: source.parallel_count,
    model_ids: source.model_ids ? [...source.model_ids] : undefined,
    quality: {
      trigger_on_upstream_5xx: policy.trigger_on_upstream_5xx ?? false,
      oauth_401_max_rounds: policy.oauth_401_max_rounds ?? 0,
      expected_answer: source.question_kind === 'random_reasoning' ? '' : policy.expected_answer,
      action,
      remove_group_ids: qualityUsesGroups(action) ? [...(policy.remove_group_ids || [])] : [],
      remove_models: action === 'remove_models' ? [...(policy.remove_models || [])] : undefined,
      recovery_concurrency: action === 'remove_models' ? policy.recovery_concurrency : undefined,
      ...(qualityUsesRetestRest(action) && policy.retest_rest_minutes !== undefined ? { retest_rest_minutes: policy.retest_rest_minutes } : {}),
      auto_restore: action === 'observe_only' ? false : policy.auto_restore ?? false,
      judge: policy.judge ? { ...(policy.judge.kind ? { kind: policy.judge.kind } : {}), group_id: policy.judge.group_id, model_id: policy.judge.model_id, prompt: policy.judge.prompt } : undefined,
      bps: bps ? {
        failure_threshold: bps.failure_threshold, usage_percent: bps.usage_percent, require_all: bps.require_all,
        all_models: bps.all_models, models: [...(bps.models || [])], omit_unsupported_tools: bps.omit_unsupported_tools,
        ignore_encrypted_content: bps.ignore_encrypted_content, auto_disable_on_403: bps.auto_disable_on_403,
        auto_recover_on_403: bps.auto_recover_on_403, recovery_interval_minutes: bps.recovery_interval_minutes,
        auto_move_on_403: bps.auto_move_on_403, target_group_id: bps.target_group_id,
        session_proxy: bps.session_proxy, proxy_source: bps.proxy_source, cache_creation_as_input: bps.cache_creation_as_input,
        pass_threshold: bps.pass_threshold, hold_on_usage: bps.hold_on_usage,
      } : undefined,
    },
  }
}

const validPassCount = (count: number) => Number.isInteger(count) && count >= 1 && count <= 100

// 与后端 validateQualityBPSPolicy 对应的前置检查，返回 i18n 键；空串表示通过。
export function qualityBPSError(bps: QualityBPSPolicy): string {
  const count = bps.failure_threshold, usage = bps.usage_percent
  if (!Number.isInteger(count) || count < 0 || count > 100) return 'qualityOps.bpsCountInvalid'
  if (!Number.isFinite(usage) || usage < 0 || usage > 100) return 'qualityOps.bpsUsageInvalid'
  if (!count && !usage) return 'qualityOps.bpsTriggerRequired'
  if (!validPassCount(bps.pass_threshold)) return 'qualityOps.bpsPassCountInvalid'
  if (!bps.all_models && !bps.models.some(model => model.trim())) return 'qualityOps.bpsModelsRequired'
  if (bps.recovery_interval_minutes !== undefined && !isValidBPSRecoveryInterval(bps.recovery_interval_minutes)) return 'admin.accounts.openai.excelBPS403RecoveryIntervalInvalid'
  if (bps.auto_move_on_403 && !(bps.target_group_id >= 0)) return 'qualityOps.bpsTargetGroupRequired'
  return ''
}

// 表单里「未选目标分组」用 -1 占位；提交前按后端口径归一，避免未勾选的子项残留旧值。
export function qualityBPSPayload(bps: QualityBPSPolicy): QualityBPSPolicy {
  return {
    ...bps,
    recovery_interval_minutes: bps.recovery_interval_minutes ?? DEFAULT_BPS_RECOVERY_INTERVAL_MINUTES,
    models: bps.all_models ? [] : [...bps.models],
    target_group_id: bps.auto_move_on_403 ? bps.target_group_id : 0,
    proxy_source: bps.session_proxy ? bps.proxy_source || 'mihomo' : '',
  }
}

export function qualityUsesGroups(action?: QualityPolicy['action']) {
  return action === 'remove_groups' || action === 'isolate_models'
}

export function qualityUsesModelResults(action?: QualityPolicy['action']) {
  return action === 'remove_models' || action === 'isolate_models'
}

export function qualityUsesRetestRest(action?: QualityPolicy['action']) {
  return action === 'remove_groups' || action === 'disable_scheduling' || qualityUsesModelResults(action)
}

function oauth401MaxRounds(value?: number): number {
  const rounds = value ?? 0
  if (!Number.isInteger(rounds) || rounds < 0 || rounds > 100) throw new Error('qualityOps.invalidOAuth401MaxRounds')
  return rounds
}

// Used by single-account creation, templates and bulk editing. Action cleanup
// and validation belong here so all entry points preserve the same semantics.
export function qualityActionPayload(policy: QualityPolicy): QualityPolicy {
  oauth401MaxRounds(policy.oauth_401_max_rounds)
  const next = { ...policy, remove_group_ids: qualityUsesGroups(policy.action) ? [...policy.remove_group_ids] : [] }
  const rest = qualityUsesRetestRest(next.action) ? policy.retest_rest_minutes ?? 0 : 0
  if (!Number.isInteger(rest) || rest < 0 || rest > 10080) throw new Error('qualityOps.invalidRetestRest')
  if (rest > 0) next.retest_rest_minutes = rest
  else delete next.retest_rest_minutes
  if (qualityUsesGroups(next.action) && !next.remove_group_ids.length) throw new Error('qualityOps.selectGroups')
  if (next.action === 'remove_models') {
    next.remove_models = [...(policy.remove_models || [])]
    if (!next.remove_models.length) throw new Error('qualityOps.selectModels')
    const cap = policy.recovery_concurrency ?? 5
    if (!Number.isInteger(cap) || cap < 1 || cap > 10000) throw new Error('qualityOps.invalidRecoveryConcurrency')
    next.recovery_concurrency = cap
  } else { delete next.remove_models; delete next.recovery_concurrency }
  if (next.action === 'enable_bps') {
    if (!next.bps) throw new Error('qualityOps.bpsTriggerRequired')
    const error = qualityBPSError(next.bps)
    if (error) throw new Error(error)
    next.bps = qualityBPSPayload(next.bps)
  } else delete next.bps
  if (next.action === 'observe_only') next.auto_restore = false
  return next
}

export const qualityRuleFields = ['model', 'schedule', 'test', 'action', 'restore', 'retention', 'trigger', 'oauth401', 'enabled'] as const
export type QualityRuleField = typeof qualityRuleFields[number]
export type QualityRuleDraft = {
  model_id: string
  cron_expression: string
  max_results?: number
  enabled: boolean
  pelican_config: PelicanTestConfig & { quality: QualityPolicy }
}

// Compare only the business fields owned by each bulk checkbox. Dependencies
// between fields must be confirmed too when importing a configuration.
export function qualityRuleFieldValue(rule: Pick<ScheduledTestPlan, 'model_id' | 'cron_expression' | 'enabled' | 'pelican_config'> & { max_results?: number }, field: QualityRuleField): unknown {
  const cfg = rule.pelican_config, q = cfg?.quality, b = q?.bps
  switch (field) {
    case 'model': return [...new Set([rule.model_id, ...(cfg?.model_ids || [])])]
    case 'schedule': return rule.cron_expression
    case 'enabled': return rule.enabled
    case 'retention': return rule.max_results
    case 'trigger': return q?.trigger_on_upstream_5xx ?? false
    case 'oauth401': return q?.oauth_401_max_rounds ?? 0
    case 'test': return [cfg?.question_kind, cfg?.test_channel || 'account', cfg?.prompt, cfg?.reasoning_effort, cfg?.parallel_count,
      q?.expected_answer, q?.judge?.kind || 'llm', q?.judge?.group_id, q?.judge?.model_id, q?.judge?.prompt]
    case 'restore': return [q?.auto_restore, b?.pass_threshold, b?.hold_on_usage]
    case 'action': return [q?.action, q?.remove_group_ids || [], q?.remove_models || [], q?.recovery_concurrency, q?.retest_rest_minutes ?? 0,
      b ? [b.failure_threshold, b.usage_percent, b.require_all, b.all_models, b.models || [], b.omit_unsupported_tools,
        b.ignore_encrypted_content, b.auto_disable_on_403, b.auto_recover_on_403, b.recovery_interval_minutes,
        b.auto_move_on_403, b.target_group_id, b.session_proxy, b.proxy_source, b.cache_creation_as_input] : null]
  }
}

// The API replaces pelican_config as a whole. Merge selected fields into each
// rule's own configuration, never into a shared copy of the first rule.
export function buildQualityRulePatch(
  plan: ScheduledTestPlan,
  draft: QualityRuleDraft,
  fields: readonly QualityRuleField[],
): UpdateScheduledTestPlanRequest {
  const patch: UpdateScheduledTestPlanRequest = {}
  if (!fields.length) throw new Error('qualityOps.selectBulkFields')
  if (fields.includes('model')) {
    if (!draft.model_id.trim()) throw new Error('qualityOps.modelRequired')
    patch.model_id = draft.model_id.trim()
  }
  if (fields.includes('schedule')) {
    if (!draft.cron_expression.trim()) throw new Error('qualityOps.scheduleRequired')
    patch.cron_expression = draft.cron_expression.trim()
  }
  if (fields.includes('retention')) {
    if (!Number.isInteger(draft.max_results) || draft.max_results! < 1 || draft.max_results! > 200) throw new Error('qualityOps.invalidMaxResults')
    patch.max_results = draft.max_results
  }
  if (fields.includes('enabled')) patch.enabled = draft.enabled
  if (fields.includes('retention') && !fields.includes('test') && !fields.includes('model') && (plan.pelican_config?.model_ids?.length || 1) * (plan.pelican_config?.parallel_count || 1) > patch.max_results!) throw new Error('qualityOps.retentionTooSmall')
  if (!fields.some(field => ['test', 'action', 'restore', 'trigger', 'oauth401'].includes(field)) &&
    !(fields.includes('model') && (draft.pelican_config.model_ids?.length || plan.pelican_config?.model_ids?.length))) return patch

  if (!plan.pelican_config?.quality) throw new Error('qualityOps.ruleConfigMissing')
  const config: PelicanTestConfig & { quality: QualityPolicy } = JSON.parse(JSON.stringify(plan.pelican_config))
  const source = draft.pelican_config
  if (fields.includes('model')) {
    config.model_ids = [...new Set([draft.model_id.trim(), ...(source.model_ids || []).map(model => model.trim())])]
    if (config.model_ids.some(model => !model || model.length > 100)) throw new Error('qualityOps.modelRequired')
  }
  if (fields.includes('test')) {
    config.question_kind = source.question_kind
    if (source.test_channel) config.test_channel = source.test_channel
    else delete config.test_channel
    config.reasoning_effort = source.reasoning_effort
    const probe = source.question_kind === STATE_PROBE_QUESTION
    const random = source.question_kind === 'random_reasoning'
    delete config.random_question
    config.prompt = probe || random ? '' : source.prompt
    config.parallel_count = probe ? 1 : source.parallel_count
    config.quality.expected_answer = probe || random ? '' : source.quality.expected_answer
    if (probe) { delete config.quality.judge; config.model_ids = [patch.model_id || plan.model_id] }
    else {
      const judge = copyQualityRuleConfig(source).quality.judge
      if (!qualityJudgeConfigured(judge)) throw new Error('qualityOps.configureJudge')
      if (!random && (!config.prompt.trim() || !config.quality.expected_answer.trim())) throw new Error('qualityOps.questionRequired')
      config.quality.judge = { ...judge }
    }
  }
  if (fields.includes('trigger')) config.quality.trigger_on_upstream_5xx = source.quality.trigger_on_upstream_5xx ?? false
  if (fields.includes('oauth401')) config.quality.oauth_401_max_rounds = oauth401MaxRounds(source.quality.oauth_401_max_rounds)
  if (fields.includes('action')) {
    config.quality = qualityActionPayload({ ...config.quality,
      action: source.quality.action, remove_group_ids: source.quality.remove_group_ids,
      remove_models: source.quality.remove_models, recovery_concurrency: source.quality.recovery_concurrency,
      retest_rest_minutes: source.quality.retest_rest_minutes,
      bps: source.quality.bps })
  }

  // Explicit BPS observation includes giving up this rule's account actions.
  if (config.test_channel === 'bps') config.quality.action = 'observe_only'
  if (config.quality.action === 'observe_only') {
    config.quality.remove_group_ids = []
    config.quality.auto_restore = false
    delete config.quality.bps
    delete config.quality.retest_rest_minutes
  }
  // Automatic BPS switching remains a separate native-probe policy.
  if (config.quality.action === 'enable_bps' && config.question_kind !== STATE_PROBE_QUESTION) throw new Error('qualityOps.bpsRequiresProbe')
  if (fields.includes('restore') && config.quality.action !== 'observe_only') config.quality.auto_restore = source.quality.auto_restore
  // 满血关闭次数和「用量仍高时先不关」跟着「自动恢复」走：勾选修改自动恢复且开着时才用表单里的值，
  // 否则各规则保留自己的；原本不是开 BPS 的规则用默认值。
  if (config.quality.action === 'enable_bps' && config.quality.bps) {
    const draftBPS = fields.includes('restore') && source.quality.auto_restore ? source.quality.bps : undefined
    if (draftBPS && !validPassCount(draftBPS.pass_threshold)) throw new Error('qualityOps.bpsPassCountInvalid')
    const from = draftBPS || plan.pelican_config.quality.bps || defaultQualityBPS()
    config.quality.bps.pass_threshold = from.pass_threshold
    config.quality.bps.hold_on_usage = from.hold_on_usage
  }
  if (config.quality.action === 'isolate_models' && config.question_kind !== 'candy' && config.question_kind !== 'random_reasoning') throw new Error('qualityOps.isolationRequiresCandy')
  if (config.question_kind === STATE_PROBE_QUESTION && (config.model_ids?.length || 0) > 1) throw new Error('qualityOps.probeSingleModel')
  const samples = (config.model_ids?.length || 1) * config.parallel_count
  if (samples > 100 || (config.model_ids?.length || 0) > 50) throw new Error('qualityOps.tooManyModelSamples')
  if (fields.includes('retention') && samples > patch.max_results!) throw new Error('qualityOps.retentionTooSmall')
  if (!fields.includes('retention') && samples > plan.max_results) patch.max_results = samples
  patch.pelican_config = config
  return patch
}
