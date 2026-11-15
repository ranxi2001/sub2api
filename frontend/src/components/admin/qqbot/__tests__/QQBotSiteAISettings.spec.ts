import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { reactive } from 'vue'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import Select from '@/components/common/Select.vue'
import QQBotSiteAISettings from '../QQBotSiteAISettings.vue'
import type { QQBotConfig, QQSetupOptions } from '@/api/qqbot'

const api = vi.hoisted(() => ({ setupOptions: vi.fn() }))
vi.mock('@/api/qqbot', async original => ({
  ...await original<typeof import('@/api/qqbot')>(), qqbotAPI: api,
}))

const wrappers: VueWrapper[] = []
const options: QQSetupOptions = {
  groups: [
    { id: 1, name: 'Chat group', platform: 'openai', chat_protocols: ['chat_completions', 'responses'], models: ['chat-alpha', 'chat-beta'] },
    { id: 2, name: 'Vision group', platform: 'openai', chat_protocols: ['chat_completions', 'responses'], models: ['vision-one', 'vision-two'] },
    { id: 3, name: 'Gemini group', platform: 'gemini', chat_protocols: ['chat_completions'], models: ['gemini-vision'] },
  ],
  frontend_url: 'https://site.example', email_configured: true, current_quota: 20, current_used: 2,
}

function config(overrides: Partial<QQBotConfig> = {}): QQBotConfig {
  return {
    enabled: true, app_id: 'app', app_secret: '', app_secret_set: true, sandbox: false,
    allowed_groups: ['group'], ai_connection: 'site', site_group_id: 1,
    base_url: '', api_key: '', api_key_set: false, chat_model: '', chat_protocol: 'chat_completions',
    image_enabled: false, image_base_url: '', image_api_key: '', image_api_key_set: false,
    image_model: '', image_size: '1024x1024', system_prompt: '', max_output_tokens: 2048,
    history_messages: 12, daily_chat_limit: 30, daily_image_limit: 2,
    total_daily_chat_limit: 500, total_daily_image_limit: 20, cooldown_seconds: 5,
    checkin_reward: 0, invite_enabled: false, invite_reward: 0, invite_daily_limit: 10,
    custom_commands: [], ...overrides,
  }
}

async function setup(model = config()) {
  const draft = reactive(model)
  const wrapper = mount(QQBotSiteAISettings, {
    attachTo: document.body,
    props: { modelValue: draft, quota: 0 },
    global: {
      plugins: [createI18n({ legacy: false, locale: 'zh', messages: { zh, en } })],
      stubs: { teleport: true, RouterLink: { template: '<a><slot /></a>' } },
    },
  })
  wrappers.push(wrapper)
  await flushPromises()
  return { wrapper, model: draft }
}

async function openModels(wrapper: VueWrapper) {
  await wrapper.get('[data-testid="setup-chat-model"] button').trigger('click')
  await flushPromises()
}

const modelOptions = (wrapper: VueWrapper) => wrapper.findAll('[role="option"]').map(option => option.text())

beforeEach(() => {
  vi.resetAllMocks()
  api.setupOptions.mockResolvedValue(options)
})
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()) })

