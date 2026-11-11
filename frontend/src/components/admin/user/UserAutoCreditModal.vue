<template>
  <BaseDialog :show="show" :title="t('autoCredit.title')" width="wide" @close="emit('close')">
    <p v-if="!auth.isAdmin" class="text-sm text-red-600">{{ t('autoCredit.restricted') }}</p>
    <div v-else class="space-y-5">
      <div class="flex flex-wrap items-center gap-2">
        <p class="break-all font-medium">{{ user?.email }}</p>
        <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.vipAutoCredit" :user-id="auth.user?.id" />
      </div>
      <div v-if="error" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/40 dark:text-red-300">{{ error }}</div>
      <p v-if="loading">{{ t('autoCredit.loading') }}</p>
      <template v-if="state">
        <div class="rounded-xl border border-amber-300 bg-amber-50 p-4 text-amber-900 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-200">
          <p class="text-lg font-semibold" data-testid="outstanding">{{ t('autoCredit.debt', { amount: Number(state.outstanding).toFixed(2) }) }}</p>
          <p class="mt-1 text-sm">{{ t('autoCredit.note') }}</p>
          <p v-if="state.cap_blocked" class="mt-2 font-semibold" data-testid="cap-blocked">{{ t('autoCredit.blocked') }}</p>
        </div>
        <form class="space-y-3" @submit.prevent="save">
          <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('autoCredit.explanation') }}</p>
          <label class="flex items-center gap-2 font-medium"><input v-model="form.enabled" type="checkbox" name="enabled" :disabled="busy || loading" />{{ t('autoCredit.enabled') }}</label>
          <div class="space-y-1">
            <div class="flex items-start gap-2">
              <label class="flex min-w-0 items-start gap-2 font-medium"><input v-model="form.notify_email" type="checkbox" name="notify_email" class="mt-1 shrink-0" :disabled="busy || loading" /><span>{{ t('autoCredit.notifyEmail') }}</span></label>
              <NewFeatureBadge :notice-id="NEW_FEATURE_NOTICES.vipAutoCreditEmail" :user-id="auth.user?.id" />
            </div>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('autoCredit.notifyEmailHelp') }}</p>
          </div>
          <div class="grid gap-3 sm:grid-cols-3">
            <label v-for="field in moneyFields" :key="field" class="block text-sm">
              <span class="mb-1 block">{{ t(`autoCredit.${field}`) }}</span>
              <input v-model="form[field]" :name="field" type="text" inputmode="decimal" maxlength="16" autocomplete="off" class="input w-full" :disabled="busy || loading" :required="form.enabled" />
            </label>
          </div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('autoCredit.precision') }}</p>
          <button type="submit" class="btn btn-primary" :disabled="busy || loading">{{ t('autoCredit.save') }}</button>
        </form>
        <section class="space-y-3" :aria-label="t('autoCredit.history')">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <h4 class="font-semibold">{{ t('autoCredit.history') }}</h4>
            <select v-model="status" :disabled="busy || loading" class="input w-auto" :aria-label="t('autoCredit.history')" @change="load()">
              <option v-for="s in statuses" :key="s" :value="s">{{ t(`autoCredit.${s}`) }}</option>
            </select>
          </div>
          <p v-if="!state.entries.length" class="text-sm text-gray-500">{{ t('autoCredit.noHistory') }}</p>
          <article v-for="entry in state.entries" :key="entry.id" class="rounded-xl border border-gray-200 p-3 dark:border-dark-600">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <p class="font-semibold">#{{ entry.id }} · ${{ Number(entry.amount).toFixed(2) }}</p>
              <span :class="entry.status === 'pending' ? 'text-amber-700 dark:text-amber-300' : 'text-emerald-700 dark:text-emerald-300'">{{ t(`autoCredit.${entry.status}`) }}</span>
            </div>
            <p class="mt-1 text-xs text-gray-500">{{ entry.created_at }} · {{ t('autoCredit.beforeAfter', { before: Number(entry.balance_before).toFixed(2), after: Number(entry.balance_after).toFixed(2) }) }}</p>
            <p class="mt-2 break-words text-sm">{{ entry.note }}</p>
            <p v-if="entry.settled_by" class="mt-1 text-xs text-gray-500">{{ t('autoCredit.collectedBy', { id: entry.settled_by }) }} · {{ entry.settled_at }}</p>
            <button v-if="entry.status === 'pending'" type="button" :data-testid="`settle-${entry.id}`" class="btn btn-secondary mt-3" :disabled="busy || loading" @click="confirming = entry">{{ t('autoCredit.settle') }}</button>
          </article>
          <button v-if="state.has_more" type="button" data-testid="older-history" class="btn btn-secondary" :disabled="busy || loading" @click="load(true)">{{ t('autoCredit.more') }}</button>
        </section>
        <div v-if="confirming" role="alertdialog" :aria-label="t('autoCredit.settle')" class="rounded-xl border-2 border-amber-500 bg-amber-50 p-4 dark:bg-amber-950/40">
          <p>{{ t('autoCredit.confirmation', { id: confirming.id, amount: Number(confirming.amount).toFixed(2) }) }}</p>
          <div class="mt-3 flex flex-wrap gap-2">
            <button type="button" data-testid="confirm-settlement" class="btn btn-primary" :disabled="busy" @click="settle">{{ t('autoCredit.confirm') }}</button>
            <button type="button" class="btn btn-secondary" :disabled="busy" @click="confirming = null">{{ t('autoCredit.cancel') }}</button>
          </div>
        </div>
      </template>
      <button type="button" class="btn btn-secondary" :disabled="busy || loading" @click="load()">{{ t('autoCredit.reload') }}</button>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useAuthStore } from '@/stores/auth'
