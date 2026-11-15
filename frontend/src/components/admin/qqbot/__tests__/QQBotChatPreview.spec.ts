import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import QQBotChatPreview from '../QQBotChatPreview.vue'
import type { QQBotConfig, QQChatPreviewResult } from '@/api/qqbot'

const api = vi.hoisted(() => ({ chatPreview: vi.fn(), members: vi.fn() }))
vi.mock('@/api/qqbot', () => ({ qqbotAPI: api }))
const wrappers: VueWrapper[] = []
const config = { app_id: 'app', allowed_groups: ['first', 'second'], group_policies: [{ group_id: 'first', label: 'Primary group' }] } as QQBotConfig
const result: QQChatPreviewResult = { text: '<script>not executable</script>真实回答', action: 'site_query', input_tokens: 20, output_tokens: 10, duration_ms: 120, knowledge_sources: [{ id: 'guide', title: '接入教程', url: 'https://example.com/guide', updated_at: '2026-10-30T10:00:00Z' }, { id: 'unsafe', title: '无效链接', url: 'javascript:alert(1)', updated_at: '' }], query: { kind: 'channel_status', model: '', activity: '', page: 1, status: 'failed', error_code: 'provider_error', duration_ms: 12 } }
function mountPreview(locale = 'zh') {
  const wrapper = mount(QQBotChatPreview, { props: { config }, global: { plugins: [createI18n({ legacy: false, locale, messages: { zh, en } })] } })
  wrappers.push(wrapper); return wrapper
}
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
async function ready(wrapper: VueWrapper) {
  await wrapper.get('[data-testid="qq-preview-group"]').setValue('first')
  await wrapper.get('[data-testid="qq-preview-question"]').setValue('渠道稳不稳？')
  await flushPromises()
}
describe('QQ real question trials', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    api.members.mockResolvedValue({ app_id: 'app', items: [{ group_id: 'first', member_id: 'member', nickname: 'Alice', email: '', message_preview: '', last_seen_at: '', group_label: 'Primary group' }], total: 1, page: 1, page_size: 20 })
    api.chatPreview.mockResolvedValue(result)
  })
  afterEach(() => wrappers.splice(0).forEach(wrapper => wrapper.unmount()))
  it.each(['zh', 'en'])('shows retry attempts and incomplete trial usage in %s', async locale => {
    api.chatPreview.mockResolvedValue({ ...result, input_tokens: 0, output_tokens: 0, chat_execution: { attempts: 3, retries: 2, usage_complete: false, last_error_code: 'http_503' } })
    const wrapper = mountPreview(locale); await ready(wrapper)
    await wrapper.get('[data-testid="qq-preview-run"]').trigger('click'); await flushPromises()
    const messages = locale === 'zh' ? zh : en
    const i18n = createI18n({ legacy: false, locale, messages: { [locale]: messages } })
    expect(wrapper.get('[data-testid="qq-preview-chat-execution"]').text()).toContain(i18n.global.t('qqbot.chatExecution.attempts', { attempts: 3, retries: 2 }))
    expect(wrapper.get('[data-testid="qq-preview-chat-execution"]').text()).toContain('http_503')
    expect(wrapper.get('[data-testid="qq-preview-result"]').text()).toContain(`${i18n.global.t('qqbot.chatExecution.knownTokens')}: 0 / 0`)
    expect(wrapper.get('[data-testid="qq-preview-usage-incomplete"]').text()).toBe(i18n.global.t('qqbot.chatExecution.incompleteUsage'))
  })
  it.each(['zh', 'en'])('previews publicly by default and safely renders real outcome and sources in %s', async locale => {
    const wrapper = mountPreview(locale)
    await ready(wrapper)
    await wrapper.get('[data-testid="qq-preview-run"]').trigger('click'); await flushPromises()
    expect(api.chatPreview).toHaveBeenCalledOnce()
    expect(api.chatPreview).toHaveBeenCalledWith({ group_id: 'first', member_id: undefined, question: '渠道稳不稳？' })
    const output = wrapper.get('[data-testid="qq-preview-result"]')
    expect(output.text()).toContain(result.text)
    expect(output.find('script').exists()).toBe(false)
    expect(output.text()).toContain('provider_error')
    expect(output.text()).toContain('20 / 10')
    expect(output.find('[data-testid="qq-preview-chat-execution"]').exists()).toBe(false)
    expect(output.find('[data-testid="qq-preview-usage-incomplete"]').exists()).toBe(false)
    expect(output.findAll('a')).toHaveLength(1)
    expect(output.get('a').attributes('href')).toBe('https://example.com/guide')
    expect(output.get('a').attributes('rel')).toBe('noopener noreferrer')
  })
  it('selects an actual member within the chosen group and resets it on group changes', async () => {
    const wrapper = mountPreview()
    await ready(wrapper)
    await wrapper.get('[data-testid="qq-member-select"]').setValue(JSON.stringify(['first', 'member']))
    await wrapper.get('[data-testid="qq-preview-run"]').trigger('click'); await flushPromises()
    expect(api.chatPreview).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: 'first', member_id: 'member' }))
    await wrapper.get('[data-testid="qq-preview-group"]').setValue('second'); await flushPromises()
    expect(wrapper.find('[data-testid="qq-preview-result"]').exists()).toBe(false)
    await wrapper.get('[data-testid="qq-preview-run"]').trigger('click'); await flushPromises()
    expect(api.chatPreview).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: 'second', member_id: undefined }))
  })
  it('discards a slow result after question changes and prevents overlapping paid requests', async () => {
    const late = deferred<QQChatPreviewResult>()
    api.chatPreview.mockReturnValueOnce(late.promise)
    const wrapper = mountPreview(); await ready(wrapper)
    await wrapper.get('[data-testid="qq-preview-run"]').trigger('click')
    expect(wrapper.get('[data-testid="qq-preview-run"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="qq-preview-question"]').setValue('另一个问题')
    await wrapper.get('[data-testid="qq-preview-question"]').trigger('keydown', { key: 'Enter', ctrlKey: true })
    expect(api.chatPreview).toHaveBeenCalledOnce()
    late.resolve(result); await flushPromises()
    expect(wrapper.find('[data-testid="qq-preview-result"]').exists()).toBe(false)
    await wrapper.get('[data-testid="qq-preview-run"]').trigger('click'); await flushPromises()
    expect(api.chatPreview).toHaveBeenLastCalledWith(expect.objectContaining({ question: '另一个问题' }))
    expect(wrapper.find('[data-testid="qq-preview-result"]').exists()).toBe(true)
  })
  it('discards slow errors and clears identity when the saved bot changes', async () => {
    let reject!: (reason: Error) => void
    api.chatPreview.mockReturnValueOnce(new Promise((_resolve, fail) => { reject = fail }))
    const wrapper = mountPreview(); await ready(wrapper)
    await wrapper.get('[data-testid="qq-preview-run"]').trigger('click')
    await wrapper.setProps({ config: { ...config, app_id: 'replacement-app' } })
    reject(new Error('stale secret failure')); await flushPromises()
    expect(wrapper.text()).not.toContain('stale secret failure')
    expect((wrapper.get('[data-testid="qq-preview-group"]').element as HTMLSelectElement).value).toBe('')
  })
})
