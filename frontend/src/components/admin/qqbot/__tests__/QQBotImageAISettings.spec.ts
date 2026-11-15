import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import QQBotImageAISettings from '../QQBotImageAISettings.vue'
import type { QQBotConfig, QQSetupOptions } from '@/api/qqbot'

const api = vi.hoisted(() => ({ setupOptions: vi.fn() }))
vi.mock('@/api/qqbot', async original => ({ ...await original<typeof import('@/api/qqbot')>(), qqbotAPI: api }))
const wrappers: VueWrapper[] = []
const options: QQSetupOptions = {
  groups: [{ id: 1, name: 'Chat group', models: ['chat-model'] }],
  image_groups: [
    { id: 20, name: 'Image group', models: ['image-one', 'image-two'], platform: 'openai' },
    { id: 21, name: 'Another image group', models: ['image-three'], platform: 'openai' },
  ],
  frontend_url: '', email_configured: true, current_quota: 10, current_used: 1,
  current_image_quota: 55, current_image_used: 7,
}
function config(overrides: Partial<QQBotConfig> = {}): QQBotConfig {
  return {
    enabled: true, app_id: 'app', app_secret: '', app_secret_set: true, sandbox: false,
    allowed_groups: ['group'], ai_connection: 'site', site_group_id: 1,
    base_url: '', api_key: '', api_key_set: true, chat_model: 'chat-model', chat_protocol: 'responses',
    image_enabled: true, image_base_url: '', image_api_key: '', image_api_key_set: false,
    image_model: 'saved-image-model', image_size: '1024x1024', system_prompt: '', max_output_tokens: 2048,
    history_messages: 12, daily_chat_limit: 30, daily_image_limit: 2,
    total_daily_chat_limit: 500, total_daily_image_limit: 20, cooldown_seconds: 5,
    checkin_reward: 0, invite_enabled: false, invite_reward: 0, invite_daily_limit: 10,
    custom_commands: [], ...overrides,
  }
}
async function setup(initial = config()) {
  const model = reactive(initial)
  const wrapper: VueWrapper = mount(QQBotImageAISettings, {
    attachTo: document.body,
    props: {
      modelValue: model, quota: 0, regenerate: false,
      'onUpdate:quota': (value: number) => { void wrapper.setProps({ quota: value }) },
      'onUpdate:regenerate': (value: boolean) => { void wrapper.setProps({ regenerate: value }) },
    },
    global: {
      plugins: [createI18n({ legacy: false, locale: 'zh', messages: { zh, en } })],
      stubs: { teleport: true, RouterLink: { template: '<a><slot /></a>' } },
    },
  })
  wrappers.push(wrapper)
  await flushPromises()
  return { wrapper, model }
}
async function openModels(wrapper: VueWrapper) {
  await wrapper.get('[data-testid="image-model"] button').trigger('click')
  await flushPromises()
}
const modelOptions = (wrapper: VueWrapper) => wrapper.findAll('[role="option"]').map(option => option.text())
const validate = (wrapper: VueWrapper) => (wrapper.vm as unknown as { validate: () => boolean }).validate()
beforeEach(() => { vi.resetAllMocks(); api.setupOptions.mockResolvedValue(options) })
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()) })

