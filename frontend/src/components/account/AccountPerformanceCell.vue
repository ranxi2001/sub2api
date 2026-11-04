<template>
  <div class="min-w-[150px] text-xs" :title="t('accountPerformance.semantics', { minutes: windowMinutes })">
    <div v-for="line in lines" :key="line.key" :data-metric="line.key" class="flex justify-between gap-3 py-0.5">
      <span class="inline-flex items-center text-gray-500 dark:text-gray-400">
        {{ line.label }}
        <template v-if="line.key === 'upstream'">
          <NewFeatureBadge v-if="noticeUserId != null" :notice-id="NEW_FEATURE_NOTICES.accountUpstreamHTTPErrorRate" :user-id="noticeUserId" compact />
          <HelpTooltip :content="upstreamTooltip" trigger="click" width-class="w-80">
            <template #trigger><button type="button" data-testid="upstream-error-help" class="rounded px-1 text-gray-400" :aria-label="t('accountPerformance.upstreamErrorRate')">ⓘ</button></template>
          </HelpTooltip>
        </template>
      </span>
      <span class="font-mono tabular-nums">{{ unavailable ? t('accountPerformance.unavailable') : line.value }}</span>
    </div>
    <button v-if="canConfigure" type="button" data-testid="performance-configure" class="mt-1 text-primary-600 hover:underline dark:text-primary-400" @click="emit('configure')">
      {{ metrics?.paused ? t('accountPerformance.paused') : t('accountPerformance.configure') }}
    </button>
    <span v-else-if="metrics?.paused" class="text-amber-600">{{ t('accountPerformance.paused') }}</span>
  </div>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { performanceWindowMinutes, type AccountPerformance } from '@/types/accountPerformance'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
const props = withDefaults(defineProps<{ metrics?: Partial<AccountPerformance>; unavailable?: boolean; canConfigure?: boolean; noticeUserId?: number | null }>(), {canConfigure:true})
const emit = defineEmits<{configure: []}>()
const {t}=useI18n()
const windowMinutes = computed(() => performanceWindowMinutes(props.metrics))
const format=(n: number | null | undefined,unit:string,divisor=1) => n == null || !Number.isFinite(n) ? t('accountPerformance.uncollected') : (n/divisor).toFixed(divisor===1000?2:1)+unit
const upstream = computed(() => props.metrics?.upstream_error)
const upstreamValue = computed(() => {
 const e = upstream.value
 if (!e || e.source !== 'process_local_http_responses' || !Number.isSafeInteger(e.total) || e.total <= 0 || !Number.isSafeInteger(e.failures) || e.failures < 0 || e.failures > e.total || e.percent == null || !Number.isFinite(e.percent)) return t('accountPerformance.uncollected')
 return format(100 * e.failures / e.total, '%') + ' · ' + t('accountPerformance.localSample')
})
const upstreamTooltip = computed(() => {
 const e = upstream.value
 return t('accountPerformance.upstreamHTTPNote', { minutes: windowMinutes.value, failures: e?.failures ?? 0, total: e?.total ?? 0, since: e?.observed_since ?? '—', validity: t(e?.window_complete ? 'accountPerformance.localWindowComplete' : 'accountPerformance.partialHistory') })
})
const lines=computed(()=>[
 {key:'tps',label:t('accountPerformance.tps'),value:format(props.metrics?.avg_tps,' t/s')},
 {key:'degradation',label:t('accountPerformance.degradation', { minutes: windowMinutes.value }),value:format(props.metrics?.degradation_percent,'%')},
 {key:'ttft',label:t('accountPerformance.ttft'),value:format(props.metrics?.avg_ttft_ms,'s',1000)},
 {key:'upstream',label:t('accountPerformance.upstreamErrorRate'),value:upstreamValue.value}
])
</script>
