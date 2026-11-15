<template>
  <div class="space-y-6"><p v-if="tasksDraft.dirty.value || periodicDraft.dirty.value" class="text-sm text-amber-700">{{ t('qqbot.workspace.unsaved') }}</p>
    <section class="space-y-4 rounded-2xl border border-primary-100 bg-gradient-to-br from-primary-50 to-white p-5 dark:border-primary-900 dark:from-primary-950/30 dark:to-dark-800">
      <div class="flex flex-wrap items-start justify-between gap-3"><div><h2 class="text-xl font-bold">{{ t('qqbot.growth.playbookTitle') }}</h2><p class="mt-2 max-w-3xl text-sm text-gray-600 dark:text-gray-300">{{ t('qqbot.growth.help') }}</p></div><span class="badge badge-primary">{{ t('qqbot.growth.autoSettle') }}</span></div>
      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3" data-testid="growth-playbook">
        <button v-for="preset in challengePresets" :key="preset.id" type="button" :data-testid="'challenge-preset-' + preset.id" class="flex items-start gap-3 rounded-xl border border-white/80 bg-white p-4 text-left shadow-sm transition hover:border-primary-300 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 disabled:opacity-50 dark:border-dark-600 dark:bg-dark-800" :disabled="!form || busy || form.growth.tasks.length >= maxTasks || hasPreset(preset.id)" @click="addChallenge(preset)">
          <span class="text-2xl" aria-hidden="true">{{ preset.icon }}</span><span class="min-w-0"><span class="block font-semibold">{{ t('qqbot.growth.presets.' + preset.id + '.name') }}</span><span class="mt-1 block text-xs leading-5 text-gray-500">{{ t('qqbot.growth.presets.' + preset.id + '.help') }}</span><span class="mt-2 inline-block text-xs font-medium text-primary-600">{{ t(hasPreset(preset.id) ? 'qqbot.growth.presetAdded' : 'qqbot.growth.addPreset') }}</span></span>
        </button>
      </div>
      <p class="text-xs text-gray-500">{{ t('qqbot.growth.presetHelp') }}</p>
    </section>
    <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-4 text-red-700 dark:bg-red-950">{{ error }}</p>
    <p v-if="notice" role="status" class="rounded-lg bg-green-50 p-4 text-green-700 dark:bg-green-950">{{ notice }}</p>
    <form v-if="form" data-testid="one-time-growth-form" class="card space-y-5 p-6" @submit.prevent="save('tasks')">
      <fieldset class="min-w-0 space-y-4" :disabled="busy">
        <legend class="mb-2 font-medium">{{ t('qqbot.growth.tasksTitle') }}</legend>
        <p class="text-sm text-gray-500">{{ t('qqbot.growth.taskSummary', { total: form.growth.tasks.length, enabled: enabledTasks }) }}</p>
        <p class="rounded-lg bg-gray-50 p-3 text-sm text-gray-600 dark:bg-dark-800 dark:text-gray-300">{{ t('qqbot.growth.identityHelp') }}</p>
        <div class="flex flex-wrap gap-2" role="group" :aria-label="t('qqbot.growth.periodFilter')"><button v-for="period in periodFilters" :key="period" type="button" class="btn" :class="taskFilter === period ? 'btn-primary' : 'btn-secondary'" :aria-pressed="taskFilter === period" @click="taskFilter = period">{{ t('qqbot.growth.periods.' + period) }} <span class="ml-1 opacity-70">{{ periodCount(period) }}</span></button></div>
        <article v-for="({ task, index }) in visibleTasks" :key="task.id" data-testid="growth-task" class="space-y-4 rounded-lg border p-4 dark:border-dark-700">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div class="flex flex-wrap items-center gap-2"><h3 class="font-medium">{{ t('qqbot.growth.taskNumber', { number: index + 1 }) }}</h3><span class="rounded-full bg-primary-50 px-2 py-0.5 text-xs text-primary-700 dark:bg-primary-950 dark:text-primary-300">{{ t('qqbot.growth.periods.' + (task.period || 'once')) }}</span></div>
            <div class="flex items-center gap-4">
              <label class="flex items-center gap-2 text-sm"><input v-model="task.enabled" :data-testid="'task-enabled-' + index" type="checkbox" />{{ t('qqbot.growth.enabled') }}</label>
              <button type="button" class="text-sm text-red-600 hover:text-red-700 dark:text-red-400" :data-testid="'task-remove-' + index" :aria-label="t('qqbot.growth.removeTask', { number: index + 1 })" @click="form.growth.tasks.splice(index, 1)">{{ t('common.delete') }}</button>
            </div>
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            <label class="block space-y-1">{{ t('qqbot.growth.name') }}<input v-model="task.name" :data-testid="'task-name-' + index" maxlength="40" required class="input" /></label>
            <label class="block space-y-1">{{ t('qqbot.growth.kind') }}
              <select v-model="task.kind" :data-testid="'task-kind-' + index" class="input" @change="changeTaskKind(task)">
                <option v-for="kind in taskKinds(task.period)" :key="kind" :value="kind">{{ t('qqbot.growth.kinds.' + kind) }}</option>
              </select>
            </label>
            <label class="block space-y-1">{{ t('qqbot.growth.period') }}<select v-model="task.period" :data-testid="'task-period-' + index" class="input" :disabled="persistedTaskIDs.has(task.id)" :title="persistedTaskIDs.has(task.id) ? t('qqbot.growth.periodLocked') : undefined" @change="changeTaskPeriod(task)"><option v-for="period in taskPeriods(task.kind)" :key="period" :value="period">{{ t('qqbot.growth.periods.' + period) }}</option></select></label>
            <label class="block space-y-1">{{ t('qqbot.growth.targets.' + task.kind) }}<input v-model.number="task.target" :data-testid="'task-target-' + index" type="number" :min="isAmountKind(task.kind) ? 0.01 : 1" :max="targetLimit(task.kind, task.period)" :step="isAmountKind(task.kind) ? 0.01 : 1" :readonly="task.kind === 'binding'" required class="input" /></label>
            <label v-if="isDailyAmountKind(task.kind)" class="block space-y-1">{{ t('qqbot.growth.dailyAmount') }}<input v-model.number="task.daily_amount" :data-testid="'task-daily-amount-' + index" type="number" min="0.01" max="1000000000" step="0.01" required class="input" /></label>
          </div>
          <QQBotGrowthRewardFields v-model="task.reward" prefix="task" :index="index" />
          <p class="text-sm text-gray-500">{{ t('qqbot.growth.typeHelp.' + task.kind) }}</p>
          <p v-if="isDailyAmountKind(task.kind)" class="text-sm text-gray-500">{{ t('qqbot.growth.dailyAmountHelp') }}</p>
        </article>
        <p v-if="!form.growth.tasks.length" class="rounded-lg border border-dashed p-6 text-center text-sm text-gray-500 dark:border-dark-700">{{ t('qqbot.growth.empty') }}</p>
        <div class="flex flex-wrap items-end gap-3">
          <label class="min-w-0 flex-1 space-y-1">{{ t('qqbot.growth.preset') }}
            <select v-model="newTaskKind" data-testid="task-preset" class="input">
              <option v-for="kind in kinds" :key="kind" :value="kind">{{ t('qqbot.growth.kinds.' + kind) }}</option>
            </select>
          </label>
          <button type="button" class="btn btn-secondary" data-testid="add-growth-task" :disabled="form.growth.tasks.length >= maxTasks" @click="addTask('tasks')">{{ t('qqbot.growth.addTask') }}</button>
        </div>
        <p class="text-sm text-gray-500">{{ t('qqbot.growth.limitHelp', { max: maxTasks }) }}</p>
        <p class="text-xs text-gray-500">{{ t('qqbot.growth.periodLocked') }}</p>
        <p class="text-sm text-gray-500">{{ t('qqbot.growth.commands') }}</p>
      </fieldset>
      <button type="submit" class="btn btn-primary" :disabled="busy">{{ t('common.save') }}</button>
    </form>
    <p v-else-if="!error">{{ t('common.loading') }}</p>
    <section class="space-y-4" data-testid="periodic-growth-tasks">
      <h2 class="text-lg font-semibold">{{ t('qqbot.growth.periodicTitle') }}</h2>
      <p class="text-sm text-gray-500">{{ t('qqbot.growth.periodicHelp') }}</p>
      <form v-if="form" data-testid="periodic-growth-form" class="card space-y-5 p-6" @submit.prevent="save('periodic_tasks')">
        <fieldset class="min-w-0 space-y-4" :disabled="busy">
          <legend class="mb-2 font-medium">{{ t('qqbot.growth.repeatTitle') }}</legend>
          <p class="rounded-lg bg-gray-50 p-3 text-sm text-gray-600 dark:bg-dark-800 dark:text-gray-300">{{ t('qqbot.growth.periodicIdentityHelp') }}</p>
          <article v-for="(task, index) in form.growth.periodic_tasks" :key="task.id" data-testid="periodic-task" class="space-y-4 rounded-lg border p-4 dark:border-dark-700">
            <div class="flex flex-wrap items-center justify-between gap-3">
              <h3 class="font-medium">{{ t('qqbot.growth.taskNumber', { number: index + 1 }) }}</h3>
              <div class="flex items-center gap-4">
                <label class="flex items-center gap-2 text-sm"><input v-model="task.enabled" :data-testid="'periodic-enabled-' + index" type="checkbox" />{{ t('qqbot.growth.enabled') }}</label>
                <button type="button" class="text-sm text-red-600 hover:text-red-700 dark:text-red-400" :data-testid="'periodic-remove-' + index" :aria-label="t('qqbot.growth.removeTask', { number: index + 1 })" @click="form.growth.periodic_tasks.splice(index, 1)">{{ t('common.delete') }}</button>
              </div>
            </div>
            <div class="grid gap-4 sm:grid-cols-2">
              <label class="block space-y-1">{{ t('qqbot.growth.name') }}<input v-model="task.name" :data-testid="'periodic-name-' + index" maxlength="40" required class="input" /></label>
              <label class="block space-y-1">{{ t('qqbot.growth.kind') }}
                <select v-model="task.kind" :data-testid="'periodic-kind-' + index" class="input" @change="changeTaskKind(task)">
                  <option v-for="kind in periodicKinds" :key="kind" :value="kind">{{ t('qqbot.growth.kinds.' + kind) }}</option>
                </select>
              </label>
              <label class="block space-y-1">{{ t('qqbot.growth.periodicTargets.' + task.kind) }}<input v-model.number="task.target" :data-testid="'periodic-target-' + index" type="number" :min="isAmountKind(task.kind) ? 0.01 : 1" :max="targetLimit(task.kind, task.period)" :step="isAmountKind(task.kind) ? 0.01 : 1" required class="input" /></label>
              <label v-if="isDailyAmountKind(task.kind)" class="block space-y-1">{{ t('qqbot.growth.dailyAmount') }}<input v-model.number="task.daily_amount" :data-testid="'periodic-daily-amount-' + index" type="number" min="0.01" max="1000000000" step="0.01" required class="input" /></label>
            </div>
            <QQBotGrowthRewardFields v-model="task.reward" prefix="periodic" :index="index" />
            <p class="text-sm text-gray-500">{{ t('qqbot.growth.typeHelp.' + task.kind) }}</p>
            <template v-if="isDailyAmountKind(task.kind)"><p class="text-sm text-gray-500">{{ t('qqbot.growth.dailyAmountHelp') }}</p><p class="text-sm text-gray-500">{{ t('qqbot.growth.periodicDailyAmountHelp') }}</p></template>
          </article>
          <p v-if="!form.growth.periodic_tasks.length" class="rounded-lg border border-dashed p-6 text-center text-sm text-gray-500 dark:border-dark-700">{{ t('qqbot.growth.empty') }}</p>
          <div class="flex flex-wrap items-end gap-3">
            <label class="min-w-0 flex-1 space-y-1">{{ t('qqbot.growth.preset') }}
              <select v-model="newPeriodicKind" data-testid="periodic-preset" class="input">
                <option v-for="kind in periodicKinds" :key="kind" :value="kind">{{ t('qqbot.growth.kinds.' + kind) }}</option>
              </select>
            </label>
            <button type="button" class="btn btn-secondary" data-testid="add-periodic-task" :disabled="form.growth.periodic_tasks.length >= maxTasks" @click="addTask('periodic_tasks')">{{ t('qqbot.growth.addTask') }}</button>
          </div>
          <p class="text-sm text-gray-500">{{ t('qqbot.growth.limitHelp', { max: maxTasks }) }}</p>
        </fieldset>
        <button type="submit" class="btn btn-primary" :disabled="busy">{{ t('common.save') }}</button>
      </form>

    </section>
  </div>
