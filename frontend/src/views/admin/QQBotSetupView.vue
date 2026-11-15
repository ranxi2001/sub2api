<template>
  <AppLayout>
    <QQCommunityNav />
    <div class="mx-auto max-w-4xl space-y-6">
      <header class="flex flex-wrap items-start justify-between gap-3"><div><h1 class="text-2xl font-semibold">{{ t('qqbot.simple.setup') }}</h1><p class="mt-2 text-sm text-gray-500">{{ t('qqbot.simple.setupHelp') }}</p></div><RouterLink to="/admin/qqbot/config" class="btn btn-secondary">{{ t('qqbot.simple.advanced') }}</RouterLink></header>
      <ol class="grid grid-cols-2 gap-3 sm:grid-cols-4"><li v-for="(label, index) in steps" :key="label" class="rounded-xl border p-3 text-sm" :class="step === index ? 'border-primary-500 bg-primary-50 text-primary-800 dark:bg-primary-900/20 dark:text-primary-200' : 'dark:border-dark-700'"><span class="font-semibold">{{ index + 1 }}.</span> {{ t('qqbot.simple.' + label) }}</li></ol>
      <p v-if="error" role="alert" class="rounded-lg bg-red-50 p-4 text-red-700">{{ error }} <button type="button" :disabled="busy" class="underline" @click="reload">{{ t('qqbot.simple.reload') }}</button></p>
      <p v-if="notice" role="status" class="rounded-lg bg-green-50 p-4 text-green-800">{{ notice }}</p>
      <p v-if="saved?.enabled" class="rounded-lg bg-amber-50 p-4 text-sm text-amber-800">{{ t('qqbot.simple.liveHelp') }}</p>
      <p v-if="warning" role="alert" class="rounded-lg bg-amber-50 p-4 text-amber-800">{{ warning }}</p>
      <form v-if="form" ref="formElement" class="space-y-5" @submit.prevent="next">
        <fieldset :disabled="busy" class="space-y-5">
          <section v-if="step === 0" class="card space-y-4 p-6">
            <h2 class="text-lg font-semibold">{{ t('qqbot.simple.credentials') }}</h2>
            <label class="block">App ID<input v-model.trim="form.app_id" class="input" required maxlength="128" data-testid="setup-app-id" /></label>
            <label class="block">App Secret<input v-model="form.app_secret" type="password" autocomplete="new-password" class="input" :required="!form.app_secret_set" :placeholder="t(form.app_secret_set ? 'qqbot.secretStored' : 'qqbot.secretEmpty')" data-testid="setup-secret" /></label>
            <label class="flex items-center gap-2 text-sm"><input v-model="form.sandbox" type="checkbox" />{{ t('qqbot.sandbox') }}</label>
            <p class="text-sm text-gray-500">{{ t('qqbot.simple.callbackHelp') }}</p><code class="block break-all rounded bg-gray-50 p-3 text-sm dark:bg-dark-800">{{ webhookURL }}</code>
          </section>
          <section v-if="step === 1" class="card space-y-5 p-6">
            <h2 class="text-lg font-semibold">{{ t('qqbot.simple.groupsAI') }}</h2>
            <QQGroupPicker v-model="form.allowed_groups" :groups="knownGroups" :policies="form.group_policies" :label="t('qqbot.allowedGroups')" />
            <p class="text-sm text-gray-500">{{ t('qqbot.simple.discoverHelp') }}</p>
            <button type="button" class="btn btn-secondary" @click="refreshGroups">{{ t('qqbot.simple.refreshGroups') }}</button>
            <details><summary class="cursor-pointer text-sm">{{ t('qqbot.simple.manualGroups') }}</summary><textarea v-model="manualGroups" class="input mt-3" rows="3" /></details>
            <label class="flex items-center gap-2"><input :checked="!form.chat_disabled" type="checkbox" @change="form.chat_disabled = !($event.target as HTMLInputElement).checked" />{{ t('qqbot.simple.chatCapability') }}</label>
            <QQBotSiteAISettings v-if="!form.chat_disabled" ref="siteAISettings" v-model="form" v-model:quota="quota" v-model:regenerate="regenerateSiteKey" />
          </section>
          <section v-if="step === 2" class="card space-y-5 p-6">
            <h2 class="text-lg font-semibold">{{ t('qqbot.simple.capabilities') }}</h2>
            <label class="flex items-center gap-2"><input :checked="!form.chat_disabled" type="checkbox" @change="form.chat_disabled = !($event.target as HTMLInputElement).checked" />{{ t('qqbot.simple.chatCapability') }}</label>
            <label class="flex items-center gap-2"><input v-model="form.image_enabled" type="checkbox" />{{ t('qqbot.images') }}</label>
            <QQBotImageAISettings v-if="form.image_enabled" ref="imageAISettings" v-model="form" v-model:quota="imageQuota" v-model:regenerate="regenerateImageSiteKey" :saved-group-id="saved?.image_site_group_id" />
            <label v-if="form.autonomous" class="flex items-center gap-2"><input v-model="autoChatEnabled" type="checkbox" :disabled="form.chat_disabled" />{{ t('qqbot.autonomous.title') }}</label>
            <p v-if="form.autonomous?.enabled" class="text-sm text-gray-500">{{ t('qqbot.autonomous.simpleHelp') }} <RouterLink to="/admin/qqbot/config?tab=autonomous" class="text-primary-600 underline">{{ t('qqbot.autonomous.workspaceTitle') }}</RouterLink></p>
            <label v-if="form.moderation" class="flex items-center gap-2"><input v-model="form.moderation.enabled" type="checkbox" />{{ t('qqbot.moderation.title') }}</label>
            <label v-if="form.moderation?.enabled" class="flex items-center gap-2 text-sm"><input v-model="form.moderation.inherit_groups" type="checkbox" />{{ t('qqbot.simple.inheritGroups') }}</label>
            <QQGroupPicker v-if="form.moderation?.enabled && !form.moderation.inherit_groups" v-model="form.moderation.groups" :groups="form.allowed_groups" :label="t('qqbot.moderation.groups')" />
            <p class="text-sm text-gray-500">{{ t('qqbot.simple.permissions') }}</p>
            <details><summary class="cursor-pointer text-sm">{{ t('qqbot.simple.usageLimits') }}</summary><div class="mt-3 grid gap-3 sm:grid-cols-2"><label v-for="item in limits" :key="item.key">{{ t('qqbot.' + item.label) }}<input v-model.number="form[item.key]" type="number" class="input" min="0" max="100000" step="1" required /></label></div></details>
            <p class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">{{ t('qqbot.simple.activitiesLater') }} <RouterLink to="/admin/qqbot/activities?tab=templates" class="text-primary-600 underline">{{ t('qqbot.simple.templates') }}</RouterLink></p>
          </section>
          <section v-if="step === 3" class="space-y-4">
            <QQBotDiagnostics :key="diagnosticRevision" @ready="ready = $event" @busy="probing = $event" />
            <label class="flex items-start gap-2 text-sm"><input v-model="reviewed" type="checkbox" class="mt-1" />{{ t('qqbot.simple.reviewWarnings') }}</label>
            <button v-if="!saved?.enabled" type="button" class="btn btn-primary" :disabled="!ready || probing || !reviewed" data-testid="setup-enable" @click="enable">{{ t('qqbot.simple.enable') }}</button>
            <RouterLink v-else to="/admin/qqbot/overview" class="btn btn-primary">{{ t('qqbot.workspace.overview') }}</RouterLink>
          </section>
          <footer class="flex justify-between gap-3"><button v-if="step > 0" type="button" class="btn btn-secondary" :disabled="probing" @click="step--; ready = false">{{ t('qqbot.simple.back') }}</button><span v-else /><button v-if="step < 3" type="submit" class="btn btn-primary" data-testid="setup-next">{{ t(step === 0 ? 'qqbot.simple.saveCredentials' : step === 2 ? 'qqbot.simple.saveCheck' : 'qqbot.simple.next') }}</button></footer>
        </fieldset>
      </form>
    </div>
  </AppLayout>
