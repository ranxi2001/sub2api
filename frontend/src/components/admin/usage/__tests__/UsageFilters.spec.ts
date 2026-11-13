import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

import UsageFilters from '../UsageFilters.vue'
import { observerUsageContext } from '../observerUsageContext'

// --- i18n messages (only what UsageFilters needs) ---
const messages: Record<string, string> = {
  'admin.usage.userDeletedBadge': 'deleted',
  'admin.usage.userFilter': 'User',
  'admin.usage.searchUserPlaceholder': 'Search user...',
  'usage.apiKeyFilter': 'API Key',
  'admin.usage.searchApiKeyPlaceholder': 'Search API key...',
  'usage.model': 'Model',
  'admin.usage.allModels': 'All Models',
  'admin.usage.account': 'Account',
  'admin.usage.searchAccountPlaceholder': 'Search account...',
  'usage.type': 'Type',
  'admin.usage.allTypes': 'All Types',
  'usage.ws': 'WS',
  'usage.stream': 'Stream',
  'usage.sync': 'Sync',
  'usage.compactionFilter': 'Request Kind',
  'usage.allCompactionTypes': 'All Requests',
  'usage.compactionOnly': 'Compaction Only',
  'admin.usage.billingType': 'Billing Type',
  'admin.usage.allBillingTypes': 'All Billing Types',
  'admin.usage.billingTypeBalance': 'Balance',
  'admin.usage.billingTypeSubscription': 'Subscription',
  'admin.usage.billingMode': 'Billing Mode',
  'admin.usage.allBillingModes': 'All Billing Modes',
  'admin.usage.billingModeToken': 'Token',
  'admin.usage.billingModePerRequest': 'Per Request',
  'admin.usage.billingModeImage': 'Image',
	'admin.usage.upstreamModelAudit': 'Upstream model audit',
	'admin.usage.allUpstreamModelAudit': 'All response model states',
	'admin.usage.upstreamModelMismatchOnly': 'Mismatched only',
	'admin.usage.upstreamModelMatchedOnly': 'Matched only',
  'admin.usage.group': 'Group',
  'admin.usage.allGroups': 'All Groups',
  'common.refresh': 'Refresh',
  'common.reset': 'Reset',
  'admin.usage.cleanup.button': 'Cleanup',
  'usage.exportExcel': 'Export',
}

// Mock vue-i18n
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

// Mock the admin API module — we control searchUsers return value per test
const mockSearchUsers = vi.fn()
const mockSearchApiKeys = vi.fn().mockResolvedValue([])
const mockGroupsList = vi.fn().mockResolvedValue({ items: [] })
const mockGetModelStats = vi.fn().mockResolvedValue({ models: [] })
const mockAccountsList = vi.fn().mockResolvedValue({ items: [] })

vi.mock('@/api/admin', () => ({
  adminAPI: {
    usage: {
      searchUsers: (...args: any[]) => mockSearchUsers(...args),
      searchApiKeys: (...args: any[]) => mockSearchApiKeys(...args),
    },
    groups: { list: (...args: any[]) => mockGroupsList(...args) },
    dashboard: { getModelStats: (...args: any[]) => mockGetModelStats(...args) },
    accounts: { list: (...args: any[]) => mockAccountsList(...args) },
  },
}))

const ownOptions = vi.fn().mockResolvedValue([])
vi.mock('@/api/observerUsage', () => ({ observerUsageAPI: { filterOptions: (...args: any[]) => ownOptions(...args) } }))

// Default props helper
const defaultFilters = () => ({
  user_id: undefined,
  api_key_id: undefined,
  account_id: undefined,
  model: null,
  request_type: null,
  native_compaction_v2: null,
  billing_type: null,
  billing_mode: null,
	upstream_model_mismatch: null,
  group_id: null,
  start_date: '',
  end_date: '',
})

function mountFilters(filters = defaultFilters(), mode: 'usage' | 'errors' | 'ranking' | 'connections' | 'latency' = 'usage') {
  return mount(UsageFilters, {
    props: {
      modelValue: filters,
      mode,
      exporting: false,
      startDate: '2026-05-01',
      endDate: '2026-05-28',
      showActions: false,
      modelOptions: [],
    },
    global: {
      stubs: {
        Select: true,
        Teleport: true,
      },
    },
  })
}

describe('UsageFilters latency mode', () => {
  it('shows only filters supported by the latency report', async () => {
    const wrapper = mountFilters(defaultFilters(), 'latency')
    await flushPromises()

    const labels = wrapper.findAll('label').map(label => label.text())
    expect(labels).toContain('User')
    expect(labels).toContain('API Key')
    expect(labels).toContain('Model')
    expect(labels).toContain('Account')
    expect(labels).toContain('Group')
    expect(labels).not.toContain('Type')
    expect(labels).not.toContain('Request Kind')
    expect(labels).not.toContain('Billing Type')
    expect(labels).not.toContain('Billing Mode')
    wrapper.unmount()
  })
})

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })

  return { promise, resolve, reject }
}