</template>

<script setup lang="ts">
import { useQQDraft } from './useQQDraft'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { QQCheckinConfig, QQGrowthTask, QQGrowthTaskKind, QQGrowthTaskPeriod } from '@/api/qqbot'
import { loadCheckinConfig, saveGrowthSettings } from './checkinSettings'
import QQBotGrowthRewardFields from './QQBotGrowthRewardFields.vue'

import { normalizeGrowthReward, rewardEnabled, validGrowthReward } from './growthReward'

const { t } = useI18n()
const dailyAmountKinds: QQGrowthTaskKind[] = ['usage_amount_days', 'usage_amount_streak', 'recharge_days', 'recharge_streak']
const kinds: QQGrowthTaskKind[] = ['binding', 'usage_count', 'usage_days', 'model_count', 'invite_count', 'recharge_total', 'usage_tokens', 'usage_amount', ...dailyAmountKinds]
const periodicKinds: QQGrowthTaskKind[] = ['usage_amount', 'usage_tokens', 'usage_count', 'recharge_total', ...dailyAmountKinds]
const targets: Record<QQGrowthTaskKind, number> = { binding: 1, usage_count: 10, checkin_total: 7, checkin_streak: 7, recharge_total: 10, usage_tokens: 1_000_000, usage_amount: 10, usage_days: 3, model_count: 3, invite_count: 3, usage_amount_days: 7, usage_amount_streak: 7, recharge_days: 7, recharge_streak: 7 }
const maxTasks = 12
const newTaskKind = ref<QQGrowthTaskKind>('usage_count')
const newPeriodicKind = ref<QQGrowthTaskKind>('usage_amount')
const form = ref<QQCheckinConfig | null>(null)
const persistedTaskIDs = ref(new Set<string>())
const tasksDraft = useQQDraft(() => form.value?.growth.tasks)
const periodicDraft = useQQDraft(() => form.value?.growth.periodic_tasks)
const enabledTasks = computed(() => form.value?.growth.tasks.filter(task => task.enabled && rewardEnabled(task.reward)).length ?? 0)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const periods: QQGrowthTaskPeriod[] = ['once', 'daily', 'weekly']
const periodFilters = ['all', ...periods] as const
const taskFilter = ref<(typeof periodFilters)[number]>('all')
const visibleTasks = computed(() => (form.value?.growth.tasks ?? []).map((task, index) => ({ task, index })).filter(({ task }) => taskFilter.value === 'all' || (task.period || 'once') === taskFilter.value))
function periodCount(period: (typeof periodFilters)[number]) { return form.value?.growth.tasks.filter(task => period === 'all' || (task.period || 'once') === period).length ?? 0 }
interface ChallengePreset { id: string; icon: string; kind: QQGrowthTaskKind; target: number; period: QQGrowthTaskPeriod; chances: number }
const challengePresets: ChallengePreset[] = [
  { id: 'first_call', icon: '🚀', kind: 'usage_count', target: 1, period: 'once', chances: 1 },
  { id: 'daily_sprint', icon: '⚡', kind: 'usage_count', target: 3, period: 'daily', chances: 1 },
  { id: 'weekly_regular', icon: '🗓️', kind: 'usage_days', target: 3, period: 'weekly', chances: 2 },
  { id: 'model_explorer', icon: '🧭', kind: 'model_count', target: 3, period: 'once', chances: 2 },
  { id: 'weekly_explorer', icon: '🔭', kind: 'model_count', target: 3, period: 'weekly', chances: 1 },
  { id: 'community_builder', icon: '🤝', kind: 'invite_count', target: 3, period: 'once', chances: 3 },
]
function hasPreset(id: string) { return form.value?.growth.tasks.some(task => task.id === 'challenge_' + id) ?? false }
function addChallenge(preset: ChallengePreset) {
  if (!form.value || busy.value || form.value.growth.tasks.length >= maxTasks || hasPreset(preset.id)) return
  form.value.growth.tasks.push({ id: 'challenge_' + preset.id, name: t('qqbot.growth.presets.' + preset.id + '.name'), kind: preset.kind, target: preset.target, period: preset.period, chances: preset.chances, reward: normalizeGrowthReward(undefined, preset.chances), enabled: false })
  taskFilter.value = 'all'
}
function isDailyAmountKind(kind: QQGrowthTaskKind) { return dailyAmountKinds.includes(kind) }
function taskPeriods(kind: QQGrowthTaskKind) { return isDailyAmountKind(kind) ? ['once'] : periods }
function taskKinds(period?: QQGrowthTaskPeriod) { return kinds.filter(kind => !period || period === 'once' || (kind !== 'binding' && kind !== 'recharge_total' && !isDailyAmountKind(kind))) }
function changeTaskPeriod(task: QQGrowthTask) {
  if (!taskKinds(task.period).includes(task.kind)) {
    task.kind = 'usage_count'
    task.target = targets.usage_count
    delete task.daily_amount
  }
  task.target = Math.min(task.target, targetLimit(task.kind, task.period))
}
function changeTaskKind(task: QQGrowthTask) {
  task.target = Math.min(targets[task.kind], targetLimit(task.kind, task.period))
  if (isDailyAmountKind(task.kind)) { task.daily_amount = 10; task.period = 'once' }
  else delete task.daily_amount
}


