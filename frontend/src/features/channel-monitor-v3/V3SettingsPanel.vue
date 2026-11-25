<template>
  <section class="w-full min-w-0 space-y-5" data-testid="monitor-v3-settings">
    <div
      v-if="siteMode !== 'v3'"
      class="rounded-2xl border border-amber-200 bg-amber-50/90 px-4 py-3 text-sm text-amber-900 dark:border-amber-800/50 dark:bg-amber-900/20 dark:text-amber-100"
      role="status"
      data-testid="monitor-v3-mode-banner"
    >
      {{ t(siteMode === 'v2' ? 'channelMonitorV3.admin.modeBannerV2' : 'channelMonitorV3.admin.modeBanner', { mode: t(`channelMonitorV3.modes.${siteMode}`) }) }}
    </div>

    <div v-if="loading && !settings" class="h-64 animate-pulse rounded-2xl bg-gray-100 dark:bg-dark-800" />
    <p v-else-if="loadError" class="rounded-2xl bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-200">{{ loadError }}</p>

    <template v-else-if="settings && draft">
      <div class="grid gap-5 xl:grid-cols-[minmax(340px,0.38fr)_minmax(0,0.62fr)]">
        <!-- Page-wide settings -->
        <div class="card min-w-0 !rounded-2xl" data-testid="monitor-v3-config">
          <div class="border-b border-gray-100 px-5 py-4 dark:border-dark-700">
            <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('channelMonitorV3.admin.pageSettings') }}</h3>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.admin.pageSettingsHint') }}</p>
            <p class="mt-2 text-xs" :class="stale ? 'text-amber-600 dark:text-amber-400' : 'text-gray-500 dark:text-gray-400'" data-testid="monitor-v3-freshness">
              {{ freshness }}
            </p>
          </div>
          <div class="space-y-4 p-5">
            <div class="grid grid-cols-2 gap-3">
              <div>
                <label class="input-label">{{ t('channelMonitorV3.admin.interval') }}</label>
                <Select v-model="draft.interval_minutes" :options="intervalOptions" :aria-label="t('channelMonitorV3.admin.interval')" data-testid="monitor-v3-interval" />
              </div>
              <div>
                <label class="input-label" for="monitor-v3-cells">{{ t('channelMonitorV3.admin.cells') }}</label>
                <input id="monitor-v3-cells" v-model.number="draft.cells" type="number" min="30" max="120" class="input" />
              </div>
            </div>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.admin.windowHint', { hours: windowHours, cells: draft.cells }) }}</p>
            <div>
              <label class="input-label">{{ t('channelMonitorV3.admin.range') }}</label>
              <Select v-model="draft.availability_range" :options="rangeOptions" :aria-label="t('channelMonitorV3.admin.range')" data-testid="monitor-v3-range" />
            </div>

            <div class="rounded-xl border border-gray-100 p-3 dark:border-dark-700">
              <p class="text-xs font-medium text-gray-700 dark:text-gray-200">{{ t('channelMonitorV3.admin.rulesTitle') }}</p>
              <div class="mt-2 grid grid-cols-2 gap-3">
                <div>
                  <label class="input-label" for="monitor-v3-down-rate">{{ t('channelMonitorV3.admin.downRate') }}</label>
                  <input id="monitor-v3-down-rate" v-model.number="downPercent" type="number" min="1" max="100" step="1" class="input" />
                </div>
                <div>
                  <label class="input-label" for="monitor-v3-degraded-rate">{{ t('channelMonitorV3.admin.degradedRate') }}</label>
                  <input id="monitor-v3-degraded-rate" v-model.number="degradedPercent" type="number" min="1" max="100" step="1" class="input" />
                </div>
                <div>
                  <label class="input-label">{{ t('channelMonitorV3.admin.ttft') }}</label>
                  <Select v-model="draft.degraded_ttft_ms" :options="ttftOptions" :aria-label="t('channelMonitorV3.admin.ttft')" data-testid="monitor-v3-ttft" />
                </div>
                <div>
                  <label class="input-label" for="monitor-v3-min">{{ t('channelMonitorV3.admin.minRequests') }}</label>
                  <input id="monitor-v3-min" v-model.number="draft.min_requests" type="number" min="1" max="1000" class="input" />
                </div>
              </div>
              <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.admin.rulesHint') }}</p>
            </div>

            <div>
              <label class="input-label">{{ t('channelMonitorV3.admin.ignored') }}</label>
              <div class="flex flex-wrap gap-1.5" data-testid="monitor-v3-ignored">
                <button
                  v-for="category in settings.error_categories"
                  :key="category"
                  type="button"
                  class="rounded-full border px-2 py-0.5 text-[11px] transition"
                  :class="draft.ignored_error_categories.includes(category) ? 'border-gray-300 bg-gray-100 text-gray-500 line-through dark:border-dark-500 dark:bg-dark-700 dark:text-gray-400' : 'border-primary-200 bg-primary-50 text-primary-700 dark:border-primary-800 dark:bg-primary-900/20 dark:text-primary-300'"
                  :aria-pressed="draft.ignored_error_categories.includes(category)"
                  :data-category="category"
                  @click="toggleIgnored(category)"
                >
                  {{ t(`channelMonitorV2.errorCategories.${category}`) }}
                </button>
              </div>
              <p class="mt-1 text-xs text-gray-400">{{ t('channelMonitorV3.admin.ignoredHint') }}</p>
            </div>

            <div>
              <label class="input-label">{{ t('channelMonitorV3.admin.featured') }}</label>
              <Select v-model="draft.featured_component_id" :options="featuredOptions" :aria-label="t('channelMonitorV3.admin.featured')" data-testid="monitor-v3-featured-select" />
              <p class="mt-1 text-xs text-gray-400">{{ t('channelMonitorV3.admin.featuredHint') }}</p>
            </div>
            <div>
              <label class="input-label" for="monitor-v3-footer">{{ t('channelMonitorV3.admin.footer') }}</label>
              <textarea id="monitor-v3-footer" v-model="draft.footer_note" rows="2" maxlength="500" class="input" :placeholder="t('channelMonitorV3.page.defaultFooter')" />
            </div>
            <div class="flex items-center justify-between border-t border-gray-100 pt-4 dark:border-dark-700">
              <span class="text-xs text-gray-400">{{ dirty ? t('channelMonitorV3.admin.unsaved') : t('channelMonitorV3.admin.saved') }}</span>
              <button type="button" class="btn btn-primary btn-sm" :disabled="!dirty || savingConfig" data-testid="monitor-v3-save-config" @click="saveConfig">
                {{ t('common.save') }}
              </button>
            </div>
          </div>
        </div>

        <!-- Categories and components -->
        <div class="card min-w-0 !rounded-2xl" data-testid="monitor-v3-layout">
          <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
            <div>
              <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('channelMonitorV3.admin.components') }}</h3>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.admin.componentsHint') }}</p>
            </div>
            <div class="flex gap-2">
              <button type="button" class="btn btn-secondary btn-sm" data-testid="monitor-v3-add-category" @click="openCategory(null)">
                <Icon name="plus" size="xs" />{{ t('channelMonitorV3.admin.addCategory') }}
              </button>
              <button type="button" class="btn btn-primary btn-sm" data-testid="monitor-v3-add-component" @click="openComponent(null, null)">
                <Icon name="plus" size="xs" />{{ t('channelMonitorV3.admin.addComponent') }}
              </button>
            </div>
          </div>

          <div v-if="!settings.categories.length && !settings.components.length" class="px-5 py-12 text-center text-sm text-gray-500 dark:text-gray-400">
            {{ t('channelMonitorV3.admin.emptyLayout') }}
          </div>
          <div v-else class="divide-y divide-gray-100 dark:divide-dark-700">
            <div v-for="(section, sectionIndex) in sections" :key="section.key" class="px-5 py-4" data-testid="monitor-v3-section">
              <div class="flex flex-wrap items-center justify-between gap-2">
                <div class="min-w-0">
                  <p class="flex items-center gap-2 text-sm font-semibold text-gray-900 dark:text-white">
                    {{ section.category ? section.category.name : t('channelMonitorV3.admin.uncategorized') }}
                    <span class="rounded-md bg-gray-100 px-1.5 py-0.5 text-[11px] font-normal text-gray-500 dark:bg-dark-700 dark:text-gray-400">{{ section.components.length }}</span>
                  </p>
                  <p v-if="section.category?.description" class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ section.category.description }}</p>
                  <p v-else-if="!section.category" class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.admin.uncategorizedHint') }}</p>
                </div>
                <div v-if="section.category" class="flex items-center gap-1">
                  <button type="button" class="icon-btn" :disabled="sectionIndex === 0" :title="t('channelMonitorV3.admin.moveUp')" @click="moveCategory(section.category.id, -1)"><Icon name="arrowUp" size="xs" /></button>
                  <button type="button" class="icon-btn" :disabled="sectionIndex >= settings.categories.length - 1" :title="t('channelMonitorV3.admin.moveDown')" @click="moveCategory(section.category.id, 1)"><Icon name="arrowDown" size="xs" /></button>
                  <button type="button" class="icon-btn" :title="t('channelMonitorV3.admin.addHere')" @click="openComponent(null, section.category.id)"><Icon name="plus" size="xs" /></button>
                  <button type="button" class="icon-btn" :title="t('common.edit')" @click="openCategory(section.category)"><Icon name="edit" size="xs" /></button>
                  <button type="button" class="icon-btn hover:text-red-500" :title="t('common.delete')" @click="deleting = { kind: 'category', id: section.category.id, name: section.category.name }"><Icon name="trash" size="xs" /></button>
                </div>
              </div>
              <p v-if="!section.components.length" class="mt-3 rounded-xl border border-dashed border-gray-200 px-3 py-3 text-center text-xs text-gray-400 dark:border-dark-600">
                {{ t('channelMonitorV3.admin.emptyCategory') }}
              </p>
              <ul v-else class="mt-3 space-y-2">
                <li
                  v-for="(component, componentIndex) in section.components"
                  :key="component.id"
                  class="flex flex-wrap items-center gap-3 rounded-xl border border-gray-100 px-3 py-2.5 dark:border-dark-700"
                  :class="component.enabled ? '' : 'opacity-60'"
                  data-testid="monitor-v3-admin-component"
                >
                  <div class="min-w-0 flex-1">
                    <p class="flex flex-wrap items-center gap-1.5 text-sm font-medium text-gray-900 dark:text-white">
                      <span class="truncate">{{ component.name }}</span>
                      <span v-if="component.show_multiplier" class="rounded-md bg-gray-100 px-1.5 font-mono text-[10px] text-gray-500 dark:bg-dark-700 dark:text-gray-400">{{ formatMonitorV3Multiplier(component.group_rate_multiplier) }}</span>
                      <span v-if="settings.config.featured_component_id === component.id" class="rounded-md bg-primary-50 px-1.5 text-[10px] text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">{{ t('channelMonitorV3.admin.featuredBadge') }}</span>
                      <span v-if="component.group_deleted || (component.group_status && component.group_status !== 'active')" class="rounded-md bg-red-50 px-1.5 text-[10px] text-red-600 dark:bg-red-900/30 dark:text-red-300">{{ t('channelMonitorV3.admin.groupUnavailable') }}</span>
                    </p>
                    <p class="mt-0.5 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs text-gray-500 dark:text-gray-400">
                      <span class="rounded px-1 py-px text-[10px]" :class="platformBadgeLightClass(component.group_platform)">{{ platformLabel(component.group_platform) }}</span>
                      <span class="truncate">{{ component.group_name || '#' + component.group_id }}</span>
                      <span>·</span>
                      <code v-if="componentModels(component).length" class="break-all rounded bg-gray-100 px-1 font-mono text-[11px] text-gray-700 dark:bg-dark-700 dark:text-gray-300">{{ componentModels(component).join(', ') }}</code>
                      <span v-else>{{ t('channelMonitorV3.admin.allModels') }}</span>
                      <span>·</span>
                      <span>{{ t(`channelMonitorV3.visibility.${component.visibility}`) }}</span>
                    </p>
                  </div>
                  <div class="flex items-center gap-1">
                    <Toggle :model-value="component.enabled" :aria-label="t('channelMonitorV3.editor.enabled')" @update:model-value="toggleEnabled(component, $event)" />
                    <button type="button" class="icon-btn" :disabled="componentIndex === 0" :title="t('channelMonitorV3.admin.moveUp')" @click="moveComponent(section, component.id, -1)"><Icon name="arrowUp" size="xs" /></button>
                    <button type="button" class="icon-btn" :disabled="componentIndex >= section.components.length - 1" :title="t('channelMonitorV3.admin.moveDown')" @click="moveComponent(section, component.id, 1)"><Icon name="arrowDown" size="xs" /></button>
                    <button type="button" class="icon-btn" :title="t('common.edit')" data-testid="monitor-v3-edit-component" @click="openComponent(component, component.category_id)"><Icon name="edit" size="xs" /></button>
                    <button type="button" class="icon-btn hover:text-red-500" :title="t('common.delete')" @click="deleting = { kind: 'component', id: component.id, name: component.name }"><Icon name="trash" size="xs" /></button>
                  </div>
                </li>
              </ul>
            </div>
          </div>
        </div>
      </div>

      <IdleProbeSettings
        :probe="settings.idle_probe || NO_IDLE_PROBE"
        :stats="settings.idle_probe_stats || []"
        :components="settings.components"
        @saved="onIdleProbeSaved"
      />

      <!-- Live preview through the admin endpoint, available in any mode -->
      <div class="card !rounded-2xl" data-testid="monitor-v3-preview">
        <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
          <div>
            <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('channelMonitorV3.admin.preview') }}</h3>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV3.admin.previewHint') }}</p>
          </div>
          <router-link v-if="siteMode === 'v3'" to="/monitor" target="_blank" class="btn btn-secondary btn-sm">
            <Icon name="externalLink" size="xs" />{{ t('channelMonitorV3.admin.openUserPage') }}
          </router-link>
        </div>
        <div class="bg-gray-50/80 p-4 dark:bg-dark-900/40 sm:p-5">
          <StatusPage :status="preview" :loading="previewLoading" @refresh="loadPreview(previewEnd)" @navigate="loadPreview" @open-incidents="showIncidents = true" />
        </div>
      </div>
    </template>

    <ComponentEditorDialog
      :show="editor.show"
      :component="editor.component"
      :categories="settings?.categories || []"
      :groups="groups"
      :default-category-id="editor.categoryId"
      :inherited-ttft-ms="settings?.config.degraded_ttft_ms || 10000"
      @close="editor.show = false"
      @saved="onComponentSaved"
    />

    <BaseDialog
      :show="categoryEditor.show"
      :title="categoryEditor.id ? t('channelMonitorV3.admin.editCategory') : t('channelMonitorV3.admin.addCategory')"
      width="narrow"
      @close="categoryEditor.show = false"
    >
      <form id="monitor-v3-category-form" class="space-y-4" @submit.prevent="saveCategory">
        <div>
          <label class="input-label" for="monitor-v3-category-name">{{ t('channelMonitorV3.admin.categoryName') }}</label>
          <input id="monitor-v3-category-name" v-model="categoryEditor.name" class="input" maxlength="64" :placeholder="t('channelMonitorV3.admin.categoryNamePlaceholder')" data-testid="monitor-v3-category-name" />
        </div>
        <div>
          <label class="input-label" for="monitor-v3-category-description">{{ t('channelMonitorV3.admin.categoryDescription') }}</label>
          <input id="monitor-v3-category-description" v-model="categoryEditor.description" class="input" maxlength="500" :placeholder="t('channelMonitorV3.admin.categoryDescriptionPlaceholder')" />
        </div>
      </form>
      <template #footer>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn btn-secondary" @click="categoryEditor.show = false">{{ t('common.cancel') }}</button>
          <button type="submit" form="monitor-v3-category-form" class="btn btn-primary" :disabled="!categoryEditor.name.trim()" data-testid="monitor-v3-category-save">{{ t('common.save') }}</button>
        </div>
      </template>
    </BaseDialog>

    <ConfirmDialog
      :show="deleting !== null"
      :title="t('common.delete')"
      :message="deleting ? t(deleting.kind === 'category' ? 'channelMonitorV3.admin.deleteCategoryConfirm' : 'channelMonitorV3.admin.deleteComponentConfirm', { name: deleting.name }) : ''"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      :danger="true"
      @confirm="confirmDelete"
      @cancel="deleting = null"
    />
    <IncidentsDialog :show="showIncidents" :admin="true" @close="showIncidents = false" />
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import {
  MONITOR_V3_INTERVALS,
  MONITOR_V3_RANGES,
  MONITOR_V3_TTFT_THRESHOLDS,
  createCategory,
  deleteCategory,
  deleteComponent,
  getSettings,
  getStatus,
  reorder,
  updateCategory,
  updateComponent,
  updateConfig,
  type MonitorV3Category,
  type MonitorV3Component,
  type MonitorV3Config,
  type MonitorV3IdleProbe,
  type MonitorV3Settings,
  type MonitorV3StatusPage,
} from '@/api/channelMonitorV3'
import { useAppStore } from '@/stores/app'
import type { AdminGroup } from '@/types'
import { extractApiErrorMessage } from '@/utils/apiError'
import { getChannelMonitorMode } from '@/utils/featureFlags'
import { platformBadgeLightClass, platformLabel } from '@/utils/platformColors'
import ComponentEditorDialog from './ComponentEditorDialog.vue'
import IdleProbeSettings from './IdleProbeSettings.vue'
import IncidentsDialog from './IncidentsDialog.vue'
import StatusPage from './StatusPage.vue'
import { componentModels, formatMonitorV3Full, formatMonitorV3Multiplier } from './monitorV3'

