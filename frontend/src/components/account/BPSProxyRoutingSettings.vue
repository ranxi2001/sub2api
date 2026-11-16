<template>
  <fieldset class="space-y-3" data-testid="bps-proxy-routing">
    <legend v-if="!embedded" class="flex items-center gap-2 text-sm font-medium text-gray-700 dark:text-gray-300">
      {{ t('admin.accounts.openai.bpsProxyRouting') }}
      <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.bpsAccountMultiProxy" :user-id="userId" />
    </legend>
    <div v-if="!embedded" class="flex flex-wrap gap-4">
      <label class="flex items-center gap-1.5 text-sm">
        <input :checked="mode === 'session'" :name="modeName" type="radio" value="session" data-testid="bps-proxy-mode-session"
          class="h-4 w-4 border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500"
          @change="emit('update:mode', 'session')" />
        {{ t('admin.accounts.openai.bpsProxyModeSession') }}
      </label>
      <label class="flex items-center gap-1.5 text-sm">
        <input :checked="mode === 'request'" :name="modeName" type="radio" value="request" data-testid="bps-proxy-mode-request"
          class="h-4 w-4 border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500"
          @change="emit('update:mode', 'request')" />
        {{ t('admin.accounts.openai.bpsProxyModeRequest') }}
      </label>
    </div>
    <template v-if="mode === 'request'">
      <p class="input-hint">{{ t(embedded ? 'admin.accounts.openai.proxyRequestHint' : 'admin.accounts.openai.bpsProxyRequestHint') }}</p>
      <label :id="labelId" class="block text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ t(embedded ? 'admin.accounts.openai.proxyGroup' : 'admin.accounts.openai.bpsProxyGroup') }}
      </label>
      <div ref="containerRef" class="relative" @keydown.esc="escape">
        <button ref="triggerRef" type="button" data-testid="bps-proxy-trigger" :disabled="loading" :aria-expanded="isOpen"
          :aria-controls="optionsId" :aria-labelledby="labelId + ' ' + countId" class="select-trigger"
          :class="{ 'select-trigger-open': isOpen }" @click="toggle">
          <span :id="countId">{{ t('admin.accounts.openai.bpsProxySelectionCount', { count: proxyIds.length }) }}</span>
          <Icon name="chevronDown" size="md" :class="['text-gray-400 transition-transform', isOpen && 'rotate-180']" />
        </button>
        <div v-if="isOpen" :id="optionsId" class="select-dropdown" data-testid="bps-proxy-options">
          <div class="flex items-center gap-2 border-b border-gray-100 px-3 py-2 dark:border-dark-700">
            <Icon name="search" size="sm" class="shrink-0 text-gray-400" />
            <input ref="searchInputRef" v-model="search" type="search" class="select-search-input"
              :aria-label="t('admin.proxies.searchProxies')" :placeholder="t('admin.proxies.searchProxies')" />
            <button type="button" class="batch-test-btn" data-testid="bps-proxy-batch-test"
              :disabled="loading || loadError || batchTesting || filteredProxies.length === 0"
              :title="t('admin.accounts.openai.bpsProxyBatchTest')" :aria-label="t('admin.accounts.openai.bpsProxyBatchTest')" @click="testResultsBatch">
              <svg v-if="batchTesting" class="h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24" aria-hidden="true">
                <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
                <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z" />
              </svg>
              <Icon v-else name="play" size="sm" />
            </button>
          </div>
          <p v-if="batchTotal > 0" class="px-3 pt-2 text-xs text-gray-500 dark:text-gray-400" aria-live="polite">
            {{ t('admin.accounts.openai.bpsProxyTestProgress', { done: batchCompleted, total: batchTotal }) }}
          </p>
          <div class="space-y-1 border-b border-gray-100 px-3 py-2 dark:border-dark-700">
            <div class="flex flex-wrap gap-x-3 gap-y-1">
              <button type="button" class="selection-action" data-testid="bps-proxy-select-results" :disabled="loading || loadError" @click="selectResults">
                {{ t('admin.accounts.openai.bpsProxySelectResults') }}
              </button>
              <button type="button" class="selection-action" data-testid="bps-proxy-select-unused" :disabled="loading || loadError" @click="selectUnused">
                {{ t('admin.accounts.openai.bpsProxySelectUnused') }}
              </button>
              <button type="button" class="selection-action" data-testid="bps-proxy-invert-results" :disabled="loading || loadError" @click="invertResults">
                {{ t('admin.accounts.openai.bpsProxyInvertResults') }}
              </button>
              <button type="button" class="selection-action" data-testid="bps-proxy-clear-results" :disabled="loading || loadError" @click="clearResults">
                {{ t('admin.accounts.openai.bpsProxyClearResults') }}
              </button>
              <button type="button" class="selection-action" data-testid="bps-proxy-clear-all" :disabled="loading || loadError || proxyIds.length === 0" @click="clearAll">
                {{ t('admin.accounts.openai.bpsProxyClearAll') }}
              </button>
            </div>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.bpsProxySelectionScope') }}</p>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.bpsProxyUsageHint') }}</p>
          </div>
          <div class="max-h-72 overflow-y-auto py-1">
            <div v-for="proxy in filteredProxies" :key="proxy.id" :data-testid="'bps-proxy-row-' + proxy.id"
              class="select-option" :class="{ 'select-option-selected': proxyIds.includes(proxy.id) }">
              <label class="flex min-w-0 flex-1 cursor-pointer items-start gap-2">
                <input :checked="proxyIds.includes(proxy.id)" type="checkbox" :data-testid="'bps-proxy-' + proxy.id"
                  :disabled="loading || ((loadError || !available(proxy)) && !proxyIds.includes(proxy.id))"
                  class="mt-0.5 h-4 w-4 shrink-0 rounded border-gray-300 text-primary-600 focus:ring-primary-500 disabled:opacity-50 dark:border-dark-500"
                  @change="select(proxy.id, ($event.target as HTMLInputElement).checked)" />
                <span class="min-w-0 flex-1">
                  <span class="flex flex-wrap items-center gap-2">
                    <span class="break-all font-medium">{{ proxy.name }}</span>
                    <span v-if="!available(proxy)" class="text-xs text-amber-600 dark:text-amber-400">{{ t('admin.accounts.openai.bpsProxyUnavailable') }}</span>
                  </span>
                  <span class="block break-all text-xs text-gray-500 dark:text-gray-400">{{ proxy.protocol }}://{{ proxy.host }}:{{ proxy.port }}</span>
                  <span v-if="testResults[proxy.id]" class="mt-1 block text-xs" aria-live="polite">
                    <span v-if="testResults[proxy.id].success" class="inline-flex flex-wrap gap-1 rounded bg-emerald-100 px-1.5 py-0.5 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400">
                      <span>{{ t('admin.accounts.openai.bpsProxyTestSuccess') }}</span>
                      <span v-if="testResults[proxy.id].country">{{ testResults[proxy.id].country }}</span>
                      <span v-if="testResults[proxy.id].latency_ms !== undefined">{{ testResults[proxy.id].latency_ms }}ms</span>
                    </span>
                    <span v-else class="block break-words text-red-600 dark:text-red-400">
                      {{ t('admin.proxies.testFailed') }}: {{ testResults[proxy.id].message }}
                    </span>
                  </span>
                  <span v-if="usage[proxy.id]?.status !== 'ready'" class="mt-1 block text-xs text-gray-500 dark:text-gray-400">
                    {{ t(usage[proxy.id]?.status === 'error' ? 'admin.accounts.openai.bpsProxyUsageUnknown' : 'admin.accounts.openai.bpsProxyUsageLoading') }}
                  </span>
                  <span v-else-if="usage[proxy.id].accounts.length === 0" class="mt-1 block text-xs text-gray-500 dark:text-gray-400">
                    {{ t('admin.accounts.openai.bpsProxyUsageUnused') }}
                  </span>
                  <span v-else class="mt-1 flex flex-wrap gap-1">
                    <span v-for="account in usage[proxy.id].accounts" :key="account.id" class="rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-600 dark:bg-dark-600 dark:text-gray-300">
                      {{ account.name }} #{{ account.id }}<span v-if="account.id === currentAccountId"> · {{ t('admin.accounts.openai.bpsProxyUsageCurrent') }}</span>
                    </span>
                  </span>
                </span>
              </label>
              <button type="button" class="test-btn" :data-testid="'bps-proxy-test-' + proxy.id"
                :disabled="loading || loadError || testingProxyIds.has(proxy.id)"
                :aria-label="t('admin.accounts.openai.bpsProxyTestNamed', { name: proxy.name })"
                :title="t('admin.proxies.testConnection')" @click="testOne(proxy.id)">
                <svg v-if="testingProxyIds.has(proxy.id)" class="h-3.5 w-3.5 animate-spin" fill="none" viewBox="0 0 24 24" aria-hidden="true">
                  <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
                  <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z" />
                </svg>
                <Icon v-else name="play" size="xs" />
              </button>
              <button v-if="usage[proxy.id]?.status === 'error'" type="button" class="selection-action shrink-0"
                :data-testid="'bps-proxy-usage-retry-' + proxy.id" @click="loadAccounts(proxy.id)">
                {{ t('admin.accounts.openai.bpsProxyRetry') }}
              </button>
            </div>
            <label v-for="id in missingProxyIds" :key="id" class="select-option text-amber-600 dark:text-amber-400">
              <input checked type="checkbox" :data-testid="'bps-proxy-' + id"
                class="h-4 w-4 shrink-0 rounded border-gray-300 text-primary-600 focus:ring-primary-500" @change="select(id, false)" />
              <span>{{ t('admin.accounts.openai.bpsProxyMissing', { id }) }}</span>
            </label>
            <p v-if="!loading && !loadError && filteredProxies.length === 0 && missingProxyIds.length === 0" class="px-4 py-6 text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.openai.bpsProxyNoMatches') }}
            </p>
          </div>
        </div>
      </div>
      <p v-if="loadError" class="flex items-center gap-2 text-sm text-amber-600 dark:text-amber-400" role="status">
        {{ t('admin.accounts.openai.bpsProxyLoadFailed') }}
        <button type="button" class="selection-action" data-testid="bps-proxy-catalog-retry" :disabled="loading" @click="emit('retry')">{{ t('admin.accounts.openai.bpsProxyRetry') }}</button>
      </p>
      <p class="input-hint">{{ t('admin.accounts.openai.bpsProxySelected', { count: proxyIds.length }) }}</p>
    </template>
  </fieldset>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { Proxy, ProxyAccountSummary } from '@/types'
