<template>
  <div class="min-w-[150px] text-xs" :title="t('accountPerformance.semantics')">
    <div v-for="line in lines" :key="line.key" :data-metric="line.key" class="flex justify-between gap-3 py-0.5">
      <span class="text-gray-500 dark:text-gray-400">{{ line.label }}</span>
      <span class="font-mono tabular-nums">{{ unavailable ? t('accountPerformance.unavailable') : line.value }}</span>
    </div>
    <button v-if="canConfigure" type="button" class="mt-1 text-primary-600 hover:underline dark:text-primary-400" @click="emit('configure')">
      {{ metrics?.paused ? t('accountPerformance.paused') : t('accountPerformance.configure') }}
    </button>
    <span v-else-if="metrics?.paused" class="text-amber-600">{{ t('accountPerformance.paused') }}</span>
  </div>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountPerformance } from '@/types/accountPerformance'
const props = withDefaults(defineProps<{ metrics?: Partial<AccountPerformance>; unavailable?: boolean; canConfigure?: boolean }>(), {canConfigure:true})
const emit = defineEmits<{configure: []}>()
const {t}=useI18n()
const format=(n: number | null | undefined,unit:string,divisor=1) => n == null || !Number.isFinite(n) ? t('accountPerformance.uncollected') : (n/divisor).toFixed(divisor===1000?2:1)+unit
const lines=computed(()=>[
 {key:'tps',label:t('accountPerformance.tps'),value:format(props.metrics?.avg_tps,' t/s')},
 {key:'degradation',label:t('accountPerformance.degradation'),value:format(props.metrics?.degradation_percent,'%')},
 {key:'ttft',label:t('accountPerformance.ttft'),value:format(props.metrics?.avg_ttft_ms,'s',1000)}
])
</script>
