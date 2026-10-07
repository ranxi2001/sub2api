<template>
  <div class="flex flex-col items-start gap-1">
    <span
      v-if="protocolEnabled && isExcelBPSEnabled"
      data-testid="bps-status-badge"
      class="inline-flex items-center rounded bg-[#217346] px-1.5 py-0.5 text-[10px] font-semibold leading-3 text-white"
      :title="t('admin.accounts.openai.excelBPS')"
    >bps</span>
    <div class="flex items-center gap-2">
      <!-- OpenAI OAuth RPM Display - keep the pause reason explicit -->
      <div v-if="isRPMPaused" class="flex flex-col items-center gap-1">
        <span class="badge text-xs badge-warning">{{ t('admin.accounts.status.rpmPaused') }}</span>
        <span class="text-[11px] text-gray-400 dark:text-gray-500">{{ rpmResumeText }}</span>
      </div>

      <!-- Rate Limit Display (429) - Two-line layout -->
      <div v-else-if="isRateLimited" class="flex flex-col items-center gap-1">
        <span class="badge text-xs badge-warning">{{ t('admin.accounts.status.rateLimited') }}</span>
        <span class="text-[11px] text-gray-400 dark:text-gray-500">{{ rateLimitResumeText }}</span>
      </div>

      <!-- Overload Display (529) - Two-line layout -->
      <div v-else-if="isOverloaded" class="flex flex-col items-center gap-1">
        <span class="badge text-xs badge-danger">{{ t('admin.accounts.status.overloaded') }}</span>
        <span class="text-[11px] text-gray-400 dark:text-gray-500">{{ overloadCountdown }}</span>
      </div>

      <!-- Main Status Badge (shown when not rate limited/overloaded) -->
      <template v-else>
        <div v-if="isTempUnschedulable" class="flex flex-col items-center gap-1">
          <button
            type="button"
            :class="['badge text-xs', statusClass, 'cursor-pointer']"
            :title="t('admin.accounts.status.viewTempUnschedDetails')"
            @click="handleTempUnschedClick"
          >
            {{ statusText }}
          </button>
          <span class="max-w-[180px] text-center text-[11px] leading-4 text-gray-500 dark:text-gray-400">
            {{ tempUnschedRecoveryText }}
          </span>
        </div>
        <span v-else :class="['badge text-xs', statusClass]">
          {{ statusText }}
        </span>
      </template>

      <!-- Error Info Indicator -->
      <HelpTooltip v-if="hasError && account.error_message" class="!ml-0" width-class="w-72">
        <template #trigger>
          <svg
            class="h-4 w-4 cursor-help text-red-500 transition-colors hover:text-red-600 dark:text-red-400 dark:hover:text-red-300"
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
            stroke-width="2"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              d="M9.879 7.519c1.171-1.025 3.071-1.025 4.242 0 1.172 1.025 1.172 2.687 0 3.712-.203.179-.43.326-.67.442-.745.361-1.45.999-1.45 1.827v.75M21 12a9 9 0 11-18 0 9 9 0 0118 0zm-9 5.25h.008v.008H12v-.008z"
            />
          </svg>
        </template>
        <div class="whitespace-pre-wrap break-words">{{ account.error_message }}</div>
      </HelpTooltip>

      <!-- Rate Limit Indicator (429) -->
      <HelpTooltip v-if="isRateLimited" class="!ml-0" width-class="w-56">
        <template #trigger>
          <span
            class="inline-flex items-center gap-1 rounded bg-amber-100 px-1.5 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-400"
          >
            <Icon name="exclamationTriangle" size="xs" :stroke-width="2" />
            429
          </span>
        </template>
        {{ t('admin.accounts.status.rateLimitedUntil', { time: formatDateTime(account.rate_limit_reset_at) }) }}
      </HelpTooltip>

      <!-- Model Status Indicators (普通限流 / 超量请求中) -->
      <div
        v-if="activeModelStatuses.length > 0"
        :class="[
          activeModelStatuses.length <= 4
            ? 'flex flex-col gap-1'
            : activeModelStatuses.length <= 8
              ? 'columns-2 gap-x-2'
              : 'columns-3 gap-x-2'
        ]"
      >
        <HelpTooltip v-for="item in activeModelStatuses" :key="`${item.kind}-${item.model}`" class="!ml-0 !flex mb-1 break-inside-avoid" width-class="w-80">
          <template #trigger>
            <!-- 积分已用尽 -->
            <span
              v-if="item.kind === 'credits_exhausted'"
              class="inline-flex items-center gap-1 rounded bg-red-100 px-1.5 py-0.5 text-xs font-medium text-red-700 dark:bg-red-900/30 dark:text-red-400"
            >
              <Icon name="exclamationTriangle" size="xs" :stroke-width="2" />
              {{ t('admin.accounts.status.creditsExhausted') }}
              <span class="text-[10px] opacity-70">{{ formatCountdown(item.reset_at) }}</span>
            </span>
            <!-- 正在走积分（模型限流但积分可用）-->
            <span
              v-else-if="item.kind === 'credits_active'"
              class="inline-flex items-center gap-1 rounded bg-amber-100 px-1.5 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-400"
            >
              <span>⚡</span>
              {{ formatScopeName(item.model) }}
              <span class="text-[10px] opacity-70">{{ formatCountdown(item.reset_at) }}</span>
            </span>
            <!-- 普通模型限流 -->
            <span
              v-else
              class="inline-flex items-center gap-1 rounded bg-purple-100 px-1.5 py-0.5 text-xs font-medium text-purple-700 dark:bg-purple-900/30 dark:text-purple-400"
            >
              <Icon name="exclamationTriangle" size="xs" :stroke-width="2" />
              {{ formatScopeName(item.model) }}
              <span class="text-[10px] opacity-70">{{ formatCountdown(item.reset_at) }}</span>
            </span>
          </template>
          {{
            item.kind === 'credits_exhausted'
              ? t('admin.accounts.status.creditsExhaustedUntil', { time: formatDateTimeToMinute(item.reset_at) })
              : item.kind === 'credits_active'
                ? t('admin.accounts.status.modelCreditOveragesUntil', { model: formatScopeName(item.model), time: formatDateTimeToMinute(item.reset_at) })
                : t('admin.accounts.status.modelRateLimitedUntil', { model: formatScopeName(item.model), time: formatDateTimeToMinute(item.reset_at) })
          }}
        </HelpTooltip>
      </div>

      <!-- Overload Indicator (529) -->
      <HelpTooltip v-if="isOverloaded" class="!ml-0" width-class="w-56">
        <template #trigger>
          <span
            class="inline-flex items-center gap-1 rounded bg-red-100 px-1.5 py-0.5 text-xs font-medium text-red-700 dark:bg-red-900/30 dark:text-red-400"
          >
            <Icon name="exclamationTriangle" size="xs" :stroke-width="2" />
            529
          </span>
        </template>
        {{ t('admin.accounts.status.overloadedUntil', { time: formatTime(account.overload_until) }) }}
      </HelpTooltip>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Account } from '@/types'
