import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpsErrorDetailModal from '../OpsErrorDetailModal.vue'
import { observerUsageContext } from '@/components/admin/usage/observerUsageContext'

const mocks = vi.hoisted(() => ({
  getRequestErrorDetail: vi.fn(),
  own: vi.fn(),
  listRequestErrorUpstreamErrors: vi.fn()
}))

vi.mock('@/api/observerUsage', () => ({ observerUsageAPI: { getErrorDetail: mocks.own } }))
vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    getRequestErrorDetail: mocks.getRequestErrorDetail,
    getUpstreamErrorDetail: vi.fn(),
    listRequestErrorUpstreamErrors: mocks.listRequestErrorUpstreamErrors
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: vi.fn() })
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

describe('OpsErrorDetailModal', () => {
  beforeEach(() => {
    mocks.getRequestErrorDetail.mockReset()
    mocks.listRequestErrorUpstreamErrors.mockReset()
    mocks.listRequestErrorUpstreamErrors.mockResolvedValue({ items: [] })
  })

  it('prioritizes upstream root cause and deduplicates diagnostic payloads', async () => {
    mocks.getRequestErrorDetail.mockResolvedValue({
      id: 1,
      created_at: '2026-08-19T00:00:00Z',
      phase: 'request',
      type: 'upstream_error',
      error_owner: 'provider',
      error_source: 'gateway',
      severity: 'P1',
      status_code: 502,
      upstream_status_code: 429,
      platform: 'openai',
      model: 'gpt-5.6',
      resolved: false,
      request_id: 'rid-1',
      message: 'All available accounts exhausted',
      error_body: '{"error":"same"}',
      upstream_error_message: 'provider rate limit exhausted',
      upstream_error_detail: '{"error":"same"}',
      upstream_errors: '[]',
      account_name: 'account',
      group_name: 'group',
      is_business_limited: false
    })

    const wrapper = shallowMount(OpsErrorDetailModal, {
      props: { show: true, errorId: 1, errorType: 'request' },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /></div>' },
          Icon: true
        }
      }
    })
    await flushPromises()

    expect(wrapper.text()).toContain('provider rate limit exhausted')
    expect(wrapper.text()).toContain('admin.ops.errorDetail.upstreamStatus')
    expect(wrapper.text()).toContain('429')
    expect(wrapper.findAll('pre')).toHaveLength(2)
    expect(wrapper.text()).not.toContain('admin.ops.errorDetail.payloads.upstream_detail')
  })
})

it('loads only the owned observer error and never requests correlated admin details', async () => {
  vi.clearAllMocks()
  mocks.own.mockResolvedValue({ id: 42, status_code: 502, message: 'own error' })
  const wrapper = shallowMount(OpsErrorDetailModal, {
    props: { show: true, errorId: 42, errorType: 'request' },
    global: { provide: { [observerUsageContext as symbol]: true } },
  })
  await flushPromises()
  expect(mocks.own).toHaveBeenCalledWith(42)
  expect(mocks.getRequestErrorDetail).not.toHaveBeenCalled()
  expect(mocks.listRequestErrorUpstreamErrors).not.toHaveBeenCalled()
  wrapper.unmount()
})

it.each(['user', 'upstream'])('explains %s balance failures and preserves diagnostics', async source => {
  vi.clearAllMocks()
  mocks.listRequestErrorUpstreamErrors.mockResolvedValue({ items: [] })
  mocks.getRequestErrorDetail.mockResolvedValue({
    id: 1, status_code: 403, phase: 'request',
    error_owner: source === 'user' ? 'client' : 'provider',
    error_source: source === 'user' ? 'client_request' : 'upstream_http',
    user_id: 7, account_id: source === 'upstream' ? 9 : null,
    message: 'insufficient balance', error_body: '{"error":{"message":"insufficient balance"}}',
  })
  const wrapper = shallowMount(OpsErrorDetailModal, {
    props: { show: true, errorId: 1, errorType: 'request' },
    global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, Icon: true } },
  })
  await flushPromises()
  expect(wrapper.get('[data-testid="balance-source"]').text()).toContain(`admin.ops.balanceError.${source}Hint`)
  expect(wrapper.find('pre').text()).toContain('insufficient balance')
  wrapper.unmount()
})

it('labels HTTP 200 as wire status when the stream failed', async () => {
  vi.clearAllMocks()
  mocks.listRequestErrorUpstreamErrors.mockResolvedValue({ items: [] })
  mocks.getRequestErrorDetail.mockResolvedValue({
    id: 1, status_code: 200, phase: 'network', type: 'transport_error',
    message: 'Upstream connection reset', upstream_status_code: null,
  })
  const wrapper = shallowMount(OpsErrorDetailModal, {
    props: { show: true, errorId: 1, errorType: 'request' },
    global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, Icon: true } },
  })
  await flushPromises()
  expect(wrapper.text()).toContain('usage.errors.detail.clientHttpStatus')
  expect(wrapper.text()).toContain('usage.errors.detail.streamFailedAfterHeaders')
  expect(wrapper.text()).toContain('200')
  wrapper.unmount()
})

it('shows value-free encrypted-history control diagnostics from the error body', async () => {
  vi.clearAllMocks()
  mocks.listRequestErrorUpstreamErrors.mockResolvedValue({ items: [] })
  mocks.getRequestErrorDetail.mockResolvedValue({
    id: 1, status_code: 409, phase: 'request', type: 'invalid_request_error',
    message: 'Encrypted history recovery refused',
    error_body: JSON.stringify({ error: { code: 'encrypted_agent_recovery_wire_changed', controls: { snapshot: 'oauth_replay', tools: 'changed', tool_choice: 'match', previous_response_id_present: false } } }),
  })
  const wrapper = shallowMount(OpsErrorDetailModal, {
    props: { show: true, errorId: 1, errorType: 'request' },
    global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, Icon: true } },
  })
  await flushPromises()
  expect(wrapper.text()).toContain('"snapshot": "oauth_replay"')
  expect(wrapper.text()).toContain('"tools": "changed"')
  expect(wrapper.text()).toContain('"previous_response_id_present": false')
  wrapper.unmount()
})
