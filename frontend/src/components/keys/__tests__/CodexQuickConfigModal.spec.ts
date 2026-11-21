import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { saveAs } from 'file-saver'
import CodexQuickConfigModal from '../CodexQuickConfigModal.vue'

const fetchCodexModelsManifest = vi.hoisted(() => vi.fn())

vi.mock('@/api/codex', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/api/codex')>(),
  fetchCodexModelsManifest
}))
vi.mock('file-saver', () => ({ saveAs: vi.fn() }))
vi.mock('@/components/common/BaseDialog.vue', () => ({
  default: { template: '<div><slot /><slot name="footer" /></div>' }
}))
vi.mock('@/components/icons/Icon.vue', () => ({ default: { template: '<span />' } }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => params
      ? `${key}:${JSON.stringify(params)}`
      : key
  })
}))

function mountModal() {
  return mount(CodexQuickConfigModal, {
    props: {
      show: true,
      apiKey: 'sk-test',
      baseUrl: 'https://example.com',
      platform: 'openai',
      siteName: '示例站'
    }
  })
}

function decodeBase64Utf8(value: string): string {
  const bytes = atob(value).split('').map((character) => character.charCodeAt(0))
  return new TextDecoder().decode(new Uint8Array(bytes))
}

describe('CodexQuickConfigModal', () => {
  beforeEach(() => {
    fetchCodexModelsManifest.mockReset()
    vi.mocked(saveAs).mockClear()
    Object.defineProperty(window.navigator, 'userAgent', { configurable: true, value: 'Mozilla/5.0' })
  })

  it('starts with a usable script and switches platforms', async () => {
    const wrapper = mountModal()
    expect(wrapper.get('[data-testid="quick-config-copy"]').attributes('disabled')).toBeUndefined()
    const unixPayload = wrapper.get('pre').text().match(/PAYLOAD='([^']+)'/)?.[1]
    expect(unixPayload).toBeDefined()
    const config = decodeBase64Utf8(unixPayload!)
    expect(config).toContain('experimental_bearer_token')
    expect(config).toContain('name = "示例站"')
    expect(config).toContain('model_catalog_url = "https://example.com/v1/models"')
    expect(wrapper.text()).toContain('https://example.com/v1/models')
    expect(fetchCodexModelsManifest).not.toHaveBeenCalled()

    await wrapper.get('[data-testid="quick-config-windows-tab"]').trigger('click')
    expect(wrapper.get('pre').text()).toContain('@echo off')
    expect(wrapper.get('[data-testid="quick-config-windows-tab"]').attributes('aria-selected')).toBe('true')

    await wrapper.get('[data-testid="quick-config-catalog-mode"]').setValue('none')
    const windowsPayload = wrapper.get('pre').text().match(/\$configPayload = @\(\s*'([^']+)'/)?.[1]
    expect(decodeBase64Utf8(windowsPayload!)).not.toContain('model_catalog_url')
  })

  it('shows a clear success message after copying or downloading', async () => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) }
    })
    const wrapper = mountModal()

    await wrapper.get('[data-testid="quick-config-copy"]').trigger('click')
    expect(wrapper.get('[data-testid="quick-config-action-status"]').text()).toContain('copySuccessMessage')

    await wrapper.get('[data-testid="quick-config-download"]').trigger('click')
    expect(wrapper.get('[data-testid="quick-config-action-status"]').text()).toContain('downloadSuccessMessage')
  })

  it('downloads an ASCII Windows CMD file with CRLF and system PowerShell bootstrap', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="quick-config-windows-tab"]').trigger('click')
    await wrapper.get('[data-testid="quick-config-download"]').trigger('click')
    const [blob, fileName] = vi.mocked(saveAs).mock.calls[0]
    expect(fileName).toBe('sub2api-codex-config.cmd')
    const content = await new Promise<string>((resolve) => {
      const reader = new FileReader()
      reader.onload = () => resolve(String(reader.result))
      reader.readAsText(blob as Blob)
    })
    expect(content.startsWith('@echo off\r\n')).toBe(true)
    expect([...content].every((character) => character.charCodeAt(0) <= 0x7f)).toBe(true)
    expect(content).toContain('%SystemRoot%\\System32\\WindowsPowerShell')
  })

  it('waits for the model catalog before enabling copy and download', async () => {
    let resolveRequest!: (value: { content: string; modelCount: number }) => void
    fetchCodexModelsManifest.mockReturnValue(new Promise((resolve) => { resolveRequest = resolve }))
    const wrapper = mountModal()

    await wrapper.get('[data-testid="quick-config-catalog-mode"]').setValue('file')
    await flushPromises()
    expect(fetchCodexModelsManifest).toHaveBeenCalledWith('https://example.com', 'sk-test', expect.any(AbortSignal), '0.166.0')
    expect(wrapper.get('[data-testid="quick-config-catalog-loading"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="quick-config-copy"]').attributes('disabled')).toBeDefined()

    resolveRequest({ content: '{"models":[]}', modelCount: 0 })
    await flushPromises()
    expect(wrapper.get('[data-testid="quick-config-catalog-ready"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="quick-config-download"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('pre').text()).toContain('model_catalog_json')
    const payload = wrapper.get('pre').text().match(/PAYLOAD='([^']+)'/)?.[1]
    expect(decodeBase64Utf8(payload!)).not.toContain('model_catalog_url')
  })

  it('shows a recoverable catalog error', async () => {
    fetchCodexModelsManifest.mockRejectedValue(new Error('unavailable'))
    const wrapper = mountModal()
    await wrapper.get('[data-testid="quick-config-catalog-mode"]').setValue('file')
    await flushPromises()
    expect(wrapper.get('[data-testid="quick-config-catalog-error"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="quick-config-copy"]').attributes('disabled')).toBeDefined()
  })
})