function isAmountKind(kind: QQGrowthTaskKind) { return kind === 'recharge_total' || kind === 'usage_amount' }

function targetLimit(kind: QQGrowthTaskKind, period?: QQGrowthTaskPeriod) {
  if (kind === 'usage_days') return period === 'daily' ? 1 : period === 'weekly' ? 7 : 3650
  if (kind === 'model_count') return 10000
  return kind === 'binding' ? 1 : kind === 'usage_count' || kind === 'invite_count' ? 1_000_000 : kind === 'usage_tokens' ? 1_000_000_000_000_000 : isAmountKind(kind) ? 1_000_000_000 : 3650
}

function addTask(section: 'tasks' | 'periodic_tasks') {
  if (!form.value || busy.value || form.value.growth[section].length >= maxTasks) return
  const kind = section === 'tasks' ? newTaskKind.value : newPeriodicKind.value
  form.value.growth[section].push({ id: 'task_' + crypto.randomUUID().replace(/-/g, ''), name: Array.from(t('qqbot.growth.kinds.' + kind)).slice(0, 40).join(''), kind, target: targets[kind], ...(section === 'tasks' || isDailyAmountKind(kind) ? { period: 'once' as const } : {}), ...(isDailyAmountKind(kind) ? { daily_amount: 10 } : {}), chances: 1, reward: normalizeGrowthReward(undefined, 1), enabled: false })
}

