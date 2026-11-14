import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { defineComponent, ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import ExcelBPSSearchOptions from '../ExcelBPSSearchOptions.vue'
import { defaultExcelBPSDefaults, excelBPSDefaultsPayload } from '@/utils/excelBPSDefaults'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 42 } }) }))
describe('BPS search options', () => {
  it('requires search before automatic search and clears the dependent toggle', async () => {
    const host = defineComponent({ components: { ExcelBPSSearchOptions }, setup: () => ({ enabled: ref(false), automatic: ref(false) }), template: '<ExcelBPSSearchOptions v-model:enabled="enabled" v-model:automatic="automatic" />' })
    const wrapper = mount(host, { global: { plugins: [createPinia()] } })
    const search = wrapper.get<HTMLInputElement>('[data-testid="excel-bps-web-search"]')
    const automatic = wrapper.get<HTMLInputElement>('[data-testid="excel-bps-web-search-auto"]')
    expect(search.element.checked).toBe(false)
    expect(automatic.element.disabled).toBe(true)
    await search.setValue(true)
    await automatic.setValue(true)
    expect(automatic.element.checked).toBe(true)
    await search.setValue(false)
    expect(automatic.element.checked).toBe(false)
    expect(automatic.element.disabled).toBe(true)
    wrapper.unmount()
  })
  it('keeps old templates off and normalizes inconsistent automatic settings', () => {
    const defaults = defaultExcelBPSDefaults()
    expect(defaults.web_search).toBe(false)
    expect(defaults.web_search_auto).toBe(false)
    expect(excelBPSDefaultsPayload({ ...defaults, web_search_auto: true }).web_search_auto).toBe(false)
  })
})