import { getAutoCredit, saveAutoCredit, settleAutoCredit, type AutoCreditEntry, type AutoCreditState, type CreditStatus } from '@/api/admin/autoCredit'
import type { AdminUser } from '@/types'
const props = defineProps<{ show: boolean; user: AdminUser | null }>()
const emit = defineEmits<{ (event: 'close'): void; (event: 'success'): void }>()
const { t } = useI18n()
const auth = useAuthStore()
const state = ref<AutoCreditState | null>(null)
const status = ref<CreditStatus>('all')
const statuses: CreditStatus[] = ['all', 'pending', 'settled']
const moneyFields = ['threshold', 'amount', 'cap'] as const
const form = reactive({ enabled: false, notify_email: false, threshold: '', amount: '', cap: '' })
const error = ref('')
const loading = ref(false)
const busy = ref(false)
const confirming = ref<AutoCreditEntry | null>(null)
let generation = 0
function message(e: unknown): string {
  if (e && typeof e === 'object' && 'reason' in e) {
    if (e.reason === 'AUTO_CREDIT_INVALID') return t('autoCredit.invalid')
    if (e.reason === 'AUTO_CREDIT_CONFLICT') return t('autoCredit.conflict')
  }
  return e && typeof e === 'object' && 'message' in e && typeof e.message === 'string' ? e.message : t('autoCredit.error')
}
function apply(s: AutoCreditState) {
  state.value = s
  form.enabled = s.config.enabled
  form.notify_email = s.config.notify_email ?? false
  for (const field of moneyFields) form[field] = s.config[field] ?? ''
}
async function load(older = false) {
  if (!props.show || !props.user || !auth.isAdmin) return
  const current = ++generation
  loading.value = true; error.value = ''; confirming.value = null
  try {
    const s = await getAutoCredit(props.user.id, status.value, older ? state.value?.next_before_id ?? 0 : 0)
    if (current !== generation) return
    if (older && state.value) state.value = { ...s, entries: [...state.value.entries, ...s.entries] }
    else apply(s)
  } catch (e) { if (current === generation) error.value = message(e) }
  finally { if (current === generation) loading.value = false }
}
async function save() {
  if (!auth.isAdmin || !props.user || !state.value || busy.value || loading.value) return
  const current = generation
  busy.value = true; error.value = ''
  try {
    const s = await saveAutoCredit(props.user.id, { version: state.value.config.version, enabled: form.enabled, notify_email: form.notify_email,
      threshold: form.threshold.trim() || null, amount: form.amount.trim() || null, cap: form.cap.trim() || null })
    if (current !== generation) return
    status.value = 'all'; confirming.value = null; apply(s); emit('success')
  } catch (e) { if (current === generation) error.value = message(e) }
  finally { busy.value = false }
}
async function settle() {
  if (!auth.isAdmin || !props.user || !confirming.value || busy.value) return
  const current = generation
  busy.value = true; error.value = ''
  try {
    const s = await settleAutoCredit(props.user.id, confirming.value.id)
    if (current !== generation) return
    status.value = 'all'; confirming.value = null; apply(s); emit('success')
  } catch (e) { if (current === generation) error.value = message(e) }
  finally { busy.value = false }
}
watch(() => [props.show, props.user?.id, auth.isAdmin], () => {
  generation++; state.value = null; confirming.value = null; status.value = 'all'; error.value = ''; loading.value = false
  void load()
}, { immediate: true })
</script>
