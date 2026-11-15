<template>
  <div class="space-y-4">
    <label class="block space-y-1">{{ t('qqbot.imageAI.source') }}
      <select :value="source" class="input" data-testid="image-ai-source" @change="changeSource">
        <option value="reuse_chat">{{ t('qqbot.imageAI.reuseChat') }}</option>
        <option value="site">{{ t('qqbot.simple.siteAI') }}</option>
        <option value="external">{{ t('qqbot.simple.externalAI') }}</option>
      </select>
    </label>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <template v-if="source === 'site'">
      <p class="rounded-lg bg-primary-50 p-3 text-sm text-primary-800 dark:bg-primary-900/20 dark:text-primary-200">{{ t('qqbot.imageAI.managedHelp') }}</p>
      <button v-if="!options" type="button" class="btn btn-secondary" :disabled="loading" @click="load">{{ t('common.refresh') }}</button>
      <label class="block space-y-1">{{ t('qqbot.imageAI.siteGroup') }}<select v-model.number="model.image_site_group_id" required class="input" data-testid="image-site-group"><option :value="0" disabled>{{ t('qqbot.simple.chooseGroup') }}</option><option v-for="group in options?.image_groups || []" :key="group.id" :value="group.id">{{ group.name }}</option></select></label>
      <p v-if="options && !options.image_groups?.length" class="text-sm text-amber-700">{{ t('qqbot.imageAI.noSiteGroups') }}</p>
      <label v-if="model.image_site_api_key_id" class="flex items-start gap-2 text-sm"><input v-model="regenerate" type="checkbox" class="mt-1" data-testid="image-regenerate-key" />{{ t('qqbot.imageAI.regenerateKey') }}</label>
      <label class="block space-y-1">{{ t('qqbot.imageAI.keyQuota') }}<input v-model.number="quota" type="number" class="input" min="0" max="1000000" step="0.01" required :disabled="existingKey && !regenerate" data-testid="image-site-quota" /></label>
      <p class="text-xs text-gray-500">{{ t('qqbot.simple.quotaHelp') }}</p>
      <p v-if="existingKey" class="text-sm text-gray-500">{{ t('qqbot.simple.keyUsage', { used: options?.current_image_used ?? 0, quota: options?.current_image_quota ?? 0, id: model.image_site_api_key_id }) }} <RouterLink class="text-primary-600 underline" to="/keys">{{ t('qqbot.simple.manageKey') }}</RouterLink></p>
    </template>
    <div v-else-if="source === 'external'" class="grid gap-4 md:grid-cols-2">
      <label class="space-y-1">{{ t('qqbot.imageBase') }}<input v-model.trim="model.image_base_url" class="input" :placeholder="t('qqbot.reuseChat')" data-testid="image-base-url" /></label>
      <label class="space-y-1">{{ t('qqbot.imageKey') }}<input v-model="model.image_api_key" type="password" class="input" autocomplete="new-password" :placeholder="t(model.image_api_key_set ? 'qqbot.secretStored' : 'qqbot.secretEmpty')" data-testid="image-api-key" /><span class="flex gap-2 text-xs"><input v-model="model.clear_image_api_key" type="checkbox" />{{ t('qqbot.clearSecret') }}</span></label>
    </div>
    <p v-else class="text-sm text-gray-500">{{ t('qqbot.imageAI.reuseHelp') }}</p>
    <div class="space-y-1">
      <label :for="modelId" class="block">{{ t('qqbot.imageModel') }}</label>
      <Select v-if="source === 'site'" :id="modelId" :model-value="model.image_model" :options="models" :aria-label="t('qqbot.imageModel')" :placeholder="t('qqbot.simple.chooseModel')" :search-placeholder="t('qqbot.simple.searchModel')" :creatable-prefix="t('qqbot.simple.useModel')" :error="!!modelError" :loading="loading" searchable creatable clearable data-testid="image-model" @update:model-value="selectModel" />
      <input v-else :id="modelId" v-model.trim="model.image_model" class="input" maxlength="200" :required="model.image_enabled" data-testid="image-model" />
      <p v-if="modelError" role="alert" class="text-sm text-red-600">{{ modelError }}</p>
    </div>
    <p v-if="source === 'site'" class="text-xs text-gray-500">{{ t('qqbot.simple.candidates') }}</p>
  </div>
</template>
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import { qqbotAPI, type QQBotConfig, type QQSetupOptions } from '@/api/qqbot'

const model = defineModel<QQBotConfig>({ required: true })
const quota = defineModel<number>('quota', { required: true })
const regenerate = defineModel<boolean>('regenerate', { default: false })
const props = defineProps<{ savedGroupId?: number }>()
const { t } = useI18n()
const options = ref<QQSetupOptions>()
const quotaKeyId = ref<number>()
const error = ref(''), modelError = ref(''), loading = ref(false)
const originalGroup = ref(model.value.image_site_api_key_id ? model.value.image_site_group_id : 0)
const source = computed(() => model.value.image_ai_connection || (model.value.image_base_url || model.value.image_api_key || model.value.image_api_key_set ? 'external' : 'reuse_chat'))
const existingKey = computed(() => !!model.value.image_site_api_key_id && (props.savedGroupId ?? originalGroup.value) === model.value.image_site_group_id)
const models = computed(() => (options.value?.image_groups?.find(group => group.id === model.value.image_site_group_id)?.models || []).map(name => ({ value: name, label: name })))
const modelId = useId()
let alive = true, request = 0
async function load() {
  const current = ++request
  const keyId = model.value.image_site_api_key_id
  loading.value = true; error.value = ''
  try {
    const result = await qqbotAPI.setupOptions()
    if (alive && current === request) { options.value = result; quotaKeyId.value = keyId; syncExistingQuota() }
  } catch (e) { if (alive && current === request) error.value = (e as Error).message }
  finally { if (alive && current === request) loading.value = false }
}
function syncExistingQuota() {
  if (options.value && source.value === 'site' && existingKey.value && !regenerate.value && quotaKeyId.value === model.value.image_site_api_key_id) quota.value = options.value.current_image_quota ?? 0
}
function changeSource(event: Event) {
  const previous = source.value
  const next = (event.target as HTMLSelectElement).value as Exclude<QQBotConfig['image_ai_connection'], '' | undefined>
  model.value.image_ai_connection = next
  regenerate.value = false
  model.value.image_api_key = ''; model.value.clear_image_api_key = false
  if (next !== 'external' || previous === 'site') { model.value.image_base_url = ''; model.value.image_api_key_set = false }
}
function selectModel(value: string | number | boolean | null) { model.value.image_model = typeof value === 'string' ? value.trim() : '' }
function validate() {
  modelError.value = model.value.image_enabled && (!model.value.image_model.trim() || model.value.image_model.length > 200) ? t('qqbot.imageAI.invalidModel') : ''
  return !modelError.value
}
defineExpose({ validate, reload: load })
watch(() => model.value.image_model, () => { modelError.value = '' })
watch([source, () => model.value.image_site_group_id, () => regenerate.value], syncExistingQuota)
watch(() => model.value.image_site_api_key_id, (current, previous) => { if (current && current !== previous) { originalGroup.value = model.value.image_site_group_id; void load() } })
onMounted(load)
onBeforeUnmount(() => { alive = false; request++ })
</script>
