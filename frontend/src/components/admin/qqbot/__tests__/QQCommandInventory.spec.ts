import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import QQCommandInventory from '../QQCommandInventory.vue'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'

const commands = vi.hoisted(() => vi.fn())
vi.mock('@/api/qqbot', () => ({ qqbotAPI: { commands } }))
const wrappers: VueWrapper[] = []

describe('QQ command inventory', () => {
  beforeEach(() => {
    commands.mockResolvedValue([
      { command: '/签到', usage: '/签到', description: '每日签到', builtin: true },
      { command: '/faq', usage: '/faq', description: 'Custom reply', builtin: false, reply: '<img src="bad" />' }
    ])
  })
  afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); vi.clearAllMocks() })

  it.each(['zh', 'en'] as const)('%s displays the API command inventory and escapes custom reply previews', async locale => {
    const i18n = createI18n({ legacy: false, locale, messages: { zh, en } })
    const wrapper = mount(QQCommandInventory, { global: { plugins: [i18n] } })
    wrappers.push(wrapper)
    await flushPromises()
    expect(wrapper.findAll('[data-testid="qq-command-inventory-item"]')).toHaveLength(2)
    expect(wrapper.findAll('code').map(code => code.text())).toEqual(['签到', 'faq'])
    expect(wrapper.text()).not.toContain('/举报垃圾信息')
    expect(wrapper.find('[data-testid="qq-report-command-help"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('<img src="bad" />')
    expect(wrapper.find('img').exists()).toBe(false)
    expect(commands).toHaveBeenCalledOnce()
  })
})
