<template>
  <section id="relogin-service" class="card space-y-4 p-5">
    <h2 class="text-base font-semibold">{{ text('重登服务', 'Relogin service') }}</h2>
    <p class="text-sm text-gray-500">{{ text('供 2FA 首次登录和 Session Studio 重登使用。邮箱、密码与 2FA 密钥会发送到此地址，使用服务端出口。', 'Used by initial 2FA login and Session Studio. Email, password and 2FA secrets are sent to this endpoint through the server connection.') }}</p>
    <button v-if="!draft" type="button" class="btn btn-secondary" :disabled="busy" @click="load">{{ text('编辑服务配置', 'Edit service configuration') }}</button>
    <form v-else class="space-y-3" @submit.prevent="save">
      <label class="block text-sm">{{ text('HTTPS 重登地址', 'HTTPS relogin endpoint') }}
        <input v-model.trim="draft.relogin_endpoint" type="url" required class="input mt-1 w-full" :disabled="busy" />
      </label>
      <label class="block text-sm">{{ text('额外请求头（JSON）', 'Additional headers (JSON)') }}
        <textarea v-model="headers" rows="4" class="input mt-1 w-full font-mono" autocomplete="off" spellcheck="false" :disabled="busy" />
      </label>
      <button type="submit" class="btn btn-primary" :disabled="busy">{{ text('保存', 'Save') }}</button>
    </form>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <p v-if="saved" role="status" class="text-sm text-green-600">{{ text('已保存', 'Saved') }}</p>
  </section>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getReloginConfig, saveReloginConfig, type OpenAIReloginConfig } from '@/api/admin/openaiRelogin'

const { locale } = useI18n()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
const draft = ref<OpenAIReloginConfig | null>(null)
const headers = ref('{}')
const busy = ref(false)
const error = ref('')
const saved = ref(false)

async function load() {
  busy.value = true
  error.value = ''
  try {
    draft.value = await getReloginConfig()
    headers.value = JSON.stringify(draft.value.relogin_headers ?? {}, null, 2)
  } catch {
    error.value = text('无法读取重登服务配置', 'Could not load relogin configuration')
  } finally { busy.value = false }
}

async function save() {
  if (!draft.value) return
  busy.value = true
  saved.value = false
  error.value = ''
  try {
    const parsed: unknown = JSON.parse(headers.value)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed) ||
        Object.values(parsed).some(value => typeof value !== 'string')) throw new Error('invalid_headers')
    draft.value = await saveReloginConfig({ ...draft.value, relogin_headers: parsed as Record<string, string> })
    headers.value = JSON.stringify(draft.value.relogin_headers ?? {}, null, 2)
    saved.value = true
  } catch {
    error.value = text('保存失败，请检查 HTTPS 地址和请求头 JSON', 'Save failed; check the HTTPS endpoint and headers JSON')
  } finally { busy.value = false }
}
</script>