import { formatCountdown, formatDateTime, formatDateTimeToMinute, formatCountdownWithSuffix, formatTime } from '@/utils/format'

const { t } = useI18n()

const props = withDefaults(defineProps<{
  account: Account
  globalBpsEnabled?: boolean
}>(), { globalBpsEnabled: true })

const emit = defineEmits<{
  (e: 'show-temp-unsched', account: Account): void
}>()

const protocolEnabled = computed(() => props.globalBpsEnabled)

// Keep eligibility aligned with Account.IsExcelBPSEnabled on the backend.
const isExcelBPSEnabled = computed(() => {
  const account = props.account
  if (account.platform !== 'openai' || account.type !== 'oauth' || account.parent_account_id != null ||
      account.extra?.openai_excel_bps !== true) return false
  const credential = (key: string) => {
    const value = account.credentials?.[key]
    return typeof value === 'string' ? value.trim().toLowerCase() : ''
  }
  const isPAT = (mode: string) => mode === 'personalaccesstoken' || mode === 'personal_access_token'
  return credential('plan_type') !== 'free' && credential('auth_mode') !== 'agentidentity' &&
    !isPAT(credential('auth_mode')) && !isPAT(credential('openai_auth_mode'))
})

// Computed: is rate limited (429)
const isRateLimited = computed(() => {
  if (!props.account.rate_limit_reset_at) return false
  return new Date(props.account.rate_limit_reset_at) > new Date()
})

