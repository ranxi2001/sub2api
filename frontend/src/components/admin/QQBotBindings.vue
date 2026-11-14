<template>
  <section class="card space-y-4 p-6">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h2 class="text-lg font-semibold">{{ t('qqbot.bindingsTitle') }}</h2>
      <button type="button" class="btn btn-secondary" :disabled="loading || unbinding" @click="load">{{ t('common.refresh') }}</button>
    </div>
    <p class="text-sm text-gray-500">{{ t('qqbot.bindingsHelp') }}</p>
    <form class="flex flex-wrap gap-2" @submit.prevent="searchBindings">
      <input v-model="search" class="input min-w-0 flex-1" :aria-label="t('qqbot.bindingsSearch')" :placeholder="t('qqbot.bindingsSearch')" maxlength="254" />
      <button class="btn btn-secondary" :disabled="loading || unbinding">{{ t('common.search') }}</button>
    </form>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <p v-if="notice" role="status" class="text-sm text-green-700 dark:text-green-300">{{ notice }}</p>
    <p v-if="loading" role="status" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
    <div class="space-y-3 md:hidden">
      <article v-for="binding in data.items" :key="`${binding.app_id}:${binding.member_id}:${binding.created_at}`" class="space-y-3 rounded-xl border border-gray-200 p-4 text-sm dark:border-dark-700">
        <div class="flex items-start justify-between gap-3">
          <div class="min-w-0"><p class="break-all font-medium">{{ binding.email }}</p><p class="mt-1 text-xs text-gray-500">ID: {{ binding.user_id }} · {{ binding.user_deleted ? t('qqbot.accountDeleted') : binding.user_status }}</p></div>
          <button type="button" class="shrink-0 text-sm text-red-600" :disabled="loading || unbinding" @click="selected = binding; dialogError = ''">{{ t('qqbot.unbind') }}</button>
        </div>
        <dl class="space-y-2 text-xs">
          <div><dt class="text-gray-500">{{ t('qqbot.emailMember') }} · {{ binding.nickname || 'QQ' }}</dt><dd class="mt-1 break-all font-mono">{{ binding.member_id }}</dd></div>
          <div><dt class="text-gray-500">{{ t('qqbot.group') }}</dt><dd class="mt-1 break-all font-mono">{{ binding.group_id }}</dd></div>
          <div><dt class="text-gray-500">{{ t('qqbot.boundAt') }}</dt><dd class="mt-1">{{ formatDate(binding.created_at) }}</dd></div>
        </dl>
      </article>
    </div>
    <div class="hidden overflow-x-auto md:block">
      <table class="w-full min-w-[740px] text-left text-sm">
        <thead><tr><th class="pb-2 pr-4">{{ t('qqbot.websiteAccount') }}</th><th class="pb-2 pr-4">{{ t('qqbot.emailMember') }}</th><th class="pb-2 pr-4">{{ t('qqbot.group') }}</th><th class="pb-2 pr-4">{{ t('qqbot.boundAt') }}</th><th class="pb-2">{{ t('common.actions') }}</th></tr></thead>
        <tbody>
          <tr v-for="binding in data.items" :key="`${binding.app_id}:${binding.member_id}:${binding.created_at}`" class="border-t dark:border-dark-700">
            <td class="py-3 pr-4"><p class="max-w-64 break-all">{{ binding.email }}</p><p class="text-xs text-gray-500">ID: {{ binding.user_id }} · {{ binding.user_deleted ? t('qqbot.accountDeleted') : binding.user_status }}</p></td>
            <td class="py-3 pr-4"><p class="max-w-48 break-all">{{ binding.nickname || 'QQ' }}</p><code class="block max-w-48 break-all text-xs">{{ binding.member_id }}</code></td>
            <td class="py-3 pr-4"><code class="block max-w-48 break-all text-xs">{{ binding.group_id }}</code></td>
            <td class="whitespace-nowrap py-3 pr-4 text-xs">{{ formatDate(binding.created_at) }}</td>
            <td class="py-3"><button type="button" class="btn btn-secondary whitespace-nowrap text-red-600" :disabled="loading || unbinding" @click="selected = binding; dialogError = ''">{{ t('qqbot.unbind') }}</button></td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-if="!loading && !data.items.length" class="py-4 text-gray-500">{{ t('common.noData') }}</p>
    <div class="flex flex-wrap items-center justify-between gap-3 text-sm">
      <span>{{ t('qqbot.bindingPage', { total: data.total, page: data.page, pages: totalPages }) }}</span>
      <div class="flex gap-2">
        <button type="button" class="btn btn-secondary" :disabled="loading || unbinding || data.page <= 1" @click="changePage(-1)">{{ t('qqbot.previousPage') }}</button>
        <button type="button" class="btn btn-secondary" :disabled="loading || unbinding || data.page >= totalPages" @click="changePage(1)">{{ t('qqbot.nextPage') }}</button>
      </div>
    </div>
    <BaseDialog :show="!!selected" :title="t('qqbot.unbindTitle')" width="narrow" :close-on-escape="!unbinding" :show-close-button="!unbinding" @close="closeDialog">
      <div v-if="selected" class="space-y-3">
        <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('qqbot.unbindHelp') }}</p>
        <div class="space-y-2 rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">
          <p class="break-all">{{ selected.email }} · ID: {{ selected.user_id }}</p>
          <p class="break-all">{{ selected.nickname || 'QQ' }} · {{ selected.member_id }}</p>
          <p class="break-all text-xs">{{ t('qqbot.group') }}：{{ selected.group_id }}</p>
        </div>
        <p v-if="dialogError" role="alert" class="text-sm text-red-600">{{ dialogError }}</p>
      </div>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" :disabled="unbinding" @click="closeDialog">{{ t('common.cancel') }}</button>
          <button type="button" class="btn btn-primary" :disabled="unbinding" @click="confirmUnbind">{{ unbinding ? t('common.loading') : t('qqbot.confirmUnbind') }}</button>
        </div>
      </template>
    </BaseDialog>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { qqbotAPI, type QQAdminBinding, type QQAdminBindingsPage } from '@/api/qqbot'

