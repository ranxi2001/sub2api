import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import type { AxiosAdapter } from 'axios'
import BulkEditAccountModal from '../BulkEditAccountModal.vue'
import { apiClient } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { i18n, loadLocaleMessages } from '@/i18n'

const adapter = apiClient.defaults.adapter
const calls: number[] = []
const wrappers: { unmount(): void }[] = []
const start = '[data-testid="batch-upstream-start"]'
const row = (id: number) => `[data-sync-account="${id}"]`

function fixture(ids: number[], sync: (id: number) => Promise<object>, role = 'admin') {
  const pinia = createPinia()
  useAuthStore(pinia).user = { id: 17, role } as ReturnType<typeof useAuthStore>['user']
  apiClient.defaults.adapter = (async config => {
    let data = {}
    if (config.method === 'post') {
      const match = /^\/admin\/accounts\/(\d+)\/models\/sync-upstream$/.exec(config.url ?? '')
      expect(match).toBeTruthy()
      expect(JSON.parse(config.data)).toEqual({ apply: true, whitelist_only: true })
      const id = Number(match![1])
      calls.push(id)
      data = await sync(id)
    }
    return { data: { code: 0, data }, status: 200, statusText: 'OK', headers: {}, config }
  }) satisfies AxiosAdapter
  const wrapper = mount(BulkEditAccountModal, {
    props: { show: true, accountIds: ids, selectedPlatforms: ['anthropic'], selectedTypes: ['apikey'], proxies: [], groups: [] },
    global: { plugins: [pinia, i18n], stubs: { teleport: true, transition: false } }
  })
  wrappers.push(wrapper)
  return wrapper
}
const applied = (id: number) => ({ models: ['fixture-' + id], applied: true, changed: true })
beforeEach(async () => {
  calls.length = 0
  localStorage.clear()
  await loadLocaleMessages('en')
  i18n.global.locale.value = 'en'
})
afterEach(() => {
  wrappers.splice(0).forEach(w => w.unmount())
  apiClient.defaults.adapter = adapter
})

describe('batch upstream whitelist', () => {
  it('submits each selected ID separately and reports each committed result', async () => {
    const w = fixture([41, 42], async id => applied(id))
    await w.get('#bulk-edit-model-restriction-enabled').setValue(true)
    expect(w.find(start).exists(), 'selected accounts need a per-account upstream action').toBe(true)
    await w.get(start).trigger('click')
    await flushPromises()
    expect(calls).toEqual([41, 42])
    for (const id of calls) expect(w.get(row(id)).text()).toContain('Saved')
    expect(w.get<HTMLInputElement>('#bulk-edit-model-restriction-enabled').element.checked).toBe(false)
  })
})
