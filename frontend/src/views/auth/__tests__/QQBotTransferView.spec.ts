import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import QQBotTransferView from '../QQBotTransferView.vue'

const api = vi.hoisted(() => ({ previewTransfer: vi.fn(), confirmTransfer: vi.fn() }))
vi.mock('@/api/qqbot', () => ({ qqbotAPI: api }))
vi.mock('@/components/layout/AuthLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))

function i18n(locale: 'zh' | 'en') {
  return createI18n({ legacy: false, locale, messages: { zh, en } })
}

describe('QQBotTransferView', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    api.previewTransfer.mockResolvedValue({
      from_email: 'sender@example.com', to_email: 'recipient@example.com', amount: '5.0000',
      fee: '0.0500', net_amount: '4.9500', expires_at: '2026-10-16T10:00:00Z'
    })
    api.confirmTransfer.mockResolvedValue({ transferred: true })
  })

  it.each(['zh', 'en'] as const)('loads the token from the fragment and confirms the transfer (%s)', async locale => {
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/qqbot/transfer', component: QQBotTransferView }] })
    await router.push('/qqbot/transfer#token=0123456789abcdef0123456789abcdef0123456789abcdef')
    const wrapper = mount(QQBotTransferView, { global: { plugins: [i18n(locale), router] } })
    await flushPromises()
    expect(api.previewTransfer).toHaveBeenCalledWith('0123456789abcdef0123456789abcdef0123456789abcdef')
    expect(wrapper.text()).toContain('sender@example.com')
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(api.confirmTransfer).toHaveBeenCalledWith('0123456789abcdef0123456789abcdef0123456789abcdef')
    expect(wrapper.get('[role="status"]').text()).toBe(i18n(locale).global.t('qqbot.transferSuccess'))
  })

  it('shows a retry message for an invalid fragment token', async () => {
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/qqbot/transfer', component: QQBotTransferView }] })
    await router.push('/qqbot/transfer#token=bad')
    const wrapper = mount(QQBotTransferView, { global: { plugins: [i18n('zh'), router] } })
    await flushPromises()
    expect(api.previewTransfer).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toBe(i18n('zh').global.t('qqbot.transferInvalid'))
  })
})
