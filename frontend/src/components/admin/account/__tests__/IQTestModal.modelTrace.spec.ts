import { createPinia, setActivePinia } from 'pinia'
import { beforeEach as setupPiniaBeforeEach } from 'vitest'
setupPiniaBeforeEach(() => setActivePinia(createPinia()))
import { expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { Account } from '@/types'
import IQTestModal from '../IQTestModal.vue'

vi.mock('@/api/admin/accounts', async () => {
  const actual = await vi.importActual<typeof import('@/api/admin/accounts')>('@/api/admin/accounts')
  return {
    ...actual,
    getAvailableModels: vi.fn().mockResolvedValue([]),
    getModelReasoning: vi.fn().mockResolvedValue({ supported_reasoning_levels: [], default_reasoning_level: '' }),
    probeOpenAICodexState: vi.fn()
  }
})
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

it('opens the ModelTrace panel without starting the ordinary question flow', async () => {
  const wrapper = mount(IQTestModal, { props: { show: true, account: { id: 42, name: 'Mock account', platform: 'openai', type: 'apikey' } as Account }, global: { stubs: {
    BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
    ModelTracePanel: { template: '<div data-testid="modeltrace-child" />' },
    Select: true, Input: true, TextArea: true, Icon: true
  } } })
  await flushPromises()
  await wrapper.get('[data-testid="mode-modeltrace"]').trigger('click')
  expect(wrapper.find('[data-testid="modeltrace-child"]').exists()).toBe(true)
  expect(wrapper.find('[data-testid="question-select"]').exists()).toBe(false)
  expect(wrapper.text()).not.toContain('admin.accounts.pelicanTest.start')
  wrapper.unmount()
})
