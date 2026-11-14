import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { InternalAxiosRequestConfig } from 'axios'
import { apiClient } from '@/api/client'
import { createAuthConnection } from '@/api/admin/openaiAuthConnections'
import OpenAIAuthConnections from '../OpenAIAuthConnections.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/i18n', () => ({ getLocale: () => 'en' }))
const originalAdapter = apiClient.defaults.adapter
const adapter = vi.fn()
const record = (id: string, kind = 'siwc', capabilities: string[] = []) => ({ id, account_id: 42, kind, label: `Synthetic ${id}`, legacy: false, status: 'active', client_id: `issued-${id}`, scopes: [], capabilities, preferred: false, revision: 2 })
let records: ReturnType<typeof record>[]
beforeEach(() => {
  vi.resetAllMocks(); localStorage.clear(); records = [record('identity'), record('responses', 'siwc', ['responses'])]
  adapter.mockImplementation(async (config: InternalAxiosRequestConfig) => ({ config, status: 200, statusText: 'OK', headers: {}, data: { code: 0, message: '', data: config.method === 'get' ? records : config.url?.endsWith('/authorize') ? { session_id: 'synthetic-session', auth_url: 'https://auth.openai.com/authorize' } : {} } }))
  apiClient.defaults.adapter = adapter
})
afterEach(() => { apiClient.defaults.adapter = originalAdapter })

