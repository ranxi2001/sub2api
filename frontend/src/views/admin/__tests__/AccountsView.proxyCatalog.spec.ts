import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount, type VueWrapper } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'
import CreateAccountModal from '@/components/account/CreateAccountModal.vue'
import EditAccountModal from '@/components/account/EditAccountModal.vue'

const { getAllProxies, getAllGroups, listAccounts } = vi.hoisted(() => ({
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  listAccounts: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      getManagementCapabilities: vi.fn().mockResolvedValue({ concurrency_upgrade_enabled: false }),
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: false }),
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} })
    },
    proxies: { getAll: getAllProxies },
    groups: { getAll: getAllGroups }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showWarning: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token', isSimpleMode: false })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

const proxyCatalog = [{
  id: 11, name: 'primary', protocol: 'http', host: 'proxy.example.com', port: 8080, status: 'active'
}]
const groups = [{ id: 7, name: 'codex', platform: 'openai' }]
const wrappers: VueWrapper[] = []

function mountView() {
  const wrapper = shallowMount(AccountsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /></div>' }
      }
    }
  })
  wrappers.push(wrapper)
  return wrapper
}

function modals(wrapper: VueWrapper) {
  return [wrapper.getComponent(CreateAccountModal), wrapper.getComponent(EditAccountModal)]
}

// This suite verifies the real parent-to-modal props/events boundary. Selection
// persistence inside the real modals is covered by their integration suites.
describe('admin AccountsView proxy catalog', () => {
  beforeEach(() => {
    localStorage.clear()
    getAllProxies.mockReset().mockResolvedValue(proxyCatalog)
    getAllGroups.mockReset().mockResolvedValue(groups)
    listAccounts.mockReset().mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  afterEach(() => {
    wrappers.splice(0).forEach(wrapper => wrapper.unmount())
    vi.restoreAllMocks()
  })

  it('passes pending state to both modals and keeps them mounted when the catalog resolves', async () => {
    const pending = deferred<typeof proxyCatalog>()
    getAllProxies.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await flushPromises()
    const initialModals = modals(wrapper)
    for (const modal of initialModals) {
      expect(modal.props()).toMatchObject({ proxies: [], proxiesLoading: true, proxiesLoadError: false })
    }

    pending.resolve(proxyCatalog)
    await flushPromises()

    modals(wrapper).forEach((modal, index) => {
      expect(modal.vm).toBe(initialModals[index]!.vm)
      expect(modal.props()).toMatchObject({ proxies: proxyCatalog, proxiesLoading: false, proxiesLoadError: false, groups })
    })
  })

  it('reports catalog failure without losing the independently loaded group catalog', async () => {
    getAllProxies.mockRejectedValueOnce(new Error('proxy catalog unavailable'))
    const wrapper = mountView()
    await flushPromises()

    expect(getAllGroups).toHaveBeenCalledTimes(1)
    for (const modal of modals(wrapper)) {
      expect(modal.props()).toMatchObject({ proxies: [], proxiesLoading: false, proxiesLoadError: true, groups })
    }
  })

  it('coalesces retries from both modals and clears the error after recovery', async () => {
    const retry = deferred<typeof proxyCatalog>()
    getAllProxies.mockRejectedValueOnce(new Error('proxy catalog unavailable')).mockReturnValueOnce(retry.promise)
    const wrapper = mountView()
    await flushPromises()
    const initialModals = modals(wrapper)

    initialModals[0]!.vm.$emit('retry-proxies')
    initialModals[1]!.vm.$emit('retry-proxies')
    initialModals[0]!.vm.$emit('retry-proxies')
    await flushPromises()

    expect(getAllProxies).toHaveBeenCalledTimes(2)
    expect(getAllGroups).toHaveBeenCalledTimes(1)
    for (const modal of modals(wrapper)) {
      expect(modal.props()).toMatchObject({ proxies: [], proxiesLoading: true, proxiesLoadError: false, groups })
    }

    retry.resolve(proxyCatalog)
    await flushPromises()

    modals(wrapper).forEach((modal, index) => {
      expect(modal.vm).toBe(initialModals[index]!.vm)
      expect(modal.props()).toMatchObject({ proxies: proxyCatalog, proxiesLoading: false, proxiesLoadError: false, groups })
    })
  })

  it('retains the previous catalog and modal instances during a failed refresh', async () => {
    const refresh = deferred<typeof proxyCatalog>()
    const wrapper = mountView()
    await flushPromises()
    const initialModals = modals(wrapper)
    getAllProxies.mockReturnValueOnce(refresh.promise)

    initialModals[1]!.vm.$emit('retry-proxies')
    await flushPromises()
    for (const modal of modals(wrapper)) {
      expect(modal.props()).toMatchObject({ proxies: proxyCatalog, proxiesLoading: true, proxiesLoadError: false, groups })
    }

    refresh.reject(new Error('refresh unavailable'))
    await flushPromises()

    modals(wrapper).forEach((modal, index) => {
      expect(modal.vm).toBe(initialModals[index]!.vm)
      expect(modal.props()).toMatchObject({ proxies: proxyCatalog, proxiesLoading: false, proxiesLoadError: true, groups })
    })
  })
})
