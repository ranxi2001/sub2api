<template>
  <section class="card space-y-4 p-6" :aria-label="t('qqbot.autonomous.title')">
    <h2 class="text-lg font-semibold">{{ t('qqbot.autonomous.title') }}</h2>
    <p class="text-sm text-gray-500">{{ t('qqbot.autonomous.help') }}</p>
    <label class="flex items-center justify-between gap-4 rounded-xl bg-primary-50 p-4 dark:bg-primary-950/30">
      <span class="font-medium">{{ t('qqbot.autonomous.enabled') }}</span>
      <input v-model="enabled" data-testid="autonomous-enabled" type="checkbox" role="switch" class="h-5 w-5" />
    </label>
    <p class="text-sm text-gray-500">{{ t('qqbot.autonomous.simpleHelp') }}</p>
    <label class="block space-y-2 text-sm">
      <span class="font-medium">{{ t('qqbot.autonomous.replyMode') }}</span>
      <select v-model="value.reply_mode" data-testid="autonomous-mode" class="input">
        <option v-for="mode in modes" :key="mode" :value="mode">{{ t('qqbot.autonomous.modes.' + mode) }}</option>
      </select>
    </label>
    <p class="text-sm text-gray-500">{{ t('qqbot.autonomous.modeHelp.' + (value.reply_mode || 'natural')) }}</p>
    <div v-if="value.reply_mode === 'always'" class="space-y-2 rounded-lg bg-primary-50 p-3 text-sm dark:bg-primary-950/30">
      <p>{{ t('qqbot.autonomous.alwaysHelp') }}</p>
      <button type="button" class="btn btn-secondary" data-testid="autonomous-apply-always" @click="applyPreset('allMessages')">{{ t('qqbot.autonomous.applyAlways') }}</button>
    </div>
    <p class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">{{ t('qqbot.autonomous.deliverySummary', { cooldown: value.cooldown_seconds, hourly: value.hourly_replies, daily: value.daily_replies, calls: value.daily_calls, merge: value.merge_seconds ?? 0, global: props.totalDailyChatLimit ?? '—' }) }}</p>
    <p v-if="blockingLimits" data-testid="autonomous-limit-warning" class="text-sm text-amber-700 dark:text-amber-300">{{ t('qqbot.autonomous.blockingLimits') }}</p>
    <p v-if="!groups?.length" class="text-sm text-amber-700 dark:text-amber-300">{{ t('qqbot.autonomous.noGroups') }}</p>
    <details data-testid="autonomous-advanced">
      <summary class="cursor-pointer text-sm">{{ t('qqbot.workspace.advanced') }}</summary>
      <div class="mt-4 space-y-4">
        <p class="text-xs text-gray-500">{{ t('qqbot.autonomous.customHelp') }}</p>
        <p class="text-sm font-medium">{{ t('qqbot.autonomous.frequencyPresets') }}</p>
        <div class="flex flex-wrap gap-2"><button v-for="preset in autonomousPresets" :key="preset.key" type="button" class="btn btn-secondary" @click="applyPreset(preset.key)">{{ preset.key === 'allMessages' ? t('qqbot.autonomous.allMessages') : t('qqbot.workspace.' + preset.key) }}</button></div>
        <p v-if="value.reply_mode !== 'always'" class="text-sm text-gray-500">{{ t('qqbot.workspace.behaviorSummary', { sample: value.sample_percent, cooldown: value.cooldown_seconds, messages: value.min_messages, hourly: value.hourly_replies, daily: value.daily_replies, calls: value.daily_calls, start: value.start_hour, end: value.end_hour }) }}</p>
        <label class="flex items-center gap-2 text-sm"><input v-model="value.inherit_groups" type="checkbox" />{{ t('qqbot.simple.inheritGroups') }}</label>
        <QQGroupPicker v-if="!value.inherit_groups" v-model="value.groups" :groups="groups || []" :policies="policies" :names="props.names" :label="t('qqbot.autonomous.groups')" />
        <div class="grid gap-4 md:grid-cols-2">
          <label class="space-y-1">{{ t('qqbot.autonomous.names') }}<textarea v-model="botNames" data-testid="autonomous-names" class="input" rows="2" /></label>
          <label class="space-y-1 md:col-span-2">{{ t('qqbot.autonomous.topics') }}<textarea v-model="topics" data-testid="autonomous-topics" class="input" rows="2" /></label>
          <label v-for="field in fields" :key="field.key" class="space-y-1">{{ t('qqbot.autonomous.' + field.key) }}<input v-model.number="value[field.key]" :data-testid="'autonomous-' + field.key" type="number" :min="field.min" :max="field.max" :disabled="value.reply_mode === 'always' && (field.key === 'sample_percent' || field.key === 'min_messages')" step="1" required class="input disabled:opacity-50" /></label>
        </div>
        <label class="flex items-center gap-2"><input v-model="value.mention_user" data-testid="autonomous-mention" type="checkbox" />{{ t('qqbot.autonomous.mention') }}</label>
        <p class="text-xs text-gray-500">{{ t('qqbot.autonomous.limitsHelp') }}</p>
      </div>
    </details>
    <details>
      <summary class="cursor-pointer text-sm font-medium">{{ t('qqbot.autonomous.stats') }}</summary>
      <div v-for="summary in summaries" :key="summary.group" data-testid="autonomous-summary" class="mt-3 space-y-3 rounded-lg border p-4 dark:border-dark-700">
        <p class="break-all font-medium">{{ props.names?.[summary.group] || summary.group }}</p>
        <div class="grid grid-cols-2 gap-3 md:grid-cols-3">
          <div v-for="key in funnelKeys" :key="key"><p class="text-xs text-gray-500">{{ reasonLabel(key) }}</p><p class="text-lg font-semibold">{{ summary.metrics[key] ?? 0 }}</p></div>
        </div>
        <p class="text-sm">{{ t('qqbot.autonomous.rates', { selected: summary.selectionRate, reply: summary.replyRate, delivered: summary.deliveryRate }) }}</p>
        <p class="text-sm">{{ t('qqbot.autonomous.costSummary', { input: summary.metrics.input_tokens ?? 0, output: summary.metrics.output_tokens ?? 0, skipped: summary.metrics.skip_tokens ?? 0, discarded: summary.metrics.discarded_tokens ?? 0, errors: summary.metrics.error_tokens ?? 0 }) }}</p>
        <p class="text-xs text-gray-500">{{ t('qqbot.autonomous.latencySummary', { model: summary.metrics.model_latency_ms ?? 0, delivery: summary.metrics.delivery_latency_ms ?? 0 }) }}</p>
      </div>
      <p class="mt-3 text-xs text-gray-500">{{ t('qqbot.autonomous.statsHelp') }}</p>
      <p v-if="!runtime?.autonomous_stats?.length" class="mt-3 text-sm text-gray-500">{{ t('qqbot.autonomous.empty') }}</p>
      <div v-for="stat in runtime?.autonomous_stats ?? []" :key="stat.group_id + stat.reason" class="mt-2 text-sm break-all">{{ stat.group_id }} · {{ reasonLabel(stat.reason) }}: {{ stat.count }}</div>
    </details>
    <details>
      <summary class="cursor-pointer text-sm font-medium">{{ t('qqbot.autonomous.records') }}</summary>
      <p v-if="!jobs.length" class="mt-3 text-sm text-gray-500">{{ t('qqbot.autonomous.empty') }}</p>
      <div v-for="job in jobs" :key="job.id" class="mt-3 space-y-1 rounded-lg border p-3 text-sm dark:border-dark-700">
        <p class="break-all">{{ job.group_id }} · {{ new Date(job.created_at).toLocaleString() }}</p>
        <p>{{ reasonLabel(job.trigger_reason || '') }} · {{ reasonLabel(job.decision_reason || job.status) }}</p>
        <p>Tokens: {{ job.input_tokens }} / {{ job.output_tokens }}</p>
        <p v-if="job.autonomous_data?.batch_messages">{{ t('qqbot.autonomous.batchSummary', { count: job.autonomous_data.batch_messages, duration: job.autonomous_data.model_ms ?? 0 }) }}</p>
        <p v-if="job.autonomous_data?.input_estimate" class="text-xs text-gray-500">{{ t('qqbot.autonomous.inputSummary', job.autonomous_data.input_estimate) }}</p>
        <p v-if="job.error" class="text-red-600 dark:text-red-400">{{ job.error }}</p>
      </div>
    </details>
  </section>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import QQGroupPicker from './QQGroupPicker.vue'