import Icon from '@/components/icons/Icon.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'

const props = withDefaults(defineProps<{
  mode: 'session' | 'request'
  proxyIds: number[]
  proxies: Proxy[]
  userId?: number | null
  currentAccountId?: number | null
  loading?: boolean
  loadError?: boolean
  embedded?: boolean
}>(), { loading: false, loadError: false, embedded: false })
const emit = defineEmits<{ 'update:mode': [value: 'session' | 'request']; 'update:proxyIds': [value: number[]]; retry: [] }>()
const { t } = useI18n()
const search = ref('')
const isOpen = ref(false)
const labelId = useId()
const countId = useId()
const optionsId = useId()
const modeName = useId()
const containerRef = ref<HTMLElement | null>(null)
const triggerRef = ref<HTMLButtonElement | null>(null)
const searchInputRef = ref<HTMLInputElement | null>(null)
const usage = reactive<Record<number, { status: 'loading' | 'ready' | 'error'; accounts: ProxyAccountSummary[] }>>({})
const testResults = reactive<Record<number, Awaited<ReturnType<typeof adminAPI.proxies.testProxy>>>>({})
const testingProxyIds = reactive(new Set<number>())
const batchTesting = ref(false)
const batchTotal = ref(0)
const batchCompleted = ref(0)
const testQueue: Array<{ id: number; resolve: () => void }> = []
const pendingTests = new Map<number, Promise<void>>()
let testActive = 0
const usageQueue: number[] = []
const usagePending = new Set<number>()
let usageActive = 0
let disposed = false
const filteredProxies = computed(() => {
  const query = search.value.trim().toLowerCase()
  return props.proxies.filter(proxy => [proxy.name, proxy.protocol + '://' + proxy.host + ':' + proxy.port, '#' + proxy.id,
    ...(usage[proxy.id]?.accounts.flatMap(account => [account.name, '#' + account.id]) ?? [])
  ].some(value => value.toLowerCase().includes(query)))
})
const missingProxyIds = computed(() => (props.loading || props.loadError) ? [] : props.proxyIds.filter(id => !props.proxies.some(proxy => proxy.id === id)))
function available(proxy: Proxy): boolean {
  return proxy.status === 'active' && (!proxy.expires_at || Date.parse(proxy.expires_at) > Date.now())
}
function select(id: number, selected: boolean): void {
  if (props.loading || (props.loadError && selected)) return
  emit('update:proxyIds', selected ? [...new Set([...props.proxyIds, id])] : props.proxyIds.filter(value => value !== id))
}
function selectResults(): void {
  if (props.loading || props.loadError) return
  emit('update:proxyIds', [...new Set([...props.proxyIds, ...filteredProxies.value.filter(available).map(proxy => proxy.id)])])
}
function replaceResults(ids: number[], replaceable: number[] = filteredProxies.value.filter(available).map(proxy => proxy.id)): void {
  if (props.loading || props.loadError) return
  const changing = new Set(replaceable)
  emit('update:proxyIds', [...new Set([...props.proxyIds.filter(id => !changing.has(id)), ...ids])])
}
function selectUnused(): void {
  const known = filteredProxies.value.filter(proxy => available(proxy) && usage[proxy.id]?.status === 'ready')
  replaceResults(known.filter(proxy => usage[proxy.id].accounts.length === 0).map(proxy => proxy.id), known.map(proxy => proxy.id))
}
function invertResults(): void {
  replaceResults(filteredProxies.value.filter(proxy => available(proxy) && !props.proxyIds.includes(proxy.id)).map(proxy => proxy.id))
}
function clearResults(): void { replaceResults([]) }
function clearAll(): void {
  if (!props.loading && !props.loadError) emit('update:proxyIds', [])
}
function toggle(): void {
  if (props.loading) return
  isOpen.value = !isOpen.value
  if (isOpen.value) void nextTick(() => searchInputRef.value?.focus())
}
function testOne(id: number): Promise<void> {
  const pending = pendingTests.get(id)
  if (pending) return pending
  if (disposed || props.loading || props.loadError) return Promise.resolve()
  testingProxyIds.add(id)
  let resolve!: () => void
  const promise = new Promise<void>(done => { resolve = done })
  pendingTests.set(id, promise)
  testQueue.push({ id, resolve })
  runTestQueue()
  return promise
}
function runTestQueue(): void {
  while (!disposed && testActive < 5 && testQueue.length > 0) {
    testActive++
    void performTest(testQueue.shift()!)
  }
}
async function performTest({ id, resolve }: { id: number; resolve: () => void }): Promise<void> {
  try {
    const result = await adminAPI.proxies.testProxy(id)
    if (!disposed) testResults[id] = result
  } catch (error: unknown) {
    if (!disposed) testResults[id] = { success: false, message: testFailure(error) }
  } finally {
    testActive--
    testingProxyIds.delete(id)
    pendingTests.delete(id)
    resolve()
    runTestQueue()
  }
}
async function testResultsBatch(): Promise<void> {
  if (disposed || props.loading || props.loadError || batchTesting.value) return
  const ids = [...new Set(filteredProxies.value.map(proxy => proxy.id))]
  if (ids.length === 0) return
  batchTesting.value = true
  batchTotal.value = ids.length
  batchCompleted.value = 0
  await Promise.all(ids.map(id => testOne(id).then(() => { if (!disposed) batchCompleted.value++ })))
  if (!disposed) batchTesting.value = false
}
function testFailure(error: unknown): string {
  if (error && typeof error === 'object' && 'response' in error) {
    const response = error.response
    if (response && typeof response === 'object' && 'data' in response) {
      const data = response.data
      if (data && typeof data === 'object' && 'detail' in data && typeof data.detail === 'string') return data.detail
    }
  }
  return error instanceof Error ? error.message : t('admin.proxies.testFailed')
}
function loadAccounts(id: number): void {
  if (disposed || props.loading || props.loadError || usagePending.has(id)) return
  usagePending.add(id)
  usage[id] = { status: 'loading', accounts: [] }
  usageQueue.push(id)
  runUsageQueue()
}
function runUsageQueue(): void {
  while (!disposed && isOpen.value && !props.loading && !props.loadError && props.mode === 'request' && usageActive < 5 && usageQueue.length > 0) {
    const id = usageQueue.shift()!
    usageActive++
    void adminAPI.proxies.getProxyAccounts(id).then(accounts => {
      if (!disposed) {
        if (!Array.isArray(accounts)) throw new Error('Invalid proxy account list')
        usage[id] = { status: 'ready', accounts }
      }
    }).catch(() => {
      if (!disposed) usage[id] = { status: 'error', accounts: [] }
    }).finally(() => { usageActive--; usagePending.delete(id); runUsageQueue() })
  }
}
function close(): void { isOpen.value = false; search.value = '' }
function clickOutside(event: MouseEvent): void {
  const container = containerRef.value
  if (!container) return
  // The original icon can be detached when testing replaces it with a spinner.
  if (event.composedPath().includes(container)) return
  if (!(event.target instanceof Node) || !container.contains(event.target)) close()
}
function escape(event: KeyboardEvent): void {
  if (!isOpen.value) return
  event.stopPropagation()
  event.preventDefault()
  close()
  void nextTick(() => triggerRef.value?.focus())
}
watch(() => props.mode, mode => { if (mode !== 'request') close() })
watch([isOpen, () => props.loading, () => props.loadError, () => props.proxies.map(proxy => proxy.id).join(',')], ([open, loading, loadError], previous) => {
  if (!open || loading || loadError || props.mode !== 'request') return
  runUsageQueue()
  for (const proxy of props.proxies) {
    if (!previous?.[0] || !usage[proxy.id]) loadAccounts(proxy.id)
  }
})
onMounted(() => { document.addEventListener('click', clickOutside) })
onBeforeUnmount(() => {
  disposed = true
  usageQueue.length = 0
  for (const job of testQueue.splice(0)) { pendingTests.delete(job.id); job.resolve() }
  document.removeEventListener('click', clickOutside)
})
</script>

