const ipGeoMocks = vi.hoisted(() => ({
  getEntry: vi.fn(() => ({ status: 'idle' as const })),
  fetchOne: vi.fn(),
  fetchBatch: vi.fn(),
}))

const appStoreMocks = vi.hoisted(() => ({
  showSuccess: vi.fn(),
  showError: vi.fn(),
  cachedPublicSettings: undefined as { usage_show_long_context_badge?: boolean; usage_show_cache_rate?: boolean; usage_show_cache_rate_to_users?: boolean; usage_show_upstream_response_model_to_users?: boolean } | undefined,
}))

vi.mock('@/utils/ipGeoLookup', () => ipGeoMocks)
vi.mock('@/stores/app', () => ({ useAppStore: () => appStoreMocks }))

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import en from '@/i18n/locales/en/admin/resources'
import zh from '@/i18n/locales/zh/admin/resources'
import UsageTable from '../UsageTable.vue'
import enDashboard from '@/i18n/locales/en/dashboard'
import zhDashboard from '@/i18n/locales/zh/dashboard'
import enTiming from '@/i18n/locales/en/requestTiming'
import zhTiming from '@/i18n/locales/zh/requestTiming'
import enLatencyObservability from '@/i18n/locales/en/latencyObservability'
import zhLatencyObservability from '@/i18n/locales/zh/latencyObservability'

let locale: 'en' | 'zh' = 'en'
const localizedMessages: Record<'en' | 'zh', Record<string, string>> = {
  en: {
    'requestTiming.legacyFirst': enTiming.legacyFirst,
    'requestTiming.legacyTotal': enTiming.legacyTotal,
    'usage.latencyFirstTokenHint': enDashboard.usage.latencyFirstTokenHint,
    'usage.latencyDurationHint': enDashboard.usage.latencyDurationHint,
    'usage.latencyFirstToken': enDashboard.usage.latencyFirstToken,
    'usage.latencyFirstTokenShort': enDashboard.usage.latencyFirstTokenShort,
    'usage.latencyDuration': enDashboard.usage.latencyDuration,
    'usage.latencyDurationShort': enDashboard.usage.latencyDurationShort,
    'usage.latencyTps': enDashboard.usage.latencyTps,
    'usage.latencyCacheRate': enDashboard.usage.latencyCacheRate,
    'latencyObservability.reasoning_first_ms': enLatencyObservability.reasoning_first_ms,
    'latencyObservability.visible_text_first_ms': enLatencyObservability.visible_text_first_ms,
    'latencyObservability.response_byte_first_ms': enLatencyObservability.response_byte_first_ms,
    'latencyObservability.typedScope': enLatencyObservability.typedScope,
    'admin.usage.longContext': en.usage.longContext,
    'admin.usage.longContextPricingTooltip': en.usage.longContextPricingTooltip,
  },
  zh: {
    'requestTiming.legacyFirst': zhTiming.legacyFirst,
    'requestTiming.legacyTotal': zhTiming.legacyTotal,
    'usage.latencyFirstTokenHint': zhDashboard.usage.latencyFirstTokenHint,
    'usage.latencyDurationHint': zhDashboard.usage.latencyDurationHint,
    'usage.latencyFirstToken': zhDashboard.usage.latencyFirstToken,
    'usage.latencyFirstTokenShort': zhDashboard.usage.latencyFirstTokenShort,
    'usage.latencyDuration': zhDashboard.usage.latencyDuration,
    'usage.latencyDurationShort': zhDashboard.usage.latencyDurationShort,
    'usage.latencyTps': zhDashboard.usage.latencyTps,
    'usage.latencyCacheRate': zhDashboard.usage.latencyCacheRate,
    'latencyObservability.reasoning_first_ms': zhLatencyObservability.reasoning_first_ms,
    'latencyObservability.visible_text_first_ms': zhLatencyObservability.visible_text_first_ms,
    'latencyObservability.response_byte_first_ms': zhLatencyObservability.response_byte_first_ms,
    'latencyObservability.typedScope': zhLatencyObservability.typedScope,
    'admin.usage.longContext': zh.usage.longContext,
    'admin.usage.longContextPricingTooltip': zh.usage.longContextPricingTooltip,
  },
}

const messages: Record<string, string> = {
  'usage.latencyTps': 'Avg TPS',
  'usage.latencyCacheRate': 'Cache rate',
  'usage.latencyCacheRateHint': 'cache rate hint',
  'admin.usage.userDeletedBadge': 'Deleted',
  'usage.costDetails': 'Cost Breakdown',
  'admin.usage.inputCost': 'Input Cost',
  'admin.usage.outputCost': 'Output Cost',
  'admin.usage.cacheCreationCost': 'Cache Creation Cost',
  'admin.usage.cacheReadCost': 'Cache Read Cost',
  'usage.inputTokenPrice': 'Input price',
  'usage.outputTokenPrice': 'Output price',
  'usage.perMillionTokens': '/ 1M tokens',
  'usage.serviceTier': 'Service tier',
  'usage.serviceTierPriority': 'Fast',
  'usage.serviceTierUltrafast': 'Ultrafast',
  'usage.serviceTierFlex': 'Flex',
  'usage.serviceTierStandard': 'Standard',
  'usage.rate': 'Rate',
  'usage.accountMultiplier': 'Account rate',
  'usage.original': 'Original',
  'usage.userBilled': 'User billed',
  'usage.accountBilled': 'Account billed',
  'usage.imageUnit': ' images',
  'usage.imageCount': 'Image count',
  'usage.imageBillingSize': 'Billing size',
  'usage.imageInputSize': 'Input size',
  'usage.imageOutputSize': 'Output size',
  'usage.imageSizeSource': 'Size source',
  'usage.imageSizeBreakdown': 'Size breakdown',
  'usage.imageSizeSourceOutput': 'Upstream output',
  'usage.imageSizeSourceInput': 'Request input',
  'usage.imageSizeSourceDefault': 'Default billing tier',
  'usage.imageSizeSourceLegacy': 'Legacy record',
  'usage.imageSizeSourceMissing': 'Not recorded',
  'usage.imageSizeNotRecorded': 'not recorded',
  'usage.imageSizeLegacyUnstandardized': 'legacy unstandardized',
  'usage.imageSizeUnknown': 'unknown',
  'usage.imageUnitPrice': 'Per-image price',
  'usage.imageTotalPrice': 'Image total price',
  'usage.stream': 'Stream',
  'usage.sync': 'Sync',
  'usage.nativeCompactionV2': 'Compaction',
  'admin.usage.billingModeToken': 'Token',
  'admin.usage.billingModePerRequest': 'Per request',
  'admin.usage.billingModeImage': 'Image',
	'admin.usage.requestIdCopied': 'Request ID copied',
	'admin.usage.upstreamRequestIdCopied': 'Upstream ID copied',
	'keys.copied': 'Copied',
	'keys.copyToClipboard': 'Copy to clipboard',
	'common.copyFailed': 'Copy failed',
	'usage.requestedModel': 'Requested',
	'usage.sentUpstreamModel': 'Sent upstream',
	'usage.upstreamResponseModel': 'Upstream response',
	'usage.modelVariant': 'Possible version variant',
	'usage.modelMismatch': 'Different model',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) => {
        const message = localizedMessages[locale][key] ?? messages[key] ?? key
        return Object.entries(params ?? {}).reduce((text, [name, value]) => text.replaceAll(`{${name}}`, String(value)), message)
      },
    }),
  }
})

