<template>
  <div class="min-w-[150px] text-xs" :title="t('accountPerformance.semantics', { minutes: windowMinutes })">
    <div v-for="line in lines" :key="line.key" :data-metric="line.key" class="flex justify-between gap-3 py-0.5">
      <span class="inline-flex items-center text-gray-500 dark:text-gray-400">
        {{ line.label }}
        <template v-if="line.key === 'cache'">
          <NewFeatureBadge v-if="noticeUserId != null" :notice-id="NEW_FEATURE_NOTICES.accountAverageCache" :user-id="noticeUserId" compact />
          <HelpTooltip :content="cacheTooltip" trigger="click" width-class="w-80">
            <template #trigger><button type="button" data-testid="cache-help" class="rounded px-1 text-gray-400" :aria-label="t('accountPerformance.avgCache')">ⓘ</button></template>
          </HelpTooltip>
        </template>
        <template v-if="line.key === 'upstream'">
          <NewFeatureBadge v-if="noticeUserId != null" :notice-id="NEW_FEATURE_NOTICES.accountUpstreamHTTPErrorRate" :user-id="noticeUserId" compact />
          <HelpTooltip :content="upstreamTooltip" trigger="click" width-class="w-80">
            <template #trigger><button type="button" data-testid="upstream-error-help" class="rounded px-1 text-gray-400" :aria-label="t('accountPerformance.upstreamErrorRate')">ⓘ</button></template>
          </HelpTooltip>
        </template>
      </span>
      <span class="font-mono tabular-nums" :class="unavailable ? uncollectedClass : line.color">{{ unavailable ? t('accountPerformance.unavailable') : line.value }}</span>
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
import { firstTokenSeverity, tpsSeverity, cacheRateSeverity, LATENCY_TEXT_CLASSES, type LatencySeverity } from '@/utils/latencyHealth'
import { upstreamErrorRateColorClass } from '@/utils/opsMetricThresholds'
const props = withDefaults(defineProps<{ metrics?: Partial<AccountPerformance>; unavailable?: boolean; canConfigure?: boolean; noticeUserId?: number | null; upstreamErrorThreshold?: number | null }>(), {canConfigure:true})
const emit = defineEmits<{configure: []}>()
const {t}=useI18n()
const windowMinutes = computed(() => performanceWindowMinutes(props.metrics))
const uncollectedClass = 'text-gray-500 dark:text-gray-400'
const metricColor = (value: number | null | undefined, classify: (value: number) => LatencySeverity) =>
 value == null || !Number.isFinite(value) || value < 0 ? uncollectedClass : LATENCY_TEXT_CLASSES[classify(value)]
// A collected probe result is either healthy (0%) or contains degradation (>0%),
// matching the quality monitor's healthy/degraded colors.
const degradationColor = (value: number | null | undefined) =>
 value != null && value > 100 ? uncollectedClass : metricColor(value, n => n > 0 ? 'critical' : 'good')
const format=(n: number | null | undefined,unit:string,divisor=1) => n == null || !Number.isFinite(n) ? t('accountPerformance.uncollected') : (n/divisor).toFixed(divisor===1000?2:1)+unit
const upstream = computed(() => props.metrics?.upstream_error)
const upstreamPercent = computed(() => {
 const e = upstream.value
 if (!e || e.source !== 'process_local_http_responses' || !Number.isSafeInteger(e.total) || e.total <= 0 || !Number.isSafeInteger(e.failures) || e.failures < 0 || e.failures > e.total || e.percent == null || !Number.isFinite(e.percent)) return null
 return 100 * e.failures / e.total
})
const upstreamValue = computed(() => upstreamPercent.value == null
 ? t('accountPerformance.uncollected')
 : format(upstreamPercent.value, '%') + ' · ' + t('accountPerformance.localSample'))
const upstreamColor = computed(() => {
 if (upstreamPercent.value == null || props.upstreamErrorThreshold === undefined) return uncollectedClass
 return upstreamErrorRateColorClass(upstreamPercent.value, props.upstreamErrorThreshold)
})
const upstreamTooltip = computed(() => {
 const e = upstream.value
 return t('accountPerformance.upstreamHTTPNote', { minutes: windowMinutes.value, failures: e?.failures ?? 0, total: e?.total ?? 0, since: e?.observed_since ?? '—', validity: t(e?.window_complete ? 'accountPerformance.localWindowComplete' : 'accountPerformance.partialHistory') })
})
const cachePercent = computed(() => {
 const value = props.metrics?.avg_cache_percent
 const samples = props.metrics?.cache_samples
 return value != null && Number.isFinite(value) && value >= 0 && value <= 100 && Number.isSafeInteger(samples) && (samples ?? 0) > 0 ? value : null
})
const cacheTooltip = computed(() => t('accountPerformance.cacheNote', { minutes: windowMinutes.value, samples: props.metrics?.cache_samples ?? 0 }))
const lines=computed(()=>[
 {key:'tps',label:t('accountPerformance.tps'),value:format(props.metrics?.avg_tps,' t/s'),color:metricColor(props.metrics?.avg_tps,tpsSeverity)},
 {key:'degradation',label:t('accountPerformance.degradation', { minutes: windowMinutes.value }),value:format(props.metrics?.degradation_percent,'%'),color:degradationColor(props.metrics?.degradation_percent)},
 {key:'ttft',label:t('accountPerformance.ttft'),value:format(props.metrics?.avg_ttft_ms,'s',1000),color:metricColor(props.metrics?.avg_ttft_ms,firstTokenSeverity)},
 {key:'cache',label:t('accountPerformance.avgCache'),value:format(cachePercent.value,'%'),color:metricColor(cachePercent.value,cacheRateSeverity)},
 {key:'upstream',label:t('accountPerformance.upstreamErrorRate'),value:upstreamValue.value,color:upstreamColor.value}
])
</script>
