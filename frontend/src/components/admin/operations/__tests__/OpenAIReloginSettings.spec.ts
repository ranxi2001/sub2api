import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import Panel from '../OpenAIReloginSettings.vue'
import { getReloginConfig, saveReloginConfig } from '@/api/admin/openaiRelogin'

vi.mock('@/api/admin/openaiRelogin', () => ({ getReloginConfig: vi.fn(), saveReloginConfig: vi.fn() }))

function mountPanel() {
  return mount(Panel, { global: { plugins: [createI18n({ legacy: false, locale: 'zh', messages: {} })] } })
}
beforeEach(() => vi.resetAllMocks())

describe('shared relogin configuration', () => {
  it('loads secrets only on explicit edit and saves through the independent API', async () => {
    const config = { relogin_endpoint: 'https://login.example/relogin', relogin_headers: { 'X-Provider': 'fixture' } }
    vi.mocked(getReloginConfig).mockResolvedValue(config)
    vi.mocked(saveReloginConfig).mockResolvedValue(config)
    const panel = mountPanel()
    expect(getReloginConfig).not.toHaveBeenCalled()
    await panel.get('button').trigger('click')
    await flushPromises()
    expect(saveReloginConfig).not.toHaveBeenCalled()
    await panel.get('form').trigger('submit')
    await flushPromises()
    expect(saveReloginConfig).toHaveBeenCalledWith(config)
    expect(panel.get('[role="status"]').text()).toBe('已保存')
  })

  it('rejects non-string headers before sending credentials to the server', async () => {
    vi.mocked(getReloginConfig).mockResolvedValue({ relogin_endpoint: 'https://login.example/relogin', relogin_headers: null })
    const panel = mountPanel()
    await panel.get('button').trigger('click')
    await flushPromises()
    await panel.get('textarea').setValue('{"X-Provider": {"nested": "secret"}}')
    await panel.get('form').trigger('submit')
    await flushPromises()
    expect(saveReloginConfig).not.toHaveBeenCalled()
    expect(panel.get('[role="alert"]').text()).not.toContain('secret')
  })
})
