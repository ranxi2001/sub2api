import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ServerlessSettings from './ServerlessSettings.vue'
import { apiClient } from '@/api/client'
import { createPinia, setActivePinia } from 'pinia'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'

vi.mock('@/api/client', () => ({ apiClient: { get: vi.fn(), put: vi.fn(), post: vi.fn() } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: { value: 'zh-CN' }, t: (key: string) => key }) }))
const pod = { id: 'us-one', boot: 'boot', endpoint: 'https://pod.example.com', region: 'US', version: 'test', ready: true, at: 100 }
const config = () => ({ enabled: false, pods: [], regions: [] })
beforeEach(() => {
  vi.resetAllMocks()
  vi.mocked(apiClient.get).mockResolvedValue({ data: { config: config(), pods: [pod], stats: { 'us-one|US|region|requests': '12', 'us-one|US|region|errors': '2' } } })
  vi.mocked(apiClient.put).mockImplementation(async (_url, value) => ({ data: value }))
})
async function opened() {
  const wrapper = mount(ServerlessSettings)
  const details = wrapper.get('details')
  ;(details.element as HTMLDetailsElement).open = true
  await details.trigger('toggle')
  await flushPromises()
  return wrapper
}
function button(wrapper: ReturnType<typeof mount>, text: string) {
  const found = wrapper.findAll('button').find(b => b.text() === text)
  if (!found) throw new Error('Missing button: ' + text)
  return found
}
describe('Gateway Serverless settings', () => {
  it('does not fetch or enable routing until the optional panel opens', async () => {
    const wrapper = mount(ServerlessSettings)
    await flushPromises()
    expect(apiClient.get).not.toHaveBeenCalled()
    expect((wrapper.get('[data-testid="serverless-enabled"]').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.find('form').exists()).toBe(false)
    wrapper.unmount()
  })
  it('approves a reported endpoint without implicitly enabling it', async () => {
    const wrapper = await opened()
    await button(wrapper, '确认此地址').trigger('click')
    expect(apiClient.put).not.toHaveBeenCalled()
    await button(wrapper, '保存 Serverless 配置').trigger('click')
    await flushPromises()
    expect(apiClient.put).toHaveBeenCalledWith('/admin/serverless', { enabled: false, affinity_scope: 'api_key', fallback_to_primary: false, pods: [{ id: pod.id, endpoint: pod.endpoint, enabled: false }], regions: [] })
    expect(wrapper.text()).toContain('心跳就绪')
    expect(wrapper.text()).toContain('地区命中')
    wrapper.unmount()
  })
  it('refreshes runtime state without overwriting unsaved routing changes', async () => {
    const wrapper = await opened()
    await wrapper.get('[data-testid="serverless-enabled"]').setValue(true)
    await button(wrapper, '刷新状态').trigger('click')
    await flushPromises()
    expect((wrapper.get('[data-testid="serverless-enabled"]').element as HTMLInputElement).checked).toBe(true)
    wrapper.unmount()
  })
  it('saves a country-to-Pod rule without requiring IP datasets', async () => {
    const wrapper = await opened()
    await button(wrapper, '确认此地址').trigger('click')
    await button(wrapper, '添加地区').trigger('click')
    await wrapper.get('input[placeholder="US"]').setValue('us')
    await wrapper.get('input[value="us-one"]').setValue(true)
    await button(wrapper, '保存 Serverless 配置').trigger('click')
    await flushPromises()
    expect(apiClient.post).not.toHaveBeenCalled()
    const payload = vi.mocked(apiClient.put).mock.calls[0]?.[1]
    expect(payload.regions).toEqual([{ country: 'US', pod_ids: ['us-one'] }])
    expect(wrapper.find('textarea').exists()).toBe(false)
    wrapper.unmount()
  })
  it('keeps the draft on save failure and shows an actionable error', async () => {
    const wrapper = await opened()
    await wrapper.get('[data-testid="serverless-enabled"]').setValue(true)
    vi.mocked(apiClient.put).mockRejectedValue(new Error('network'))
    await button(wrapper, '保存 Serverless 配置').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('集群密钥')
    expect((wrapper.get('[data-testid="serverless-enabled"]').element as HTMLInputElement).checked).toBe(true)
    wrapper.unmount()
  })
  it('loads legacy settings with session-preserving defaults and explains the shared country cache', async () => {
    const wrapper = await opened()
    expect((wrapper.get('[data-testid="serverless-affinity-scope"]').element as HTMLSelectElement).value).toBe('api_key')
    expect((wrapper.get('[data-testid="serverless-primary-fallback"]').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.text()).toContain('Redis 缓存 24 小时')
    expect(wrapper.text()).toContain('不会重试已发送请求')
    wrapper.unmount()
  })
  it('saves IP-scoped bindings and explicit fallback without changing them on refresh', async () => {
    const wrapper = await opened()
    await wrapper.get('[data-testid="serverless-affinity-scope"]').setValue('api_key_ip')
    await wrapper.get('[data-testid="serverless-primary-fallback"]').setValue(true)
    await button(wrapper, '刷新状态').trigger('click')
    await flushPromises()
    await button(wrapper, '保存 Serverless 配置').trigger('click')
    await flushPromises()
    expect(apiClient.put).toHaveBeenCalledWith('/admin/serverless', expect.objectContaining({ affinity_scope: 'api_key_ip', fallback_to_primary: true }))
    expect((wrapper.get('[data-testid="serverless-primary-fallback"]').element as HTMLInputElement).checked).toBe(true)
    wrapper.unmount()
  })
  it('shares and persists the new notice independently of older and later notices', async () => {
    localStorage.clear()
    const pinia = createPinia()
    setActivePinia(pinia)
    const store = useNewFeatureNoticeStore()
    const oldNotice = NEW_FEATURE_NOTICES.protocolFeatureSwitches
    store.dismiss(41, oldNotice)
    const wrapper = mount(ServerlessSettings, { props: { userId: 41 }, global: { plugins: [pinia] } })
    expect(wrapper.findAll('[data-testid="new-feature-badge"]')).toHaveLength(2)
    await wrapper.get('[data-testid="new-feature-badge"]').trigger('click')
    expect(wrapper.findAll('[data-testid="new-feature-badge"]')).toHaveLength(0)
    expect(store.isDismissed(41, oldNotice)).toBe(true)
    wrapper.unmount()
    const fresh = createPinia()
    const reloaded = mount(ServerlessSettings, { props: { userId: 41 }, global: { plugins: [fresh] } })
    expect(reloaded.findAll('[data-testid="new-feature-badge"]')).toHaveLength(0)
    setActivePinia(fresh)
    expect(useNewFeatureNoticeStore().isDismissed(41, 'a-later-feature-announcement')).toBe(false)
    expect(useNewFeatureNoticeStore().isDismissed(42, NEW_FEATURE_NOTICES.serverlessIPRouting)).toBe(false)
    reloaded.unmount()
    localStorage.clear()
  })
})