describe('UsageFilters — user search dropdown', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    mockSearchUsers.mockReset()
    mockSearchApiKeys.mockResolvedValue([])
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('(a) labels deleted users with the i18n badge and (b) sorts active users before deleted ones, (c) selection sets user_id', async () => {
    // Arrange: mock returns deleted FIRST (proves sorting re-orders to active-first)
    mockSearchUsers.mockResolvedValue([
      { id: 2, email: 'gone@test.com', deleted: true },
      { id: 1, email: 'active@test.com', deleted: false },
    ])

    const wrapper = mountFilters()

    // Trigger focus (sets showUserDropdown = true) then input (fires debounceUserSearch)
    const input = wrapper.find('input[type="text"]')
    await input.trigger('focus')
    await input.setValue('test')
    await input.trigger('input')

    // Advance debounce timer (300ms) then flush the resolved promise
    vi.advanceTimersByTime(300)
    await flushPromises()

    // --- (b) Sort: active user should appear BEFORE deleted user ---
    // Check the underlying component state via rendered DOM order
    const buttons = wrapper.findAll('.usage-filter-dropdown button[type="button"]')
    const emailTexts = buttons.map((b) => b.text())

    // active@test.com should be listed first
    const activeIdx = emailTexts.findIndex((t) => t.includes('active@test.com'))
    const deletedIdx = emailTexts.findIndex((t) => t.includes('gone@test.com'))
    expect(activeIdx).toBeGreaterThanOrEqual(0)
    expect(deletedIdx).toBeGreaterThanOrEqual(0)
    expect(activeIdx).toBeLessThan(deletedIdx)

    // --- (a) Label: deleted user's button shows the badge text ---
    const deletedButton = buttons[deletedIdx]
    expect(deletedButton.text()).toContain('deleted')

    // active user's button does NOT show the badge text
    const activeButton = buttons[activeIdx]
    expect(activeButton.text()).not.toContain('deleted')

    // --- (c) Selection: clicking active user button sets filters.user_id ---
    await activeButton.trigger('click')
    await flushPromises()

    // The component emits 'update:modelValue' or modifies filters.user_id via toRef
    // selectUser sets filters.value.user_id = u.id and emits 'change'
    const changeEmits = wrapper.emitted('change')
    expect(changeEmits).toBeTruthy()
    expect(changeEmits!.length).toBeGreaterThan(0)

    // Also confirm user_id was set by checking the emitted change came through
    // (the component uses toRef so modelValue is mutated in place and 'change' is emitted)
    expect(wrapper.props('modelValue').user_id).toBe(1)
  })

  it('keeps results from the latest user search when responses arrive out of order', async () => {
    const firstSearch = deferred<Array<{ id: number; email: string; deleted: boolean }>>()
    const secondSearch = deferred<Array<{ id: number; email: string; deleted: boolean }>>()
    mockSearchUsers
      .mockImplementationOnce(() => firstSearch.promise)
      .mockImplementationOnce(() => secondSearch.promise)

    const wrapper = mountFilters()
    const input = wrapper.find('input[type="text"]')
    await input.trigger('focus')

    await input.setValue('a')
    vi.advanceTimersByTime(300)
    await flushPromises()

    await input.setValue('ab')
    vi.advanceTimersByTime(300)
    await flushPromises()

    secondSearch.resolve([{ id: 2, email: 'ab@test.com', deleted: false }])
    await flushPromises()
    expect(wrapper.text()).toContain('ab@test.com')

    firstSearch.resolve([{ id: 1, email: 'a@test.com', deleted: false }])
    await flushPromises()
    expect(wrapper.text()).toContain('ab@test.com')
    expect(wrapper.text()).not.toContain('a@test.com')
  })

  it('does not restore stale user results after the search is cleared', async () => {
    const pendingSearch = deferred<Array<{ id: number; email: string; deleted: boolean }>>()
    mockSearchUsers.mockImplementationOnce(() => pendingSearch.promise)

    const wrapper = mountFilters()
    const input = wrapper.find('input[type="text"]')
    await input.trigger('focus')

    await input.setValue('stale')
    vi.advanceTimersByTime(300)
    await flushPromises()

    await input.setValue('')
    vi.advanceTimersByTime(300)
    await flushPromises()

    pendingSearch.resolve([{ id: 3, email: 'stale@test.com', deleted: false }])
    await flushPromises()
    expect(wrapper.text()).not.toContain('stale@test.com')
  })
})

