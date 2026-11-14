<template>
  <section class="space-y-3">
    <h3 class="font-semibold">{{ t('qqbot.workspace.commandInventory') }}</h3>
    <p class="text-xs text-gray-500">{{ t('qqbot.workspace.commandHelp') }}</p>
    <p v-if="error" class="text-red-600">{{ error }}</p>
    <div class="grid gap-2 sm:grid-cols-2">
      <article v-for="command in items" :key="command.command" class="rounded border p-3 text-sm dark:border-dark-700" data-testid="qq-command-inventory-item">
        <code>{{ (command.usage || command.command).replace(/^\//, '') }}</code>
        <p>{{ command.description }} · {{ t(command.builtin ? 'qqbot.workspace.builtin' : 'qqbot.workspace.custom') }}</p>
        <p v-if="command.reply" class="mt-2 whitespace-pre-wrap break-words rounded bg-gray-50 p-2 dark:bg-dark-800">{{ command.reply }}</p>
      </article>
    </div>
  </section>
</template>
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { qqbotAPI, type QQCommandInfo } from '@/api/qqbot'
const { t } = useI18n()
const items = ref<QQCommandInfo[]>([]), error = ref('')
onMounted(async () => { try { items.value = await qqbotAPI.commands() } catch (e) { error.value = (e as Error).message } })
</script>