<style scoped>
.select-trigger {
  @apply flex w-full items-center justify-between gap-2 rounded-xl border border-gray-200 bg-white px-4 py-2.5 text-left text-sm text-gray-900 transition-all;
  @apply hover:border-gray-300 focus:border-primary-500 focus:outline-none focus:ring-2 focus:ring-primary-500/30 disabled:cursor-not-allowed disabled:opacity-60;
  @apply dark:border-dark-600 dark:bg-dark-800 dark:text-gray-100 dark:hover:border-dark-500;
}
.select-trigger-open { @apply border-primary-500 ring-2 ring-primary-500/30; }
.select-dropdown {
  @apply absolute z-[100] mt-2 w-full overflow-hidden rounded-xl border border-gray-200 bg-white shadow-lg shadow-black/10;
  @apply dark:border-dark-700 dark:bg-dark-800 dark:shadow-black/30;
}
.select-search-input {
  @apply min-w-0 flex-1 bg-transparent text-sm text-gray-900 placeholder:text-gray-400 focus:outline-none;
  @apply dark:text-gray-100 dark:placeholder:text-dark-400;
}
.select-option {
  @apply flex items-start gap-2 px-4 py-2.5 text-sm text-gray-700 transition-colors hover:bg-gray-50;
  @apply dark:text-gray-300 dark:hover:bg-dark-700;
}
.select-option-selected { @apply bg-primary-50 text-primary-700 dark:bg-primary-900/20 dark:text-primary-300; }
.selection-action { @apply text-xs text-primary-600 hover:underline disabled:cursor-not-allowed disabled:opacity-50 dark:text-primary-400; }
.test-btn {
  @apply shrink-0 rounded p-1 text-gray-400 transition-colors hover:bg-emerald-50 hover:text-emerald-600 disabled:cursor-not-allowed disabled:opacity-50;
  @apply dark:hover:bg-emerald-900/20 dark:hover:text-emerald-400;
}
.batch-test-btn {
  @apply shrink-0 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-emerald-50 hover:text-emerald-600 disabled:cursor-not-allowed disabled:opacity-50;
  @apply dark:hover:bg-emerald-900/20 dark:hover:text-emerald-400;
}
</style>
