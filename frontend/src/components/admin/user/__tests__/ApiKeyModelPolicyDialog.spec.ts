import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiKey } from '@/types'
import ApiKeyModelPolicyDialog from '../ApiKeyModelPolicyDialog.vue'

const { updatePolicy, showSuccess } = vi.hoisted(() => ({ updatePolicy: vi.fn(), showSuccess: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { apiKeys: { updateApiKeyModelPolicy: updatePolicy } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isAdmin: true, user: { id: 41 } }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

enableAutoUnmount(afterEach)
beforeEach(() => { updatePolicy.mockReset(); showSuccess.mockReset() })

const key = (id = 17, overrides: Partial<ApiKey> = {}): ApiKey => ({
  id, name: `key-${id}`, model_mapping: { 'gpt-6.1-sol': 'gpt-5.6-sol' }, reasoning_effort: 'high', ...overrides
}) as ApiKey

function open(apiKey = key()) {
  return mount(ApiKeyModelPolicyDialog, {
    props: { show: true, apiKey },
    global: { stubs: { NewFeatureBadge: true, BaseDialog: {
      props: ['show'],
      template: '<div v-if="show"><slot /><slot name="footer" /></div>'
    } } }
  })
}

describe('API key model policy', () => {
  it('loads saved settings and writes only this key’s policy through the admin endpoint', async () => {
    const wrapper = open()
    expect((wrapper.get('[data-test="mapping-source"]').element as HTMLInputElement).value).toBe('gpt-6.1-sol')
    expect((wrapper.get('[data-test="reasoning-effort"]').element as HTMLSelectElement).value).toBe('high')
    await wrapper.get('[data-test="mapping-target"]').setValue('gpt-5.6-sol-fast')
    await wrapper.get('[data-test="reasoning-effort"]').setValue('xhigh')
    const updated = key(17, { model_mapping: { 'gpt-6.1-sol': 'gpt-5.6-sol-fast' }, reasoning_effort: 'xhigh' })
    updatePolicy.mockResolvedValue({ api_key: updated })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updatePolicy).toHaveBeenCalledTimes(1)
    expect(updatePolicy).toHaveBeenCalledWith(17, {
      model_mapping: { 'gpt-6.1-sol': 'gpt-5.6-sol-fast' }, reasoning_effort: 'xhigh', allow_long_context: true, context_token_limit: 0
    })
    expect(wrapper.emitted('updated')).toEqual([[updated]])
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('explicitly clears mappings and fixed effort', async () => {
    const wrapper = open()
    await wrapper.get('button[aria-label="apiKeyModelPolicy.removeMapping"]').trigger('click')
    await wrapper.get('[data-test="reasoning-effort"]').setValue('')
    updatePolicy.mockResolvedValue({ api_key: key(17, { model_mapping: {}, reasoning_effort: '' }) })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updatePolicy).toHaveBeenCalledWith(17, { model_mapping: {}, reasoning_effort: '', allow_long_context: true, context_token_limit: 0 })
  })

  it.each([
    [undefined, 'true'],
    [true, 'true'],
    [false, 'false']
  ] as const)('loads long-context permission %s without changing legacy defaults', (saved, expected) => {
    const wrapper = open(key(17, { allow_long_context: saved }))
    expect(wrapper.get('[data-test="allow-long-context"]').attributes('aria-checked')).toBe(expected)
    expect(wrapper.find('[data-test="long-context-restrictions"]').exists()).toBe(saved === false)
  })

  it('saves an explicit false and restores it when the saved key is reopened', async () => {
    const wrapper = open()
    await wrapper.get('[data-test="allow-long-context"]').trigger('click')
    const updated = key(17, { allow_long_context: false })
    updatePolicy.mockResolvedValue({ api_key: updated })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updatePolicy).toHaveBeenCalledWith(17, {
      model_mapping: { 'gpt-6.1-sol': 'gpt-5.6-sol' }, reasoning_effort: 'high', allow_long_context: false, context_token_limit: 0
    })
    await wrapper.setProps({ show: false, apiKey: updated })
    await wrapper.setProps({ show: true })
    expect(wrapper.get('[data-test="allow-long-context"]').attributes('aria-checked')).toBe('false')
  })

  it('keeps long-context permission isolated when switching keys', async () => {
    const wrapper = open(key(17, { allow_long_context: false }))
    expect(wrapper.get('[data-test="allow-long-context"]').attributes('aria-checked')).toBe('false')
    await wrapper.setProps({ apiKey: key(18, { allow_long_context: true }) })
    expect(wrapper.get('[data-test="allow-long-context"]').attributes('aria-checked')).toBe('true')
    await wrapper.setProps({ apiKey: key(17, { allow_long_context: false }) })
    expect(wrapper.get('[data-test="allow-long-context"]').attributes('aria-checked')).toBe('false')
  })

  it('disables the long-context switch while saving', async () => {
    let finish!: (value: unknown) => void
    updatePolicy.mockReturnValue(new Promise(resolve => { finish = resolve }))
    const wrapper = open(key(17, { allow_long_context: false }))
    await wrapper.get('form').trigger('submit')
    expect(wrapper.get('[data-test="allow-long-context"]').attributes()).toHaveProperty('disabled')
    finish({ api_key: key(17, { allow_long_context: false }) })
    await flushPromises()
    expect(wrapper.get('[data-test="allow-long-context"]').attributes()).not.toHaveProperty('disabled')
  })

  it('rejects duplicate source models instead of silently overwriting a mapping', async () => {
    const wrapper = open()
    await wrapper.get('[data-test="add-mapping"]').trigger('click')
    await wrapper.findAll('[data-test="mapping-source"]')[1].setValue('gpt-6.1-sol')
    await wrapper.findAll('[data-test="mapping-target"]')[1].setValue('gpt-5.6')
    await wrapper.get('form').trigger('submit')
    expect(updatePolicy).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toBe('apiKeyModelPolicy.duplicateModel')
  })

  it.each(['', ' invalid', 'model name', 'a'.repeat(201), '模'.repeat(67), `model${String.fromCharCode(128)}`, `model${String.fromCharCode(159)}`])('rejects invalid model ID %j', async value => {
    const wrapper = open()
    await wrapper.get('[data-test="mapping-source"]').setValue(value)
    await wrapper.get('form').trigger('submit')
    expect(updatePolicy).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toBe('apiKeyModelPolicy.invalidModel')
  })

  it('keeps edits available after a failed save', async () => {
    updatePolicy.mockRejectedValue(new Error('request failed'))
    const wrapper = open()
    await wrapper.get('[data-test="reasoning-effort"]').setValue('low')
    await wrapper.get('[data-test="allow-long-context"]').trigger('click')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('request failed')
    expect((wrapper.get('[data-test="reasoning-effort"]').element as HTMLSelectElement).value).toBe('low')
    expect(wrapper.get('[data-test="allow-long-context"]').attributes('aria-checked')).toBe('false')
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it.each([
    [{ status: 400, message: 'invalid model policy' }, 'invalid model policy'],
    [{ message: 'Request failed with status 400', response: { data: { message: 'model not supported' } } }, 'model not supported'],
    [{ response: { data: { detail: 'group unavailable' } } }, 'group unavailable'],
    [{ response: { data: '<html>upstream error</html>' } }, 'apiKeyModelPolicy.saveFailed']
  ])('shows API error details for %j', async (cause, expected) => {
    updatePolicy.mockRejectedValue(cause)
    const wrapper = open()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe(expected)
  })

  it('ignores a completed save after the dialog is unmounted', async () => {
    let finish!: (value: unknown) => void
    updatePolicy.mockReturnValue(new Promise(resolve => { finish = resolve }))
    const wrapper = open()
    await wrapper.get('form').trigger('submit')
    wrapper.unmount()
    finish({ api_key: key(17) })
    await flushPromises()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(wrapper.emitted('updated')).toBeUndefined()
  })

  it('resets on a different key and ignores the previous key’s late save result', async () => {
    let finish!: (value: unknown) => void
    updatePolicy.mockReturnValue(new Promise(resolve => { finish = resolve }))
    const wrapper = open()
    await wrapper.get('form').trigger('submit')
    await wrapper.setProps({ apiKey: key(18, { model_mapping: {}, reasoning_effort: '' }) })
    expect(wrapper.find('[data-test="model-mapping-row"]').exists()).toBe(false)
    finish({ api_key: key(17) })
    await flushPromises()
    expect(wrapper.emitted('updated')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(showSuccess).not.toHaveBeenCalled()
  })
})

describe('API key context token ceiling', () => {
 it('loads, saves and clears the selected key ceiling', async () => {
  const wrapper = open(key(17, { context_token_limit: 1000 }))
  expect((wrapper.get('[data-test="context-token-limit"]').element as HTMLInputElement).value).toBe('1000')
  await wrapper.get('[data-test="context-token-limit"]').setValue('2000')
  const updated = key(17, { context_token_limit: 2000 })
  updatePolicy.mockResolvedValue({ api_key: updated })
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  expect(updatePolicy).toHaveBeenLastCalledWith(17, expect.objectContaining({ context_token_limit: 2000 }))
  await wrapper.setProps({ show: false, apiKey: updated })
  await wrapper.setProps({ show: true })
  expect((wrapper.get('[data-test="context-token-limit"]').element as HTMLInputElement).value).toBe('2000')
  await wrapper.get('[data-test="context-token-limit"]').setValue('')
  await wrapper.get('form').trigger('submit')
  await flushPromises()
  expect(updatePolicy).toHaveBeenLastCalledWith(17, expect.objectContaining({ context_token_limit: 0 }))
 })
 it('resets unsaved ceilings when switching keys', async () => {
  const wrapper = open(key(17, { context_token_limit: 1000 }))
  await wrapper.get('[data-test="context-token-limit"]').setValue('4000')
  await wrapper.setProps({ apiKey: key(18) })
  expect((wrapper.get('[data-test="context-token-limit"]').element as HTMLInputElement).value).toBe('')
  await wrapper.setProps({ apiKey: key(17, { context_token_limit: 1000 }) })
  expect((wrapper.get('[data-test="context-token-limit"]').element as HTMLInputElement).value).toBe('1000')
 })
 it.each(['-1', '1.5', '2147483648'])('rejects invalid ceiling %s before API writes', async value => {
  const wrapper = open()
  await wrapper.get('[data-test="context-token-limit"]').setValue(value)
  await wrapper.get('form').trigger('submit')
  expect(wrapper.get('[role="alert"]').text()).toBe('apiKeyModelPolicy.invalidContextTokenLimit')
  expect(updatePolicy).not.toHaveBeenCalled()
 })
})