/** Facts older than this mean the passive aggregation is not keeping up. */
const STALE_MS = 10 * 60_000
/** What a server without idle probes reports. */
const NO_IDLE_PROBE: MonitorV3IdleProbe = { enabled: false, groups: [] }

const { t } = useI18n()
const appStore = useAppStore()

const settings = ref<MonitorV3Settings | null>(null)
const draft = ref<MonitorV3Config | null>(null)
const original = ref('')
const loading = ref(true)
const loadError = ref('')
const savingConfig = ref(false)
const groups = ref<AdminGroup[]>([])
const preview = ref<MonitorV3StatusPage | null>(null)
const previewLoading = ref(false)
const previewEnd = ref<number | null>(null)
const showIncidents = ref(false)
const deleting = ref<{ kind: 'category' | 'component'; id: number; name: string } | null>(null)
const editor = reactive<{ show: boolean; component: MonitorV3Component | null; categoryId: number | null }>({ show: false, component: null, categoryId: null })
const categoryEditor = reactive({ show: false, id: 0, name: '', description: '' })

const siteMode = computed(() => (appStore.cachedPublicSettings?.channel_monitor_enabled === false ? 'off' : getChannelMonitorMode()))
const dirty = computed(() => !!draft.value && JSON.stringify(draft.value) !== original.value)
const intervalOptions = MONITOR_V3_INTERVALS.map((value) => ({ value, label: t('channelMonitorV3.admin.minutes', { count: value }) }))
const rangeOptions = MONITOR_V3_RANGES.map((value) => ({ value, label: t(`channelMonitorV3.page.availabilityRange.r${value}`) }))
const ttftOptions = MONITOR_V3_TTFT_THRESHOLDS.map((value) => ({ value, label: t('channelMonitorV3.admin.seconds', { count: value / 1000 }) }))
const percent = (key: 'down_error_rate' | 'degraded_error_rate') => computed({
  get: () => Math.round((draft.value?.[key] || 0) * 1000) / 10,
  set: (value: number) => { if (draft.value) draft.value[key] = Math.round((Number(value) || 0) * 10) / 1000 },
})
const downPercent = percent('down_error_rate')
const degradedPercent = percent('degraded_error_rate')
const windowHours = computed(() => (draft.value ? Number(((draft.value.cells * draft.value.interval_minutes) / 60).toFixed(1)) : 0))
const featuredOptions = computed(() => [
  { value: null, label: t('channelMonitorV3.admin.noFeatured') },
  ...(settings.value?.components || []).map((component) => ({ value: component.id, label: component.name })),
])
const stale = computed(() => {
  const through = settings.value?.data_through
  return !through || Date.now() - new Date(through).getTime() > STALE_MS
})
const freshness = computed(() => {
  const through = settings.value?.data_through
  if (!through) return t('channelMonitorV3.admin.noFacts')
  const text = t('channelMonitorV3.page.dataThrough', { time: formatMonitorV3Full(through) })
  return stale.value ? `${text} · ${t('channelMonitorV3.admin.staleFacts')}` : text
})

