<template>
  <div v-if="eligible" class="flex min-w-[7rem] flex-col">
    <div class="flex h-6 items-center gap-1">
      <HelpTooltip class="-ml-1" width-class="w-max max-w-[calc(100vw-2rem)]" data-testid="upstream-billing-details">
        <template #trigger>
          <button
            v-if="canConfigure && (newAPIGateway || (account.type === 'apikey' && (snapshot?.status === 'unsupported' || account.extra?.upstream_billing_provider === 'new_api')))"
            type="button"
            class="cursor-help border-b border-dotted border-gray-300 text-sm font-medium focus-visible:outline focus-visible:outline-2 focus-visible:outline-blue-500 dark:border-dark-600"
            :class="hasEffectiveRate ? 'font-mono text-gray-800 dark:text-gray-200' : statusClass || 'text-gray-400 dark:text-gray-500'"
            data-testid="upstream-billing-configure"
            :aria-label="`${primaryValue}, ${t('admin.accounts.upstreamBilling.newAPI.editConfig')}`"
            @click="$emit('configure')"
          >
            <span data-testid="upstream-billing-rate">{{ primaryValue }}</span>
          </button>
          <span
            v-else
            class="cursor-help border-b border-dotted border-gray-300 text-sm font-medium dark:border-dark-600"
            :class="hasEffectiveRate ? 'font-mono text-gray-800 dark:text-gray-200' : statusClass || 'text-gray-400 dark:text-gray-500'"
            data-testid="upstream-billing-rate"
          >
            {{ primaryValue }}
          </span>
        </template>
        <div class="space-y-1">
          <template v-if="isMultiGroup">
            <p>{{ t('admin.accounts.upstreamBilling.multiGroupHint') }}</p>
            <p>{{ t('admin.accounts.upstreamBilling.multiGroupSyncPaused') }}</p>
            <p>{{ t('admin.accounts.upstreamBilling.updatedAt', { value: formatDate(snapshot?.received_at) }) }}</p>
          </template>
          <template v-else-if="hasEffectiveRate && data">
            <p>{{ t('admin.accounts.upstreamBilling.groupRate', { value: data.group_rate_multiplier }) }}</p>
            <p v-if="data.user_rate_multiplier != null">
              {{ t('admin.accounts.upstreamBilling.userRate', { value: data.user_rate_multiplier }) }}
            </p>
            <p>
              {{
                data.peak_rate_enabled
                  ? t('admin.accounts.upstreamBilling.peakRate', {
                      start: data.peak_start,
                      end: data.peak_end,
                      value: data.peak_rate_multiplier,
                      timezone: data.timezone
                    })
                  : t('admin.accounts.upstreamBilling.noPeakRate')
              }}
            </p>
            <p v-if="convertedRate != null">{{ t('admin.accounts.upstreamBilling.newAPI.convertedDeclaration', { rate: currentEffectiveRate, value: convertedRate }) }}</p>
            <p v-else>{{ t('admin.accounts.upstreamBilling.effectiveRate', { value: currentEffectiveRate ?? '-' }) }}</p>
            <p v-if="isNewAPIGroup">{{ t('admin.accounts.upstreamBilling.newAPI.groupRatioHint') }}</p>
            <p>{{ t('admin.accounts.upstreamBilling.updatedAt', { value: formatDate(snapshot?.received_at) }) }}</p>
          </template>
          <template v-else-if="stale && lastDetectedRate != null">
            <p data-testid="upstream-billing-last-rate">
              {{ t('admin.accounts.upstreamBilling.lastDetectedRate', { value: lastDetectedRate }) }}
            </p>
            <p data-testid="upstream-billing-last-time">
              {{ t('admin.accounts.upstreamBilling.lastDetectedAt', { value: formatDate(snapshot?.received_at) }) }}
            </p>
            <p data-testid="upstream-billing-elapsed">
              {{ t('admin.accounts.upstreamBilling.elapsedSince', { value: elapsedSinceLastSuccess }) }}
            </p>
          </template>
          <p v-else>{{ statusLabel || '-' }}</p>
          <p
            v-if="probeEnabled && globalProbeEnabled !== false && nextProbeAt"
            data-testid="upstream-billing-next-probe"
          >
            {{ t('admin.accounts.upstreamBilling.nextProbeAt', { value: formatDate(nextProbeAt) }) }}
          </p>
          <p class="mt-2 border-t border-white/15 pt-2" data-testid="upstream-billing-probe-state">
            {{ t('admin.accounts.upstreamBilling.accountProbeState') }}
            <span :class="probeEnabled ? 'text-emerald-400' : 'text-red-400'">
              {{ probeEnabled ? t('admin.accounts.upstreamBilling.enabled') : t('admin.accounts.upstreamBilling.disabled') }}
            </span>
          </p>
          <p
            v-if="globalProbeEnabled === false"
            class="mt-1"
            data-testid="upstream-billing-global-probe-state"
          >
            {{ t('admin.accounts.upstreamBilling.globalProbeState') }}
            <span class="text-red-400">{{ t('admin.accounts.upstreamBilling.disabled') }}</span>
          </p>
        </div>
      </HelpTooltip>
      <span v-if="hasEffectiveRate && statusLabel" :class="statusClass" class="whitespace-nowrap text-[10px] font-medium">
        {{ statusLabel }}
      </span>
      <button
        type="button"
        class="inline-flex h-6 w-6 flex-shrink-0 items-center justify-center rounded text-blue-600 transition-colors hover:bg-blue-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-blue-400 dark:hover:bg-blue-900/30"
        :disabled="probing"
        :aria-label="t('admin.accounts.upstreamBilling.manualProbe')"
        :title="t('admin.accounts.upstreamBilling.manualProbe')"
        data-testid="upstream-billing-probe"
        @click="$emit('probe')"
      >
        <Icon name="refresh" size="xs" :class="{ 'animate-spin': probing }" />
      </button>
    </div>
    <details v-if="isMultiGroup" class="my-1 max-w-sm text-xs" data-testid="upstream-billing-groups">
      <summary class="cursor-pointer text-blue-600 dark:text-blue-400">{{ t('admin.accounts.upstreamBilling.multiGroupCount', { count: declaredGroups.length }) }}</summary>
      <p class="mt-1 text-gray-500">{{ t('admin.accounts.upstreamBilling.multiGroupSyncPaused') }}</p>
      <p v-if="stale" class="text-amber-600">{{ t('admin.accounts.upstreamBilling.lastDetectedAt', { value: formatDate(snapshot?.received_at) }) }}</p>
      <div v-for="group in declaredGroups" :key="group.group_id" class="mt-2 rounded bg-gray-50 p-2 dark:bg-dark-800">
        <p class="font-medium text-gray-800 dark:text-gray-200">{{ group.group_name || `#${group.group_id}` }}</p>
        <p>{{ t('admin.accounts.upstreamBilling.groupRate', { value: group.group_rate_multiplier }) }}</p>
        <p v-if="group.user_rate_multiplier != null">{{ t('admin.accounts.upstreamBilling.userRate', { value: group.user_rate_multiplier }) }}</p>
        <p>{{ t('admin.accounts.upstreamBilling.effectiveRate', { value: formatGroupRate(group) }) }}</p>
        <p v-if="group.peak_rate_enabled">{{ t('admin.accounts.upstreamBilling.peakRate', { start: group.peak_start, end: group.peak_end, value: group.peak_rate_multiplier, timezone: group.timezone }) }}</p>
        <p v-if="group.status !== 'active'" class="text-amber-600">{{ t('admin.accounts.upstreamBilling.groupInactive') }}</p>
      </div>
    </details>
    <div v-if="showBalance" class="flex h-5 items-center gap-1 text-xs" data-testid="upstream-balance">
      <HelpTooltip class="-ml-1" width-class="w-max max-w-[calc(100vw-2rem)]" data-testid="upstream-balance-details">
        <template #trigger>
          <span class="cursor-help whitespace-nowrap border-b border-dotted border-gray-300 dark:border-dark-600">
            <span class="text-gray-400 dark:text-gray-500">{{ t('admin.accounts.upstreamBilling.balance.label') }}</span>
            <span class="ml-1 font-medium" :class="balanceValueClass" data-testid="upstream-balance-value">
              {{ balancePrimary }}
            </span>
          </span>
        </template>
        <div class="space-y-1">
          <template v-if="balanceFresh && balanceData">
            <p v-if="balanceKind">{{ balanceKind }}</p>
            <p v-if="balanceAmount != null">
              {{ t('admin.accounts.upstreamBilling.balance.remaining', { value: balanceAmount }) }}
            </p>
            <p v-if="balanceQuota">{{ t('admin.accounts.upstreamBilling.balance.keyQuota', balanceQuota) }}</p>
            <p v-for="window in balanceWindows" :key="window.key" data-testid="upstream-balance-window">
              {{
                window.reset
                  ? t('admin.accounts.upstreamBilling.balance.windowWithReset', {
                      window: window.label,
                      used: window.used,
                      limit: window.limit,
                      reset: window.reset
                    })
                  : t('admin.accounts.upstreamBilling.balance.window', {
                      window: window.label,
                      used: window.used,
                      limit: window.limit
                    })
              }}
            </p>
            <p v-if="balanceData.expires_at">
              {{ t('admin.accounts.upstreamBilling.balance.expiresAt', { value: formatDate(balanceData.expires_at) }) }}
            </p>
            <p v-if="balanceKeyStatus" class="text-amber-300">
              {{ t('admin.accounts.upstreamBilling.balance.keyStatus', { value: balanceKeyStatus }) }}
            </p>
            <p v-if="balanceData.is_valid === false" class="text-red-400">
              {{ t('admin.accounts.upstreamBilling.balance.invalid') }}
            </p>
            <p>{{ t('admin.accounts.upstreamBilling.updatedAt', { value: formatDate(balance?.received_at) }) }}</p>
          </template>
          <template v-else-if="balanceStale && lastBalance != null">
            <p data-testid="upstream-balance-last-value">
              {{ t('admin.accounts.upstreamBilling.balance.lastBalance', { value: lastBalance }) }}
            </p>
            <p>{{ t('admin.accounts.upstreamBilling.lastDetectedAt', { value: formatDate(balance?.received_at) }) }}</p>
            <p>{{ t('admin.accounts.upstreamBilling.elapsedSince', { value: balanceElapsed }) }}</p>
          </template>
          <p v-else-if="balance?.status === 'unsupported'">
            {{ t('admin.accounts.upstreamBilling.balance.unsupported') }}
          </p>
          <p v-if="balance?.status === 'failed'" class="text-red-400" data-testid="upstream-balance-error">
            {{ t('admin.accounts.upstreamBilling.balance.lastError', { value: balanceFailure }) }}
          </p>
        </div>
      </HelpTooltip>
      <span
        v-if="balanceFresh && balance?.status === 'failed'"
        class="whitespace-nowrap text-[10px] font-medium text-red-600 dark:text-red-400"
      >
        {{ t('admin.accounts.upstreamBilling.failed') }}
      </span>
    </div>
  </div>
  <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'
