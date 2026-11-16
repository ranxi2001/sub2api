import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent, ref } from 'vue'
import BPSProxyRoutingSettings from '../BPSProxyRoutingSettings.vue'
import type { Proxy } from '@/types'

const { testProxy, getProxyAccounts } = vi.hoisted(() => ({ testProxy: vi.fn(), getProxyAccounts: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { proxies: { testProxy, getProxyAccounts } } }))
vi.mock('@/components/common/NewFeatureBadge.vue', () => ({ default: { template: '<span />' } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => key + (params ? ' ' + JSON.stringify(params) : '') }) }))
enableAutoUnmount(afterEach)
beforeEach(() => { vi.clearAllMocks(); getProxyAccounts.mockResolvedValue([]); testProxy.mockResolvedValue({ success: true, message: 'ok', latency_ms: 42, country: 'GB' }) })

function proxy(id: number, overrides: Partial<Proxy> = {}): Proxy {
  return { id, name: 'Proxy ' + id, host: '10.0.0.' + id, port: 8080, protocol: 'http', status: 'active', expires_at: null, ...overrides } as Proxy
}
function render(proxies: Proxy[], selected: number[] = [], extra: Record<string, unknown> = {}) {
  const host = defineComponent({
    components: { BPSProxyRoutingSettings },
    setup: () => ({ ids: ref(selected), proxies, extra }),
    template: '<BPSProxyRoutingSettings mode="request" v-model:proxy-ids="ids" :proxies="proxies" v-bind="extra" /><output data-testid="selection">{{ JSON.stringify(ids) }}</output>',
  })
  return mount(host, { global: { stubs: { Icon: true } } })
}