describe('QQ bot image AI connection', () => {
  it('uses independent image groups and models while preserving custom names and chat settings', async () => {
    const initial = config({ image_ai_connection: 'site', image_site_group_id: 20 })
    const before = structuredClone(initial)
    const { wrapper, model } = await setup(initial)
    expect(model).toEqual(before)
    expect(wrapper.get('[data-testid="image-model"] button').text()).toContain('saved-image-model')
    expect(wrapper.find('[data-testid="image-base-url"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="image-api-key"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="image-site-group"]').text()).not.toContain('Chat group')
    await openModels(wrapper)
    expect(modelOptions(wrapper)).toEqual(['image-one', 'image-two'])
    await wrapper.get('[data-testid="image-site-group"]').setValue('21')
    expect(model.image_model).toBe('saved-image-model')
    expect(modelOptions(wrapper)).toEqual(['image-three'])
    await wrapper.get('[role="option"]').trigger('click')
    expect(model.image_model).toBe('image-three')
    expect(model.chat_model).toBe('chat-model')
    expect(model.site_group_id).toBe(1)
  })

  it('loads and edits only the image key quota when replacing an existing image key', async () => {
    const { wrapper, model } = await setup(config({ image_ai_connection: 'site', image_site_group_id: 20, image_site_api_key_id: 88 }))
    const quota = wrapper.get<HTMLInputElement>('[data-testid="image-site-quota"]')
    expect(quota.element.value).toBe('55')
    expect(quota.element.disabled).toBe(true)
    expect(wrapper.text()).toContain('7 / 总额度 55')
    await wrapper.get('[data-testid="image-regenerate-key"]').setValue(true)
    expect(quota.element.disabled).toBe(false)
    await quota.setValue('77')
    expect(wrapper.emitted('update:quota')?.at(-1)).toEqual([77])
    expect(wrapper.emitted('update:regenerate')?.at(-1)).toEqual([true])
    expect(model.image_site_api_key_id).toBe(88)
    expect(model.site_group_id).toBe(1)
    await wrapper.get('[data-testid="image-regenerate-key"]').setValue(false)
    expect(quota.element.disabled).toBe(true)
    expect(quota.element.value).toBe('55')
    await wrapper.get('[data-testid="image-regenerate-key"]').setValue(true)
    await quota.setValue('99')
    await wrapper.get('[data-testid="image-ai-source"]').setValue('reuse_chat')
    await wrapper.get('[data-testid="image-ai-source"]').setValue('site')
    expect(wrapper.get<HTMLInputElement>('[data-testid="image-site-quota"]').element.value).toBe('55')
    expect(wrapper.get<HTMLInputElement>('[data-testid="image-regenerate-key"]').element.checked).toBe(false)
  })

  it.each([
    [{ image_base_url: 'https://images.example/v1' }, 'external'],
    [{ image_api_key_set: true }, 'external'],
    [{ image_api_key: 'draft-image-key' }, 'external'],
    [{}, 'reuse_chat'],
  ] as const)('infers legacy image sources without rewriting saved values (%s)', async (legacy, source) => {
    const initial = config(legacy)
    const before = structuredClone(initial)
    const { wrapper, model } = await setup(initial)
    expect((wrapper.get('[data-testid="image-ai-source"]').element as HTMLSelectElement).value).toBe(source)
    expect(model).toEqual(before)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.find('[data-testid="image-base-url"]').exists()).toBe(source === 'external')
  })

  it('keeps the legacy key-only external connection and explicitly switches to reuse', async () => {
    const { wrapper, model } = await setup(config({ image_api_key_set: true }))
    expect(wrapper.get<HTMLInputElement>('[data-testid="image-base-url"]').element.value).toBe('')
    expect(wrapper.get('[data-testid="image-api-key"]').attributes('placeholder')).toContain('已保存')
    await wrapper.get('[data-testid="image-ai-source"]').setValue('reuse_chat')
    expect(model.image_ai_connection).toBe('reuse_chat')
    expect(model.image_base_url).toBe('')
    expect(model.image_api_key).toBe('')
    expect(wrapper.find('[data-testid="image-api-key"]').exists()).toBe(false)
  })

  it('supports a manually entered image model after candidate loading fails and validates length', async () => {
    api.setupOptions.mockRejectedValue(new Error('image candidates unavailable'))
    const { wrapper, model } = await setup(config({ image_ai_connection: 'site', image_site_group_id: 20, image_model: '' }))
    expect(wrapper.get('[role="alert"]').text()).toContain('image candidates unavailable')
    expect(validate(wrapper)).toBe(false)
    await openModels(wrapper)
    await wrapper.get('.select-search-input').setValue('custom-image-model')
    await wrapper.get('[role="option"]').trigger('click')
    expect(model.image_model).toBe('custom-image-model')
    expect(validate(wrapper)).toBe(true)
    model.image_model = 'x'.repeat(201)
    expect(validate(wrapper)).toBe(false)
    model.image_enabled = false
    expect(validate(wrapper)).toBe(true)
  })
})
