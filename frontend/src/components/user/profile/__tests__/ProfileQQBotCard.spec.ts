import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ProfileQQBotCard from '../ProfileQQBotCard.vue'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'

const api = vi.hoisted(() => ({ binding: vi.fn(), createCode: vi.fn(), confirm: vi.fn() }))
vi.mock('@/api/qqbot', () => ({ qqbotAPI: api }))

describe('QQ binding confirmation', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })
  afterEach(() => {
    try {
      // Development logs message errors instead of throwing as production does.
      expect(console.error).not.toHaveBeenCalled()
    } finally {
      vi.restoreAllMocks()
    }
  })
  it.each(['zh', 'en'] as const)('%s renders code instructions and requires separate confirmation', async (locale) => {
    const i18n = createI18n({ legacy: false, locale, messages: { zh, en } })
    api.binding.mockResolvedValue({ enabled: true, binding: null, pending: null })
    api.createCode.mockResolvedValue({ code: 'single-use-code' })
    const wrapper = mount(ProfileQQBotCard, { global: { plugins: [i18n] } })
    await flushPromises()
    await wrapper.findAll('button')[0].trigger('click')
    await flushPromises()
    expect(wrapper.get('code').text()).toBe('绑定 single-use-code')
    expect(wrapper.text()).toContain(i18n.global.t('qqbot.sendCode'))
    expect(wrapper.text()).not.toContain('qqbot.sendCode')
    if (locale === 'zh') expect(wrapper.text()).toContain('在允许的群里发送：')
    expect(api.confirm).not.toHaveBeenCalled()
    api.binding.mockResolvedValue({ enabled: true, binding: null, pending: { member_id: 'qq-member', nickname: 'Alice', group_id: 'allowed-group' } })
    await wrapper.findAll('button')[1].trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Alice')
    expect(api.confirm).not.toHaveBeenCalled()
    api.confirm.mockResolvedValue({ bound: true })
    api.binding.mockResolvedValue({ enabled: true, binding: { member_id: 'qq-member', nickname: 'Alice' }, pending: null })
    await wrapper.findAll('button')[2].trigger('click')
    await flushPromises()
    expect(api.confirm).toHaveBeenCalledWith('qq-member')
    expect(wrapper.text()).not.toContain('single-use-code')
    expect(wrapper.text()).toContain(i18n.global.t('qqbot.bound'))
  })
  it('does not show binding controls when the bot is disabled', async () => {
    const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh, en } })
    api.binding.mockResolvedValue({ enabled: false, binding: null, pending: null })
    const wrapper = mount(ProfileQQBotCard, { global: { plugins: [i18n] } })
    await flushPromises()
    expect(wrapper.find('section').exists()).toBe(false)
  })
})