function validTask(task: QQGrowthTask) {
  return task.name.trim().length > 0 && Array.from(task.name).length <= 40 && !/[\r\n\t]/.test(task.name)
    && kinds.includes(task.kind) && taskKinds(task.period).includes(task.kind) && (!task.period || periods.includes(task.period)) && Number.isFinite(task.target) && task.target >= (isAmountKind(task.kind) ? 0.01 : 1)
    && task.target <= targetLimit(task.kind, task.period)
    && (isAmountKind(task.kind) ? Number(task.target.toFixed(2)) === task.target : Number.isInteger(task.target))
    && (!isDailyAmountKind(task.kind) || (typeof task.daily_amount === 'number' && Number.isFinite(task.daily_amount) && task.daily_amount >= 0.01 && task.daily_amount <= 1_000_000_000 && Number(task.daily_amount.toFixed(2)) === task.daily_amount))
    && validGrowthReward(task.reward)
}

async function save(section: 'tasks' | 'periodic_tasks') {
  if (!form.value || busy.value) return
  error.value = ''; notice.value = ''
  const tasks = form.value.growth[section]
  if (tasks.length > maxTasks || !tasks.every(task => validTask(task) && (section === 'tasks' || periodicKinds.includes(task.kind)))) {
    error.value = t('qqbot.growth.invalidTask')
    return
  }
  busy.value = true
  try {
    const saved = await saveGrowthSettings(form.value, section)
    form.value.growth[section] = saved.growth[section] ?? []
    if (section === 'tasks') persistedTaskIDs.value = new Set(form.value.growth.tasks.map(task => task.id))
    form.value._version = saved._version
    if (saved._sections) form.value._sections = { ...form.value._sections, [section]: saved._sections[section]! }
    ;(section === 'tasks' ? tasksDraft : periodicDraft).markSaved()
    notice.value = t('common.saved')
  }
  catch (e) { error.value = (e as Error).message || t('common.error') }
  finally { busy.value = false }
}

onMounted(async () => {
  try { form.value = await loadCheckinConfig(); persistedTaskIDs.value = new Set(form.value.growth.tasks.map(task => task.id)) }
  catch (e) { error.value = (e as Error).message || t('common.error') }
})
</script>