describe('UsageFilters — model options come from prop (no dup request)', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    mockGetModelStats.mockClear()
    mockGroupsList.mockClear()
  })
  afterEach(() => { vi.useRealTimers() })

  it('does not call dashboard.getModelStats on mount and renders model options from prop', async () => {
    const wrapper = mount(UsageFilters, {
      props: {
        modelValue: defaultFilters(),
        exporting: false,
        startDate: '2026-05-01',
        endDate: '2026-05-28',
        showActions: false,
        modelOptions: ['claude-3', 'gpt-4o'],
      },
      global: { stubs: { Select: true, Teleport: true } },
    })
    await flushPromises()

    expect(mockGetModelStats).not.toHaveBeenCalled()

    const opts = (wrapper.vm as any).modelOptions as Array<{ value: string | null; label: string }>
    expect(opts.map((o) => o.value)).toEqual([null, 'claude-3', 'gpt-4o'])
  })
})

describe('UsageFilters — native compaction filter', () => {
  it('offers only All/Compaction and emits the independent boolean filter', async () => {
    const SelectStub = {
      name: 'Select',
      props: ['modelValue', 'options'],
      emits: ['update:modelValue', 'change'],
      template: '<div />',
    }
    const filters = defaultFilters()
    const wrapper = mount(UsageFilters, {
      props: {
        modelValue: filters,
        exporting: false,
        startDate: '2026-05-01',
        endDate: '2026-05-28',
        showActions: false,
        modelOptions: [],
      },
      global: { stubs: { Select: SelectStub, Teleport: true } },
    })

    const compactionSelect = wrapper.findAllComponents(SelectStub).find((select: any) =>
      (select.props('options') as Array<{ value: unknown }>).some((option) => option.value === true)
    )
    expect(compactionSelect).toBeDefined()
    expect(compactionSelect!.props('options')).toEqual([
      { value: null, label: 'All Requests' },
      { value: true, label: 'Compaction Only' },
    ])
    expect(compactionSelect!.props('options')).not.toEqual(
      expect.arrayContaining([expect.objectContaining({ value: false })])
    )

    compactionSelect!.vm.$emit('update:modelValue', true)
    compactionSelect!.vm.$emit('change')
    await wrapper.vm.$nextTick()

    expect(filters.native_compaction_v2).toBe(true)
    expect(wrapper.emitted('change')).toBeTruthy()
  })
})


describe('observer usage filters', () => {
  it('keeps export, hides user search and cleanup, and only searches own choices', async () => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    const wrapper = mount(UsageFilters, {
      props: { modelValue: defaultFilters(), exporting: false, startDate: '', endDate: '' },
      global: { provide: { [observerUsageContext as symbol]: true }, stubs: { Select: true } },
    })
    await flushPromises()
    expect(ownOptions).toHaveBeenCalledWith('group')
    expect(wrapper.find('input[placeholder="Search user..."]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Cleanup')
    const exportButton = wrapper.findAll('button').find(button => button.text() === 'Export')!
    await exportButton.trigger('click')
    expect(wrapper.emitted('export')).toHaveLength(1)
    await wrapper.find('input[placeholder="Search API key..."]').setValue('my-key')
    await wrapper.find('input[placeholder="Search account..."]').setValue('my-account')
    await vi.advanceTimersByTimeAsync(350)
    expect(ownOptions).toHaveBeenCalledWith('api_key', 'my-key')
    expect(ownOptions).toHaveBeenCalledWith('account', 'my-account')
    for (const fn of [mockSearchUsers, mockSearchApiKeys, mockGroupsList, mockAccountsList]) expect(fn).not.toHaveBeenCalled()
    wrapper.unmount()
    vi.useRealTimers()
  })
})

