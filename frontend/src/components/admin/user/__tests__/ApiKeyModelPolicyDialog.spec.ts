import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiKey } from '@/types'
import ApiKeyModelPolicyDialog from '../ApiKeyModelPolicyDialog.vue'

const { updatePolicy, showSuccess } = vi.hoisted(() => ({ updatePolicy: vi.fn(), showSuccess: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { apiKeys: { updateApiKeyModelPolicy: updatePolicy } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

enableAutoUnmount(afterEach)
beforeEach(() => { updatePolicy.mockReset(); showSuccess.mockReset() })

const key = (id = 17, overrides: Partial<ApiKey> = {}): ApiKey => ({
  id, name: `key-${id}`, model_mapping: { 'gpt-6.1-sol': 'gpt-5.6-sol' }, reasoning_effort: 'high', ...overrides
}) as ApiKey

function open(apiKey = key()) {
  return mount(ApiKeyModelPolicyDialog, {
    props: { show: true, apiKey },
    global: { stubs: { BaseDialog: {
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
      model_mapping: { 'gpt-6.1-sol': 'gpt-5.6-sol-fast' }, reasoning_effort: 'xhigh'
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
    expect(updatePolicy).toHaveBeenCalledWith(17, { model_mapping: {}, reasoning_effort: '' })
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
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('request failed')
    expect((wrapper.get('[data-test="reasoning-effort"]').element as HTMLSelectElement).value).toBe('low')
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
