<template>
  <AppLayout>
    <QQCommunityNav />
    <div class="mx-auto max-w-7xl space-y-6">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div><h1 class="text-2xl font-semibold">{{ t('qqbot.workspace.records') }}</h1><p class="mt-2 text-sm text-gray-500">{{ t('qqbot.recordsHelp') }}</p></div>
        <div class="flex gap-2"><button v-if="recordMode !== 'activities'" class="btn btn-secondary" :disabled="appLoading" data-testid="qq-records-refresh-app" @click="loadApp">{{ t('common.refresh') }}</button><RouterLink class="btn btn-secondary" :to="{ path: '/admin/qqbot/activities', query: { tab: activeTab } }">{{ t('qqbot.activitiesTitle') }}</RouterLink></div>
      </div>
      <div class="flex flex-wrap gap-2"><RouterLink v-for="mode in recordModes" :key="mode" :to="{ query: { tab: mode } }" class="btn" :class="recordMode === mode ? 'btn-primary' : 'btn-secondary'">{{ t(`qqbot.workspace.${mode}`) }}</RouterLink></div>
      <p v-if="appError && recordMode !== 'activities'" role="alert" class="text-red-600">{{ appError }}</p>
      <QQBotDiagnostics v-if="recordMode === 'diagnostics'" />
      <QQBotConfigHistory v-else-if="recordMode === 'history'" />
      <QQJobRecords v-else-if="recordMode === 'tasks' || recordMode === 'notifications'" :notifications="recordMode === 'notifications'" :app-id="appId" />
      <QQModerationRecords v-else-if="recordMode === 'moderation'" :app-id="appId" />
      <template v-else>
      <nav role="tablist" :aria-label="t('qqbot.recordsTitle')" class="flex gap-1 rounded-xl border border-gray-200 bg-gray-50 p-1 dark:border-dark-700 dark:bg-dark-800">
        <button v-for="tab in tabs" :id="`records-tab-${tab.key}`" :key="tab.key" type="button" role="tab"
          :aria-selected="activeTab === tab.key" :aria-controls="`records-panel-${tab.key}`" :tabindex="activeTab === tab.key ? 0 : -1"
          class="min-h-[44px] min-w-0 flex-1 rounded-lg px-2 py-2 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
          :class="activeTab === tab.key ? 'bg-white text-primary-700 shadow-sm dark:bg-dark-700 dark:text-primary-300' : 'text-gray-600 hover:bg-white/60 dark:text-gray-300 dark:hover:bg-dark-700/60'"
          @click="selectTab(tab.key)" @keydown="handleKeydown($event, tab.key)">{{ t(tab.label) }}</button>
      </nav>
      <section :id="`records-panel-${activeTab}`" role="tabpanel" :aria-labelledby="`records-tab-${activeTab}`" :aria-busy="state.loading" class="card space-y-4 p-4 sm:p-6">
        <p v-if="activeTab === 'invite'" class="text-sm text-gray-500">{{ t('qqbot.inviteRecordsHelp') }}</p>
        <form class="flex flex-wrap gap-3" @submit.prevent="searchRecords">
          <input v-model="state.search" class="input min-w-0 flex-1" :aria-label="t('qqbot.recordSearch')" :placeholder="t('qqbot.recordSearch')" maxlength="254" />
          <button type="submit" class="btn btn-primary" :disabled="state.loading">{{ t('common.search') }}</button>
          <button type="button" class="btn btn-secondary" :disabled="state.loading" @click="load(activeTab, state.data.page)">{{ t('common.refresh') }}</button>
        </form>
        <p v-if="state.error" role="alert" class="text-sm text-red-600">{{ state.error }}</p>
        <p v-if="state.loading" role="status" class="py-6 text-sm text-gray-500">{{ t('common.loading') }}</p>
        <template v-else-if="!state.error">
          <div class="space-y-3 lg:hidden">
            <article v-for="record in state.data.items" :key="record.id" class="space-y-4 rounded-xl border border-gray-200 p-4 dark:border-dark-700">
              <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <div v-if="record.inviter"><p class="mb-2 text-xs text-gray-500">{{ t('qqbot.recordInviter') }}</p><QQBotRecordParticipant :person="record.inviter" /></div>
                <div><p class="mb-2 text-xs text-gray-500">{{ t(activeTab === 'invite' ? 'qqbot.recordInvitee' : 'qqbot.recordParticipant') }}</p><QQBotRecordParticipant :person="record.participant" /></div>
              </div>
              <div class="flex flex-wrap items-center justify-between gap-2 border-t pt-3 text-sm dark:border-dark-700">
                <span class="rounded-md bg-gray-100 px-2 py-1 dark:bg-dark-700">{{ t(`qqbot.recordStatus.${record.status}`) }}</span>
                <span v-if="activeTab !== 'invite' || record.rewarded_at" class="font-semibold tabular-nums">${{ record.amount.toFixed(2) }}</span>
              </div>
              <p v-if="record.coupon_quantity" class="text-sm font-medium text-primary-600">{{ t('coupon.awardedQuantity', { count: record.coupon_quantity }) }}</p>
              <p v-if="activeTab === 'lottery'" class="text-sm">{{ record.prize_name }} · {{ t('qqbot.remainingAtDraw') }} {{ record.remaining }}</p>
              <p v-if="activeTab === 'lottery' && record.chances" class="text-sm font-medium text-primary-600">{{ t('qqbot.chanceReward', { count: record.chances }) }}</p>
              <p v-if="activeTab === 'lottery' && record.is_guaranteed" class="text-sm font-medium text-primary-600 dark:text-primary-400">{{ t('qqbot.guaranteedDraw') }}</p>
              <p v-if="record.rewarded_at" class="break-all text-xs text-gray-500">{{ t('qqbot.recordRewardAccount') }}：{{ record.reward_email || record.reward_user_id }} · ID: {{ record.reward_user_id }}</p>
              <dl class="space-y-1 text-xs text-gray-500">
                <div class="break-all">{{ t('qqbot.group') }}：{{ record.group_id }}</div>
                <template v-if="activeTab === 'invite'">
                  <div>{{ t('qqbot.recordMembership') }}：{{ membership(record) }}</div>
                  <div>{{ t('qqbot.recordInvitedAt') }}：{{ formatDate(record.invited_at) }}</div>
                  <div>{{ t('qqbot.recordJoinedAt') }}：{{ formatDate(record.joined_at) }}</div>
                  <div v-if="record.left_at">{{ t('qqbot.recordLeftAt') }}：{{ formatDate(record.left_at) }}</div>
                  <div v-if="record.rewarded_at">{{ t('qqbot.recordRewardedAt') }}：{{ formatDate(record.rewarded_at) }}</div>
                </template>
                <div v-else>{{ t('qqbot.time') }}：{{ formatDate(record.created_at) }}</div>
              </dl>
            </article>
          </div>
          <div class="hidden overflow-x-auto lg:block">
            <table class="w-full text-left text-sm" :class="activeTab === 'invite' ? 'min-w-[1080px]' : 'min-w-[800px]'">
              <thead class="text-xs text-gray-500"><tr>
                <th v-if="activeTab === 'invite'" class="w-1/5 pb-3 pr-5">{{ t('qqbot.recordInviter') }}</th>
                <th class="w-1/5 pb-3 pr-5">{{ t(activeTab === 'invite' ? 'qqbot.recordInvitee' : 'qqbot.recordParticipant') }}</th>
                <th class="pb-3 pr-5">{{ t('qqbot.group') }}</th>
                <th class="pb-3 pr-5">{{ t('common.status') }}</th>
                <th v-if="activeTab === 'lottery'" class="pb-3 pr-5">{{ t('qqbot.prizeName') }}</th>
                <th class="pb-3 pr-5">{{ t('qqbot.recordAmount') }}</th>
                <th class="pb-3">{{ t('qqbot.time') }}</th>
              </tr></thead>
              <tbody><tr v-for="record in state.data.items" :key="record.id" class="border-t align-top dark:border-dark-700">
                <td v-if="record.inviter" class="py-4 pr-5"><QQBotRecordParticipant :person="record.inviter" /></td>
                <td class="py-4 pr-5"><QQBotRecordParticipant :person="record.participant" /></td>
                <td class="py-4 pr-5"><code class="block max-w-36 break-all text-xs text-gray-500">{{ record.group_id }}</code></td>
                <td class="py-4 pr-5"><span class="inline-block whitespace-nowrap rounded-md bg-gray-100 px-2 py-1 text-xs dark:bg-dark-700">{{ t(`qqbot.recordStatus.${record.status}`) }}</span><p v-if="activeTab === 'invite'" class="mt-2 text-xs text-gray-500">{{ membership(record) }}</p></td>
                <td v-if="activeTab === 'lottery'" class="py-4 pr-5"><p class="max-w-40 break-words">{{ record.prize_name }}</p><p v-if="record.chances" class="mt-1 text-xs font-medium text-primary-600">{{ t('qqbot.chanceReward', { count: record.chances }) }}</p><p v-if="record.is_guaranteed" class="mt-1 text-xs font-medium text-primary-600 dark:text-primary-400">{{ t('qqbot.guaranteedDraw') }}</p><p class="mt-1 text-xs text-gray-500">{{ t('qqbot.remainingAtDraw') }}：{{ record.remaining }}</p></td>
                <td class="py-4 pr-5"><p v-if="record.coupon_quantity" class="mb-1 font-medium text-primary-600">{{ t('coupon.awardedQuantity', { count: record.coupon_quantity }) }}</p><p class="font-medium tabular-nums">{{ activeTab === 'invite' && !record.rewarded_at ? '—' : `$${record.amount.toFixed(2)}` }}</p><p v-if="record.rewarded_at" class="mt-1 max-w-48 break-all text-xs text-gray-500">{{ t('qqbot.recordRewardAccount') }}：{{ record.reward_email || record.reward_user_id }} · ID: {{ record.reward_user_id }}</p></td>
                <td class="space-y-1 whitespace-nowrap py-4 text-xs text-gray-500">
                  <template v-if="activeTab === 'invite'">
                    <p>{{ t('qqbot.recordInvitedAt') }}：{{ formatDate(record.invited_at) }}</p>
                    <p>{{ t('qqbot.recordJoinedAt') }}：{{ formatDate(record.joined_at) }}</p>
                    <p v-if="record.left_at">{{ t('qqbot.recordLeftAt') }}：{{ formatDate(record.left_at) }}</p>
                    <p v-if="record.rewarded_at">{{ t('qqbot.recordRewardedAt') }}：{{ formatDate(record.rewarded_at) }}</p>
                  </template>
                  <template v-else>{{ formatDate(record.created_at) }}</template>
                </td>
              </tr></tbody>
            </table>
          </div>
          <p v-if="!state.data.items.length" class="py-8 text-center text-sm text-gray-500">{{ t('common.noData') }}</p>
        </template>
        <div class="flex flex-wrap items-center justify-between gap-3 border-t pt-4 text-sm dark:border-dark-700">
          <span class="text-gray-500">{{ t('qqbot.recordsPage', { total: state.data.total, page: state.data.page, pages }) }}</span>
          <div class="flex gap-2"><button type="button" class="btn btn-secondary" :disabled="state.loading || !!state.error || state.data.page <= 1" @click="load(activeTab, state.data.page - 1)">{{ t('qqbot.previousPage') }}</button><button type="button" class="btn btn-secondary" :disabled="state.loading || !!state.error || state.data.page >= pages" @click="load(activeTab, state.data.page + 1)">{{ t('qqbot.nextPage') }}</button></div>
        </div>
      </section>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import QQCommunityNav from '@/components/admin/qqbot/QQCommunityNav.vue'