import { getPlatformSpec } from '@/constants/platformCatalog'
import { formatMultiplier } from '@/utils/formatters'
import type { UpstreamBillingData, Account, UpstreamBalanceData, UpstreamBalanceSnapshot, UpstreamBillingProbeSnapshot } from '@/types'

const props = withDefaults(defineProps<{
  account: Account
  now: number
  probing?: boolean
  globalProbeEnabled?: boolean
  canConfigure?: boolean
}>(), {
  globalProbeEnabled: true,
  canConfigure: true
})

defineEmits<{
  (event: 'probe'): void
  (event: 'configure'): void
}>()

const { t } = useI18n()
const CLOCK_SKEW_TOLERANCE_MS = 5 * 60 * 1000
// 探测资格已放宽到全部 API-key 平台（上游是 sub2api 即可应答）。
const eligible = computed(() => props.account.type === 'apikey' || props.account.type === 'gateway')
const newAPIGateway = computed(() => props.account.type === 'gateway' && props.account.credentials?.upstream_kind === 'new_api')
const snapshot = computed<UpstreamBillingProbeSnapshot | undefined>(() => props.account.extra?.upstream_billing_probe)
const data = computed(() => snapshot.value?.data)
const isMultiGroup = computed(() => data.value?.schema_version === 2 && data.value.billing_mode === 'multi_group' && Array.isArray(data.value.groups))
const declaredGroups = computed(() => isMultiGroup.value ? data.value?.groups ?? [] : [])
const isNewAPIGroup = computed(() => data.value?.provider === 'new_api' && data.value.object === 'new_api.group_billing' && data.value.billing_scope === 'group')
const probeEnabled = computed(() => props.account.extra?.upstream_billing_probe_enabled === true)
const nextProbeAt = computed(() => {
  const value = snapshot.value?.next_probe_at
  return typeof value === 'string' && Number.isFinite(Date.parse(value)) ? value : ''
})
const receivedAt = computed(() => typeof snapshot.value?.received_at === 'string' ? Date.parse(snapshot.value.received_at) : Number.NaN)
const freshUntil = computed(() => {
  if (typeof snapshot.value?.fresh_until === 'string') return Date.parse(snapshot.value.fresh_until)
  if (snapshot.value?.status !== 'ok' || typeof snapshot.value.next_probe_at !== 'string') return Number.NaN
  const nextProbeAt = Date.parse(snapshot.value.next_probe_at)
  return Number.isFinite(nextProbeAt) && nextProbeAt > receivedAt.value
    ? receivedAt.value + 2 * (nextProbeAt - receivedAt.value)
    : Number.NaN
})
const validTimestamps = computed(() => {
  if (!Number.isFinite(receivedAt.value) || receivedAt.value > props.now + CLOCK_SKEW_TOLERANCE_MS) return false
  return Number.isFinite(freshUntil.value) && freshUntil.value > receivedAt.value
})
const stale = computed(() => {
  if (!snapshot.value) return false
  if (!Number.isFinite(receivedAt.value)) return snapshot.value.status === 'ok'
  if (!validTimestamps.value) return true
  return props.now > freshUntil.value
})
const parseMinute = (value?: string) => {
  if (typeof value !== 'string') return null
  const match = /^(\d{2}):(\d{2})$/.exec(value)
  if (!match) return null
  const hour = Number(match[1])
  const minute = Number(match[2])
  return hour < 24 && minute < 60 ? hour * 60 + minute : null
}
const minuteInTimeZone = (timestamp: number, timeZone?: string) => {
  if (!timeZone) return null
  try {
    const parts = new Intl.DateTimeFormat('en-GB', {
      timeZone,
      hour: '2-digit',
      minute: '2-digit',
      hourCycle: 'h23'
    }).formatToParts(new Date(timestamp))
    const hour = Number(parts.find(part => part.type === 'hour')?.value)
    const minute = Number(parts.find(part => part.type === 'minute')?.value)
    return Number.isInteger(hour) && Number.isInteger(minute) ? hour * 60 + minute : null
  } catch {
    return null
  }
}
const effectiveMultiplier = (billing?: UpstreamBillingData) => {
  if (!billing) return null
  if (billing.billing_scope !== 'token' && billing.provider !== 'new_api') return null
  const base = billing.resolved_rate_multiplier
  if (typeof base !== 'number' || !Number.isFinite(base) || base < 0) return null
  if (typeof billing.peak_rate_enabled !== 'boolean') return null
  if (!billing.peak_rate_enabled) return base
  const start = parseMinute(billing.peak_start)
  const end = parseMinute(billing.peak_end)
  const minute = minuteInTimeZone(props.now, billing.timezone)
  const peak = billing.peak_rate_multiplier
  if (start == null || end == null || minute == null || start >= end || typeof peak !== 'number' || !Number.isFinite(peak) || peak < 0) return null
  const value = minute >= start && minute < end ? base * peak : base
  return Number.isFinite(value) ? value : null
}
const currentEffectiveRate = computed(() => effectiveMultiplier(data.value))
const convertedRate = computed(() => {
  const rate = currentEffectiveRate.value
  if (rate == null || !isNewAPIGroup.value || props.account.extra?.new_api_apply_usd_rate === false) return null
  if (getPlatformSpec(props.account.platform)?.cn_provider !== false) return null
  return Number((rate * 7).toPrecision(15))
})
const formatGroupRate = (group: UpstreamBillingData): string => {
  const rate = effectiveMultiplier(group)
  return rate == null ? '-' : formatMultiplier(rate)
}
const multiGroupRange = computed(() => {
  const rates = declaredGroups.value.map(group => effectiveMultiplier(group)).filter((rate): rate is number => rate != null)
  if (rates.length === 0) return '-'
  const min = Math.min(...rates), max = Math.max(...rates)
  return min === max ? `${formatMultiplier(min)}x` : `${formatMultiplier(min)}–${formatMultiplier(max)}x`
})
const lastDetectedRate = computed(() => {
  const value = data.value?.effective_rate_multiplier
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
    ? Number(value.toPrecision(12))
    : null
})
const formatElapsedSince = (timestamp: number) => {
  if (!Number.isFinite(timestamp)) return '-'
  const elapsedMinutes = Math.max(0, Math.floor((props.now - timestamp) / 60_000))
  if (elapsedMinutes < 1) return t('admin.accounts.upstreamBilling.justNow')
  if (elapsedMinutes < 60) return t('admin.accounts.upstreamBilling.minutesAgo', { count: elapsedMinutes })
  const elapsedHours = Math.floor(elapsedMinutes / 60)
  if (elapsedHours < 24) return t('admin.accounts.upstreamBilling.hoursAgo', { count: elapsedHours })
  return t('admin.accounts.upstreamBilling.daysAgo', { count: Math.floor(elapsedHours / 24) })
}
const elapsedSinceLastSuccess = computed(() => formatElapsedSince(receivedAt.value))
const effectiveRate = computed(() => {
  if (!validTimestamps.value || stale.value || !['ok', 'failed'].includes(snapshot.value?.status ?? '')) return '-'
  if (isMultiGroup.value) return multiGroupRange.value
  const converted = convertedRate.value
  if (converted != null) return `${converted >= 0.1 ? converted.toFixed(2) : formatMultiplier(converted)}x`
  const value = currentEffectiveRate.value
  return value == null ? '-' : `${formatMultiplier(value)}x`
})
const statusLabel = computed(() => {
  if (!snapshot.value) return t('admin.accounts.upstreamBilling.notProbed')
  if (snapshot.value.status === 'unsupported') return t('admin.accounts.upstreamBilling.unsupported')
  if (stale.value) return t('admin.accounts.upstreamBilling.stale')
  if (snapshot.value.status === 'failed') return t('admin.accounts.upstreamBilling.failed')
  return ''
})
const statusClass = computed(() => {
  if (!snapshot.value) return 'text-gray-400 dark:text-gray-500'
  if (snapshot.value.status === 'unsupported') return 'text-gray-500 dark:text-gray-400'
  if (stale.value) return 'text-amber-600 dark:text-amber-400'
  if (snapshot.value.status === 'failed') return 'text-red-600 dark:text-red-400'
  return ''
})
const hasEffectiveRate = computed(() => effectiveRate.value !== '-')
const primaryValue = computed(() => hasEffectiveRate.value ? effectiveRate.value : statusLabel.value || '-')
const formatDate = (value?: string) => value
  ? new Date(value).toLocaleString(undefined, {
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit'
    })
  : '-'