const { t } = useI18n()
const emit = defineEmits<{ changed: [] }>()
const data = ref<QQAdminBindingsPage>({ items: [], total: 0, page: 1, page_size: 20 })
const search = ref('')
const activeSearch = ref('')
const loading = ref(false)
const unbinding = ref(false)
const error = ref('')
const notice = ref('')
const selected = ref<QQAdminBinding | null>(null)
const dialogError = ref('')
const totalPages = computed(() => Math.max(1, Math.ceil(data.value.total / data.value.page_size)))
function message(e: unknown) { return (e as { message?: string })?.message || t('common.error') }
function formatDate(value: string) { return new Date(value).toLocaleString() }
async function load() {
  if (loading.value) return
  loading.value = true; error.value = ''
  try {
    data.value = await qqbotAPI.bindings(activeSearch.value, data.value.page)
    if (data.value.page > totalPages.value) data.value = await qqbotAPI.bindings(activeSearch.value, totalPages.value)
  } catch (e) { error.value = message(e) } finally { loading.value = false }
}
async function searchBindings() {
  if (loading.value || unbinding.value) return
  activeSearch.value = search.value.trim(); data.value.page = 1
  await load()
}
async function changePage(delta: number) {
  if (loading.value || unbinding.value) return
  data.value.page += delta
  await load()
}
function closeDialog() { if (!unbinding.value) selected.value = null }
async function confirmUnbind() {
  if (!selected.value || unbinding.value) return
  unbinding.value = true; dialogError.value = ''; notice.value = ''
  try {
    await qqbotAPI.unbind(selected.value)
    selected.value = null
    notice.value = t('qqbot.unbound')
    emit('changed')
    await load()
  } catch (e) { dialogError.value = message(e) } finally { unbinding.value = false }
}
onMounted(load)
</script>
