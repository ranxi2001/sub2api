import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { describe, expect, it, vi } from 'vitest'
import en from '@/i18n/locales/en/usageApi'
import zh from '@/i18n/locales/zh/usageApi'
import UsageApiGuide from '../UsageApiGuide.vue'

const { settings, copy } = vi.hoisted(() => ({ settings: { api_base_url: '' }, copy: vi.fn() }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ cachedPublicSettings: settings }) }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: copy }) }))

describe('UsageApiGuide', () => {
  it.each([
    ['en', 'https://api.example/relay/v1/', 'https://api.example/relay/v1/usage/records'],
    ['zh', '', window.location.origin + '/v1/usage/records'],
  ])('renders and copies the localized examples (%s)', async (locale, base, endpoint) => {
    settings.api_base_url = base
    copy.mockClear()
    const i18n = createI18n({ legacy: false, locale, messages: {
      en: { usageApi: en, common: { copy: 'Copy' } }, zh: { usageApi: zh, common: { copy: '复制' } },
    } })
    const wrapper = mount(UsageApiGuide, { global: { plugins: [i18n] } })
    expect(wrapper.text()).toContain(locale === 'en' ? en.intro : zh.intro)
    expect(wrapper.text()).not.toContain('usageApi.')
    expect(wrapper.get('article > div code').text()).toBe('GET ' + endpoint)
    expect(wrapper.findAll('tbody th').map(cell => cell.text())).toEqual(['start_time', 'end_time', 'limit', 'cursor', 'format'])
    for (const text of ['X-Request-Id', 'X-Sub2api-Usage-Id', 'cyber_policy', '60', '120', '409', 'SUB2API_USAGE_OVERLAP_HOURS']) {
      expect(wrapper.text()).toContain(text)
    }
    const examples = wrapper.findAll('pre code').map(el => el.element.textContent)
    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(4)
    for (const [index, button] of buttons.entries()) {
      await button.trigger('click')
      expect(copy).toHaveBeenLastCalledWith(examples[index])
    }
    expect(examples[0]).toContain(endpoint)
    expect(JSON.parse(examples[1]!)).toMatchObject({ object: 'list', next_cursor: null, data: [{ id: 42 }] })
    expect(examples[2]).toContain('format=csv')
    expect(examples[3]).toContain(endpoint)
    expect(examples[3]).toContain('page["next_cursor"]')
    wrapper.unmount()
  })
})
