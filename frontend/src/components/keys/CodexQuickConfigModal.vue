<template>
  <BaseDialog
    :show="show"
    :title="t('keys.quickConfigureModal.title')"
    width="wide"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <div class="flex items-start gap-3 rounded-lg border border-amber-200 bg-amber-50 p-3 dark:border-amber-800 dark:bg-amber-950/30">
        <Icon name="exclamationCircle" size="md" class="mt-0.5 flex-shrink-0 text-amber-500" />
        <p class="text-sm text-amber-800 dark:text-amber-200">
          {{ t('keys.quickConfigureModal.warning') }}
        </p>
      </div>

      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('keys.quickConfigureModal.description') }}
      </p>

      <div class="grid grid-cols-2 gap-1 rounded-lg bg-gray-100 p-1 dark:bg-dark-700" role="tablist">
        <button
          type="button"
          role="tab"
          :aria-selected="activePlatform === 'unix'"
          data-testid="quick-config-unix-tab"
          :class="tabClass(activePlatform === 'unix')"
          @click="activePlatform = 'unix'"
        >
          {{ t('keys.quickConfigureModal.macLinux') }}
        </button>
        <button
          type="button"
          role="tab"
          :aria-selected="activePlatform === 'windows'"
          data-testid="quick-config-windows-tab"
          :class="tabClass(activePlatform === 'windows')"
          @click="activePlatform = 'windows'"
        >
          {{ t('keys.quickConfigureModal.windows') }}
        </button>
      </div>

      <div class="rounded-lg border border-gray-200 p-3 dark:border-dark-700">
        <label v-if="isOpenAI" class="block">
          <span class="block text-sm font-medium text-gray-900 dark:text-white">
            {{ t('keys.useKeyModal.codexModelCatalog.mode') }}
          </span>
          <select v-model="catalogMode" data-testid="quick-config-catalog-mode" class="input mt-2 text-sm">
            <option value="remote">{{ t('keys.useKeyModal.codexModelCatalog.remote') }}</option>
            <option value="file">{{ t('keys.useKeyModal.codexModelCatalog.local') }}</option>
            <option value="none">{{ t('keys.quickConfigureModal.noCatalog') }}</option>
          </select>
        </label>
        <p v-if="isOpenAI && catalogMode === 'remote'" class="mt-2 break-all font-mono text-xs text-gray-700 dark:text-gray-300">
          {{ modelCatalogUrl }}
        </p>
        <label v-if="!isOpenAI" class="flex cursor-pointer items-start gap-3">
          <input
            v-model="includeModelCatalog"
            type="checkbox"
            class="mt-0.5 h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-800"
            data-testid="quick-config-import-catalog"
          />
          <span class="min-w-0">
            <span class="block text-sm font-medium text-gray-900 dark:text-white">
              {{ t('keys.quickConfigureModal.importCatalog') }}
            </span>
            <span class="mt-1 block text-xs text-gray-500 dark:text-gray-400">
              {{ t('keys.quickConfigureModal.importCatalogHint') }}
            </span>
          </span>
        </label>
        <p v-if="includeModelCatalog && catalogState === 'loading'" data-testid="quick-config-catalog-loading" class="mt-2 text-xs text-primary-600 dark:text-primary-400">
          {{ t('keys.quickConfigureModal.importCatalogLoading') }}
        </p>
        <p v-else-if="includeModelCatalog && catalogState === 'ready'" data-testid="quick-config-catalog-ready" class="mt-2 text-xs text-emerald-600 dark:text-emerald-400">
          {{ t('keys.quickConfigureModal.importCatalogReady', { count: catalogModelCount }) }}
        </p>
        <p v-else-if="includeModelCatalog && catalogState === 'error'" data-testid="quick-config-catalog-error" class="mt-2 text-xs text-red-600 dark:text-red-400">
          {{ t('keys.quickConfigureModal.importCatalogError') }}
        </p>
      </div>

      <div class="overflow-hidden rounded-lg bg-gray-900 dark:bg-dark-900">
        <div class="flex items-center justify-between border-b border-gray-700 bg-gray-800 px-4 py-2 dark:bg-dark-800">
          <span class="truncate font-mono text-xs text-gray-400">{{ fileName }}</span>
          <div class="flex items-center gap-2">
            <button
              type="button"
              :disabled="!scriptReady"
              class="flex items-center gap-1.5 rounded-lg bg-gray-700 px-2.5 py-1 text-xs font-medium text-gray-300 transition-colors hover:bg-gray-600 hover:text-white disabled:cursor-not-allowed disabled:opacity-50"
              data-testid="quick-config-copy"
              @click="copyScript"
            >
              <Icon :name="copied ? 'check' : 'clipboard'" size="sm" />
              {{ copied ? t('keys.quickConfigureModal.copied') : t('keys.quickConfigureModal.copy') }}
            </button>
            <button
              type="button"
              :disabled="!scriptReady"
              class="flex items-center gap-1.5 rounded-lg bg-primary-600 px-2.5 py-1 text-xs font-medium text-white transition-colors hover:bg-primary-700 disabled:cursor-not-allowed disabled:opacity-50"
              data-testid="quick-config-download"
              @click="downloadScript"
            >
              <Icon name="download" size="sm" />
              {{ t('keys.quickConfigureModal.download') }}
            </button>
          </div>
        </div>
        <pre class="max-h-96 overflow-auto p-4 text-xs leading-5 text-gray-100"><code>{{ script }}</code></pre>
      </div>

      <div
        v-if="actionStatus"
        role="status"
        aria-live="polite"
        data-testid="quick-config-action-status"
        class="flex items-start gap-2 rounded-lg border p-3 text-sm"
        :class="actionStatus === 'error'
          ? 'border-red-200 bg-red-50 text-red-700 dark:border-red-800 dark:bg-red-950/30 dark:text-red-300'
          : 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-800 dark:bg-emerald-950/30 dark:text-emerald-300'"
      >
        <Icon :name="actionStatus === 'error' ? 'exclamationCircle' : 'checkCircle'" size="md" class="mt-0.5 flex-shrink-0" />
        <p>{{ t(`keys.quickConfigureModal.${actionStatus}Message`) }}</p>
      </div>

      <div class="rounded-lg border border-gray-200 bg-gray-50 p-3 text-sm text-gray-600 dark:border-dark-700 dark:bg-dark-800/50 dark:text-gray-300">
        <p class="font-medium text-gray-900 dark:text-white">{{ t('keys.quickConfigureModal.runTitle') }}</p>
        <code class="mt-1 block break-all text-xs">{{ runCommand }}</code>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <button type="button" class="btn btn-secondary" @click="emit('close')">
          {{ t('common.close') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { saveAs } from 'file-saver'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { buildCodexModelCatalogUrl, fetchCodexModelsManifest, OPENAI_CODEX_CATALOG_CLIENT_VERSION } from '@/api/codex'
import {
  buildMacLinuxCodexQuickConfigScript,
  buildWindowsCmdCodexQuickConfigScript,
  type CodexQuickConfigPlatform
} from '@/utils/codexQuickConfig'

const props = defineProps<{
  show: boolean
  apiKey: string
  baseUrl: string
  platform?: CodexQuickConfigPlatform | null
  siteName?: string
}>()

const emit = defineEmits<{ (event: 'close'): void }>()
const { t } = useI18n()

type TargetPlatform = 'unix' | 'windows'
type CatalogMode = 'none' | 'remote' | 'file'
type CatalogState = 'idle' | 'loading' | 'ready' | 'error'

const detectPlatform = (): TargetPlatform => /windows/i.test(navigator.userAgent) ? 'windows' : 'unix'
const activePlatform = ref<TargetPlatform>(detectPlatform())
const copied = ref(false)
type ActionStatus = 'copySuccess' | 'downloadSuccess' | 'error' | null
const actionStatus = ref<ActionStatus>(null)
const isOpenAI = computed(() => !props.platform || props.platform === 'openai')
const catalogMode = ref<CatalogMode>(isOpenAI.value ? 'remote' : 'none')
const includeModelCatalog = computed({
  get: () => catalogMode.value === 'file',
  set: (enabled: boolean) => { catalogMode.value = enabled ? 'file' : 'none' }
})
const modelCatalogUrl = computed(() => buildCodexModelCatalogUrl(props.baseUrl))
const catalogState = ref<CatalogState>('idle')
const catalogContent = ref('')
const catalogModelCount = ref(0)
let catalogController: AbortController | null = null
let catalogRequestID = 0

const input = computed(() => ({
  apiKey: props.apiKey,
  baseUrl: props.baseUrl.trim() || window.location.origin,
  platform: props.platform,
  siteName: props.siteName,
  modelCatalogUrl: isOpenAI.value && catalogMode.value === 'remote' ? modelCatalogUrl.value : undefined,
  modelCatalogContent: includeModelCatalog.value && catalogState.value === 'ready' ? catalogContent.value : undefined
}))

const script = computed(() => activePlatform.value === 'windows'
  ? buildWindowsCmdCodexQuickConfigScript(input.value)
  : buildMacLinuxCodexQuickConfigScript(input.value))

const fileName = computed(() => activePlatform.value === 'windows'
  ? 'sub2api-codex-config.cmd'
  : 'sub2api-codex-config.sh')

const runCommand = computed(() => activePlatform.value === 'windows'
  ? t('keys.quickConfigureModal.windowsRun')
  : `chmod +x ${fileName.value} && ./${fileName.value}`)

const scriptReady = computed(() => !includeModelCatalog.value || catalogState.value === 'ready')

const tabClass = (selected: boolean) => [
  'rounded-md px-3 py-2 text-sm font-medium transition-colors',
  selected
    ? 'bg-white text-primary-700 shadow-sm dark:bg-dark-800 dark:text-primary-300'
    : 'text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'
]

watch(() => props.show, (show) => {
  if (show) {
    activePlatform.value = detectPlatform()
    copied.value = false
    actionStatus.value = null
    catalogMode.value = isOpenAI.value ? 'remote' : 'none'
  }
})

watch([() => props.show, includeModelCatalog, () => props.baseUrl, () => props.apiKey], ([show, enabled]) => {
  if (show && enabled) void loadCatalog()
  else resetCatalog()
})

function resetCatalog() {
  catalogController?.abort()
  catalogController = null
  catalogRequestID += 1
  catalogState.value = 'idle'
  catalogContent.value = ''
  catalogModelCount.value = 0
}

async function loadCatalog() {
  if (!includeModelCatalog.value || !props.apiKey) return
  catalogController?.abort()
  const controller = new AbortController()
  const requestID = ++catalogRequestID
  catalogController = controller
  catalogState.value = 'loading'
  try {
    const result = await fetchCodexModelsManifest(
      props.baseUrl,
      props.apiKey,
      controller.signal,
      isOpenAI.value ? OPENAI_CODEX_CATALOG_CLIENT_VERSION : undefined
    )
    if (requestID !== catalogRequestID) return
    catalogContent.value = result.content
    catalogModelCount.value = result.modelCount
    catalogState.value = 'ready'
  } catch (error) {
    const errorName = error && typeof error === 'object' && 'name' in error
      ? String((error as { name?: unknown }).name || '')
      : ''
    if (requestID !== catalogRequestID || errorName === 'AbortError') return
    catalogState.value = 'error'
  } finally {
    if (requestID === catalogRequestID) catalogController = null
  }
}

async function copyScript() {
  if (!scriptReady.value) return
  try {
    await navigator.clipboard.writeText(script.value)
    copied.value = true
    actionStatus.value = 'copySuccess'
    window.setTimeout(() => { copied.value = false }, 1800)
  } catch {
    copied.value = false
    actionStatus.value = 'error'
  }
}

function downloadScript() {
  if (!scriptReady.value) return
  try {
    saveAs(new Blob([script.value], { type: 'text/plain;charset=utf-8' }), fileName.value)
    actionStatus.value = 'downloadSuccess'
  } catch {
    actionStatus.value = 'error'
  }
}
</script>