type Section = { key: string; category: MonitorV3Category | null; components: MonitorV3Component[] }
const sections = computed<Section[]>(() => {
  if (!settings.value) return []
  const list: Section[] = settings.value.categories.map((category) => ({
    key: `c${category.id}`,
    category,
    components: settings.value!.components.filter((component) => component.category_id === category.id),
  }))
  const loose = settings.value.components.filter((component) => component.category_id == null)
  if (loose.length) list.push({ key: 'loose', category: null, components: loose })
  return list
})

function toggleIgnored(category: string) {
  if (!draft.value) return
  const list = draft.value.ignored_error_categories
  draft.value.ignored_error_categories = list.includes(category) ? list.filter((item) => item !== category) : [...list, category]
}

async function load() {
  loading.value = true
  try {
    const next = await getSettings()
    settings.value = next
    draft.value = { ...next.config, ignored_error_categories: [...next.config.ignored_error_categories] }
    original.value = JSON.stringify(draft.value)
    loadError.value = ''
  } catch (err: unknown) {
    loadError.value = extractApiErrorMessage(err, t('channelMonitorV3.admin.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function loadPreview(end: number | null) {
  previewLoading.value = true
  try {
    preview.value = await getStatus(end, true)
    previewEnd.value = preview.value.window.latest ? null : end
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('channelMonitorV3.page.loadFailed')))
  } finally {
    previewLoading.value = false
  }
}

async function refreshAll() {
  await Promise.all([load(), loadPreview(previewEnd.value)])
}

async function loadGroups() {
  try {
    groups.value = (await adminAPI.groups.getAll()).filter((group) => group.status === 'active')
  } catch {
    groups.value = []
  }
}

async function saveConfig() {
  if (!draft.value) return
  savingConfig.value = true
  try {
    const saved = await updateConfig(draft.value)
    if (settings.value) settings.value.config = saved
    draft.value = { ...saved, ignored_error_categories: [...saved.ignored_error_categories] }
    original.value = JSON.stringify(draft.value)
    appStore.showSuccess(t('channelMonitorV3.admin.configSaved'))
    void loadPreview(previewEnd.value)
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('channelMonitorV3.admin.saveFailed')))
  } finally {
    savingConfig.value = false
  }
}