describe('BPS account proxy selection', () => {
  it('expands a two-line searchable list and selects all available matches while preserving hidden and unavailable selections', async () => {
    const wrapper = render([proxy(1), proxy(2), proxy(3, { status: 'inactive' }), proxy(4, { name: 'Hidden proxy' })], [3, 4, 99])
    expect(wrapper.find('[data-testid="bps-proxy-options"]').exists()).toBe(false)
    await wrapper.get('[data-testid="bps-proxy-trigger"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="bps-proxy-row-1"]').text()).toContain('http://10.0.0.1:8080')
    await wrapper.get('input[type="search"]').setValue('Proxy 1')
    await wrapper.get('[data-testid="bps-proxy-select-results"]').trigger('click')
    expect(wrapper.get('[data-testid="selection"]').text()).toBe('[3,4,99,1]')
    expect(wrapper.get('[data-testid="bps-proxy-options"]').exists()).toBe(true)
  })

  it('shows live account names and IDs, marks the current account, and searches account references', async () => {
    getProxyAccounts.mockImplementation(async (id: number) => id === 1 ? [
      { id: 511, name: 'Primary BPS', platform: 'openai', type: 'oauth' },
      { id: 512, name: 'Shared Account', platform: 'openai', type: 'oauth' },
    ] : [])
    const wrapper = render([proxy(1, { account_count: 0 }), proxy(2, { account_count: 12 })], [], { currentAccountId: 511 })
    expect(getProxyAccounts).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="bps-proxy-trigger"]').trigger('click')
    await flushPromises()
    const row = wrapper.get('[data-testid="bps-proxy-row-1"]')
    expect(row.text()).toContain('Primary BPS #511')
    expect(row.text()).toContain('Shared Account #512')
    expect(row.text()).toContain('bpsProxyUsageCurrent')
    expect(wrapper.get('[data-testid="bps-proxy-row-2"]').text()).toContain('bpsProxyUsageUnused')
    await wrapper.get('input[type="search"]').setValue('shared account')
    expect(wrapper.findAll('[data-testid^="bps-proxy-row-"]')).toHaveLength(1)
    await wrapper.get('input[type="search"]').setValue('#511')
    expect(wrapper.get('[data-testid="bps-proxy-row-1"]').exists()).toBe(true)
  })

  it('searches the displayed endpoint and port without including proxy credentials', async () => {
    const wrapper = render([proxy(1, { username: 'hidden-user', password: 'hidden-password' }), proxy(2, { port: 9090 })])
    await wrapper.get('[data-testid="bps-proxy-trigger"]').trigger('click')
    for (const query of ['10.0.0.1:8080', 'http://10.0.0.1:8080', '8080']) {
      await wrapper.get('input[type="search"]').setValue(query)
      expect(wrapper.findAll('[data-testid^="bps-proxy-row-"]')).toHaveLength(1)
      expect(wrapper.get('[data-testid="bps-proxy-row-1"]').exists()).toBe(true)
    }
    for (const query of ['hidden-user', 'hidden-password']) {
      await wrapper.get('input[type="search"]').setValue(query)
      expect(wrapper.findAll('[data-testid^="bps-proxy-row-"]')).toHaveLength(0)
    }
  })

  it('keeps unknown usage out of unused selection and preserves hidden or unavailable choices in every bulk operation', async () => {
    getProxyAccounts.mockImplementation(async (id: number) => {
      if (id === 3) throw new Error('Unavailable')
      return id === 1 ? [{ id: 511, name: 'Configured account', platform: 'openai', type: 'oauth' }] : []
    })
    const wrapper = render([proxy(1), proxy(2), proxy(3), proxy(4, { status: 'inactive' }), proxy(5, { name: 'Hidden' })], [1, 4, 5, 99])
    await wrapper.get('[data-testid="bps-proxy-trigger"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="bps-proxy-row-3"]').text()).toContain('bpsProxyUsageUnknown')
    await wrapper.get('input[type="search"]').setValue('Proxy')
    await wrapper.get('[data-testid="bps-proxy-select-unused"]').trigger('click')
    expect(wrapper.get('[data-testid="selection"]').text()).toBe('[4,5,99,2]')
    await wrapper.get('[data-testid="bps-proxy-invert-results"]').trigger('click')
    expect(wrapper.get('[data-testid="selection"]').text()).toBe('[4,5,99,1,3]')
    await wrapper.get('[data-testid="bps-proxy-clear-results"]').trigger('click')
    expect(wrapper.get('[data-testid="selection"]').text()).toBe('[4,5,99]')
    getProxyAccounts.mockResolvedValue([])
    await wrapper.get('[data-testid="bps-proxy-usage-retry-3"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="bps-proxy-row-3"]').text()).toContain('bpsProxyUsageUnused')
  })

  it('preserves selection during catalog loading and load errors, emits retry, and recovers without false missing entries', async () => {
    const wrapper = mount(BPSProxyRoutingSettings, { props: { mode: 'request', proxies: [], proxyIds: [99], loading: true } })
    expect(wrapper.get('[data-testid="bps-proxy-trigger"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).not.toContain('bpsProxyMissing')
    await wrapper.setProps({ loading: false, loadError: true })
    expect(wrapper.text()).toContain('bpsProxyLoadFailed')
    await wrapper.get('[data-testid="bps-proxy-catalog-retry"]').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
    await wrapper.get('[data-testid="bps-proxy-trigger"]').trigger('click')
    expect(wrapper.text()).not.toContain('bpsProxyMissing')
    expect(wrapper.text()).not.toContain('bpsProxyNoMatches')
    await wrapper.get('[data-testid="bps-proxy-select-results"]').trigger('click')
    expect(wrapper.emitted('update:proxyIds')).toBeUndefined()
    await wrapper.setProps({ loading: true })
    await wrapper.setProps({ loading: false, loadError: false, proxies: [proxy(99)] })
    await flushPromises()
    expect(wrapper.get('[data-testid="bps-proxy-99"]').element).toHaveProperty('checked', true)
    expect(wrapper.text()).not.toContain('bpsProxyMissing')
    expect(wrapper.emitted('update:proxyIds')).toBeUndefined()
  })

  it('tests a proxy without changing selection and shows latency or failure details inline', async () => {
    const wrapper = render([proxy(1, { username: 'private', password: 'secret' }), proxy(2)], [1])
    await wrapper.get('[data-testid="bps-proxy-trigger"]').trigger('click')
    await wrapper.get('[data-testid="bps-proxy-test-1"]').trigger('click')
    await flushPromises()
    expect(testProxy).toHaveBeenCalledWith(1)
    expect(wrapper.get('[data-testid="bps-proxy-row-1"]').text()).toContain('42ms')
    expect(wrapper.text()).not.toContain('private')
    expect(wrapper.text()).not.toContain('secret')
    testProxy.mockResolvedValue({ success: false, message: 'Proxy tunnel unavailable' })
    await wrapper.get('[data-testid="bps-proxy-test-2"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="bps-proxy-row-2"]').text()).toContain('Proxy tunnel unavailable')
    expect(wrapper.get('[data-testid="selection"]').text()).toBe('[1]')
    expect(wrapper.get('[data-testid="bps-proxy-options"]').exists()).toBe(true)
  })

  it('shares five test slots across individual and filtered batch actions without duplicate tests', async () => {
    const pending = new Map<number, (value: { success: boolean; message: string }) => void>()
    let active = 0
    let highest = 0
    testProxy.mockImplementation((id: number) => new Promise(resolve => {
      active++
      highest = Math.max(highest, active)
      pending.set(id, value => { active--; resolve(value) })
    }))
    const wrapper = render([...Array.from({ length: 7 }, (_, i) => proxy(i + 1)), proxy(8, { name: 'Hidden' })], [8])
    await wrapper.get('[data-testid="bps-proxy-trigger"]').trigger('click')
    await wrapper.get('input[type="search"]').setValue('Proxy')
    await wrapper.get('[data-testid="bps-proxy-test-1"]').trigger('click')
    await wrapper.get('[data-testid="bps-proxy-batch-test"]').trigger('click')
    await flushPromises()
    expect(testProxy).toHaveBeenCalledTimes(5)
    expect(testProxy.mock.calls.filter(([id]) => id === 1)).toHaveLength(1)
    expect(wrapper.get('[data-testid="bps-proxy-test-6"]').attributes('disabled')).toBeDefined()
    pending.get(2)!({ success: false, message: 'Tunnel timeout' })
    pending.delete(2)
    await flushPromises()
    expect(testProxy).toHaveBeenCalledTimes(6)
    for (const id of [1, 3, 4, 5, 6]) { pending.get(id)!({ success: true, message: 'ok' }); pending.delete(id) }
    await flushPromises()
    pending.get(7)!({ success: true, message: 'ok' })
    await flushPromises()
    expect(highest).toBe(5)
    expect(testProxy.mock.calls.map(([id]) => id)).toEqual([1, 2, 3, 4, 5, 6, 7])
    expect(wrapper.get('[data-testid="bps-proxy-row-2"]').text()).toContain('Tunnel timeout')
    expect(wrapper.get('[data-testid="selection"]').text()).toBe('[8]')
    await wrapper.get('[data-testid="bps-proxy-test-2"]').trigger('click')
    expect(testProxy.mock.calls.filter(([id]) => id === 2)).toHaveLength(2)
    pending.get(2)!({ success: true, message: 'ok' })
    await flushPromises()
  })

  it('refreshes saved account references when reopened and does not load them in session mode', async () => {
    const wrapper = mount(BPSProxyRoutingSettings, { props: { mode: 'session', proxies: [proxy(1)], proxyIds: [1] } })
    expect(getProxyAccounts).not.toHaveBeenCalled()
    await wrapper.setProps({ mode: 'request' })
    expect(getProxyAccounts).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="bps-proxy-trigger"]').trigger('click')
    await flushPromises()
    expect(getProxyAccounts).toHaveBeenCalledTimes(1)
    getProxyAccounts.mockResolvedValue([{ id: 7, name: 'New reference', platform: 'openai', type: 'oauth' }])
    wrapper.get('input[type="search"]').element.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(wrapper.find('[data-testid="bps-proxy-options"]').exists()).toBe(false)
    await wrapper.get('[data-testid="bps-proxy-trigger"]').trigger('click')
    await flushPromises()
    expect(getProxyAccounts).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-testid="bps-proxy-row-1"]').text()).toContain('New reference #7')
    expect(wrapper.emitted('update:proxyIds')).toBeUndefined()
  })

  it('provides an explicit clear-all action including hidden, unavailable, and missing selections', async () => {
    const wrapper = render([proxy(1), proxy(2, { status: 'inactive' }), proxy(3, { name: 'Hidden' })], [1, 2, 3, 99])
    await wrapper.get('[data-testid="bps-proxy-trigger"]').trigger('click')
    await wrapper.get('input[type="search"]').setValue('Proxy 1')
    await wrapper.get('[data-testid="bps-proxy-clear-all"]').trigger('click')
    expect(wrapper.get('[data-testid="selection"]').text()).toBe('[]')
    expect(wrapper.get('[data-testid="bps-proxy-options"]').exists()).toBe(true)
  })

  it.each(['bps-proxy-test-1', 'bps-proxy-batch-test'])('keeps the menu open when a native %s click replaces its original icon target', async (testId) => {
    const wrapper = mount(BPSProxyRoutingSettings, {
      attachTo: document.body,
      props: { mode: 'request', proxies: [proxy(1)], proxyIds: [1] },
      global: { stubs: { Icon: { template: '<svg><path /></svg>' } } },
    })
    await wrapper.get('[data-testid="bps-proxy-trigger"]').trigger('click')
    const button = wrapper.get('[data-testid="' + testId + '"]').element
    const originalIcon = button.querySelector('svg')!
    const originalTarget = originalIcon.querySelector('path')!
    // Browsers may flush the Icon -> spinner update before document's listener.
    // Remove the original target during native propagation to reproduce that timing.
    button.addEventListener('click', () => originalIcon.remove(), { once: true })
    // Restore it after outside-click handling so Vue retains ownership of its DOM.
    document.addEventListener('click', () => button.append(originalIcon), { once: true })
    originalTarget.dispatchEvent(new MouseEvent('click', { bubbles: true, composed: true }))
    await flushPromises()
    expect(testProxy).toHaveBeenCalledWith(1)
    expect(wrapper.find('[data-testid="bps-proxy-options"]').exists()).toBe(true)
    expect(wrapper.emitted('update:proxyIds')).toBeUndefined()
    document.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushPromises()
    expect(wrapper.find('[data-testid="bps-proxy-options"]').exists()).toBe(false)
  })

  it('consumes Escape inside the open selector and restores focus before the account dialog can close', async () => {
    const dialogKeydown = vi.fn()
    document.addEventListener('keydown', dialogKeydown)
    const wrapper = mount(BPSProxyRoutingSettings, { attachTo: document.body, props: { mode: 'request', proxies: [proxy(1)], proxyIds: [1] } })
    try {
      await wrapper.get('[data-testid="bps-proxy-trigger"]').trigger('click')
      const search = wrapper.get('input[type="search"]').element as HTMLInputElement
      search.focus()
      const event = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
      search.dispatchEvent(event)
      await flushPromises()
      expect(wrapper.find('[data-testid="bps-proxy-options"]').exists()).toBe(false)
      expect(dialogKeydown).not.toHaveBeenCalled()
      expect(event.defaultPrevented).toBe(true)
      expect(document.activeElement).toBe(wrapper.get('[data-testid="bps-proxy-trigger"]').element)
      expect(wrapper.emitted('update:proxyIds')).toBeUndefined()
    } finally { document.removeEventListener('keydown', dialogKeydown) }
  })

  it('stops queued tests after unmount and safely settles in-flight test and reference responses', async () => {
    const testResolvers: Array<(value: { success: boolean; message: string }) => void> = []
    const usageResolvers: Array<(value: never[]) => void> = []
    testProxy.mockImplementation(() => new Promise(resolve => { testResolvers.push(resolve) }))
    getProxyAccounts.mockImplementation(() => new Promise(resolve => { usageResolvers.push(resolve) }))
    const wrapper = render(Array.from({ length: 7 }, (_, i) => proxy(i + 1)))
    await wrapper.get('[data-testid="bps-proxy-trigger"]').trigger('click')
    await wrapper.get('[data-testid="bps-proxy-batch-test"]').trigger('click')
    expect(testProxy).toHaveBeenCalledTimes(5)
    expect(getProxyAccounts).toHaveBeenCalledTimes(5)
    wrapper.unmount()
    for (const resolve of testResolvers) resolve({ success: true, message: 'ok' })
    for (const resolve of usageResolvers) resolve([])
    await flushPromises()
    expect(testProxy).toHaveBeenCalledTimes(5)
    expect(getProxyAccounts).toHaveBeenCalledTimes(5)
  })
})