import { computed, nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import QQBotDiagnostics from '@/components/admin/qqbot/QQBotDiagnostics.vue'
import QQBotConfigHistory from '@/components/admin/qqbot/QQBotConfigHistory.vue'
import QQJobRecords from '@/components/admin/qqbot/QQJobRecords.vue'
import QQModerationRecords from '@/components/admin/qqbot/QQModerationRecords.vue'
import QQBotRecordParticipant from '@/components/admin/qqbot/QQBotRecordParticipant.vue'
import { qqbotAPI, type QQActivityKind, type QQActivityRecord, type QQActivityRecordsPage } from '@/api/qqbot'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const recordModes = ['diagnostics', 'history', 'tasks', 'notifications', 'moderation', 'activities'] as const
const recordMode = computed(() => ['diagnostics', 'history', 'tasks', 'notifications', 'moderation'].includes(String(route.query.tab || 'tasks')) ? String(route.query.tab || 'tasks') : 'activities')
const appId = ref(''), appLoading = ref(false), appError = ref('')
let appRequest = 0
async function loadApp() {
  const seq = ++appRequest
  // Reload both the identity scope and its records, even when AppID is unchanged.
  appId.value = ''
  appLoading.value = true; appError.value = ''
  try { const config = await qqbotAPI.config(); if (seq === appRequest) appId.value = config.app_id }
  catch (e) { if (seq === appRequest) { appId.value = ''; appError.value = (e as Error).message } }
  finally { if (seq === appRequest) appLoading.value = false }
}
watch(recordMode, mode => { if (mode !== 'activities') void loadApp() }, { immediate: true })
onBeforeUnmount(() => { ++appRequest })
const tabs = [
  { key: 'checkin', label: 'qqbot.checkinRecords' },
  { key: 'lottery', label: 'qqbot.drawRecords' },
  { key: 'invite', label: 'qqbot.inviteRecords' }
] as const
const activeTab = computed<QQActivityKind>(() => tabs.find(tab => tab.key === route.query.tab)?.key ?? 'checkin')
function emptyState() { return { search: '', appliedSearch: '', loading: false, loaded: false, error: '', data: { items: [], total: 0, page: 1, page_size: 20 } as QQActivityRecordsPage } }
const states = reactive({ checkin: emptyState(), lottery: emptyState(), invite: emptyState() })
const state = computed(() => states[activeTab.value])
const pages = computed(() => Math.max(1, Math.ceil(state.value.data.total / state.value.data.page_size)))
function formatDate(value?: string) { return value ? new Date(value).toLocaleString() : '—' }
function membership(record: QQActivityRecord) { return t(`qqbot.recordStatus.${record.left_at ? 'left_group' : record.joined_at ? 'joined' : 'pending_join'}`) }
async function load(kind: QQActivityKind, page: number) {
  const target = states[kind]
  if (target.loading) return
  target.loading = true; target.error = ''
  try {
    let data = await qqbotAPI.activityRecords(kind, target.appliedSearch, page)
    const last = Math.max(1, Math.ceil(data.total / data.page_size))
    if (page > last) data = await qqbotAPI.activityRecords(kind, target.appliedSearch, last)
    target.data = data; target.loaded = true
  } catch (e) { target.error = (e as Error).message || t('common.error') }
  finally { target.loading = false }
}
function searchRecords() {
  if (state.value.loading) return
  state.value.appliedSearch = state.value.search.trim()
  void load(activeTab.value, 1)
}
async function selectTab(tab: QQActivityKind) { if (activeTab.value !== tab) await router.push({ query: { ...route.query, tab } }) }
async function handleKeydown(event: KeyboardEvent, tab: QQActivityKind) {
  const index = tabs.findIndex(item => item.key === tab)
  let next: number
  switch (event.key) {
    case 'ArrowRight': next = (index + 1) % tabs.length; break
    case 'ArrowLeft': next = (index + tabs.length - 1) % tabs.length; break
    case 'Home': next = 0; break
    case 'End': next = tabs.length - 1; break
    default: return
  }
  event.preventDefault()
  const target = tabs[next]!
  await selectTab(target.key)
  await nextTick()
  document.getElementById(`records-tab-${target.key}`)?.focus()
}
watch([activeTab, recordMode], ([tab, mode]) => { if (mode === 'activities' && !states[tab].loaded) void load(tab, 1) }, { immediate: true })
</script>
