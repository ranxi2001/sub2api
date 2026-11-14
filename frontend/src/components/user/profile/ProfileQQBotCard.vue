<template>
  <section v-if="state?.enabled || state?.binding" class="card space-y-4 p-6">
    <h2 class="text-lg font-semibold">{{ t('qqbot.bindingTitle') }}</h2>
    <p class="text-sm text-gray-500">{{ t('qqbot.bindingHelp') }}</p>
    <p class="text-sm text-gray-500">{{ t('qqbot.emailBindingHelp') }}</p>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <div v-if="state.binding" class="rounded-lg bg-green-50 p-3 text-green-800 dark:bg-green-950 dark:text-green-200">{{ t('qqbot.bound') }}：{{ state.binding.nickname || state.binding.member_id }}<p class="break-all text-xs">{{ state.binding.member_id }}</p></div>
    <template v-else>
      <div class="flex flex-wrap gap-2"><button class="btn btn-secondary" :disabled="busy" @click="createCode">{{ t('qqbot.createCode') }}</button><button class="btn btn-secondary" :disabled="busy" @click="refresh">{{ t('qqbot.refreshBinding') }}</button></div>
      <div v-if="code" class="rounded-lg bg-gray-50 p-3 dark:bg-dark-800"><p class="text-sm">{{ t('qqbot.sendCode') }}</p><code class="block select-all break-all py-2">绑定 {{ code }}</code><p class="text-xs text-gray-500">{{ t('qqbot.codeExpires') }}</p></div>
      <div v-if="state.pending?.member_id" class="space-y-3 rounded-lg border p-4 dark:border-dark-700">
        <p>{{ t('qqbot.confirmIdentity') }}</p><p>{{ state.pending.nickname || 'QQ' }} · <span class="break-all font-mono text-xs">{{ state.pending.member_id }}</span></p><p class="break-all text-xs">{{ t('qqbot.group') }}：{{ state.pending.group_id }}</p>
        <button class="btn btn-primary" :disabled="busy" @click="confirm">{{ t('qqbot.confirmBinding') }}</button>
      </div>
    </template>
  </section>
</template>
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { qqbotAPI, type QQBindingState } from '@/api/qqbot'
const { t } = useI18n()
const state = ref<QQBindingState | null>(null)
const code = ref('')
const busy = ref(false)
const error = ref('')
async function run(action: () => Promise<void>) { busy.value = true; error.value = ''; try { await action() } catch (e) { error.value = (e as {message?: string})?.message || t('common.error') } finally { busy.value = false } }
async function refresh() { await run(async () => { state.value = await qqbotAPI.binding() }) }
async function createCode() { await run(async () => { code.value = (await qqbotAPI.createCode()).code || ''; state.value = await qqbotAPI.binding() }) }
async function confirm() { const member = state.value?.pending?.member_id; if (!member) return; await run(async () => { await qqbotAPI.confirm(member); code.value = ''; state.value = await qqbotAPI.binding() }) }
onMounted(refresh)
</script>
