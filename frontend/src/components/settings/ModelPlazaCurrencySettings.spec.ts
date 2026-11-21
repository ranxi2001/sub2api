import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import ModelPlazaCurrencySettings from './ModelPlazaCurrencySettings.vue'
import { defaultPlazaCurrencySettings } from '@/utils/plazaCurrency'
import type { PlazaCurrencySettings } from '@/api/modelPlaza'

function render() {
 return mount(ModelPlazaCurrencySettings, { props: { modelValue: defaultPlazaCurrencySettings() }, global: { plugins: [createI18n({ legacy:false, locale:'en', missingWarn:false, fallbackWarn:false, messages:{en:{}} })] } })
}
describe('admin plaza display policy', () => {
 it('edits display currency and arbitrary platform overrides without payment fields', async () => {
  const wrapper=render()
  expect(wrapper.get('[data-testid="plaza-customer-currency"]').element).toHaveProperty('value','USD')
  await wrapper.get('[data-testid="plaza-customer-currency"]').setValue('CNY')
  await wrapper.get('button').trigger('click')
  await wrapper.get('[data-rule-target]').setValue(' CUSTOM-Provider ')
  const selects=wrapper.findAll('select')
  await selects.at(-1)!.setValue('CNY')
  const value=wrapper.emitted('update:modelValue')!.at(-1)![0] as PlazaCurrencySettings
  expect(value.customer_currency).toBe('CNY')
  expect(value.official_platform_currencies).toEqual({'custom-provider':'CNY'})
  expect(value).not.toHaveProperty('balance_recharge_multiplier')
  expect(value.wallet_currency).toBe('USD')
  wrapper.unmount()
 })
})