</template>
<script setup lang="ts">
import QQCommunityNav from '@/components/admin/qqbot/QQCommunityNav.vue'
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import QQGroupPicker from '@/components/admin/qqbot/QQGroupPicker.vue'
import QQBotSiteAISettings from '@/components/admin/qqbot/QQBotSiteAISettings.vue'
import QQBotImageAISettings from '@/components/admin/qqbot/QQBotImageAISettings.vue'
import QQBotDiagnostics from '@/components/admin/qqbot/QQBotDiagnostics.vue'
import { enableAutonomousChat } from '@/components/admin/qqbot/autonomousDefaults'
import { useQQDraft } from '@/components/admin/qqbot/useQQDraft'
import { cloneConfig, configSections, pickSection, type ConfigSection } from '@/components/admin/qqbot/configSections'
import { qqbotAPI, defaultAutonomousConfig, defaultModerationConfig, type QQBotConfig } from '@/api/qqbot'
const { t } = useI18n()
const form = ref<QQBotConfig>(), saved = ref<QQBotConfig>(), formElement = ref<HTMLFormElement>()
const siteAISettings = ref<InstanceType<typeof QQBotSiteAISettings>>()
const imageAISettings = ref<InstanceType<typeof QQBotImageAISettings>>()
const autoChatEnabled = computed({
 get: () => form.value?.autonomous?.enabled ?? false,
 set: enabled => {
  if (!form.value?.autonomous) return
  form.value.autonomous = enabled ? enableAutonomousChat(form.value.autonomous) : { ...form.value.autonomous, enabled: false }
 }
})
const regenerateSiteKey = ref(false)
const regenerateImageSiteKey = ref(false), imageQuota = ref(0)
const step = ref(0), quota = ref(0), busy = ref(false), probing = ref(false), ready = ref(false), reviewed = ref(false), diagnosticRevision = ref(0)
const error = ref(''), notice = ref(''), warning = ref(''), discovered = ref<string[]>([])
const { dirty, markSaved } = useQQDraft(() => form.value ? { config: form.value, regenerate: regenerateSiteKey.value, quota: quota.value, regenerateImage: regenerateImageSiteKey.value, imageQuota: imageQuota.value } : undefined)
const steps = ['credentials', 'groupsAI', 'capabilities', 'verify']
const limits = [{ key: 'daily_chat_limit', label: 'dailyChat' }, { key: 'total_daily_chat_limit', label: 'totalChat' }, { key: 'daily_image_limit', label: 'dailyImage' }, { key: 'total_daily_image_limit', label: 'totalImage' }] as const
const knownGroups = computed(() => [...new Set([...(form.value?.allowed_groups || []), ...discovered.value])])
const manualGroups = computed({ get: () => form.value?.allowed_groups.join('\n') || '', set: value => { if (form.value) form.value.allowed_groups = [...new Set(value.split(/[\s,，]+/).filter(Boolean))] } })
const webhookURL = computed(() => window.location.origin + '/api/v1/qqbot/webhook')
let alive = true, request = 0
function normalize(c: QQBotConfig): QQBotConfig { return { ...c, autonomous: { ...defaultAutonomousConfig(), ...c.autonomous }, moderation: { ...defaultModerationConfig(), ...c.moderation } } }
async function reload() { if (dirty.value && !window.confirm(t('qqbot.workspace.leaveConfirm'))) return; busy.value = true; error.value = ''; const seq = ++request; try { const c = normalize(await qqbotAPI.config()); if (!alive || seq !== request) return; saved.value = cloneConfig(c); form.value = cloneConfig(c); markSaved(); if (!c.app_id) { form.value.ai_connection = 'site'; form.value.autonomous!.inherit_groups = true; form.value.moderation!.inherit_groups = true } ready.value = false; reviewed.value = false; step.value = 0; await refreshGroups() } catch (e) { if (alive) error.value = (e as Error).message } finally { if (alive) busy.value = false } }
async function refreshGroups() { const identity = saved.value?.app_id; try { const r = await qqbotAPI.runtime(); if (alive && identity === saved.value?.app_id) discovered.value = r.groups.map(g => g.id) } catch (e) { if (alive) error.value = (e as Error).message } }
async function saveSections(sections: ConfigSection[]) {
  const pending = cloneConfig(form.value!)
  const previous = cloneConfig(saved.value!)
  const unsaved = (Object.keys(configSections) as ConfigSection[]).filter(section => !sections.includes(section) && JSON.stringify(pickSection(pending, section)) !== JSON.stringify(pickSection(previous, section)))
  const result = normalize(await qqbotAPI.saveSetup({ config: pending, sections, versions: saved.value?.section_versions || {}, site_quota: quota.value, regenerate_site_key: regenerateSiteKey.value, image_site_quota: imageQuota.value, regenerate_image_site_key: regenerateImageSiteKey.value }))
  if (!alive) return
  warning.value = result.setup_warning || ''
  if (sections.includes('chat')) regenerateSiteKey.value = false
  if (sections.includes('images')) regenerateImageSiteKey.value = false
  saved.value = cloneConfig(result)
  form.value = cloneConfig(result)
  markSaved()
  for (const section of unsaved) {
    Object.assign(form.value, pickSection(pending, section))
    if (previous.app_id === result.app_id && saved.value.section_versions) saved.value.section_versions[section] = previous.section_versions?.[section] || ''
  }

}
async function next() {
  if (busy.value || !form.value || !formElement.value?.reportValidity()) return
  if (step.value === 1 && siteAISettings.value && !siteAISettings.value.validate()) return
  if (step.value === 2 && imageAISettings.value && !imageAISettings.value.validate()) return
  error.value = ''; notice.value = ''; busy.value = true
  try {
    if (step.value === 0) { await saveSections(['connection']); await refreshGroups(); notice.value = t('qqbot.simple.credentialsSaved') }
    if (step.value === 1 && !form.value.allowed_groups.length) throw new Error(t('qqbot.simple.selectGroups'))
    if (step.value === 2) {
      if (form.value.chat_disabled && !form.value.site_group_id && !saved.value?.site_api_key_id) form.value.ai_connection = 'external'
      const sections = (Object.keys(configSections) as ConfigSection[]).filter(section => JSON.stringify(pickSection(form.value!, section)) !== JSON.stringify(pickSection(saved.value!, section)))
      if (regenerateSiteKey.value && !sections.includes('chat')) sections.push('chat')
      if (regenerateImageSiteKey.value && !sections.includes('images')) sections.push('images')
      if (sections.length) await saveSections(sections)
      ready.value = false; reviewed.value = false; diagnosticRevision.value++; notice.value = t('qqbot.simple.savedCheck')
    }
    if (alive) step.value++
  } catch (e) { if (alive) error.value = (e as Error).message } finally { if (alive) busy.value = false }
}
async function enable() {
  if (!ready.value || !reviewed.value || probing.value || busy.value || !form.value) return
  busy.value = true; error.value = ''
  try { form.value.enabled = true; await saveSections(['connection']); notice.value = t('qqbot.simple.enabled') }
  catch (e) { form.value.enabled = saved.value?.enabled || false; error.value = (e as Error).message; ready.value = false; diagnosticRevision.value++ }
  finally { busy.value = false }
}
watch(() => form.value?.chat_disabled, disabled => { if (disabled && form.value?.autonomous) form.value.autonomous.enabled = false })
onMounted(reload)
onBeforeUnmount(() => { alive = false; request++ })
</script>
