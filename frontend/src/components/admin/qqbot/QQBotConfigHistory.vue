<template><section class="card space-y-4 p-5"><h2 class="text-lg font-semibold">{{ t('qqbot.workspace.history') }}</h2><p v-if="error" role="alert" class="text-red-600">{{ error }}</p><p v-if="!items.length" class="text-sm text-gray-500">{{ t('common.noData') }}</p><details v-for="(item, index) in items" :key="index" class="rounded-lg border p-3 dark:border-dark-700"><summary class="cursor-pointer text-sm">{{ new Date(item.at).toLocaleString() }} · {{ item.section }}</summary><div class="mt-3 grid gap-3 md:grid-cols-2"><pre class="overflow-auto whitespace-pre-wrap break-all text-xs">{{ JSON.stringify(item.before, null, 2) }}</pre><pre class="overflow-auto whitespace-pre-wrap break-all text-xs">{{ JSON.stringify(item.after, null, 2) }}</pre></div></details></section></template>
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { qqbotAPI, type QQConfigChange } from '@/api/qqbot'
const { t } = useI18n()
const items = ref<QQConfigChange[]>([]), error = ref('')
onMounted(async () => { try { items.value = await qqbotAPI.configHistory() } catch (e) { error.value = (e as Error).message } })
</script>