type AccountModelStatusItem = {
  kind: 'rate_limit' | 'credits_exhausted' | 'credits_active'
  model: string
  reset_at: string
}

// Computed: active model statuses (普通模型限流 + 积分耗尽 + 走积分中)
const activeModelStatuses = computed<AccountModelStatusItem[]>(() => {
  const extra = props.account.extra as Record<string, unknown> | undefined
  const modelLimits = extra?.model_rate_limits as
    | Record<string, { rate_limited_at: string; rate_limit_reset_at: string }>
    | undefined
  const now = new Date()
  const items: AccountModelStatusItem[] = []

  if (!modelLimits) return items

  // 检查 AICredits key 是否生效（积分是否耗尽）
  const aiCreditsEntry = modelLimits['AICredits']
  const hasActiveAICredits = aiCreditsEntry && new Date(aiCreditsEntry.rate_limit_reset_at) > now
  const allowOverages = !!(extra?.allow_overages)

  for (const [model, info] of Object.entries(modelLimits)) {
    if (new Date(info.rate_limit_reset_at) <= now) continue

    if (model === 'AICredits') {
      // AICredits key → 积分已用尽
      items.push({ kind: 'credits_exhausted', model, reset_at: info.rate_limit_reset_at })
    } else if (allowOverages && !hasActiveAICredits) {
      // 普通模型限流 + overages 启用 + 积分可用 → 正在走积分
      items.push({ kind: 'credits_active', model, reset_at: info.rate_limit_reset_at })
    } else {
      // 普通模型限流
      items.push({ kind: 'rate_limit', model, reset_at: info.rate_limit_reset_at })
    }
  }

  return items
})

const formatScopeName = (scope: string): string => {
  const aliases: Record<string, string> = {
    // Claude 系列
    'claude-fable-5-1': 'CFable51',
    'claude-fable-5': 'CFable5',
    'claude-opus-4-6': 'COpus46',
    'claude-opus-4-6-thinking': 'COpus46T',
    'claude-opus-4-7': 'COpus47',
    'claude-opus-4-8': 'COpus48',
    'claude-opus-5-5': 'COpus55',
    'claude-opus-5': 'COpus5',
    'claude-sonnet-4-6': 'CSon46',
    'claude-sonnet-4-5': 'CSon45',
    'claude-sonnet-4-5-thinking': 'CSon45T',
    'claude-sonnet-5-5': 'CSon55',
    'claude-sonnet-5': 'CSon5',
    // Gemini 2.5 系列
    'gemini-2.5-flash': 'G25F',
    'gemini-2.5-flash-lite': 'G25FL',
    'gemini-2.5-flash-thinking': 'G25FT',
    'gemini-2.5-pro': 'G25P',
    'gemini-2.5-flash-image': 'G25I',
    // Gemini 3.5 系列
    'gemini-3.5-flash': 'G35F',
    // Gemini 3 系列
    'gemini-3-flash': 'G3F',
    'gemini-3.1-pro-high': 'G3PH',
    'gemini-3.1-pro-low': 'G3PL',
    'gemini-3-pro-image': 'G3PI',
    'gemini-3.1-flash-image': 'G31FI',
    // 其他
    'gpt-oss-120b-medium': 'GPT120',
    'tab_flash_lite_preview': 'TabFL',
    // 旧版 scope 别名（兼容）
    claude: 'Claude',
    claude_sonnet: 'CSon',
    claude_opus: 'COpus',
    claude_haiku: 'CHaiku',
    gemini_text: 'Gemini',
    gemini_image: 'GImg',
    gemini_flash: 'GFlash',
    gemini_pro: 'GPro',
  }
  return aliases[scope] || scope
}

