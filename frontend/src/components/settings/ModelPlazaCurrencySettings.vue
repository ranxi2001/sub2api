<template>
  <div ref="root" class="space-y-4" data-testid="plaza-currency-settings">
    <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.settings.features.modelPlaza.currencyHelp') }}</p>
    <div class="grid gap-4 sm:grid-cols-2">
      <label class="text-sm text-gray-700 dark:text-gray-300">{{ t('admin.settings.features.modelPlaza.customerCurrency') }}
        <select v-model="local.customer_currency" class="input mt-1" data-testid="plaza-customer-currency" @change="publish"><option value="USD">USD ($)</option><option value="CNY">CNY (¥)</option></select>
      </label>
      <label class="text-sm text-gray-700 dark:text-gray-300">{{ t('admin.settings.features.modelPlaza.officialCurrency') }}
        <select v-model="local.official_currency" class="input mt-1" data-testid="plaza-official-currency" @change="publish"><option value="USD">USD ($)</option><option value="CNY">CNY (¥)</option></select>
      </label>
    </div>
    <div>
      <label for="plaza-wallet-value" class="text-sm text-gray-700 dark:text-gray-300">{{ t('admin.settings.features.modelPlaza.walletValue') }}</label>
      <div class="mt-1 flex gap-2">
        <input id="plaza-wallet-value" v-model.number="local.wallet_unit_value" type="number" min="0.000001" step="any" required class="input max-w-xs" @input="publish" />
        <select v-model="local.wallet_currency" class="input max-w-32" :aria-label="t('admin.settings.features.modelPlaza.walletCurrency')" @change="publish"><option value="USD">USD ($)</option><option value="CNY">CNY (¥)</option></select>
      </div>
    </div>
    <div>
      <p class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.features.modelPlaza.overrides') }}</p>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.settings.features.modelPlaza.overridesHelp') }}</p>
      <div v-for="(rule, index) in rules" :key="rule.id" class="mt-2 flex flex-wrap gap-2">
        <select v-model="rule.scope" class="input max-w-32" :aria-label="t('admin.settings.features.modelPlaza.scope')" @change="publish"><option value="platform">{{ t('admin.settings.features.modelPlaza.platform') }}</option><option value="model">{{ t('admin.settings.features.modelPlaza.model') }}</option></select>
        <input v-model="rule.target" class="input min-w-40 flex-1" required maxlength="256" :aria-label="t('admin.settings.features.modelPlaza.target')" :placeholder="rule.scope === 'platform' ? 'deepseek' : 'deepseek-v4-pro'" data-rule-target @input="publish" />
        <select v-model="rule.currency" class="input max-w-32" :aria-label="t('admin.settings.features.modelPlaza.ruleCurrency')" @change="publish"><option value="USD">USD ($)</option><option value="CNY">CNY (¥)</option></select>
        <button type="button" class="btn btn-secondary" @click="rules.splice(index, 1); publish()">{{ t('common.remove') }}</button>
      </div>
      <p v-if="error" class="mt-2 text-xs text-red-600">{{ error }}</p>
      <button type="button" class="btn btn-secondary mt-2" @click="rules.push({ id: nextId++, scope: 'platform', target: '', currency: 'USD' })">{{ t('admin.settings.features.modelPlaza.addRule') }}</button>
    </div>
  </div>
</template>
<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PlazaCurrencySettings } from '@/api/modelPlaza'
import { defaultPlazaCurrencySettings, type PlazaCurrency } from '@/utils/plazaCurrency'
const props = defineProps<{ modelValue?: PlazaCurrencySettings }>()
const emit = defineEmits<{ 'update:modelValue': [value: PlazaCurrencySettings] }>()
const { t } = useI18n()
const root = ref<HTMLElement>()
const local = ref(defaultPlazaCurrencySettings())
const error = ref('')
let nextId = 0
let lastEmitted = ''
const rules = ref<Array<{ id: number; scope: 'platform' | 'model'; target: string; currency: PlazaCurrency }>>([])
watch(() => props.modelValue, value => {
  if (JSON.stringify(value) === lastEmitted) return
  local.value = { ...defaultPlazaCurrencySettings(), ...value }
  rules.value = (['platform', 'model'] as const).flatMap(scope => Object.entries(local.value[scope === 'platform' ? 'official_platform_currencies' : 'official_model_currencies']).map(([target, currency]) => ({ id: nextId++, scope, target, currency })))
}, { immediate: true })
function publish() {
  const platform: Record<string, PlazaCurrency> = {}
  const model: Record<string, PlazaCurrency> = {}
  error.value = ''
  for (const rule of rules.value) {
    const key = rule.target.trim().toLowerCase()
    const map = rule.scope === 'platform' ? platform : model
    if (!key || Object.prototype.hasOwnProperty.call(map, key)) { error.value = t('admin.settings.features.modelPlaza.invalidRules'); break }
    Object.defineProperty(map, key, { value: rule.currency, enumerable: true, configurable: true })
  }
  void nextTick(() => root.value?.querySelectorAll<HTMLInputElement>('[data-rule-target]').forEach(input => input.setCustomValidity(error.value)))
  if (error.value || !Number.isFinite(local.value.wallet_unit_value) || local.value.wallet_unit_value <= 0) return
  const value = { ...local.value, official_platform_currencies: platform, official_model_currencies: model }
  lastEmitted = JSON.stringify(value)
  emit('update:modelValue', value)
}
</script>
