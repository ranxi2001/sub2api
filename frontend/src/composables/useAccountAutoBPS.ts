import { ref } from 'vue'
import { adminAPI } from '@/api/admin'
import type { ScheduledTestPlan } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'
import { autoBPSCreateRequest, autoBPSDraftFromRule, autoBPSRuleChange, isAutoBPSRule, newAutoBPSDraft, pickAutoBPSRule, type AutoBPSDraft } from '@/utils/accountAutoBPS'
import { qualityBPSError } from '@/utils/qualityRulePatch'

// 添加/编辑账号弹窗里「降智后自动开启 BPS」开关背后的那条质量运维规则。
export function useAccountAutoBPS() {
  const draft = ref<AutoBPSDraft>(newAutoBPSDraft())
  const rule = ref<ScheduledTestPlan | null>(null)
  const conflictingRule = ref<ScheduledTestPlan | null>(null)
  const loading = ref(false)
  const loadError = ref('')
  let initial = JSON.stringify(draft.value)
  let version = 0

  function reset() {
    version++
    draft.value = newAutoBPSDraft()
    rule.value = null
    conflictingRule.value = null
    loading.value = false
    loadError.value = ''
    initial = JSON.stringify(draft.value)
  }

  async function load(accountId: number) {
    reset()
    const current = version
    loading.value = true
    try {
      const plans = await adminAPI.scheduledTests.listByAccount(accountId)
      if (current !== version) return
      rule.value = pickAutoBPSRule(plans)
      // Group/scheduling rules own a separate scope and can coexist with BPS.
      conflictingRule.value = plans.find(plan => plan.pelican_config?.quality?.action === 'enable_bps' && !isAutoBPSRule(plan)) ?? null
      draft.value = autoBPSDraftFromRule(rule.value)
      initial = JSON.stringify(draft.value)
    } catch (error) {
      if (current === version) loadError.value = extractApiErrorMessage(error, 'load failed')
    } finally {
      if (current === version) loading.value = false
    }
  }

  // 开关打开时才校验设置；返回 i18n key，空串表示通过。
  function validate(): string {
    if (!draft.value.enabled || conflictingRule.value || loading.value || loadError.value) return ''
    if (!draft.value.cronExpression.trim()) return 'qualityOps.scheduleRequired'
    return qualityBPSError(draft.value.bps)
  }

  // 新建账号后逐个建规则；单个失败不影响其它账号，返回没建成的账号。
  async function createFor(accountIds: number[], signal?: AbortSignal): Promise<{ failed: number[]; error: string }> {
    const result = { failed: [] as number[], error: '' }
    if (!draft.value.enabled) return result
    for (const id of accountIds) {
      signal?.throwIfAborted()
      try {
        const request = autoBPSCreateRequest(id, draft.value)
        if (signal) await adminAPI.scheduledTests.create(request, { signal })
        else await adminAPI.scheduledTests.create(request)
      } catch (error) {
        signal?.throwIfAborted()
        result.failed.push(id)
        result.error ||= extractApiErrorMessage(error, '')
      }
    }
    signal?.throwIfAborted()
    return result
  }

  // 编辑账号保存后同步规则。规则没读出来时不动，免得覆盖或重复建。
  async function saveFor(accountId: number): Promise<void> {
    if (loading.value || loadError.value || conflictingRule.value) return
    const change = autoBPSRuleChange(rule.value, JSON.parse(initial), draft.value, accountId)
    if (!change) return
    rule.value = change.kind === 'create'
      ? await adminAPI.scheduledTests.create(change.request)
      : await adminAPI.scheduledTests.update(change.id, change.request)
    initial = JSON.stringify(draft.value)
  }

  return { draft, rule, conflictingRule, loading, loadError, reset, load, validate, createFor, saveFor }
}