const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.request_id" :data-request-id="row.request_id">
        <slot name="cell-model" :row="row" :value="row.model" />
        <slot name="cell-reasoning_effort" :row="row" :value="row.reasoning_effort" />
        <slot name="cell-billing_mode" :row="row" />
        <slot name="cell-tokens" :row="row" />
        <slot name="cell-latency" :row="row" />
        <slot name="cell-cost" :row="row" />
        <slot name="cell-request_id" :row="row" />
        <slot name="cell-upstream_request_id" :row="row" />
      </div>
    </div>
  `,
}

const baseImageRow = {
  request_id: 'req-admin-image',
  model: 'gpt-image-2',
  actual_cost: 0.4,
  total_cost: 0.4,
  account_rate_multiplier: 1,
  rate_multiplier: 1,
  service_tier: null,
  input_cost: 0,
  output_cost: 0,
  cache_creation_cost: 0,
  cache_read_cost: 0,
  input_tokens: 0,
  output_tokens: 0,
  cache_creation_tokens: 0,
  cache_read_tokens: 0,
  cache_creation_5m_tokens: 0,
  cache_creation_1h_tokens: 0,
  cache_ttl_overridden: false,
  billing_mode: 'image',
  image_count: 2,
  image_size: '2K',
  image_input_size: null,
  image_output_size: null,
  image_size_source: null,
  image_size_breakdown: null,
}

describe('admin UsageTable tooltip', () => {
  it('shows output TPS in the latency cell using total duration', () => {
    const row = { ...baseImageRow, image_count: 0, billing_mode: 'token', output_tokens: 1000, duration_ms: 20_000, first_token_ms: 10_000 }
    const wrapper = mount(UsageTable, {
      props: {
        data: [row, { ...row, request_id: 'no-duration', duration_ms: null }, { ...row, request_id: 'image', image_count: 1 }],
        loading: false,
        columns: [{ key: 'latency', label: 'Latency' }],
      },
      global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } },
    })
    expect(wrapper.findAll('[data-testid="latency-tps"]').map(cell => cell.text())).toEqual(['50.0 t/s', '-', '-'])
    expect(wrapper.text()).toContain('Avg TPS')
  })

  it('shows queue-inclusive first token and labels the queue portion', () => {
    const row = { ...baseImageRow, image_count: 0, billing_mode: 'token', output_tokens: 100, duration_ms: 20_000, first_token_ms: 2_000, queue_wait_ms: 30_000,
      queue_timing: { status: 'measured', source: 'ws_frame_ctx_pool', scope: 'pre_final_forward', queue_clamped: false,
        server_first_token_ms: 32_000, server_duration_ms: 50_000, forward_first_token_ms: 2_000, forward_duration_ms: 20_000 } }
    const wrapper = mount(UsageTable, {
      props: { data: [row], loading: false, columns: [{ key: 'latency', label: 'Latency' }] },
      global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } },
    })
    expect(wrapper.text()).toContain('32.00s')
    expect(wrapper.text()).toContain('50.00s')
    expect(wrapper.text()).toContain('30.00s')
    expect(wrapper.find('[data-testid="latency-e2e-uncollected"]').exists()).toBe(false)
  })

  it('PR610 keeps passthrough overlap out of the server total and retains legacy TPS', () => {
    const row = { ...baseImageRow, image_count: 0, billing_mode: 'token', output_tokens: 100, first_token_ms: 32_000, duration_ms: 50_000, queue_wait_ms: 30_000,
      queue_timing: { status: 'measured', source: 'ws_frame_passthrough', scope: 'accepted_frame_including_admission', queue_clamped: false,
        server_first_token_ms: 32_000, server_duration_ms: 50_000, forward_first_token_ms: 2_000, forward_duration_ms: 20_000 } }
    const wrapper = mount(UsageTable, { props: { data: [row], loading: false, columns: [{ key: 'latency', label: 'Latency' }] }, global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } } })
    expect(wrapper.text()).toContain('32.00s')
    expect(wrapper.text()).toContain('50.00s')
    expect(wrapper.text()).not.toContain('62.00s')
    expect(wrapper.get('[data-testid="latency-tps"]').text()).toBe('2.0 t/s')
    expect(wrapper.find('[data-testid="latency-e2e-uncollected"]').exists()).toBe(false)
    wrapper.unmount()
  })

  beforeEach(() => {
    appStoreMocks.cachedPublicSettings = undefined
    locale = 'en'
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
      x: 0,
      y: 0,
      top: 20,
      left: 20,
      right: 120,
      bottom: 40,
      width: 100,
      height: 20,
      toJSON: () => ({}),
    } as DOMRect)
  })

  it.each([
    ['en', 'Long context', 'Long-context pricing was applied. Input and output rates depend on the pricing tier, not a uniform multiplier.'],
    ['zh', '长上下文', '已应用长上下文计费。输入和输出费率取决于定价档位，并非统一倍率。'],
  ] as const)('marks only applied long-context billing with localized text in %s', (language, label, tooltip) => {
    locale = language
    const wrapper = mount(UsageTable, {
      props: {
        data: [
          {
            ...baseImageRow,
            request_id: 'req-long-context-enabled',
            long_context_billing_applied: true,
          },
          {
            ...baseImageRow,
            request_id: 'req-long-context-disabled',
            long_context_billing_applied: false,
          },
          {
            ...baseImageRow,
            request_id: 'req-long-context-absent',
          },
        ],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.findAll('[data-testid="long-context-billing-marker"]')).toHaveLength(1)
    const marker = wrapper.get('[data-request-id="req-long-context-enabled"] [data-testid="long-context-billing-marker"]')
    expect(marker.text()).toBe(label)
    expect(marker.attributes('title')).toBe(tooltip)
    expect(wrapper.find('[data-request-id="req-long-context-disabled"] [data-testid="long-context-billing-marker"]').exists()).toBe(false)
    expect(wrapper.find('[data-request-id="req-long-context-absent"] [data-testid="long-context-billing-marker"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('x2')
  })

  it('hides the long-context billing marker when the public setting is disabled', () => {
    appStoreMocks.cachedPublicSettings = { usage_show_long_context_badge: false }
    const wrapper = mount(UsageTable, {
      props: {
        data: [
          {
            ...baseImageRow,
            request_id: 'req-long-context-hidden',
            long_context_billing_applied: true,
          },
        ],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.findAll('[data-testid="long-context-billing-marker"]')).toHaveLength(0)
  })

  it('keeps the request type badge and adds a separate badge only for native compaction rows', () => {
    const DataTableStreamStub = {
      props: ['data'],
      template: `
        <div>
          <div v-for="row in data" :key="row.request_id">
            <slot name="cell-stream" :row="row" />
          </div>
        </div>
      `,
    }
    const wrapper = mount(UsageTable, {
      props: {
        data: [
          {
            ...baseImageRow,
            request_id: 'req-compaction-stream',
            request_type: 'stream',
            stream: true,
            native_compaction_v2: true,
          },
          {
            ...baseImageRow,
            request_id: 'req-historical-sync',
            request_type: 'sync',
            stream: false,
            native_compaction_v2: false,
          },
        ],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStreamStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const requestBadges = wrapper.findAll('[data-testid="request-type-badge"]')
    expect(requestBadges).toHaveLength(2)
    expect(requestBadges[0].text()).toBe('Stream')
    expect(requestBadges[1].text()).toBe('Sync')
    expect(wrapper.findAll('[data-testid="native-compaction-badge"]')).toHaveLength(1)
    expect(wrapper.get('[data-testid="native-compaction-badge"]').text()).toBe('Compaction')
  })

  it.each([
    [0, '0.0x'],
    [0.5, '0.50x'],
    [undefined, '1.00x'],
  ])('shows the stored user rate %s in cost details', async (rate, expected) => {
    const wrapper = mount(UsageTable, {
      props: { data: [{ ...baseImageRow, rate_multiplier: rate }], loading: false, columns: [] },
      global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } },
    })
    const triggers = wrapper.findAll('.group.relative')
    await triggers[triggers.length - 1].trigger('mouseenter')
    const rateLabel = wrapper.get('.fixed').findAll('span').find(span => span.text() === 'Rate')!
    expect(rateLabel.element.parentElement?.textContent).toContain(expected)
    wrapper.unmount()
  })

  it('shows service tier and billing breakdown in cost tooltip', async () => {
    const row = {
      request_id: 'req-admin-1',
      actual_cost: 0.092883,
      total_cost: 0.092883,
      account_rate_multiplier: 1,
      rate_multiplier: 1,
      service_tier: 'priority',
      input_cost: 0.020285,
      output_cost: 0.00303,
      cache_creation_cost: 0,
      cache_read_cost: 0.069568,
      input_tokens: 4057,
      output_tokens: 101,
    }

    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const tooltipTriggers = wrapper.findAll('.group.relative')
    await tooltipTriggers[tooltipTriggers.length - 1].trigger('mouseenter')
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('Service tier')
    expect(text).toContain('Fast')
    expect(text).toContain('Rate')
    expect(text).toContain('1.00x')
    expect(text).toContain('Account rate')
    expect(text).toContain('User billed')
    expect(text).toContain('Account billed')
    expect(text).toContain('$0.092883')
    expect(text).toContain('$5.0000 / 1M tokens')
    expect(text).toContain('$30.0000 / 1M tokens')
    expect(text).toContain('$0.069568')
  })

  it.each(['token', 'image', 'per_request'])('keeps eight decimal places in %s cost details', async (billingMode) => {
    const row = {
      ...baseImageRow,
      billing_mode: billingMode,
      image_count: billingMode === 'image' ? 2 : 0,
      input_cost: 0.00000001,
      image_input_cost: 0.00000002,
      output_cost: 0.00000003,
      image_output_cost: 0.00000004,
      cache_creation_cost: 0.00000005,
      cache_read_cost: 0.00000006,
      total_cost: 0.00000022,
      actual_cost: 0.00000042,
      account_stats_cost: 0.00000012,
      account_rate_multiplier: 1.5,
    }
    const wrapper = mount(UsageTable, {
      props: { data: [row], loading: false, columns: [] },
      global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } },
    })
    const triggers = wrapper.findAll('.group.relative')
    await triggers[triggers.length - 1].trigger('mouseenter')
    const amounts = wrapper.get('.fixed').findAll('span').map(span => span.text())
    expect(amounts).toEqual(expect.arrayContaining([
      '$0.00000001', '$0.00000002', '$0.00000003', '$0.00000004',
      '$0.00000005', '$0.00000006', '$0.00000022', '$0.00000042', '$0.00000018',
    ]))
    if (billingMode === 'image') expect(amounts).toContain('$0.00000011')
    wrapper.unmount()
  })

  it('uses eight decimal places for missing cost values', async () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{ ...baseImageRow, billing_mode: 'per_request', image_count: 0, total_cost: undefined, actual_cost: undefined }],
        loading: false,
        columns: [],
      },
      global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } },
    })
    const triggers = wrapper.findAll('.group.relative')
    await triggers[triggers.length - 1].trigger('mouseenter')
    const amounts = wrapper.get('.fixed').findAll('span').map(span => span.text()).filter(text => text.startsWith('$'))
    expect(amounts).toEqual(['$0.00000000', '$0.00000000', '$0.00000000', '$0.00000000'])
    wrapper.unmount()
  })

  it('shows requested and upstream models separately for admin rows', () => {
    const row = {
      request_id: 'req-admin-model-1',
      model: 'claude-sonnet-4',
      upstream_model: 'claude-sonnet-4-20250514',
      actual_cost: 0,
      total_cost: 0,
      account_rate_multiplier: 1,
      rate_multiplier: 1,
      input_cost: 0,
      output_cost: 0,
      cache_creation_cost: 0,
      cache_read_cost: 0,
      input_tokens: 0,
      output_tokens: 0,
    }

    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const text = wrapper.text()
    expect(text).toContain('claude-sonnet-4')
    expect(text).toContain('claude-sonnet-4-20250514')
  })

  it('shows requested and forwarded reasoning effort separately when they differ', () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          request_id: 'req-admin-effort-1',
          model: 'gpt-5.4',
          reasoning_effort: 'max',
          upstream_reasoning_effort: 'xhigh',
        }],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const text = wrapper.text()
    expect(text).toContain('Max')
    expect(text).toContain('XHigh')
    expect(text).toContain('↳')
  })

  it('shows a single reasoning effort when requested matches forwarded', () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          request_id: 'req-admin-effort-2',
          model: 'gpt-5.6-sol',
          reasoning_effort: 'max',
        }],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    const text = wrapper.text()
    expect(text).toContain('Max')
    expect(text).not.toContain('↳')
  })

  it('hides mapped reasoning effort for user rows that only have the requested value', () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{
          request_id: 'req-user-effort-1',
          model: 'gpt-5.4',
          reasoning_effort: 'max',
        }],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.text()).toContain('Max')
    expect(wrapper.text()).not.toContain('XHigh')
    expect(wrapper.text()).not.toContain('↳')
  })

	it.each([
		{
			name: 'possible version variant',
			responseModel: 'gpt-5.5-2026-08-01',
			expectedBadge: 'Possible version variant',
		},
		{
			name: 'different upstream model',
			responseModel: 'gpt-5.4',
			expectedBadge: 'Different model',
		},
	])('shows a compact upstream response audit marker for $name', ({ responseModel, expectedBadge }) => {
		const wrapper = mount(UsageTable, {
			props: {
				data: [{
					request_id: `req-${responseModel}`,
					model: 'gpt-5.6-sol',
					upstream_model: 'gpt-5.5',
					model_mapping_chain: 'gpt-5.6-sol→gpt-5.5',
					upstream_response_model: responseModel,
					upstream_model_mismatch: true,
				}],
				loading: false,
				columns: [],
			},
			global: {
				stubs: {
					DataTable: DataTableStub,
					EmptyState: true,
					Icon: true,
					Teleport: true,
				},
			},
		})

		const text = wrapper.text()
		expect(text).toContain('gpt-5.6-sol')
		expect(text).toContain('gpt-5.5')
		expect(text).toContain(responseModel)
		expect(text).toContain(expectedBadge)
		expect(wrapper.find('[data-testid="user-upstream-response-model"]').exists()).toBe(false)
	})

	it('shows the upstream response model to users only when the admin setting is on', () => {
		const row = {
			request_id: 'req-user-response-model',
			model: 'gpt-5.4',
			upstream_response_model: 'gpt-5.4-actual',
		}
		appStoreMocks.cachedPublicSettings = undefined
		const hidden = mount(UsageTable, {
			props: { data: [row], loading: false, columns: [], cacheRateAudience: 'user' },
			global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } },
		})
		expect(hidden.find('[data-testid="user-upstream-response-model"]').exists()).toBe(false)

		appStoreMocks.cachedPublicSettings = { usage_show_upstream_response_model_to_users: true }
		const shown = mount(UsageTable, {
			props: { data: [row], loading: false, columns: [], cacheRateAudience: 'user' },
			global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } },
		})
		expect(shown.find('[data-testid="user-upstream-response-model"]').text()).toContain('gpt-5.4-actual')
		expect(shown.text()).not.toContain('Different model')
		appStoreMocks.cachedPublicSettings = undefined
	})

  it.each([
    {
      name: 'defaulted row',
      row: {
        ...baseImageRow,
        request_id: 'req-admin-default-image',
        image_size: '2K',
        image_input_size: 'auto',
        image_output_size: null,
        image_size_source: 'default',
      },
      expected: ['2K', 'Default billing tier', 'auto', 'unknown'],
    },
    {
      name: 'output-sourced row',
      row: {
        ...baseImageRow,
        request_id: 'req-admin-output-image',
        image_size: '4K',
        image_input_size: '1024x1024',
        image_output_size: '3840x2160',
        image_size_source: 'output',
        image_size_breakdown: { '4K': 1 },
      },
      expected: ['4K', 'Upstream output', '1024x1024', '3840x2160', '4K x 1'],
    },
    {
      name: 'input-sourced row',
      row: {
        ...baseImageRow,
        request_id: 'req-admin-input-image',
        image_size: '1K',
        image_input_size: '1024x1024',
        image_output_size: null,
        image_size_source: 'input',
      },
      expected: ['1K', 'Request input', '1024x1024', 'unknown'],
    },
    {
      name: 'legacy unstandardized row',
      row: {
        ...baseImageRow,
        request_id: 'req-admin-legacy-unstandardized-image',
        image_size: '512x512',
        image_input_size: null,
        image_output_size: null,
        image_size_source: null,
      },
      expected: ['legacy unstandardized: 512x512', 'Legacy record', 'unknown'],
    },
  ])('shows image usage metadata for $name', async ({ row, expected }) => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    await wrapper.find('.group.relative').trigger('mouseenter')
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('Image count')
    expect(text).toContain('Billing size')
    expect(text).toContain('Size source')
    expect(text).toContain('Input size')
    expect(text).toContain('Output size')
    expect(text).toContain('Per-image price')
    expect(text).toContain('Image total price')
    for (const value of expected) {
      expect(text).toContain(value)
    }
  })

  it('displays historical image rows with missing billing_mode as image usage without a 2K fallback', async () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [
          {
            ...baseImageRow,
            request_id: 'req-admin-legacy-missing-image',
            billing_mode: null,
            image_size: null,
            image_input_size: null,
            image_output_size: null,
            image_size_source: null,
            image_size_breakdown: null,
          },
        ],
        loading: false,
        columns: [],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    await wrapper.find('.group.relative').trigger('mouseenter')
    await nextTick()

    const text = wrapper.text()
    expect(text).toContain('Image')
    expect(text).toContain('Image count')
    expect(text).toContain('Per-image price')
    expect(text).toContain('not recorded')
    expect(text).not.toContain('(2K)')
  })
})

describe('admin UsageTable request ID column', () => {
  beforeEach(() => {
    appStoreMocks.showSuccess.mockReset()
    appStoreMocks.showError.mockReset()
  })

  it('renders and copies the request ID', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })

    const wrapper = mount(UsageTable, {
      props: {
        data: [{ ...baseImageRow, request_id: 'req-admin-visible-id' }],
        loading: false,
        columns: [{ key: 'request_id', label: 'Request ID' }],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.text()).toContain('req-admin-visible-id')
    await wrapper.get('button[title="Copy to clipboard"]').trigger('click')

    expect(writeText).toHaveBeenCalledWith('req-admin-visible-id')
    expect(appStoreMocks.showSuccess).toHaveBeenCalledWith('Request ID copied')
  })

  it('renders and copies the upstream ID', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })

    const wrapper = mount(UsageTable, {
      props: {
        data: [{ ...baseImageRow, request_id: '', upstream_request_id: '20260903082826779695' }],
        loading: false,
        columns: [{ key: 'upstream_request_id', label: 'Upstream ID' }],
      },
      global: {
        stubs: {
          DataTable: DataTableStub,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.text()).toContain('20260903082826779695')
    const copyButtons = wrapper.findAll('button[title="Copy to clipboard"]')
    expect(copyButtons).toHaveLength(1)
    await copyButtons[0].trigger('click')

    expect(writeText).toHaveBeenCalledWith('20260903082826779695')
    expect(appStoreMocks.showSuccess).toHaveBeenCalledWith('Upstream ID copied')
  })
})

describe('admin UsageTable IP geolocation batch toolbar', () => {
  const DataTableStubWithIp = {
    props: ['data'],
    template: `
      <div>
        <div v-for="row in data" :key="row.request_id">
          <slot name="cell-ip_address" :row="row" />
        </div>
      </div>
    `,
  }

  beforeEach(() => {
    ipGeoMocks.getEntry.mockReset()
    ipGeoMocks.fetchOne.mockReset()
    ipGeoMocks.fetchBatch.mockReset()
    ipGeoMocks.getEntry.mockReturnValue({ status: 'idle' })
  })

  it('does not render the batch toolbar when the ip_address column is not visible', () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [{ request_id: 'r1', ip_address: '8.8.8.8' }],
        loading: false,
        columns: [],
      },
      global: { stubs: { DataTable: DataTableStubWithIp, EmptyState: true, Teleport: true } },
    })
    expect(wrapper.text()).not.toContain('usage.ipGeo.batchFetch')
  })

  it('renders the batch toolbar with a pending count when the ip_address column is visible', () => {
    const wrapper = mount(UsageTable, {
      props: {
        data: [
          { request_id: 'r1', ip_address: '8.8.8.8' },
          { request_id: 'r2', ip_address: '8.8.8.8' },
          { request_id: 'r3', ip_address: '1.1.1.1' },
        ],
        loading: false,
        columns: [{ key: 'ip_address', label: 'IP' }],
      },
      global: { stubs: { DataTable: DataTableStubWithIp, EmptyState: true, Teleport: true } },
    })
    expect(wrapper.text()).toContain('usage.ipGeo.pending')
    const button = wrapper.find('button')
    expect(button.exists()).toBe(true)
    expect((button.element as HTMLButtonElement).disabled).toBe(false)
  })

  it('fetches deduplicated IPs from the current page when the batch button is clicked', async () => {
    ipGeoMocks.fetchBatch.mockResolvedValue(true)
    const wrapper = mount(UsageTable, {
      props: {
        data: [
          { request_id: 'r1', ip_address: '8.8.8.8' },
          { request_id: 'r2', ip_address: '8.8.8.8' },
          { request_id: 'r3', ip_address: '1.1.1.1' },
        ],
        loading: false,
        columns: [{ key: 'ip_address', label: 'IP' }],
      },
      global: { stubs: { DataTable: DataTableStubWithIp, EmptyState: true, Teleport: true } },
    })
    await wrapper.find('button').trigger('click')
    expect(ipGeoMocks.fetchBatch).toHaveBeenCalledWith(['8.8.8.8', '1.1.1.1'])
    expect(wrapper.emitted('ipGeoBatchFailed')).toBeUndefined()
  })

  it('emits ipGeoBatchFailed when the batch request reports a network-level failure', async () => {
    ipGeoMocks.fetchBatch.mockResolvedValue(false)
    const wrapper = mount(UsageTable, {
      props: {
        data: [{ request_id: 'r1', ip_address: '8.8.8.8' }],
        loading: false,
        columns: [{ key: 'ip_address', label: 'IP' }],
      },
      global: { stubs: { DataTable: DataTableStubWithIp, EmptyState: true, Teleport: true } },
    })
    await wrapper.find('button').trigger('click')
    expect(wrapper.emitted('ipGeoBatchFailed')).toHaveLength(1)
  })

  it('renders IpGeoCell content for ip_address cells', () => {
    ipGeoMocks.getEntry.mockReturnValue({ status: 'success', label: 'CN · Guangdong · Shenzhen', detail: {} })
    const wrapper = mount(UsageTable, {
      props: {
        data: [{ request_id: 'r1', ip_address: '121.35.47.43' }],
        loading: false,
        columns: [{ key: 'ip_address', label: 'IP' }],
      },
      global: { stubs: { DataTable: DataTableStubWithIp, EmptyState: true, Teleport: true } },
    })
    expect(wrapper.text()).toContain('121.35.47.43')
    expect(wrapper.text()).toContain('CN · Guangdong · Shenzhen')
  })
})

// A DataTable stub that also renders cell-user, so the deleted badge can be asserted.
const DataTableStubWithUser = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.request_id">
        <slot name="cell-user" :row="row" />
        <slot name="cell-model" :row="row" :value="row.model" />
        <slot name="cell-reasoning_effort" :row="row" :value="row.reasoning_effort" />
        <slot name="cell-billing_mode" :row="row" />
        <slot name="cell-tokens" :row="row" />
        <slot name="cell-cost" :row="row" />
      </div>
    </div>
  `,
}

