<template>
  <AppLayout>
    <div class="w-full min-w-0 pb-10">
      <p v-if="error" class="mb-4 rounded-xl border border-red-200 bg-red-50 px-4 py-2.5 text-sm text-red-700 dark:border-red-900/40 dark:bg-red-900/20 dark:text-red-200" role="alert">
        {{ error }}
      </p>
      <StatusPage
        :status="status"
        :loading="loading"
        @refresh="load(end)"
        @navigate="navigate"
        @open-incidents="showIncidents = true"
      />
    </div>
    <IncidentsDialog :show="showIncidents" :admin="false" @close="showIncidents = false" />
  </AppLayout>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import StatusPage from '@/features/channel-monitor-v3/StatusPage.vue'
import IncidentsDialog from '@/features/channel-monitor-v3/IncidentsDialog.vue'
import { getStatus, type MonitorV3StatusPage } from '@/api/channelMonitorV3'
import { extractApiErrorMessage } from '@/utils/apiError'

const REFRESH_MS = 60_000
const { t } = useI18n()
const status = ref<MonitorV3StatusPage | null>(null)
const loading = ref(false)
const error = ref('')
const end = ref<number | null>(null)
const showIncidents = ref(false)
let controller: AbortController | undefined
let timer: ReturnType<typeof setInterval> | undefined

async function load(nextEnd: number | null) {
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  try {
    const result = await getStatus(nextEnd, false, current.signal)
    if (current.signal.aborted) return
    status.value = result
    end.value = result.window.latest ? null : nextEnd
    error.value = ''
  } catch (err: unknown) {
    if (current.signal.aborted) return
    error.value = extractApiErrorMessage(err, t('channelMonitorV3.page.loadFailed'))
  } finally {
    if (controller === current) loading.value = false
  }
}

function navigate(nextEnd: number | null) {
  void load(nextEnd)
}

// Only the live window refreshes itself; an older window is history.
function tick() {
  if (document.visibilityState !== 'visible' || loading.value || end.value !== null) return
  void load(null)
}

onMounted(() => {
  void load(null)
  timer = setInterval(tick, REFRESH_MS)
})
onBeforeUnmount(() => {
  controller?.abort()
  if (timer) clearInterval(timer)
})
</script>
