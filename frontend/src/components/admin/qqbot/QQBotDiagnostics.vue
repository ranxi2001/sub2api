<template>
  <section class="card space-y-4 p-5" :aria-label="t('qqbot.simple.diagnostics')">
    <div class="flex flex-wrap items-center justify-between gap-3"><h2 class="text-lg font-semibold">{{ t('qqbot.simple.diagnostics') }}</h2><button type="button" class="btn btn-secondary" :disabled="busy" @click="load">{{ t('common.refresh') }}</button></div>
    <p class="text-sm text-gray-500">{{ t('qqbot.simple.checksHelp') }}</p>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <p v-if="running" role="status" class="text-sm text-primary-600">{{ t('qqbot.simple.probing', { name: probeLabel(running) }) }}</p>
    <ul v-if="readiness" class="divide-y dark:divide-dark-700">
      <li v-for="check in readiness.checks" :key="check.code" class="flex flex-wrap items-start gap-3 py-3 text-sm">
        <span class="rounded px-2 py-1 text-xs" :class="check.status === 'ready' ? 'bg-green-50 text-green-800' : check.status === 'blocked' ? 'bg-red-50 text-red-800' : 'bg-amber-50 text-amber-800'">{{ t('qqbot.simple.status.' + check.status) }}</span>
        <span class="min-w-0 flex-1 break-words">{{ check.message }}</span>
        <button v-if="check.code.startsWith('probe_')" type="button" class="text-primary-600 underline" :disabled="busy" @click="run([check.code.slice(6)])">{{ t('qqbot.simple.testAgain') }}</button>
        <RouterLink v-else :to="check.action" class="text-primary-600 underline">{{ t('qqbot.simple.inspect') }}</RouterLink>
      </li>
    </ul>
    <button type="button" class="btn btn-primary" :disabled="busy || !pending.length" @click="run(pending)">{{ t('qqbot.simple.runChecks') }}</button>
    <p class="text-xs text-gray-500">{{ t('qqbot.simple.testCost') }}</p>
  </section>
</template>
<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { qqbotAPI, type QQSetupReadiness } from '@/api/qqbot'
const emit = defineEmits<{ ready: [value: boolean]; busy: [value: boolean] }>()
const { t } = useI18n()
const readiness = ref<QQSetupReadiness>(), busy = ref(false), running = ref(''), error = ref('')
const pending = computed(() => readiness.value?.checks.filter(c => c.code.startsWith('probe_') && c.status !== 'ready').map(c => c.code.slice(6)) || [])
let alive = true
function probeLabel(kind: string) { return t('qqbot.simple.probes.' + kind) }
async function refresh() { const result = await qqbotAPI.setupChecks(); if (alive) { readiness.value = result; emit('ready', result.ready) } }
async function load() { if (busy.value) return; busy.value = true; emit('busy', true); emit('ready', false); error.value = ''; try { await refresh() } catch (e) { if (alive) error.value = (e as Error).message } finally { if (alive) { busy.value = false; emit('busy', false) } } }
async function run(kinds: string[]) {
  if (busy.value || !kinds.length || !window.confirm(t('qqbot.simple.probeConfirm', { names: kinds.map(probeLabel).join('、') }))) return
  busy.value = true; emit('busy', true); emit('ready', false); error.value = ''
  try { for (const kind of [...kinds]) { if (!alive) break; running.value = kind; const result = await qqbotAPI.setupProbe(kind); await refresh(); if (result.status !== 'ready') break } }
  catch (e) { if (alive) error.value = (e as Error).message }
  finally { if (alive) { running.value = ''; busy.value = false; emit('busy', false) } }
}
onMounted(load)
onBeforeUnmount(() => { alive = false })
defineExpose({ reload: load })
</script>