import { enableAutonomousChat, autonomousPresets, applyAutonomousPreset, autonomousSummary } from './autonomousDefaults'
import type { QQAutonomousConfig, QQBotRuntime, QQGroupPolicy } from '@/api/qqbot'
const value = defineModel<QQAutonomousConfig>({ required: true })
const props = defineProps<{ runtime: QQBotRuntime | null; groups?: string[]; policies?: QQGroupPolicy[]; names?: Record<string, string>; totalDailyChatLimit?: number; chatDisabled?: boolean }>()
const { t, te } = useI18n()
const enabled = computed({
  get: () => value.value.enabled,
  set: enabled => { value.value = enabled ? enableAutonomousChat(value.value) : { ...value.value, enabled: false } }
})
const list = (key: 'groups' | 'names' | 'topics') => computed({
  get: () => (value.value[key] ?? []).join('\n'),
  set: (text: string) => { value.value[key] = [...new Set(text.split(/[\n,，]+/).map(s => s.trim()).filter(Boolean))] }
})
const botNames = list('names'), topics = list('topics')
const jobs = computed(() => props.runtime?.jobs.filter(job => job.kind === 'ambient_chat' || job.trigger_reason === 'name') ?? [])
const modes = ['quiet', 'natural', 'active', 'always'] as const
const funnelKeys = ['received', 'eligible', 'selected', 'model_calls', 'generated', 'sent'] as const
const summaries = computed(() => autonomousSummary(props.runtime?.autonomous_stats ?? []))
const blockingLimits = computed(() => {
  const a = value.value
  return props.chatDisabled || a.daily_calls === 0 || a.daily_replies === 0 || a.hourly_replies === 0 || props.totalDailyChatLimit === 0 ||
    (props.totalDailyChatLimit != null && props.totalDailyChatLimit < a.daily_calls) ||
    props.policies?.some(p => (a.inherit_groups || a.groups.includes(p.group_id)) && (p.disable_autonomous || p.disable_chat || (p.chat_daily_limit != null && p.chat_daily_limit < a.daily_calls)))
})
function reasonLabel(reason: string) { const key = 'qqbot.autonomous.reasons.' + reason; return te(key) ? t(key) : reason }
function applyPreset(key: typeof autonomousPresets[number]['key']) { value.value = applyAutonomousPreset(value.value, key) }
const fields = [
  { key: 'sample_percent', min: 0, max: 100 }, { key: 'cooldown_seconds', min: 1, max: 3600 },
  { key: 'min_messages', min: 0, max: 100 }, { key: 'member_cooldown_seconds', min: 0, max: 86400 },
  { key: 'hourly_replies', min: 0, max: 3600 }, { key: 'daily_replies', min: 0, max: 50000 },
  { key: 'daily_calls', min: 0, max: 60000 }, { key: 'followup_seconds', min: 0, max: 120 },
  { key: 'merge_seconds', min: 0, max: 5 }, { key: 'input_token_budget', min: 2048, max: 32768 },
  { key: 'start_hour', min: 0, max: 23 }, { key: 'end_hour', min: 0, max: 23 }, { key: 'max_chars', min: 20, max: 300 }
] as const
</script>
