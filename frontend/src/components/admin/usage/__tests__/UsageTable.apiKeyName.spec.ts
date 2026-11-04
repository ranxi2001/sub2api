import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import UsageTable from '../UsageTable.vue'
import type { AdminUsageLog } from '@/types'

vi.mock('@/stores/app', () => ({ useAppStore: () => ({}) }))
vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))

const longName = '合成用量验证密钥-' + '12345678-abcd-4321-abcd-123456789abc'
const wrappers: ReturnType<typeof mount>[] = []
function render(name = longName) {
  const row = { id: 1, api_key: { name } } as AdminUsageLog
  const wrapper = mount(UsageTable, {
    attachTo: document.body,
    props: { data: [row], columns: [{ key: 'api_key', label: 'API Key' }] },
    global: { stubs: { UsageTimingDialog: true, transition: true } },
  })
  wrappers.push(wrapper)
  return wrapper
}
function resize(width: number) {
  Object.defineProperty(window, 'innerWidth', { value: width, configurable: true })
  window.dispatchEvent(new Event('resize'))
}
afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
  document.body.innerHTML = ''
  resize(1024)
})

describe('usage API key name', () => {
  it('reveals the original full name on hover in the real shared table', async () => {
    const wrapper = render()
    const trigger = wrapper.get('td button')
    expect(trigger.text()).toBe(longName)
    await trigger.trigger('mouseenter')
    await flushPromises()
    expect(document.body.querySelector('[role="tooltip"]')?.textContent).toBe(longName)
  })

  it('reveals the name on keyboard focus and dismisses with Escape', async () => {
    const wrapper = render()
    const trigger = wrapper.get('td button')
    await trigger.trigger('focus')
    await flushPromises()
    expect(document.body.querySelector('[role="tooltip"]')?.textContent).toBe(longName)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull()
  })

  it('keeps the full name open after the touch hover/click/leave sequence', async () => {
    resize(375)
    const wrapper = render('A'.repeat(600))
    const trigger = wrapper.get('td button')
    await trigger.trigger('mouseenter')
    await trigger.trigger('click')
    await trigger.trigger('mouseleave')
    await flushPromises()
    const dialog = document.body.querySelector('[role="dialog"]')
    expect(dialog?.querySelector('.modal-body')?.textContent).toBe('A'.repeat(600))
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(document.body.querySelector('[role="dialog"]')).toBeNull()
  })
})
