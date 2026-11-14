<template>
  <section class="card space-y-5 p-6">
    <h2 class="text-lg font-semibold">{{ t('qqbot.simple.templates') }}</h2><p class="text-sm text-gray-500">{{ t('qqbot.simple.templatesHelp') }}</p>
    <p v-if="error" role="alert" class="text-red-600">{{ error }}</p><p v-if="notice" role="status" class="text-green-700">{{ notice }}</p>
    <button v-if="!bundles" class="btn btn-secondary" :disabled="busy" @click="load">{{ t('common.refresh') }}</button>
    <template v-if="bundles">
      <form class="space-y-3 rounded-lg border p-4 dark:border-dark-700" @submit.prevent="saveBudget">
        <h3 class="font-medium">{{ t('qqbot.simple.rewardBudget') }}</h3>
        <label class="flex items-center gap-2 text-sm"><input v-model="unlimited" type="checkbox" :disabled="busy" />{{ t('qqbot.simple.unlimited') }}</label>
        <input v-model.number="budget" :disabled="busy || unlimited" type="number" class="input" min="0" max="1000000" step="0.00000001" required :aria-label="t('qqbot.simple.rewardBudget')" data-testid="reward-budget" />
        <p class="text-sm text-gray-500">{{ t('qqbot.simple.budgetHelp') }}</p><button class="btn btn-secondary" :disabled="busy">{{ t('qqbot.simple.saveBudget') }}</button>
      </form>
      <div class="grid gap-3 sm:grid-cols-2"><button v-for="item in presets" :key="item" type="button" class="rounded-xl border p-4 text-left dark:border-dark-700" :class="selected === item ? 'ring-2 ring-primary-500' : ''" :disabled="busy" @click="selected = item; confirmed = false"><strong>{{ t('qqbot.simple.preset.' + item) }}</strong><p class="mt-2 text-sm text-gray-500">{{ t('qqbot.simple.preset.' + item + 'Help') }}</p></button></div>
      <template v-if="preview">
        <ul class="space-y-2 rounded-lg bg-gray-50 p-4 text-sm dark:bg-dark-800"><li>{{ t('qqbot.simple.previewCheckin', { min: preview.checkin.min_amount, max: preview.checkin.max_amount }) }}</li><li>{{ t('qqbot.simple.previewTasks') }}</li><li>{{ t(preview.lottery.enabled ? 'qqbot.simple.previewLottery' : 'qqbot.simple.noLottery') }}</li><li>{{ t(preview.invite.enabled ? 'qqbot.simple.previewInvite' : 'qqbot.simple.noInvite') }}</li><li>{{ t(preview.ranking.rewards_enabled ? 'qqbot.simple.previewRanking' : 'qqbot.simple.noRanking') }}</li></ul>
        <p class="text-sm text-amber-700 dark:text-amber-400">{{ t('qqbot.simple.preserveTasks') }}</p>
        <details><summary class="cursor-pointer text-sm">{{ t('qqbot.simple.fullPreview') }}</summary><div class="mt-3 grid gap-3 md:grid-cols-2"><div><h4 class="text-sm font-medium">{{ t('qqbot.simple.currentRules') }}</h4><pre class="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded bg-gray-50 p-3 text-xs dark:bg-dark-800">{{ JSON.stringify(bundles.current, null, 2) }}</pre></div><div><h4 class="text-sm font-medium">{{ t('qqbot.simple.nextRules') }}</h4><pre class="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded bg-gray-50 p-3 text-xs dark:bg-dark-800">{{ JSON.stringify({ ...preview, budget: unlimited ? null : budget }, null, 2) }}</pre></div></div></details>
        <label class="flex items-start gap-2 text-sm"><input v-model="confirmed" type="checkbox" class="mt-1" :disabled="busy" />{{ t('qqbot.simple.confirmRewards') }}</label>
        <button type="button" class="btn btn-primary" :disabled="busy || !confirmed || unlimited || !(budget > 0)" data-testid="apply-template" @click="apply">{{ t('qqbot.simple.applyTemplate') }}</button>
      </template>
    </template>
  </section>
</template>
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { qqbotAPI, type QQActivityBundle, type QQBotConfig } from '@/api/qqbot'
import { useQQDraft } from './useQQDraft'
const props = defineProps<{ beforeApply?: () => boolean }>()
const emit = defineEmits<{ applied: [] }>()
const { t } = useI18n()
const bundles = ref<Record<'current' | 'light' | 'active', QQActivityBundle>>(), config = ref<QQBotConfig>()
const busy = ref(false), error = ref(''), notice = ref(''), budget = ref(0), unlimited = ref(false), confirmed = ref(false), selected = ref<'light' | 'active'>('light')
const presets = ['light', 'active'] as const
const preview = computed(() => bundles.value?.[selected.value])
const { markSaved } = useQQDraft(() => bundles.value ? { budget: budget.value, unlimited: unlimited.value } : undefined)
async function load() { busy.value = true; error.value = ''; try { const [b, c] = await Promise.all([qqbotAPI.activityTemplates(), qqbotAPI.config()]); config.value = c; budget.value = b.current.budget ?? 0; unlimited.value = b.current.budget == null; bundles.value = b; confirmed.value = false; markSaved() } catch (e) { error.value = (e as Error).message } finally { busy.value = false } }
async function saveBudget() { if (!config.value || busy.value) return; if (unlimited.value && !window.confirm(t('qqbot.simple.unlimitedConfirm'))) return; busy.value = true; error.value = ''; notice.value = ''; try { config.value = await qqbotAPI.saveSection('limits', config.value.section_versions?.limits || '', { ...config.value, reward_budget_daily: unlimited.value ? null : budget.value }); await load(); notice.value = t('common.saved') } catch (e) { error.value = (e as Error).message } finally { busy.value = false } }
async function apply() { if (busy.value || !bundles.value || !confirmed.value || unlimited.value || budget.value <= 0 || (props.beforeApply && !props.beforeApply())) return; busy.value = true; error.value = ''; notice.value = ''; try { await qqbotAPI.applyActivityTemplate({ version: bundles.value.current.version, template: selected.value, budget: budget.value, confirmed: true }); await load(); emit('applied'); notice.value = t('qqbot.simple.templateApplied') } catch (e) { error.value = (e as Error).message } finally { busy.value = false } }
onMounted(load)
</script>