function onIdleProbeSaved(probe: MonitorV3IdleProbe) {
  if (settings.value) settings.value.idle_probe = probe
  void loadPreview(previewEnd.value)
}

function openCategory(category: MonitorV3Category | null) {
  Object.assign(categoryEditor, { show: true, id: category?.id || 0, name: category?.name || '', description: category?.description || '' })
}

async function saveCategory() {
  const input = { name: categoryEditor.name.trim(), description: categoryEditor.description.trim() }
  if (!input.name) return
  try {
    if (categoryEditor.id) await updateCategory(categoryEditor.id, input)
    else await createCategory(input)
    categoryEditor.show = false
    await refreshAll()
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('channelMonitorV3.admin.saveFailed')))
  }
}

function openComponent(component: MonitorV3Component | null, categoryId: number | null) {
  Object.assign(editor, { show: true, component, categoryId })
}

async function onComponentSaved() {
  editor.show = false
  appStore.showSuccess(t('channelMonitorV3.admin.componentSaved'))
  await refreshAll()
}

async function toggleEnabled(component: MonitorV3Component, enabled: boolean) {
  try {
    await updateComponent(component.id, {
      category_id: component.category_id, name: component.name, description: component.description, group_id: component.group_id,
      model: component.model, models: componentModels(component), degraded_ttft_ms: component.degraded_ttft_ms, show_multiplier: component.show_multiplier,
      visibility: component.visibility, enabled,
    })
    component.enabled = enabled
    void loadPreview(previewEnd.value)
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('channelMonitorV3.admin.saveFailed')))
  }
}