// Computed: is overloaded (529)
const isOverloaded = computed(() => {
  if (!props.account.overload_until) return false
  return new Date(props.account.overload_until) > new Date()
})

const isRPMPaused = computed(() =>
  props.account.platform === 'openai' && props.account.type === 'oauth' &&
  props.account.status === 'active' && props.account.schedulable &&
  (props.account.base_rpm ?? 0) > 0 &&
  (props.account.rpm_paused === true ||
    (props.account.current_rpm ?? 0) >= (props.account.base_rpm ?? 0))
)

const rpmResumeText = computed(() => {
  if (props.account.rpm_reset_at) {
    return t('admin.accounts.status.rpmPausedUntil', {
      time: formatDateTime(new Date(props.account.rpm_reset_at * 1000).toISOString())
    })
  }
  return t('admin.accounts.status.rpmPausedRetry')
})

// Computed: is temp unschedulable
const isTempUnschedulable = computed(() => {
  if (!props.account.temp_unschedulable_until) return false
  return new Date(props.account.temp_unschedulable_until) > new Date()
})

// Computed: has error status
const hasError = computed(() => {
  return props.account.status === 'error'
})

const isQuotaExceeded = computed(() => {
  const exceeded = (used?: number | null, limit?: number | null) =>
    typeof limit === 'number' && limit > 0 && typeof used === 'number' && used >= limit
  return (
    exceeded(props.account.quota_used, props.account.quota_limit) ||
    exceeded(props.account.quota_daily_used, props.account.quota_daily_limit) ||
    exceeded(props.account.quota_weekly_used, props.account.quota_weekly_limit)
  )
})

// Computed: countdown text for rate limit (429)
const rateLimitCountdown = computed(() => {
  return formatCountdown(props.account.rate_limit_reset_at)
})

const rateLimitResumeText = computed(() => {
  if (!rateLimitCountdown.value) return ''
  return t('admin.accounts.status.rateLimitedAutoResume', { time: rateLimitCountdown.value })
})

// Computed: countdown text for overload (529)
const overloadCountdown = computed(() => {
  return formatCountdownWithSuffix(props.account.overload_until)
})

const tempUnschedRecoveryText = computed(() => {
  if (!isTempUnschedulable.value || !props.account.temp_unschedulable_until) return ''
  return t('admin.accounts.status.tempUnschedulableUntil', {
    time: formatDateTime(props.account.temp_unschedulable_until)
  })
})

// Computed: status badge class
const statusClass = computed(() => {
  if (hasError.value) {
    return 'badge-danger'
  }
  if (isTempUnschedulable.value) {
    return 'badge-warning'
  }
  if (props.account.status !== 'active') {
    return props.account.status === 'error' ? 'badge-danger' : 'badge-gray'
  }
  if (isQuotaExceeded.value) {
    return 'badge-warning'
  }
  if (!props.account.schedulable) {
    return 'badge-gray'
  }
  return 'badge-success'
})

// Computed: status text
const statusText = computed(() => {
  if (hasError.value) {
    return t('admin.accounts.status.error')
  }
  if (isTempUnschedulable.value) {
    return t('admin.accounts.status.tempUnschedulable')
  }
  if (props.account.status !== 'active') {
    return t(`admin.accounts.status.${props.account.status}`)
  }
  if (isQuotaExceeded.value) {
    return t('admin.accounts.status.quotaExceeded')
  }
  if (!props.account.schedulable) {
    return t('admin.accounts.status.paused')
  }
  return t(`admin.accounts.status.${props.account.status}`)
})

const handleTempUnschedClick = () => {
  if (!isTempUnschedulable.value) return
  emit('show-temp-unsched', props.account)
}
</script>
