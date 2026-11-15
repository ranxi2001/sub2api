import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import QQBotBindView from '../QQBotBindView.vue'

const api = vi.hoisted(() => ({ previewEmail: vi.fn(), confirmEmail: vi.fn() }))
const route = vi.hoisted(() => ({ hash: '', path: '/qqbot/bind', query: {} }))
const router = vi.hoisted(() => ({ replace: vi.fn() }))
vi.mock('@/api/qqbot', () => ({ qqbotAPI: api }))
vi.mock('@/components/layout/AuthLayout.vue', () => ({ default: { template: '<div><slot /></div>' } }))
vi.mock('vue-router', () => ({ useRoute: () => route, useRouter: () => router }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const options = { global: { stubs: { AuthLayout: { template: '<div><slot /></div>' } } } }
const token = 'a'.repeat(48)

describe('QQ email binding confirmation', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    route.hash = `#token=${token}`
    window.history.replaceState({}, '', `/qqbot/bind${route.hash}`)
    router.replace.mockImplementation(async () => window.history.replaceState({}, '', '/qqbot/bind'))
  })
  it('previews without binding and only submits the displayed identity on a click', async () => {
    api.previewEmail.mockResolvedValue({ member_id: 'qq-member', group_id: 'qq-group', nickname: 'Alice' })
    api.confirmEmail.mockResolvedValue({ bound: true })
    const wrapper = mount(QQBotBindView, options)
    await flushPromises()
    expect(window.location.hash).toBe('')
    expect(api.previewEmail).toHaveBeenCalledWith(token)
    expect(api.confirmEmail).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('Alice')
    expect(wrapper.text()).toContain('qq-group')
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(api.confirmEmail).toHaveBeenCalledWith(token, 'qq-member')
    expect(wrapper.text()).toContain('qqbot.emailBindSuccess')
    expect(wrapper.find('button').exists()).toBe(false)
  })
  it('rejects missing tokens without making a request', async () => {
    route.hash = ''
    const wrapper = mount(QQBotBindView, options)
    await flushPromises()
    expect(api.previewEmail).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('qqbot.emailBindInvalid')
    expect(wrapper.find('button').exists()).toBe(false)
  })
  it('shows an expired-link error without offering confirmation', async () => {
    api.previewEmail.mockRejectedValue(new Error('Expired link'))
    const wrapper = mount(QQBotBindView, options)
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('Expired link')
    expect(api.confirmEmail).not.toHaveBeenCalled()
    expect(wrapper.find('button').exists()).toBe(false)
  })
})