describe('QQ bot site model selection', () => {
  it('shows only the selected site group models and writes the chosen model back', async () => {
    const { wrapper, model } = await setup()
    expect(wrapper.findComponent(Select).exists()).toBe(true)
    expect(model.chat_model).toBe('', 'loading candidates must not silently choose a model')
    await openModels(wrapper)
    expect(modelOptions(wrapper)).toEqual(['chat-alpha', 'chat-beta'])
    await wrapper.findAll('[role="option"]')[1]!.trigger('click')
    expect(model.chat_model).toBe('chat-beta')
    expect(wrapper.get('[data-testid="setup-chat-model"] button').text()).toContain('chat-beta')

    await wrapper.get('[data-testid="site-group"]').setValue('2')
    expect(model.chat_model).toBe('chat-beta', 'switching groups preserves the draft model')
    await openModels(wrapper)
    expect(modelOptions(wrapper)).toEqual(['vision-one', 'vision-two'])
    await wrapper.findAll('[role="option"]')[0]!.trigger('click')
    expect(model.site_group_id).toBe(2)
    expect(model.chat_model).toBe('vision-one')
  })

  it('searches candidates and accepts a custom model through the real dropdown', async () => {
    const { wrapper, model } = await setup()
    await openModels(wrapper)
    await wrapper.get('.select-search-input').setValue('chat-beta')
    expect(modelOptions(wrapper)).toContain('chat-beta')
    expect(modelOptions(wrapper)).not.toContain('chat-alpha')
    await wrapper.get('.select-search-input').setValue('  private-vision-model  ')
    expect(modelOptions(wrapper)).toHaveLength(1)
    await wrapper.get('[role="option"]').trigger('click')
    expect(model.chat_model).toBe('private-vision-model')
    expect(wrapper.get('[data-testid="setup-chat-model"] button').text()).toContain('private-vision-model')

    await wrapper.get('[aria-label="Clear selection"]').trigger('click')
    expect(model.chat_model).toBe('')
  })

  it('displays a saved custom model and leaves the loaded configuration unchanged', async () => {
    const model = config({ chat_model: 'saved-custom-model', site_api_key_id: 101 })
    const before = structuredClone(model)
    const { wrapper } = await setup(model)
    expect(wrapper.get('[data-testid="setup-chat-model"] button').text()).toContain('saved-custom-model')
    expect(model).toEqual(before)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    await openModels(wrapper)
    expect(modelOptions(wrapper)).toEqual(['chat-alpha', 'chat-beta'])
    expect(model.chat_model).toBe('saved-custom-model')
  })

  it('keeps custom model entry available when site candidates fail to load', async () => {
    api.setupOptions.mockRejectedValue(new Error('candidate lookup unavailable'))
    const { wrapper, model } = await setup(config({ chat_model: 'previous-model' }))
    expect(wrapper.get('[role="alert"]').text()).toContain('candidate lookup unavailable')
    expect(wrapper.get('[data-testid="setup-chat-model"] button').text()).toContain('previous-model')
    await openModels(wrapper)
    await wrapper.get('.select-search-input').setValue('fallback-model')
    await wrapper.get('[role="option"]').trigger('click')
    expect(model.chat_model).toBe('fallback-model')
  })

  it('retains the ordinary model text input for an external connection', async () => {
    const { wrapper, model } = await setup(config({ ai_connection: 'external', chat_model: 'external-model' }))
    expect(wrapper.findComponent(Select).exists()).toBe(false)
    const input = wrapper.get<HTMLInputElement>('[data-testid="setup-chat-model"]')
    expect(input.element.tagName).toBe('INPUT')
    expect(input.element.value).toBe('external-model')
    await input.setValue('  custom-external-model  ')
    expect(model.chat_model).toBe('custom-external-model')
  })

  it('offers the selected group protocols and corrects an incompatible protocol only on group selection', async () => {
    const { wrapper, model } = await setup(config({ chat_model: 'saved-custom-model', chat_protocol: 'responses' }))
    const protocolOptions = () => wrapper.get('[data-testid="chat-protocol"]').findAll('option').map(option => option.attributes('value'))
    expect(protocolOptions()).toEqual(['chat_completions', 'responses'])
    await wrapper.get('[data-testid="site-group"]').setValue('3')
    expect(model.chat_protocol).toBe('chat_completions')
    expect(model.chat_model).toBe('saved-custom-model')
    expect(protocolOptions()).toEqual(['chat_completions'])
    await openModels(wrapper)
    expect(modelOptions(wrapper)).toEqual(['gemini-vision'])
    await wrapper.get('[data-testid="setup-chat-model"] button').trigger('click')
    await wrapper.get('[data-testid="site-group"]').setValue('2')
    expect(protocolOptions()).toEqual(['chat_completions', 'responses'])
    await wrapper.get('[data-testid="chat-protocol"]').setValue('responses')
    expect(model.chat_protocol).toBe('responses')
  })

  it('does not overwrite a saved protocol when group capabilities are initially loaded', async () => {
    const { wrapper, model } = await setup(config({ site_group_id: 3, chat_protocol: 'responses', chat_model: 'saved-gemini-name' }))
    expect(model.chat_protocol).toBe('responses')
    expect(model.chat_model).toBe('saved-gemini-name')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
})