function swap<T>(items: T[], index: number, offset: number) {
  const next = [...items]
  const target = index + offset
  if (target < 0 || target >= next.length) return next
  ;[next[index], next[target]] = [next[target], next[index]]
  return next
}

async function persistOrder(categoryIds: number[], componentIds: number[]) {
  try {
    await reorder(categoryIds, componentIds)
    await refreshAll()
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('channelMonitorV3.admin.saveFailed')))
  }
}

function moveCategory(id: number, offset: number) {
  if (!settings.value) return
  const ids = settings.value.categories.map((category) => category.id)
  void persistOrder(swap(ids, ids.indexOf(id), offset), [])
}

function moveComponent(section: Section, id: number, offset: number) {
  if (!settings.value) return
  const ids = section.components.map((component) => component.id)
  const reordered = swap(ids, ids.indexOf(id), offset)
  // Components keep one global order; rebuild it section by section.
  const all = sections.value.flatMap((item) => (item.key === section.key ? reordered : item.components.map((component) => component.id)))
  void persistOrder([], all)
}

async function confirmDelete() {
  const target = deleting.value
  deleting.value = null
  if (!target) return
  try {
    if (target.kind === 'category') await deleteCategory(target.id)
    else await deleteComponent(target.id)
    await refreshAll()
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('channelMonitorV3.admin.saveFailed')))
  }
}

onMounted(() => {
  void load()
  void loadPreview(null)
  void loadGroups()
})
</script>

<style scoped>
.icon-btn {
  @apply grid h-7 w-7 place-items-center rounded-lg text-gray-500 transition hover:bg-gray-100 hover:text-gray-800 disabled:cursor-not-allowed disabled:opacity-30 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-gray-100;
}
</style>
