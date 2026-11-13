import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en/dashboard'
import zh from '@/i18n/locales/zh/dashboard'
import UserErrorDetailModal from '../UserErrorDetailModal.vue'

vi.mock('@/api/usage', () => ({ getMyErrorDetail: async () => ({
  id: 833, created_at: '2026-11-13T10:00:00Z', model: 'example-model', inbound_endpoint: '/v1/responses',
  status_code: 200, category: 'transport_error', platform: 'openai', message: 'safe server fallback',
  reset_reason: 'INTERNAL_ERROR', error_body: '{"error":{"code":"upstream_http2_stream_error"}}', key_name: 'Demo key', key_deleted: false
}) }))
enableAutoUnmount(afterEach)

describe('peer reset detail in the real user component', () => {
  it.each([
    ['en', 'Transport error', 'reset by peer', 'Content may be incomplete', 'Do not automatically retry', 'Actual client HTTP status', 'HTTP 200 does not mean generation completed', 'Gateway-generated error details'],
    ['zh', '传输错误', '被对端重置', '内容可能不完整', '请勿自动重试', '客户端实际 HTTP 状态码', 'HTTP 200 不表示生成已完成', '网关生成的错误详情']
  ])('renders safe %s copy and the committed status', async (locale, category, reset, incomplete, retry, statusLabel, explanation, generatedBodyLabel) => {
    const i18n = createI18n({ legacy: false, locale, messages: { en, zh } })
    const wrapper = mount(UserErrorDetailModal, {
      props: { show: false, errorId: 833 },
      global: { plugins: [i18n], stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' } } }
    })
    await wrapper.setProps({ show: true }); await flushPromises()
    for (const text of [category, reset, incomplete, retry, statusLabel, explanation, generatedBodyLabel, 'HTTP/2 INTERNAL_ERROR', '200']) expect(wrapper.text()).toContain(text)
    for (const text of ['502', 'stream ID', 'safe server fallback', 'usage.errors.', '上游响应内容']) expect(wrapper.text()).not.toContain(text)
  })
})
