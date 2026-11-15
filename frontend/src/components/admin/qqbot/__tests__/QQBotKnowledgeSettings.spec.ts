import { mount, flushPromises } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import QQBotKnowledgeSettings from '../QQBotKnowledgeSettings.vue'
import type { QQSiteKnowledgeConfig } from '@/api/qqbot'

function setup(model: QQSiteKnowledgeConfig = { enabled: false, content: '' }, locale = 'zh') {
  const i18n = createI18n({ legacy: false, locale, messages: { zh, en } })
  const wrapper = mount(QQBotKnowledgeSettings, { props: { modelValue: model }, global: { plugins: [i18n] } })
  return { wrapper, model, t: i18n.global.t }
}

describe('QQ site knowledge entries', () => {
  it('preserves typed keyword separators and resets raw drafts when a saved config replaces the model', async () => {
    const { wrapper: w, model } = setup()
    await w.get('[data-testid="knowledge-add"]').trigger('click')
    const input = w.get('[data-testid="knowledge-entry-keywords-0"]')
    const field = input.element as HTMLInputElement
    field.value = 'API,'
    await input.trigger('input')
    expect(field.value).toBe('API,')
    expect(model.entries![0]!.keywords).toEqual(['API'])
    await w.get('[data-testid="knowledge-entry-title-0"]').setValue('接入教程')
    expect(field.value).toBe('API,')
    field.value += ' 充值'
    await input.trigger('input')
    expect(field.value).toBe('API, 充值')
    expect(model.entries![0]!.keywords).toEqual(['API', '充值'])
    const replacement = { ...model, entries: model.entries!.map(entry => ({ ...entry, keywords: ['新关键词'] })) }
    await w.setProps({ modelValue: replacement })
    expect((w.get('[data-testid="knowledge-entry-keywords-0"]').element as HTMLInputElement).value).toBe('新关键词')
    w.unmount()
  })

  it.each(['zh', 'en'])('%s creates, edits and removes independent entries while retaining legacy content', async locale => {
    const { wrapper: w, model, t } = setup({ enabled: true, content: 'Existing public guide' }, locale)
    await w.get('[data-testid="knowledge-add"]').trigger('click')
    expect(model.content).toBe('Existing public guide')
    expect(model.entries).toHaveLength(1)
    expect(model.entries![0]!.id).toMatch(/^knowledge_[a-f0-9]+$/)
    expect(model.entries![0]!.enabled).toBe(false)
    await w.get('[data-testid="knowledge-entry-title-0"]').setValue('Setup guide')
    await w.get('[data-testid="knowledge-entry-category-0"]').setValue('API')
    await w.get('[data-testid="knowledge-entry-keywords-0"]').setValue('setup, API URL，key')
    await w.get('[data-testid="knowledge-entry-content-0"]').setValue('Use the documented endpoint.')
    await w.get('[data-testid="knowledge-entry-url-0"]').setValue('https://example.test/guide')
    await w.get('[data-testid="knowledge-entry-enabled-0"]').setValue(true)
    expect(model.entries![0]!.keywords).toEqual(['setup', 'API URL', 'key'])
    expect(w.text()).toContain(t('qqbot.knowledge.entriesHelp'))
    expect((w.get('[data-testid="knowledge-content"]').element as HTMLTextAreaElement).validationMessage).toBe('')
    await w.get('[data-testid="knowledge-remove-0"]').trigger('click')
    expect(model.entries).toEqual([])
    expect(model.content).toBe('Existing public guide')
    w.unmount()
  })

  it('accepts entries without legacy content and validates missing content, URL and aggregate length', async () => {
    const { wrapper: w, model, t } = setup({ enabled: true, content: '' })
    await flushPromises()
    const textarea = w.get('[data-testid="knowledge-content"]').element as HTMLTextAreaElement
    expect(textarea.validationMessage).toBe(t('qqbot.knowledge.required'))
    await w.get('[data-testid="knowledge-add"]').trigger('click')
    await w.get('[data-testid="knowledge-entry-enabled-0"]').setValue(true)
    expect(textarea.validationMessage).toBe(t('qqbot.knowledge.entryRequired'))
    await w.get('[data-testid="knowledge-entry-title-0"]').setValue('充值教程')
    await w.get('[data-testid="knowledge-entry-content-0"]').setValue('公开充值步骤')
    expect(textarea.validationMessage).toBe('')
    await w.get('[data-testid="knowledge-entry-url-0"]').setValue('javascript:alert(1)')
    expect(textarea.validationMessage).toBe(t('qqbot.knowledge.invalidURL'))
    await w.get('[data-testid="knowledge-entry-url-0"]').setValue('https://user:password@example.test')
    expect(textarea.validationMessage).toBe(t('qqbot.knowledge.invalidURL'))
    await w.get('[data-testid="knowledge-entry-url-0"]').setValue('https://example.test')
    expect(textarea.validationMessage).toBe('')
    await w.get('[data-testid="knowledge-content"]').setValue('中'.repeat(12000))
    expect(textarea.validationMessage).toBe(t('qqbot.knowledge.tooLong', { max: 12000 }))
    expect(model.entries).toHaveLength(1)
    w.unmount()
  })

  it('retains disabled drafts and caps entry count at forty', async () => {
    const { wrapper: w, model } = setup()
    for (let i = 0; i < 40; i++) await w.get('[data-testid="knowledge-add"]').trigger('click')
    expect(model.entries).toHaveLength(40)
    expect(w.get('[data-testid="knowledge-add"]').attributes('disabled')).toBeDefined()
    expect((w.get('[data-testid="knowledge-content"]').element as HTMLTextAreaElement).validationMessage).toBe('')
    await w.get('[data-testid="knowledge-remove-2"]').trigger('click')
    expect(model.entries).toHaveLength(39)
    expect(w.get('[data-testid="knowledge-add"]').attributes('disabled')).toBeUndefined()
    w.unmount()
  })
})
