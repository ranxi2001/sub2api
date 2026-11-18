import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import type { AxiosAdapter } from 'axios'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import BulkEditAccountModal from '../BulkEditAccountModal.vue'
import { apiClient } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { i18n, loadLocaleMessages } from '@/i18n'

const adapter = apiClient.defaults.adapter
const calls: number[] = []
let checkRisk: (() => Promise<object>) | undefined
const bulkWrites: object[] = []
const wrappers: { unmount(): void }[] = []
const start = '[data-testid="batch-upstream-start"]'
const row = (id: number) => `[data-sync-account="${id}"]`

function fixture(ids: number[], sync: (id: number) => Promise<object>, role = 'admin') {
  const pinia = createPinia()
  useAuthStore(pinia).user = { id: 17, role } as ReturnType<typeof useAuthStore>['user']
  apiClient.defaults.adapter = (async config => {
    let data = {}
    if (config.url === '/admin/accounts/check-mixed-channel') {
      data = await checkRisk!()
    } else if (config.url === '/admin/accounts/bulk-update') {
      bulkWrites.push(JSON.parse(config.data))
      data = { success: 2, failed: 0, results: [] }
    } else if (config.method === 'post') {
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
  bulkWrites.length = 0
  checkRisk = undefined
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

  it('bounds work to two, rejects reentry and stops only queued accounts', async () => {
    const releases: (() => void)[] = []
    const w = fixture([41, 42, 43, 44], async id => {
      await new Promise<void>(done => releases.push(done))
      return applied(id)
    })
    await w.get(start).trigger('click')
    await flushPromises()
    await w.get(start).trigger('click')
    expect(calls).toEqual([41, 42])
    expect(w.get('[type="submit"]').attributes('disabled')).toBeDefined()
    await w.get('[data-testid="batch-upstream-stop"]').trigger('click')
    expect(w.get(row(41)).text()).toContain('Submitted')
    expect(w.get(row(43)).text()).toContain('Not started')
    releases.forEach(done => done())
    await flushPromises()
    expect(calls).toEqual([41, 42])
    expect(w.get(row(42)).text()).toContain('Saved')
    expect(w.find('[data-testid="batch-upstream-retry"]').exists()).toBe(false)
  })

  it('serializes the ordinary form preflight with immediate whitelist writes', async () => {
    let release!: (value: object) => void
    checkRisk = () => new Promise(resolve => { release = resolve })
    const w = fixture([41,42], async id => applied(id))
    await w.get('#bulk-edit-groups-enabled').setValue(true)
    const groupSelector = w.findComponent({ name: 'GroupSelector' })
    groupSelector.vm.$emit('update:modelValue', [5])
    await flushPromises()
    await w.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(release).toBeTypeOf('function')
    expect(w.get(start).attributes('disabled'), 'preflight must own the write lock').toBeDefined()
    await w.get(start).trigger('click')
    expect(calls).toEqual([])
    release({has_risk:false})
    await flushPromises()
    expect(bulkWrites).toHaveLength(1)
  })

  it.each([false, true])('does not retain an old preflight after reopening (risk=%s)', async risk => {
    let release!: (value: object) => void
    checkRisk = () => new Promise(resolve => { release = resolve })
    const w = fixture([41,42], async id => applied(id))
    await w.get('#bulk-edit-groups-enabled').setValue(true)
    w.findComponent({ name: 'GroupSelector' }).vm.$emit('update:modelValue', [5])
    await flushPromises()
    await w.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()
    await w.setProps({show:false})
    await w.setProps({show:true,accountIds:[77]})
    release({has_risk:risk})
    await flushPromises()
    expect(w.findComponent({ name: 'ConfirmDialog' }).props('show'), 'old confirmation must not target the new selection').toBe(false)
    expect(bulkWrites, 'old preflight cannot target the new selection').toEqual([])
  })

  it('retains the frozen selection across a table refresh and retries only failures', async () => {
    const w = fixture([41, 42, 73, 74], async id => {
      if (id === 42 && calls.filter(n => n === id).length === 1) throw Error('unreachable')
      if (id === 73) return { models: [], skipped: 'empty' }
      return applied(id)
    })
    await w.setProps({ accountIds: [88] })
    await w.get(start).trigger('click')
    await flushPromises()
    expect(calls).toEqual([41, 42, 73, 74])
    expect(w.get(row(73)).text()).toContain('empty')
    await w.get('[data-testid="batch-upstream-retry"]').trigger('click')
    await flushPromises()
    expect(calls).toEqual([41, 42, 73, 74, 42, 73])
    expect(w.get(row(42)).text()).toContain('Saved')
    expect(w.get(row(74)).text()).toContain('Saved')
  })

  it('closing stops launches and drains submitted writes before reopening for different IDs', async () => {
    const releases: (() => void)[] = []
    const w = fixture([41,42,43], async id => {
      if (id < 50) await new Promise<void>(done => releases.push(done))
      return applied(id)
    })
    await w.get(start).trigger('click')
    await flushPromises()
    await w.setProps({show:false})
    await w.setProps({accountIds:[82],show:true})
    expect(w.get(start).attributes('disabled')).toBeDefined()
    releases.forEach(done => done())
    await flushPromises()
    expect(calls).toEqual([41,42])
    expect(w.find(row(41)).exists()).toBe(false)
    await w.get(start).trigger('click')
    await flushPromises()
    expect(calls).toEqual([41,42,82])
    expect(w.get(row(82)).text()).toContain('Saved')
  })

  it.each([[0],[-1],[1.5],[41,41],[]])('rejects invalid selected IDs (%j)', async (...ids) => {
    const w = fixture(ids, async id => applied(id))
    expect(w.get(start).attributes('disabled')).toBeDefined()
    expect(calls).toEqual([])
  })

  it('does not offer writes to observers or implicit filter previews', async () => {
    const w = fixture([41], async id => applied(id), 'observer')
    expect(w.find(start).exists()).toBe(false)
    const admin = fixture([41], async id => applied(id))
    await admin.setProps({ filteredPreview: true })
    expect(admin.find(start).exists()).toBe(false)
    expect(calls).toEqual([])
  })

  it('announces B independently and remembers dismissal when the same admin returns', async () => {
    const old = NEW_FEATURE_NOTICES.accountAutoRefreshUpstreamModels
    const id = 'account-batch-upstream-whitelist-v1'
    const key = 'sub2api:new-feature-notices:v1:17'
    localStorage.setItem(key, JSON.stringify([old]))
    const w = fixture([41], async n => applied(n))
    const badge = w.findComponent(NewFeatureBadge)
    expect(badge.exists(), 'batch sync needs its own dismissible announcement').toBe(true)
    expect(badge.props('noticeId')).toBe(id)
    await badge.get('button').trigger('click')
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual([old,id])
    const reopened = fixture([41], async n => applied(n))
    expect(reopened.getComponent(NewFeatureBadge).find('button').exists()).toBe(false)
    const pinia = createPinia()
    for (const [noticeId,visible] of [[old,false],[id,false],['future-C',true]] as const) {
      const b = mount(NewFeatureBadge,{props:{noticeId,userId:17},global:{plugins:[pinia,i18n]}})
      expect(b.find('button').exists()).toBe(visible)
      b.unmount()
    }
  })
})