describe('UsageFilters — identity-only traffic filters', () => {
  let wrapper: ReturnType<typeof mount>
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    mockSearchUsers.mockResolvedValue([
      { id: 1, email: 'first@example.test', deleted: false },
      { id: 2, email: 'second@example.test', deleted: false },
    ])
    mockSearchApiKeys.mockReset().mockResolvedValue([{ id: 10, name: 'First key' }])
    wrapper = mount(UsageFilters, {
      props: { modelValue: defaultFilters(), identityOnly: true, showActions: false, exporting: false, startDate: '', endDate: '' },
    })
  })
  afterEach(() => { wrapper.unmount(); vi.useRealTimers() })

  async function chooseUser(email: string) {
    const input = wrapper.get('input[placeholder="Search user..."]')
    await input.trigger('focus'); await input.setValue('example')
    await vi.advanceTimersByTimeAsync(300); await flushPromises()
    await wrapper.findAll('button').find(button => button.text().includes(email))!.trigger('click')
    await flushPromises()
  }

  it('renders only shared user/key searches without loading unrelated filter data', async () => {
    await flushPromises()
    expect(wrapper.findAll('input')).toHaveLength(2)
    expect(wrapper.findAllComponents({ name: 'Select' })).toHaveLength(0)
    expect(mockGroupsList).not.toHaveBeenCalled()
    expect(wrapper.props('modelValue').start_date).toBe('')
  })

  it('clears the selected key when changing users and loads the new user’s keys', async () => {
    await chooseUser('first@example.test')
    await wrapper.get('input[placeholder="Search API key..."]').trigger('focus')
    await wrapper.findAll('button').find(button => button.text().includes('First key'))!.trigger('click')
    expect(wrapper.props('modelValue').api_key_id).toBe(10)
    mockSearchApiKeys.mockResolvedValue([{ id: 20, name: 'Second key' }])
    await chooseUser('second@example.test')
    expect(wrapper.props('modelValue').api_key_id).toBeUndefined()
    expect(mockSearchApiKeys).toHaveBeenLastCalledWith(2, '')
    await wrapper.get('input[placeholder="Search API key..."]').trigger('focus')
    expect(wrapper.text()).toContain('Second key')
    expect(wrapper.text()).not.toContain('First key')
    await wrapper.get('[aria-label="Clear user filter"]').trigger('click')
    expect(wrapper.props('modelValue').user_id).toBeUndefined()
    expect(wrapper.get('input[placeholder="Search API key..."]').element).toHaveProperty('value', '')
  })

  it('ignores key search responses from a previous user scope', async () => {
    const pending = deferred<Array<{ id: number; name: string }>>()
    mockSearchApiKeys.mockImplementationOnce(() => pending.promise)
    await chooseUser('first@example.test')
    mockSearchApiKeys.mockResolvedValue([{ id: 20, name: 'Second key' }])
    await chooseUser('second@example.test')
    pending.resolve([{ id: 10, name: 'Stale key' }]); await flushPromises()
    await wrapper.get('input[placeholder="Search API key..."]').trigger('focus')
    expect(wrapper.text()).toContain('Second key')
    expect(wrapper.text()).not.toContain('Stale key')
  })

  it('ignores outdated keyword results and does not restore keys after clearing', async () => {
    const pending = deferred<Array<{ id: number; name: string }>>()
    mockSearchApiKeys.mockImplementationOnce(() => pending.promise)
    const input = wrapper.get('input[placeholder="Search API key..."]')
    await input.trigger('focus'); await input.setValue('old')
    await vi.advanceTimersByTimeAsync(300)
    mockSearchApiKeys.mockResolvedValue([{ id: 20, name: 'Current key' }])
    await input.setValue('current'); await vi.advanceTimersByTimeAsync(300); await flushPromises()
    pending.resolve([{ id: 10, name: 'Old key' }]); await flushPromises()
    expect(wrapper.text()).toContain('Current key')
    expect(wrapper.text()).not.toContain('Old key')
    await wrapper.findAll('button').find(button => button.text().includes('Current key'))!.trigger('click')
    const clearing = deferred<Array<{ id: number; name: string }>>()
    mockSearchApiKeys.mockImplementationOnce(() => clearing.promise)
    await input.setValue('pending'); await vi.advanceTimersByTimeAsync(300)
    await wrapper.get('[aria-label="Clear API key filter"]').trigger('click')
    clearing.resolve([{ id: 30, name: 'Cleared key' }]); await flushPromises()
    await input.trigger('focus')
    expect(wrapper.text()).not.toContain('Cleared key')
    expect(wrapper.props('modelValue').api_key_id).toBeUndefined()
  })
})

describe('admin usage platform filter', () => {
  it.each(['errors', 'ranking', 'connections', 'latency'] as const)('keeps the platform selector out of %s', async mode => {
    const wrapper = mountFilters(defaultFilters(), mode)
    await flushPromises()
    expect(wrapper.find('[data-testid="usage-platform-filter"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('does not expose the admin platform filter in observer mode', async () => {
    const wrapper = mount(UsageFilters, {
      props: { modelValue: defaultFilters(), exporting: false, startDate: '', endDate: '' },
      global: { provide: { [observerUsageContext as symbol]: true }, stubs: { Select: true } },
    })
    await flushPromises()
    expect(wrapper.find('[data-testid="usage-platform-filter"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('offers a server-side platform filter alongside the group filter', async () => {
    const wrapper = mountFilters()
    await flushPromises()
    expect(wrapper.find('[data-testid="usage-platform-filter"]').exists(),
      'admin usage details must offer a platform selector').toBe(true)
    wrapper.unmount()
  })
})