describe('independent OpenAI connection management through actual API client', () => {
  it('hides the whole panel and makes no requests for API key accounts across watches', async () => {
    const w = mount(OpenAIAuthConnections, { props: { accountId: 42, accountType: 'apikey', active: true } })
    await flushPromises()
    expect(w.find('[data-testid="auth-connections"]').exists()).toBe(false)
    expect(adapter).not.toHaveBeenCalled()
    await w.setProps({ accountId: 43, active: false }); await w.setProps({ active: true }); await flushPromises()
    expect(adapter).not.toHaveBeenCalled()
    await w.setProps({ accountType: 'oauth' }); await flushPromises()
    expect(w.find('[data-testid="auth-connections"]').exists()).toBe(true)
    expect(adapter).toHaveBeenCalledTimes(1)
    await w.setProps({ accountType: 'apikey' }); await w.setProps({ accountId: 44 }); await flushPromises()
    expect(w.find('[data-testid="auth-connections"]').exists()).toBe(false)
    expect(adapter).toHaveBeenCalledTimes(1)
    w.unmount()
  })
  it('offers only Codex BPS SIWC and sends no API key in OAuth creation', async () => {
    const w = mount(OpenAIAuthConnections, { props: { accountId: 42, accountType: 'oauth', active: true } })
    await flushPromises()
    expect(w.findAll('option').map(o => o.attributes('value'))).toEqual(['codex', 'bps', 'siwc'])
    expect(w.find('input[type="password"]').exists()).toBe(false)
    w.unmount()
  })

  it('rejects API key and mixed OAuth create inputs before making a request', async () => {
    for (const input of [
      { kind: 'api_key', label: 'Synthetic', api_key: 'synthetic' },
      { kind: 'codex', label: 'Synthetic', api_key: 'synthetic' },
      { kind: 'bps', label: 'Synthetic', api_key: 'synthetic' },
      { kind: 'siwc', label: 'Synthetic', api_key: 'synthetic' },
    ]) {
      await expect(createAuthConnection(42, input as Parameters<typeof createAuthConnection>[1])).rejects.toThrow('OAuth connections cannot register API keys')
    }
    expect(adapter).not.toHaveBeenCalled()
  })

  it('does not nest forms or submit the surrounding account editor', async () => {
    const submit = vi.fn()
    const w = mount({ components: { OpenAIAuthConnections }, template: '<form @submit.prevent="submit"><OpenAIAuthConnections :account-id="42" account-type="oauth" :active="true" /></form>', setup: () => ({ submit }) })
    await flushPromises()
    expect(w.findAll('form')).toHaveLength(1)
    await w.get('[data-testid="connection-label"]').setValue('New registration')
    await w.get('[data-testid="add-connection"]').trigger('click'); await flushPromises()
    expect(submit).not.toHaveBeenCalled(); w.unmount()
  })

  it('shows both registrations and refuses identity-only selection', async () => {
    const w = mount(OpenAIAuthConnections, { props: { accountId: 42, accountType: 'oauth', active: true } }); await flushPromises()
    expect(w.text()).toContain('Synthetic identity'); expect(w.text()).toContain('Synthetic responses')
    const selects = w.findAll('[data-testid="select-connection"]')
    expect(selects[0].attributes('disabled')).toBeDefined(); expect(selects[1].attributes('disabled')).toBeUndefined()
    await selects[1].trigger('click'); await flushPromises()
    const request = adapter.mock.calls.map(([c]) => c).find(c => c.method === 'patch')
    expect(request.url).toBe('/admin/accounts/42/auth-connections/responses')
    expect(JSON.parse(request.data)).toEqual({ revision: 2, action: 'select' }); w.unmount()
  })
  it('creates a new registration without replacing existing ones', async () => {
    const w = mount(OpenAIAuthConnections, { props: { accountId: 42, accountType: 'oauth', active: true } }); await flushPromises()
    await w.get('[data-testid="connection-label"]').setValue('Second Codex')
    await w.get('[data-testid="add-connection"]').trigger('click'); await flushPromises()
    const request = adapter.mock.calls.map(([c]) => c).find(c => c.method === 'post')
    expect(JSON.parse(request.data)).toEqual({ kind: 'codex', label: 'Second Codex' })
    expect(w.text()).toContain('Synthetic identity'); w.unmount()
  })
  it('clears authorization material when the account or dialog changes', async () => {
    const w = mount(OpenAIAuthConnections, { props: { accountId: 42, accountType: 'oauth', active: true } }); await flushPromises()
    const button = w.findAll('button').find(b => b.text() === 'openaiAuth.authorize')!
    await button.trigger('click'); await flushPromises(); expect(w.find('[data-testid="connection-authorization"]').exists()).toBe(true)
    await w.setProps({ accountId: 43 }); await flushPromises()
    expect(w.find('[data-testid="connection-authorization"]').exists()).toBe(false)
    await w.setProps({ active: false }); expect(w.text()).not.toContain('Synthetic identity'); w.unmount()
  })
  it('makes returning to legacy storage explicit', async () => {
    const w = mount(OpenAIAuthConnections, { props: { accountId: 42, accountType: 'oauth', active: true } }); await flushPromises()
    await w.get('[data-testid="use-legacy"]').trigger('click'); await flushPromises()
    expect(adapter.mock.calls.some(([c]) => c.url === '/admin/accounts/42/auth-connections/use-legacy' && c.method === 'post')).toBe(true)
    w.unmount()
  })

  it('confirms a saved callback after a lost response without submitting it again', async () => {
    const initial = adapter.getMockImplementation()!
    adapter.mockImplementation(async (config: InternalAxiosRequestConfig) => {
      if (config.url?.endsWith('/callback')) throw new Error('network interrupted PRIVATE_CALLBACK')
      if (config.url?.endsWith('/authorization-status')) return { config, status: 200, statusText: 'OK', headers: {}, data: { code: 0, data: { status: 'saved' } } }
      return initial(config)
    })
    const w = mount(OpenAIAuthConnections, { props: { accountId: 42, accountType: 'oauth', active: true } }); await flushPromises()
    await w.findAll('button').find(b => b.text() === 'openaiAuth.authorize')!.trigger('click'); await flushPromises()
    await w.get('#connection-callback-42').setValue('https://example.invalid/callback?code=PRIVATE_CODE&state=PRIVATE_STATE')
    await w.findAll('button').find(b => b.text() === 'openaiAuth.complete')!.trigger('click'); await flushPromises()
    expect(w.text()).toContain('openaiAuth.authorizationSaved')
    expect(w.find('[data-testid="connection-authorization"]').exists()).toBe(false)
    expect(adapter.mock.calls.filter(([c]) => c.url.endsWith('/callback'))).toHaveLength(1)
    const status = adapter.mock.calls.find(([c]) => c.url.endsWith('/authorization-status'))![0]
    expect(JSON.parse(status.data)).toEqual({ session_id: 'synthetic-session' })
    expect(w.html()).not.toContain('PRIVATE_'); expect(localStorage.length).toBe(0); w.unmount()
  })

  it('keeps an already active connection unconfirmed and offers status-only retry', async () => {
    const initial = adapter.getMockImplementation()!
    adapter.mockImplementation(async (config: InternalAxiosRequestConfig) => {
      if (config.url?.endsWith('/callback')) throw new Error('lost response')
      if (config.url?.endsWith('/authorization-status')) return { config, status: 200, statusText: 'OK', headers: {}, data: { code: 0, data: { status: 'unconfirmed' } } }
      return initial(config)
    })
    const w = mount(OpenAIAuthConnections, { props: { accountId: 42, accountType: 'oauth', active: true } }); await flushPromises()
    await w.findAll('button').find(b => b.text() === 'openaiAuth.authorize')!.trigger('click'); await flushPromises()
    await w.get('#connection-callback-42').setValue('https://example.invalid/?code=PRIVATE_CODE')
    const submit = w.findAll('button').find(b => b.text() === 'openaiAuth.complete')!
    await submit.trigger('click'); await flushPromises()
    expect(w.text()).toContain('openaiAuth.authorizationUnconfirmed')
    expect(w.text()).not.toContain('openaiAuth.authorizationSaved')
    expect(w.find('#connection-callback-42').exists()).toBe(false)
    await w.get('[data-testid="check-authorization"]').trigger('click'); await flushPromises()
    expect(adapter.mock.calls.filter(([c]) => c.url.endsWith('/callback'))).toHaveLength(1)
    expect(adapter.mock.calls.filter(([c]) => c.url.endsWith('/authorization-status'))).toHaveLength(2)
    expect(w.html()).not.toContain('PRIVATE_'); w.unmount()
  })

  it('ignores late authorization completion after switching accounts', async () => {
    const initial = adapter.getMockImplementation()!
    let finish!: (value: unknown) => void
    adapter.mockImplementation(async (config: InternalAxiosRequestConfig) => {
      if (config.url?.endsWith('/callback')) return new Promise(resolve => { finish = resolve })
      return initial(config)
    })
    const w = mount(OpenAIAuthConnections, { props: { accountId: 42, accountType: 'oauth', active: true } }); await flushPromises()
    await w.findAll('button').find(b => b.text() === 'openaiAuth.authorize')!.trigger('click'); await flushPromises()
    await w.get('#connection-callback-42').setValue('https://example.invalid/?code=PRIVATE_CODE')
    await w.findAll('button').find(b => b.text() === 'openaiAuth.complete')!.trigger('click'); await flushPromises()
    await w.setProps({ accountId: 43 }); await flushPromises()
    finish({ config: {}, status: 200, statusText: 'OK', headers: {}, data: { code: 0, data: {} } }); await flushPromises()
    expect(w.text()).not.toContain('openaiAuth.authorizationSaved')
    expect(w.find('[data-testid="connection-authorization"]').exists()).toBe(false)
    w.unmount()
  })
})
