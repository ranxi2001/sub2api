<template>
  <BaseDialog :show="true" :title="t('accountPerformance.title') + ' · ' + accountName" width="wide" @close="emit('close')">
    <div class="space-y-4">
      <p v-if="loading">{{ t('accountPerformance.loading') }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <template v-if="metrics">
        <AccountPerformanceCell :metrics="metrics" :can-configure="false" />
        <p class="text-xs text-gray-500">{{ t('accountPerformance.samples',{tps:metrics.tps_samples,ttft:metrics.ttft_samples,quality:metrics.quality_samples}) }}</p>
        <p v-if="metrics.paused" class="text-sm text-amber-600">{{ t('accountPerformance.reason',{reason:metrics.reason}) }}</p>
        <label class="flex items-center gap-2 text-sm"><input v-model="config.enabled" type="checkbox" data-testid="monitor-enabled">{{ t('accountPerformance.enabled') }}</label>
        <table class="w-full text-left text-sm">
          <thead><tr><th></th><th>{{ t('accountPerformance.min') }}</th><th>{{ t('accountPerformance.max') }}</th></tr></thead>
          <tbody><tr v-for="metric in metricKeys" :key="metric">
            <th class="py-2 pr-2">{{ t('accountPerformance.' + metric, { minutes: windowMinutes }) }} {{ metric==='ttft' ? '(' + t('accountPerformance.ms') + ')' : metric==='degradation'?'(%)':'' }}</th>
            <td v-for="bound in bounds" :key="bound" class="p-1"><input v-model="fields[metric][bound]" type="number" min="0" :max="metric==='degradation'?100:1000000000" step="any" :aria-label="t('accountPerformance.'+metric, { minutes: windowMinutes })+' '+t('accountPerformance.'+bound)" :placeholder="t('accountPerformance.noBound')" class="input w-full min-w-0" :data-testid="metric+'-'+bound"></td>
          </tr></tbody>
        </table>
        <p class="text-xs text-gray-500">{{ t('accountPerformance.semantics', { minutes: windowMinutes }) }}</p>
        <p class="text-xs text-gray-500">{{ t('accountPerformance.qualityNote') }}</p>
        <p class="rounded-lg bg-amber-50 p-3 text-xs text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">{{ t('accountPerformance.latchNote') }}</p>
        <h4 class="text-sm font-semibold">{{ t('accountPerformance.audit') }}</h4>
        <p v-if="auditError" class="text-xs text-red-600">{{ t('accountPerformance.auditUnavailable') }}</p>
        <p v-else-if="!events.length" class="text-xs text-gray-500">{{ t('accountPerformance.noEvents') }}</p>
        <ul v-else class="max-h-32 overflow-auto text-xs text-gray-500"><li v-for="event in events" :key="event.id">{{ event.created_at }} · {{ t('accountPerformance.'+event.action+'Event') }} · r{{ event.revision }} / #{{ event.pause_generation }}</li></ul>
      </template>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="busy||loading" @click="reload">{{ t('accountPerformance.refresh') }}</button>
      <button v-if="metrics?.paused" type="button" class="btn btn-secondary" :disabled="busy||loading" data-testid="performance-release" @click="release">{{ t('accountPerformance.release') }}</button>
      <button type="button" class="btn btn-primary" :disabled="busy||loading||!metrics" data-testid="performance-save" @click="save">{{ t('accountPerformance.save') }}</button>
    </template>
  </BaseDialog>
</template>
<script setup lang="ts">
import { computed, onMounted, ref, reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { emptyPerformanceConfig, performanceWindowMinutes, type AccountPerformance, type PerformanceConfig, type PerformanceEvent } from '@/types/accountPerformance'
import BaseDialog from '@/components/common/BaseDialog.vue'
import AccountPerformanceCell from './AccountPerformanceCell.vue'
const props=defineProps<{accountId:number;accountName:string}>()
const emit=defineEmits<{close:[];updated:[]}>()
const {t}=useI18n()
const metrics=ref<AccountPerformance|null>(null),events=ref<PerformanceEvent[]>([])
const windowMinutes = computed(() => performanceWindowMinutes(metrics.value ?? undefined))
const config=ref<PerformanceConfig>(emptyPerformanceConfig())
const loading=ref(false),busy=ref(false),error=ref(''),auditError=ref(false)
const metricKeys=['tps','degradation','ttft'] as const,bounds=['min','max'] as const
const fields=reactive<Record<typeof metricKeys[number],Record<typeof bounds[number],string|number>>>({tps:{min:'',max:''},degradation:{min:'',max:''},ttft:{min:'',max:''}})
async function reload(){
 loading.value=true;error.value=''
 try{
  const data=await adminAPI.accounts.getPerformance([props.accountId]);const m=data[props.accountId]
  if(!m)throw new Error('missing account metrics')
  metrics.value=m;config.value={...emptyPerformanceConfig(),...m.config}
  for(const key of metricKeys)for(const bound of bounds)fields[key][bound]=m.config[key]?.[bound]??''
  auditError.value=false
  try{events.value=await adminAPI.accounts.getPerformanceEvents(props.accountId)}catch{auditError.value=true}
 }catch{metrics.value=null;error.value=t('accountPerformance.unavailable')}finally{loading.value=false}
}
async function save(){
 const next=emptyPerformanceConfig();next.enabled=config.value.enabled
 for(const key of metricKeys){
  for(const bound of bounds){const raw=fields[key][bound];next[key][bound]=raw===''?null:Number(raw)}
  const {min,max}=next[key]
  if([min,max].some(n=>n!==null&&(!Number.isFinite(n)||n<0||n>(key==='degradation'?100:1e9)))||(min!==null&&max!==null&&min>max)){error.value=t('accountPerformance.invalid');return}
 }
 if(!metrics.value)return
 busy.value=true;error.value=''
 try{await adminAPI.accounts.savePerformance(props.accountId,next,metrics.value.revision);emit('updated');await reload()}catch{error.value=t('accountPerformance.failed')}finally{busy.value=false}
}
async function release(){
 if(!metrics.value)return
 busy.value=true;error.value=''
 try{await adminAPI.accounts.releasePerformance(props.accountId,metrics.value.revision,metrics.value.pause_generation);emit('updated');await reload()}catch{error.value=t('accountPerformance.failed')}finally{busy.value=false}
}
onMounted(reload)
</script>