describe('admin UsageTable deleted-user badge', () => {
  it('renders deleted badge for a soft-deleted user row', () => {
    const row = {
      request_id: 'req-deleted-user-1',
      model: 'claude-3',
      user_id: 2,
      user: { id: 2, email: 'd@test.com', deleted_at: '2026-05-28T00:00:00Z' },
      actual_cost: 0,
      total_cost: 0,
      input_cost: 0,
      output_cost: 0,
      rate_multiplier: 1,
      input_tokens: 1,
      output_tokens: 1,
    }

    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [{ key: 'user', label: 'User' }],
      },
      global: {
        stubs: {
          DataTable: DataTableStubWithUser,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.text()).toContain('Deleted')
    expect(wrapper.text()).toContain('d@test.com')
  })

  it('does NOT render deleted badge for an active user row', () => {
    const row = {
      request_id: 'req-active-user-1',
      model: 'claude-3',
      user_id: 3,
      user: { id: 3, email: 'active@test.com', deleted_at: null },
      actual_cost: 0,
      total_cost: 0,
      input_cost: 0,
      output_cost: 0,
      rate_multiplier: 1,
      input_tokens: 1,
      output_tokens: 1,
    }

    const wrapper = mount(UsageTable, {
      props: {
        data: [row],
        loading: false,
        columns: [{ key: 'user', label: 'User' }],
      },
      global: {
        stubs: {
          DataTable: DataTableStubWithUser,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

    expect(wrapper.text()).not.toContain('Deleted')
    expect(wrapper.text()).toContain('active@test.com')
  })
})

const DataTableStubWithLatency = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.request_id" :data-row="row.request_id">
        <slot name="cell-latency" :row="row" />
      </div>
    </div>
  `,
}

const DataTableStubWithContentTiming = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.request_id" :data-row="row.request_id">
        <slot name="cell-content_reasoning_first_ms" :row="row" />
        <slot name="cell-content_visible_text_first_ms" :row="row" />
        <slot name="cell-content_response_byte_first_ms" :row="row" />
      </div>
    </div>
  `,
}

describe('admin UsageTable latency TPS', () => {
  const mountLatency = (data: Record<string, unknown>[], extra: Record<string, unknown> = {}) =>
    mount(UsageTable, {
      props: {
        data: data as any,
        loading: false,
        columns: [{ key: 'latency', label: 'Latency' }],
        ...extra,
      },
      global: {
        stubs: {
          DataTable: DataTableStubWithLatency,
          EmptyState: true,
          Icon: true,
          Teleport: true,
        },
      },
    })

  const tpsCell = (wrapper: ReturnType<typeof mountLatency>, requestId: string) =>
    wrapper.find(`[data-row="${requestId}"] [data-testid="latency-tps"]`)

  const barClasses = (wrapper: ReturnType<typeof mountLatency>, requestId: string) =>
    wrapper.find(`[data-row="${requestId}"] [data-testid="latency-bar"]`).classes()

  it('shows average output throughput over the total duration for streaming rows', () => {
    const wrapper = mountLatency([
      { request_id: 'req-tps-stream', output_tokens: 872, duration_ms: 31_260, first_token_ms: 2_910 },
    ])

    expect(wrapper.text()).toContain('Avg TPS')
    const cell = tpsCell(wrapper, 'req-tps-stream')
    expect(cell.text()).toBe('27.9 t/s')
    expect(cell.attributes('title')).toBe('usage.latencyTpsHint')
    expect(cell.classes()).toContain('text-emerald-600')
  })

  it('colors the TPS text and the bottom bar segment red below 10 t/s and yellow below 20 t/s', () => {
    const wrapper = mountLatency([
      // first token 12s (warn), total 17s (good), 1.5 t/s (critical)
      { request_id: 'req-tps-slow', output_tokens: 25, duration_ms: 17_000, first_token_ms: 12_000 },
      // first token 2s (good), total 12s (good), 12.5 t/s (warn)
      { request_id: 'req-tps-mid', output_tokens: 150, duration_ms: 12_000, first_token_ms: 2_000 },
    ])

    expect(tpsCell(wrapper, 'req-tps-slow').text()).toBe('1.5 t/s')
    expect(tpsCell(wrapper, 'req-tps-slow').classes()).toContain('text-red-600')
    expect(barClasses(wrapper, 'req-tps-slow')).toEqual(
      expect.arrayContaining(['from-amber-400', 'via-emerald-500', 'to-red-500']),
    )

    expect(tpsCell(wrapper, 'req-tps-mid').text()).toBe('12.5 t/s')
    expect(tpsCell(wrapper, 'req-tps-mid').classes()).toContain('text-amber-600')
    expect(barClasses(wrapper, 'req-tps-mid')).toEqual(
      expect.arrayContaining(['from-emerald-500', 'via-emerald-500', 'to-amber-400']),
    )
  })

  it('lets bar segments without first-token or TPS data follow the total-duration color', () => {
    const wrapper = mountLatency([
      // no first token, total 70s (warn), 1400 tokens / 70s = 20 t/s (good)
      { request_id: 'req-bar-sync', output_tokens: 1_400, duration_ms: 70_000, first_token_ms: null },
      // image row: no first token and no TPS, total 40s (good)
      { ...baseImageRow, request_id: 'req-bar-image', duration_ms: 40_000, first_token_ms: null },
    ])

    expect(barClasses(wrapper, 'req-bar-sync')).toEqual(
      expect.arrayContaining(['from-amber-400', 'via-amber-400', 'to-emerald-500']),
    )
    expect(barClasses(wrapper, 'req-bar-image')).toEqual(
      expect.arrayContaining(['from-emerald-500', 'via-emerald-500', 'to-emerald-500']),
    )
  })

  it('uses the same average and hint when first token is missing', () => {
    const wrapper = mountLatency([
      { request_id: 'req-tps-sync', output_tokens: 500, duration_ms: 10_000, first_token_ms: null },
    ])

    const cell = tpsCell(wrapper, 'req-tps-sync')
    expect(cell.text()).toBe('50.0 t/s')
    expect(cell.attributes('title')).toBe('usage.latencyTpsHint')
  })

  it('keeps buffered and terminal-only output averages and health colors meaningful', () => {
    const wrapper = mountLatency([
      { request_id: 'req-tps-buffered', output_tokens: 1_095, duration_ms: 9_250, first_token_ms: 9_240 },
      { request_id: 'req-tps-terminal', output_tokens: 73, duration_ms: 6_761, first_token_ms: 6_760 },
      { request_id: 'req-tps-same-ms', output_tokens: 73, duration_ms: 6_760, first_token_ms: 6_760 },
    ])

    expect(tpsCell(wrapper, 'req-tps-buffered').text()).toBe('118 t/s')
    for (const requestId of ['req-tps-terminal', 'req-tps-same-ms']) {
      expect(tpsCell(wrapper, requestId).text()).toBe('10.8 t/s')
      expect(tpsCell(wrapper, requestId).attributes('title')).toBe('usage.latencyTpsHint')
      expect(tpsCell(wrapper, requestId).classes()).toContain('text-amber-600')
      expect(barClasses(wrapper, requestId)).toContain('to-amber-400')
    }
  })

  it('renders a placeholder when TPS cannot be computed', () => {
    const wrapper = mountLatency([
      { request_id: 'req-tps-empty', output_tokens: 0, duration_ms: 1_200, first_token_ms: 300 },
      { request_id: 'req-tps-interrupted', output_tokens: 1, duration_ms: 21_135, first_token_ms: 973 },
      { ...baseImageRow, request_id: 'req-tps-image', duration_ms: 40_000, first_token_ms: null },
    ])

    expect(tpsCell(wrapper, 'req-tps-empty').text()).toBe('-')
    expect(tpsCell(wrapper, 'req-tps-empty').attributes('title')).toBeUndefined()
    expect(tpsCell(wrapper, 'req-tps-interrupted').text()).toBe('-')
    expect(tpsCell(wrapper, 'req-tps-interrupted').classes()).not.toContain('text-red-600')
    expect(barClasses(wrapper, 'req-tps-interrupted')).toEqual(
      expect.arrayContaining(['from-emerald-500', 'via-emerald-500', 'to-emerald-500']),
    )
    expect(tpsCell(wrapper, 'req-tps-image').text()).toBe('-')
  })

  it('shows that request cache rate beside first token, duration, and TPS', () => {
    const wrapper = mountLatency([
      { request_id: 'req-cache-hit', input_tokens: 4_057, cache_read_tokens: 278_272, cache_creation_tokens: 4, output_tokens: 101, duration_ms: 345, first_token_ms: 12 },
      { request_id: 'req-cache-miss', input_tokens: 1_200, cache_read_tokens: 0, cache_creation_tokens: 0, output_tokens: 20, duration_ms: 1_000, first_token_ms: 100 },
      { request_id: 'req-cache-empty', input_tokens: 0, cache_read_tokens: 0, cache_creation_tokens: 0, output_tokens: 20, duration_ms: 1_000, first_token_ms: 100 },
    ])

    const hit = wrapper.find('[data-row="req-cache-hit"] [data-testid="latency-cache-rate"]')
    expect(wrapper.text()).toContain('Cache rate')
    expect(hit.text()).toBe('98.6%')
    expect(hit.attributes('title')).toBe('cache rate hint')
    expect(hit.classes()).toContain('text-emerald-600')
    expect(wrapper.find('[data-row="req-cache-miss"] [data-testid="latency-cache-rate"]').classes()).toContain('text-emerald-600')
    expect(wrapper.find('[data-row="req-cache-miss"] [data-testid="latency-cache-rate"]').text()).toBe('0%')
    expect(wrapper.find('[data-row="req-cache-empty"] [data-testid="latency-cache-rate"]').text()).toBe('-')
    expect(wrapper.find('[data-row="req-cache-empty"] [data-testid="latency-cache-rate"]').attributes('title')).toBeUndefined()
    const bar = wrapper.find('[data-row="req-cache-hit"] [data-testid="latency-bar"]')
    expect(bar.classes()).toEqual(expect.arrayContaining(['row-span-4', 'from-emerald-500', 'via-emerald-500', 'to-emerald-500']))
    expect(bar.attributes('style')).toContain('linear-gradient(to bottom, #10b981 22.5%, #10b981 37.5%, #10b981 52.5%, #10b981 75%, #10b981 75%, #10b981 100%)')
  })

  it('keeps the latency bar segments beside first token, duration, and TPS when cache rate is shown', () => {
    const wrapper = mountLatency([
      { request_id: 'req-cache-bar', input_tokens: 100, cache_read_tokens: 0, cache_creation_tokens: 0, output_tokens: 25, duration_ms: 17_000, first_token_ms: 12_000 },
    ])

    const bar = wrapper.find('[data-row="req-cache-bar"] [data-testid="latency-bar"]')
    expect(bar.classes()).toContain('row-span-4')
    expect(bar.attributes('style')).toContain('#fbbf24 22.5%, #10b981 37.5%, #ef4444 52.5%, #ef4444 75%, #10b981 75%')
  })

  const cacheHitRow = { request_id: 'req-cache-visibility', input_tokens: 4_057, cache_read_tokens: 278_272, cache_creation_tokens: 4, output_tokens: 101, duration_ms: 345, first_token_ms: 12 }

  it('hides the cache rate from admins when the feature switch is off', () => {
    appStoreMocks.cachedPublicSettings = { usage_show_cache_rate: false, usage_show_cache_rate_to_users: true }
    const wrapper = mountLatency([cacheHitRow])
    expect(wrapper.find('[data-testid="latency-cache-rate"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Cache rate')
    appStoreMocks.cachedPublicSettings = undefined
  })

  it('hides the cache rate from users unless that switch is on', () => {
    appStoreMocks.cachedPublicSettings = undefined
    const hidden = mountLatency([cacheHitRow], { cacheRateAudience: 'user' })
    expect(hidden.find('[data-testid="latency-cache-rate"]').exists()).toBe(false)

    appStoreMocks.cachedPublicSettings = { usage_show_cache_rate: true, usage_show_cache_rate_to_users: true }
    const shown = mountLatency([cacheHitRow], { cacheRateAudience: 'user' })
    expect(shown.find('[data-testid="latency-cache-rate"]').text()).toBe('98.6%')
    appStoreMocks.cachedPublicSettings = undefined
  })
})

for (const [language, first, total] of [['en', 'Server TTFT', 'Server total'], ['zh', '服务端首字', '服务端总耗时']] as const) {
  it(`keeps the original four latency rows and omits typed-content timing in ${language}`, () => {
    locale = language
    const dashboardMessages = language === 'en' ? enDashboard : zhDashboard
    appStoreMocks.cachedPublicSettings = { usage_show_cache_rate: true }
    const wrapper = mount(UsageTable, {
      props: { data: [{ ...baseImageRow, image_count: 0, billing_mode: 'token', output_tokens: 100, duration_ms: 20_000, first_token_ms: 2_000, queue_wait_ms: 30_000,
        queue_timing: { status: 'measured', source: 'ws_frame_ctx_pool', scope: 'pre_final_forward', queue_clamped: false, server_first_token_ms: 32_000, server_duration_ms: 50_000, forward_first_token_ms: 2_000, forward_duration_ms: 20_000 },
        content_timing: { reasoning_first_ms: 120, visible_text_first_ms: 450, response_byte_first_ms: 30 } }], loading: false, columns: [{ key: 'latency', label: 'Latency' }] },
      global: { stubs: { DataTable: DataTableStub, EmptyState: true, Icon: true, Teleport: true } },
    })
    expect(wrapper.text()).toContain(first)
    expect(wrapper.text()).toContain(total)
    expect(wrapper.text()).toContain('32.00s')
    expect(wrapper.text()).toContain('50.00s')
    expect(wrapper.findAll('[class*="col-start-2"][class*="row-start-"]').map(node => node.text())).toEqual([
      dashboardMessages.usage.latencyFirstToken,
      dashboardMessages.usage.latencyDuration,
      dashboardMessages.usage.latencyTps,
      dashboardMessages.usage.latencyCacheRate,
    ])
    expect(wrapper.find('[data-testid="typed-content-timing"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="latency-e2e-uncollected"]').exists()).toBe(false)
    wrapper.unmount()
    appStoreMocks.cachedPublicSettings = undefined
    locale = 'en'
  })
}

it('keeps queue-inclusive admin latency compact and exposes its breakdown on hover', () => {
  const row = {
    ...baseImageRow,
    image_count: 0,
    billing_mode: 'token',
    output_tokens: 100,
    duration_ms: 20_000,
    first_token_ms: 2_000,
    queue_wait_ms: 30_000,
    queue_timing: {
      status: 'measured',
      source: 'ws_frame_ctx_pool',
      scope: 'pre_final_forward',
      queue_clamped: false,
      server_first_token_ms: 32_000,
      server_duration_ms: 50_000,
      forward_first_token_ms: 2_000,
      forward_duration_ms: 20_000,
    },
  }
  const wrapper = mount(UsageTable, {
    props: { data: [row], loading: false, enableTimingDetails: true, columns: [{ key: 'latency', label: 'Latency' }] },
    global: { stubs: { DataTable: DataTableStubWithLatency, EmptyState: true, Icon: true, Teleport: true } },
  })

  expect(wrapper.text()).toContain('First token')
  expect(wrapper.text()).toContain('Total')
  expect(wrapper.text()).toContain('32.00s')
  expect(wrapper.text()).toContain('50.00s')
  expect(wrapper.text()).not.toContain('incl. pre-forward wait')
  expect(wrapper.text()).not.toContain('30.00s')
  const firstValue = wrapper.findAll('span').find((span) => span.text() === '32.00s')
  const totalValue = wrapper.findAll('span').find((span) => span.text() === '50.00s')
  expect(firstValue?.attributes('title')).toContain('30.00s')
  expect(firstValue?.attributes('title')).toContain('2.00s')
  expect(totalValue?.attributes('title')).toContain('30.00s')
  expect(totalValue?.attributes('title')).toContain('20.00s')
  wrapper.unmount()
})

it('renders content timing in separate columns and uses a dash for missing values', () => {
  const wrapper = mount(UsageTable, {
    props: {
      data: [
        {
          ...baseImageRow,
          request_id: 'timing-measured',
          content_timing: {
            reasoning_first_ms: 120,
            visible_text_first_ms: 1_330,
            response_byte_first_ms: 30,
            status: 'measured',
            source: 'fixture',
            scope: 'final_physical_http_attempt_offsets_from_application_entry',
            reasoning_streaming_capability: 'unknown',
            capability_source: 'none',
          },
        },
        { ...baseImageRow, request_id: 'timing-missing', content_timing: null },
      ],
      loading: false,
      columns: [
        { key: 'content_reasoning_first_ms', label: enLatencyObservability.reasoning_first_ms },
        { key: 'content_visible_text_first_ms', label: enLatencyObservability.visible_text_first_ms },
        { key: 'content_response_byte_first_ms', label: enLatencyObservability.response_byte_first_ms },
      ],
    },
    global: { stubs: { DataTable: DataTableStubWithContentTiming, EmptyState: true, Icon: true, Teleport: true } },
  })

  expect(wrapper.get('[data-row="timing-measured"] [data-testid="content-timing-reasoning-first"]').text()).toBe('120ms')
  expect(wrapper.get('[data-row="timing-measured"] [data-testid="content-timing-visible-text-first"]').text()).toBe('1.33s')
  expect(wrapper.get('[data-row="timing-measured"] [data-testid="content-timing-response-byte-first"]').text()).toBe('30ms')
  expect(wrapper.get('[data-row="timing-missing"] [data-testid="content-timing-reasoning-first"]').text()).toBe('—')
  expect(wrapper.get('[data-row="timing-missing"] [data-testid="content-timing-visible-text-first"]').text()).toBe('—')
  expect(wrapper.get('[data-row="timing-missing"] [data-testid="content-timing-response-byte-first"]').text()).toBe('—')
  expect(wrapper.get('[data-testid="content-timing-reasoning-first"]').attributes('title')).toBe(enLatencyObservability.typedScope)
  expect(wrapper.text()).not.toContain('Client end-to-end time')
  wrapper.unmount()
})
