<template>
  <AppLayout>
    <QQCommunityNav /><div class="mx-auto max-w-5xl space-y-5">
    <h1 class="text-2xl font-semibold">{{ t('qqbot.workspace.members') }}</h1>
    <nav class="flex flex-wrap gap-2"><RouterLink v-for="item in tabs" :key="item.key" :to="{ query: { tab: item.key } }" class="btn" :class="tab === item.key ? 'btn-primary' : 'btn-secondary'">{{ t(item.label) }}</RouterLink></nav>
    <QQBotBindings v-if="tab === 'members'" />
    <QQBotTransferSettings v-else-if="tab === 'transfer'" />
    <section v-else class="card space-y-4 p-5"><p class="text-sm text-gray-500 dark:text-gray-400">{{ t('qqbot.workspace.transferHistoryHelp') }}</p><form class="flex flex-wrap gap-3" @submit.prevent="searchHistory"><input v-model.trim="search" class="input min-w-0 flex-1 basis-48" maxlength="254" :placeholder="t('qqbot.workspace.transferSearch')" :aria-label="t('qqbot.workspace.transferSearch')" /><button class="btn btn-primary" :disabled="loading">{{ t('common.search') }}</button></form><p v-if="error" role="alert" class="text-red-600">{{ error }}</p><article v-for="item in history.items" :key="item.id" class="space-y-1 rounded border p-3 text-sm dark:border-dark-700"><p class="break-all">{{ item.from_email }} (#{{ item.from_user_id }}) → {{ item.to_email }} (#{{ item.to_user_id }})</p><p>USD {{ item.amount }} · {{ t('qqbot.workspace.transferFee') }} {{ item.fee }} · {{ t('qqbot.workspace.transferNet') }} {{ item.net_amount }}</p><time class="text-gray-500 dark:text-gray-400">{{ new Date(item.created_at).toLocaleString() }}</time></article><p v-if="!history.items.length">{{ t('common.noData') }}</p><div class="flex flex-wrap items-center justify-between gap-2"><button class="btn btn-secondary" :disabled="loading || history.page <= 1" @click="load(history.page - 1)">{{ t('qqbot.previousPage') }}</button><span>{{ history.total }}</span><button class="btn btn-secondary" :disabled="loading || history.page * 20 >= history.total" @click="load(history.page + 1)">{{ t('qqbot.nextPage') }}</button></div></section>
  </div></AppLayout>
</template>
<script setup lang="ts">
import QQCommunityNav from '@/components/admin/qqbot/QQCommunityNav.vue'
import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import QQBotBindings from '@/components/admin/QQBotBindings.vue'
import QQBotTransferSettings from '@/components/admin/qqbot/QQBotTransferSettings.vue'
import { qqbotAPI, type QQTransferHistory } from '@/api/qqbot'
const { t } = useI18n(), route = useRoute()
const tabs = [{ key: 'members', label: 'qqbot.bindingsTitle' }, { key: 'transfer', label: 'qqbot.transferTitle' }, { key: 'history', label: 'qqbot.workspace.transferHistory' }]
const tab = computed(() => tabs.find(item => item.key === route.query.tab)?.key || 'members')
const search = ref('')
let appliedSearch = ''
function searchHistory() { appliedSearch = search.value; void load(1) }
const loading = ref(false), error = ref('')
const history = ref<QQTransferHistory>({ items: [], total: 0, page: 1, page_size: 20 })
async function load(page: number) { loading.value = true; error.value = ''; try { history.value = await qqbotAPI.transferHistory(appliedSearch, page) } catch (e) { error.value = (e as Error).message } finally { loading.value = false } }
watch(tab, value => { if (value === 'history') void load(1) }, { immediate: true })
</script>
