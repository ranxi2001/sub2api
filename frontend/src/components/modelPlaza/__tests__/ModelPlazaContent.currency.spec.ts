import { defaultPlazaCurrencySettings } from '@/utils/plazaCurrency'
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import ModelPlazaContent from '../ModelPlazaContent.vue'
import PlazaGroupSection from '../PlazaGroupSection.vue'
import type { ModelPlazaResponse } from '@/api/modelPlaza'
import zh from '@/i18n/locales/zh/dashboard'
import en from '@/i18n/locales/en/dashboard'

vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isAuthenticated: false }) }))

const response: ModelPlazaResponse = {
  description: '', usd_to_cny_rate: 7, currency: defaultPlazaCurrencySettings(),
  groups: [{ id: 1, name: 'Demo', description: '', platform: 'deepseek', subscription_type: 'standard', rate_multiplier: 0.5,
    peak_rate_enabled: false, peak_start: '', peak_end: '', peak_rate_multiplier: 1, is_exclusive: false,
    image_rate_independent: false, image_rate_multiplier: 1, video_rate_independent: false, video_rate_multiplier: 1,
    long_context_pricing_enabled: false, models: [] }]
}

function render(data = response, locale = 'zh') {
  return mount(ModelPlazaContent, { props: { response: data, loading: false }, global: {
    plugins: [createI18n({ legacy: false, locale, messages: { zh, en } })],
    stubs: { PlazaGroupSection: true, PlazaFilterBar: true, Icon: true }
  } })
}

describe('ModelPlazaContent currency selection', () => {
  it.each(['zh', 'en'])('applies site settings to all groups and exposes the basis in %s', async locale => {
    const wrapper = render(response, locale)
    expect(wrapper.text()).toContain('1 USD = 7.00 CNY')
    expect(wrapper.findComponent(PlazaGroupSection).props('priceDisplay')).toEqual({ usdToCny: 7, settings: response.currency })
    const settings = { ...defaultPlazaCurrencySettings(), customer_currency: 'CNY' as const, wallet_currency: 'CNY' as const }
    await wrapper.setProps({ response: { ...response, usd_to_cny_rate: 7.2, currency: settings } })
    expect(wrapper.findComponent(PlazaGroupSection).props('priceDisplay')).toEqual({ usdToCny: 7.2, settings })
    expect(wrapper.text()).toContain('CNY')
    wrapper.unmount()
  })
  it('keeps an old backend in explicitly labelled native units', () => {
    const wrapper = render({ ...response, usd_to_cny_rate: undefined, currency: undefined })
    expect(wrapper.find('[data-currency]').exists()).toBe(false)
    expect(wrapper.text()).toContain('客户价显示站内额度')
    expect(wrapper.findComponent(PlazaGroupSection).props('priceDisplay')).toBeUndefined()
    wrapper.unmount()
  })
})