// 上游余额：同一次探测读取上游 /v1/usage，快照独立记录状态与有效期。
const BALANCE_WINDOW_KEYS: Record<string, string> = {
  daily: 'daily',
  weekly: 'weekly',
  monthly: 'monthly',
  '5h': 'hours5',
  '1d': 'days1',
  '7d': 'days7'
}
const BALANCE_KEY_STATUS_KEYS: Record<string, string> = {
  quota_exhausted: 'quotaExhausted',
  expired: 'expired'
}
const balance = computed<UpstreamBalanceSnapshot | undefined>(() => snapshot.value?.balance)
const balanceData = computed(() => balance.value?.data)
const balanceReceivedAt = computed(() => typeof balance.value?.received_at === 'string' ? Date.parse(balance.value.received_at) : Number.NaN)
const balanceStale = computed(() => {
  if (!balanceData.value) return false
  const received = balanceReceivedAt.value
  const freshUntilValue = typeof balance.value?.fresh_until === 'string' ? Date.parse(balance.value.fresh_until) : Number.NaN
  if (!Number.isFinite(received) || received > props.now + CLOCK_SKEW_TOLERANCE_MS) return true
  if (!Number.isFinite(freshUntilValue) || freshUntilValue <= received) return true
  return props.now > freshUntilValue
})
const balanceFresh = computed(() =>
  !!balanceData.value && !balanceStale.value && ['ok', 'failed'].includes(balance.value?.status ?? '')
)
// An upstream without /v1/usage only gets a line when its rate endpoint
// answers; when both are missing the rate line already says unsupported.
const showBalance = computed(() => {
  const status = balance.value?.status
  if (!status) return false
  return status !== 'unsupported' || snapshot.value?.status === 'ok'
})
// Same "$12.34" style as the other cost cells in the account table.
const formatBalanceAmount = (value: number, unit?: string) => {
  const amount = Math.abs(value).toFixed(2)
  const sign = value < 0 && amount !== '0.00' ? '-' : ''
  const currency = unit || 'USD'
  return currency.toUpperCase() === 'USD' ? `${sign}$${amount}` : `${sign}${amount} ${currency}`
}
const describeBalance = (value?: UpstreamBalanceData) => {
  if (!value) return null
  if (value.unlimited === true) return t('admin.accounts.upstreamBilling.balance.unlimited')
  return typeof value.remaining === 'number' && Number.isFinite(value.remaining)
    ? formatBalanceAmount(value.remaining, value.unit)
    : null
}
const balanceAmount = computed(() => balanceFresh.value ? describeBalance(balanceData.value) : null)
const lastBalance = computed(() => describeBalance(balanceData.value))
const balanceElapsed = computed(() => formatElapsedSince(balanceReceivedAt.value))
const balancePrimary = computed(() => {
  if (balanceAmount.value != null) return balanceAmount.value
  if (balanceFresh.value) return '-'
  if (balance.value?.status === 'unsupported') return t('admin.accounts.upstreamBilling.unsupported')
  if (balanceStale.value) return t('admin.accounts.upstreamBilling.stale')
  if (balance.value?.status === 'failed') return t('admin.accounts.upstreamBilling.failed')
  return '-'
})
const balanceValueClass = computed(() => {
  if (balanceAmount.value == null) {
    if (balanceFresh.value) return 'text-gray-400 dark:text-gray-500'
    if (balance.value?.status === 'unsupported') return 'text-gray-500 dark:text-gray-400'
    if (balanceStale.value) return 'text-amber-600 dark:text-amber-400'
    if (balance.value?.status === 'failed') return 'text-red-600 dark:text-red-400'
    return 'text-gray-400 dark:text-gray-500'
  }
  const value = balanceData.value
  if (value?.unlimited === true) return 'text-gray-700 dark:text-gray-300'
  const depleted = value?.is_valid === false || (typeof value?.remaining === 'number' && value.remaining <= 0)
  return depleted ? 'font-mono text-red-600 dark:text-red-400' : 'font-mono text-gray-700 dark:text-gray-300'
})
const balanceKind = computed(() => {
  const value = balanceData.value
  if (!value) return ''
  if (value.mode === 'quota_limited') return t('admin.accounts.upstreamBilling.balance.kindKeyQuota')
  if (typeof value.wallet_balance === 'number') return t('admin.accounts.upstreamBilling.balance.kindWallet')
  if (value.plan_name) return t('admin.accounts.upstreamBilling.balance.kindPlan', { name: value.plan_name })
  return ''
})
const isFiniteNumber = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value)
const balanceQuota = computed(() => {
  const value = balanceData.value
  if (!value || !isFiniteNumber(value.quota_limit)) return null
  return {
    used: formatBalanceAmount(isFiniteNumber(value.quota_used) ? value.quota_used : 0, value.unit),
    limit: formatBalanceAmount(value.quota_limit, value.unit)
  }
})
const balanceWindows = computed(() => {
  const value = balanceData.value
  if (!value || !Array.isArray(value.windows)) return []
  return value.windows.flatMap((item, index) => {
    if (!item || typeof item.window !== 'string' || !isFiniteNumber(item.limit)) return []
    const labelKey = BALANCE_WINDOW_KEYS[item.window]
    return [{
      key: `${item.window}-${index}`,
      label: labelKey ? t(`admin.accounts.upstreamBilling.balance.windows.${labelKey}`) : item.window,
      used: formatBalanceAmount(isFiniteNumber(item.used) ? item.used : 0, value.unit),
      limit: formatBalanceAmount(item.limit, value.unit),
      reset: typeof item.reset_at === 'string' ? formatDate(item.reset_at) : ''
    }]
  })
})
const balanceKeyStatus = computed(() => {
  const status = balanceData.value?.key_status
  if (!status || status === 'active') return ''
  const key = BALANCE_KEY_STATUS_KEYS[status]
  return key ? t(`admin.accounts.upstreamBilling.balance.keyStatuses.${key}`) : status
})
const balanceFailure = computed(() => {
  const reason = balance.value?.last_error || '-'
  const status = balance.value?.http_status
  return status ? `${reason} (HTTP ${status})` : reason
})
</script>
