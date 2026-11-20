import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import UpstreamBillingRateCell from '../UpstreamBillingRateCell.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import type { Account, UpstreamBalanceSnapshot } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${Object.values(params).join(',')}` : key
    })
  }
})

const makeAccount = (overrides: Partial<Account> = {}): Account => ({
  id: 1,
  name: 'upstream',
  platform: 'openai',
  type: 'apikey',
  proxy_id: null,
  concurrency: 1,
  priority: 1,
  status: 'active',
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: false,
  created_at: '2026-07-13T00:00:00Z',
  updated_at: '2026-07-13T00:00:00Z',
  schedulable: true,
  rate_limited_at: null,
  rate_limit_reset_at: null,
  overload_until: null,
  temp_unschedulable_until: null,
  temp_unschedulable_reason: null,
  session_window_start: null,
  session_window_end: null,
  session_window_status: null,
  ...overrides
})

const billingData = {
  object: 'sub2api.key_billing' as const,
  schema_version: 1 as const,
  billing_scope: 'token' as const,
  group_rate_multiplier: 0.8,
  resolved_rate_multiplier: 0.6,
  peak_rate_enabled: true,
  peak_start: '09:00',
  peak_end: '18:00',
  peak_rate_multiplier: 1.5,
  applied_peak_multiplier: 1.5,
  effective_rate_multiplier: 0.9,
  timezone: 'Asia/Shanghai',
  observed_at: '2026-07-13T00:00:00Z'
}

describe('UpstreamBillingRateCell', () => {
  it('shows every upstream group rate and pauses scalar synchronization', async () => {
    const now = Date.parse('2026-07-13T00:30:00Z')
    const wrapper = mount(UpstreamBillingRateCell, { props: {
      account: makeAccount({ extra: { upstream_billing_probe: {
        status: 'ok', received_at: '2026-07-13T00:00:00Z', fresh_until: '2026-07-13T01:00:00Z',
        last_attempt_at: '2026-07-13T00:00:00Z', next_probe_at: '2026-07-13T00:30:00Z',
        data: { object: 'sub2api.key_billing', schema_version: 2, billing_scope: 'token', billing_mode: 'multi_group', observed_at: billingData.observed_at,
          groups: [
            { ...billingData, group_id: 1, group_name: 'Astra', platform: 'openai', status: 'active' },
            { ...billingData, group_id: 2, group_name: 'Economy', platform: 'openai', status: 'active', peak_rate_enabled: false, group_rate_multiplier: 0.18, resolved_rate_multiplier: 0.18, effective_rate_multiplier: 0.18 }
          ] }
      } } }), now
    } })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('0.18–0.60x')
    const details = wrapper.get('[data-testid="upstream-billing-groups"]')
    expect(details.text()).toContain('Astra')
    expect(details.text()).toContain('Economy')
    expect(details.text()).toContain('admin.accounts.upstreamBilling.multiGroupSyncPaused')
    await wrapper.setProps({ now: Date.parse('2026-07-13T02:00:00Z') })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.stale')
    expect(wrapper.get('[data-testid="upstream-billing-groups"]').text()).toContain('Astra')
  })

  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-13T00:30:00Z'))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('offers keyboard accessible configuration only for unsupported or configured accounts', async () => {
    const wrapper = mount(UpstreamBillingRateCell, { props: {
      account: makeAccount({ extra: { upstream_billing_probe: {
        status: 'unsupported', last_attempt_at: '', next_probe_at: ''
      } } }), now: Date.now()
    } })
    const configure = wrapper.find('[data-testid="upstream-billing-configure"]')
    expect(configure.exists()).toBe(true)
    expect(configure.element.tagName).toBe('BUTTON')
    await configure.trigger('click')
    expect(wrapper.emitted('configure')).toHaveLength(1)
    await wrapper.get('[data-testid="upstream-billing-probe"]').trigger('click')
    expect(wrapper.emitted('probe')).toHaveLength(1)
    expect(wrapper.emitted('configure')).toHaveLength(1)
    await wrapper.setProps({ account: makeAccount({ extra: { upstream_billing_probe: {
      status: 'failed', last_error: 'timeout', last_attempt_at: '', next_probe_at: ''
    } } }) })
    expect(wrapper.find('[data-testid="upstream-billing-configure"]').exists()).toBe(false)
    await wrapper.setProps({ account: makeAccount({ extra: { upstream_billing_provider: 'new_api' } }) })
    expect(wrapper.find('[data-testid="upstream-billing-configure"]').exists()).toBe(true)
  })

  it('leaves refresh available to observers while hiding configuration', async () => {
    const wrapper = mount(UpstreamBillingRateCell, { props: {
      account: makeAccount({ extra: { upstream_billing_provider: 'new_api', upstream_billing_probe: { status: 'unsupported', last_attempt_at: '', next_probe_at: '' } } }),
      now: Date.now(), canConfigure: false
    } })
    expect(wrapper.find('[data-testid="upstream-billing-configure"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.unsupported')
    await wrapper.get('[data-testid="upstream-billing-probe"]').trigger('click')
    expect(wrapper.emitted('probe')).toHaveLength(1)
  })

  it('shows the converted New API wallet without displaying its raw USD amount', () => {
    const wrapper = mount(UpstreamBillingRateCell, { props: {
      account: makeAccount({ extra: { upstream_billing_provider: 'new_api', upstream_billing_probe: {
        status: 'ok', data: billingData, received_at: '2026-07-13T00:00:00Z', fresh_until: '2026-07-14T00:00:00Z',
        last_attempt_at: '', next_probe_at: '', balance: {
          status: 'ok', received_at: '2026-07-13T00:00:00Z', fresh_until: '2026-07-14T00:00:00Z', last_attempt_at: '',
          data: { is_valid: true, remaining: 90.082, wallet_balance: 90.082, unit: 'CNY', source: 'new_api', raw_wallet_balance: 12.34, raw_unit: 'USD', display_exchange_rate: 7.3 }
        }
      } } }), now: Date.now()
    } })
    expect(wrapper.get('[data-testid="upstream-balance-value"]').text()).toBe('90.08 CNY')
    expect(wrapper.get('[data-testid="upstream-balance-value"]').text()).not.toContain('$12.34')
  })

  it('converts a New API declaration independently of manually set cost and retains the upstream rate', async () => {
    const wrapper = mount(UpstreamBillingRateCell, { props: {
      account: makeAccount({ extra: { upstream_billing_provider: 'new_api', cost_multiplier_auto_sync: false, cost_multiplier: 0.5, upstream_billing_probe: {
        status: 'ok', data: { ...billingData, provider: 'new_api', object: 'new_api.group_billing', billing_scope: 'group', peak_rate_enabled: false,
          group_rate_multiplier: 0.1214, effective_rate_multiplier: 0.1214, resolved_rate_multiplier: 0.1214 },
        received_at: '2026-07-13T00:00:00Z', fresh_until: '2026-07-14T00:00:00Z',
        last_attempt_at: '', next_probe_at: '', balance: {
          status: 'ok', received_at: '2026-07-13T00:00:00Z', fresh_until: '2026-07-14T00:00:00Z', last_attempt_at: '',
          data: { is_valid: true, mode: 'unrestricted', wallet_balance: 12.34, remaining: 12.34, unit: 'USD', source: 'new_api' }
        }
      } } }), now: Date.now()
    } })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('0.85x')
    expect(wrapper.get('[data-testid="upstream-balance-value"]').text()).toBe('$12.34')
    const regularAccount = JSON.parse(JSON.stringify(wrapper.props('account')))
    delete regularAccount.extra.upstream_billing_provider
    delete regularAccount.extra.upstream_billing_probe.data.provider
    regularAccount.extra.upstream_billing_probe.data.object = 'sub2api.key_billing'
    regularAccount.extra.upstream_billing_probe.data.billing_scope = 'token'
    const regular = mount(UpstreamBillingRateCell, { props: { account: regularAccount, now: Date.now() } })
    expect(regular.get('[data-testid="upstream-billing-rate"]').text()).toBe('0.1214x')
    expect(wrapper.get('[data-testid="upstream-billing-configure"]').text()).toBe('0.85x')
    await wrapper.get('[data-testid="upstream-billing-configure"]').trigger('click')
    expect(wrapper.emitted('configure')).toHaveLength(1)
    await wrapper.get('[data-testid="upstream-billing-details"]').trigger('mouseenter')
    await flushPromises()
    const tooltip = Array.from(document.body.querySelectorAll<HTMLElement>('[role="tooltip"]'))
      .find(element => element.style.display !== 'none' && element.textContent?.includes('admin.accounts.upstreamBilling.newAPI.groupRatioHint'))
    expect(tooltip).toBeDefined()
    expect(tooltip?.textContent).toContain('admin.accounts.upstreamBilling.groupRate:0.1214')
    expect(tooltip?.textContent).toContain('admin.accounts.upstreamBilling.newAPI.convertedDeclaration:0.1214,0.8498')
    await wrapper.get('[data-testid="upstream-billing-details"]').trigger('mouseleave')
    const account = JSON.parse(JSON.stringify(wrapper.props('account')))
    await wrapper.setProps({ account: { ...account, extra: { ...account.extra, new_api_apply_usd_rate: false } } })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('0.1214x')
    await wrapper.setProps({ account: { ...account, platform: 'deepseek' } })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('0.1214x')
    const unsupported = JSON.parse(JSON.stringify(account))
    unsupported.extra.upstream_billing_probe.status = 'unsupported'
    delete unsupported.extra.upstream_billing_probe.data
    await wrapper.setProps({ account: unsupported })
    expect(wrapper.get('[data-testid="upstream-billing-configure"]').text()).toBe('admin.accounts.upstreamBilling.unsupported')
    expect(wrapper.get('[data-testid="upstream-balance-value"]').text()).toBe('$12.34')
    regular.unmount()
    wrapper.unmount()
  })

  it('recomputes the current effective rate and keeps the icon-only probe action', async () => {
    const wrapper = mount(UpstreamBillingRateCell, {
      props: {
        account: makeAccount({
          extra: {
            upstream_billing_probe_enabled: true,
            upstream_billing_probe: {
              status: 'ok',
              data: billingData,
              received_at: '2026-07-13T00:00:00Z',
              fresh_until: '2026-07-14T00:00:00Z',
              last_attempt_at: '2026-07-13T00:00:00Z',
              next_probe_at: '2026-07-13T00:30:00Z'
            }
          }
        }),
        now: Date.now()
      }
    })

    expect(wrapper.text()).toContain('0.60x')
    await wrapper.setProps({ now: Date.parse('2026-07-13T01:00:00Z') })
    expect(wrapper.text()).toContain('0.90x')
    await wrapper.setProps({ now: Date.parse('2026-07-13T10:00:00Z') })
    expect(wrapper.text()).toContain('0.60x')
    expect(wrapper.text()).not.toContain('admin.accounts.upstreamBilling.latest')
    expect(wrapper.get('[data-testid="upstream-billing-probe"]').text()).toBe('')
    expect(wrapper.get('[data-testid="upstream-billing-probe"]').attributes('aria-label')).toBe(
      'admin.accounts.upstreamBilling.manualProbe'
    )
  })

  it('uses retained failed data only while it is still fresh', async () => {
    const account = makeAccount({
      extra: {
        upstream_billing_probe: {
          status: 'ok',
          data: billingData,
          received_at: '2026-07-12T22:00:00Z',
          fresh_until: '2026-07-12T23:00:00Z',
          last_attempt_at: '2026-07-12T22:00:00Z',
          next_probe_at: '2026-07-12T22:30:00Z'
        }
      }
    })
    const wrapper = mount(UpstreamBillingRateCell, { props: { account, now: Date.now() } })
    expect(wrapper.text()).toContain('admin.accounts.upstreamBilling.stale')
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.stale')

    await wrapper.setProps({
      account: makeAccount({
        extra: {
          upstream_billing_probe: {
            status: 'failed',
            data: billingData,
            received_at: '2026-07-13T00:00:00Z',
            fresh_until: '2026-07-13T01:00:00Z',
            last_attempt_at: '2026-07-13T00:00:00Z',
            next_probe_at: '2026-07-13T01:00:00Z',
            last_error: 'http_error'
          }
        }
      })
    })
    expect(wrapper.text()).toContain('0.60x')
    expect(wrapper.text()).toContain('admin.accounts.upstreamBilling.failed')

    await wrapper.setProps({ now: Date.parse('2026-07-13T01:00:00Z') })
    expect(wrapper.text()).toContain('0.90x')
    expect(wrapper.text()).not.toContain('admin.accounts.upstreamBilling.stale')

    await wrapper.setProps({ now: Date.parse('2026-07-13T01:00:00.001Z') })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.stale')
    expect(wrapper.text()).toContain('admin.accounts.upstreamBilling.stale')

    await wrapper.setProps({
      now: Date.now(),
      account: makeAccount({
        extra: {
          upstream_billing_probe: {
            status: 'failed',
            data: billingData,
            received_at: '2026-07-12T22:00:00Z',
            fresh_until: '2026-07-12T23:00:00Z',
            last_attempt_at: '2026-07-13T00:00:00Z',
            next_probe_at: '2026-07-13T01:00:00Z',
            last_error: 'http_error'
          }
        }
      })
    })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.stale')
    expect(wrapper.text()).toContain('admin.accounts.upstreamBilling.stale')
  })

  it('shows stale snapshot details, local next probe time, and the account probe state', async () => {
    const wrapper = mount(UpstreamBillingRateCell, {
      attachTo: document.body,
      props: {
        account: makeAccount({
          extra: {
            upstream_billing_probe_enabled: true,
            upstream_billing_probe: {
              status: 'ok',
              data: billingData,
              received_at: '2026-07-12T22:00:00Z',
              fresh_until: '2026-07-12T23:00:00Z',
              last_attempt_at: '2026-07-12T22:00:00Z',
              next_probe_at: '2026-07-13T01:00:00Z'
            }
          }
        }),
        now: Date.now()
      }
    })

    expect(wrapper.getComponent(HelpTooltip).props('widthClass')).toBe('w-max max-w-[calc(100vw-2rem)]')
    await wrapper.get('[data-testid="upstream-billing-details"]').trigger('mouseenter')
    await flushPromises()

    const tooltips = document.body.querySelectorAll('[role="tooltip"]')
    const tooltip = tooltips[tooltips.length - 1] as HTMLElement
    expect(tooltip.textContent).toContain('admin.accounts.upstreamBilling.lastDetectedRate:0.9')
    expect(tooltip.textContent).toContain('admin.accounts.upstreamBilling.lastDetectedAt:')
    expect(tooltip.textContent).toContain('admin.accounts.upstreamBilling.elapsedSince:admin.accounts.upstreamBilling.hoursAgo:2')
    expect(tooltip.textContent).toContain('admin.accounts.upstreamBilling.nextProbeAt:')
    expect(tooltip.textContent).not.toContain('admin.accounts.upstreamBilling.stale')
    expect(tooltip.querySelector('[data-testid="upstream-billing-probe-state"] span')?.className).toContain('text-emerald-400')

    await wrapper.setProps({
      account: makeAccount({
        extra: {
          upstream_billing_probe_enabled: false,
          upstream_billing_probe: {
            status: 'unsupported',
            last_attempt_at: '2026-07-13T00:00:00Z',
            next_probe_at: '2026-07-13T01:00:00Z'
          }
        }
      })
    })
    expect(tooltip.querySelector('[data-testid="upstream-billing-next-probe"]')).toBeNull()
    expect(tooltip.querySelector('[data-testid="upstream-billing-probe-state"] span')?.className).toContain('text-red-400')
    wrapper.unmount()
  })

  it('stacks the global-off state below the account state and hides it when globally enabled', async () => {
    const wrapper = mount(UpstreamBillingRateCell, {
      attachTo: document.body,
      props: {
        account: makeAccount({
          extra: {
            upstream_billing_probe_enabled: true,
            upstream_billing_probe: {
              status: 'unsupported',
              last_attempt_at: '2026-07-13T00:00:00Z',
              next_probe_at: '2026-07-13T01:00:00Z'
            }
          }
        }),
        globalProbeEnabled: false,
        now: Date.now()
      }
    })

    await wrapper.get('[data-testid="upstream-billing-details"]').trigger('mouseenter')
    await flushPromises()

    const tooltips = document.body.querySelectorAll('[role="tooltip"]')
    const tooltip = tooltips[tooltips.length - 1] as HTMLElement
    const accountState = tooltip.querySelector('[data-testid="upstream-billing-probe-state"]')
    const globalState = tooltip.querySelector('[data-testid="upstream-billing-global-probe-state"]')
    expect(accountState?.querySelector('span')?.className).toContain('text-emerald-400')
    expect(globalState?.textContent).toContain('admin.accounts.upstreamBilling.globalProbeState')
    expect(globalState?.querySelector('span')?.className).toContain('text-red-400')
    expect(tooltip.querySelector('[data-testid="upstream-billing-next-probe"]')).toBeNull()

    await wrapper.setProps({ globalProbeEnabled: true })
    expect(tooltip.querySelector('[data-testid="upstream-billing-global-probe-state"]')).toBeNull()
    expect(tooltip.querySelector('[data-testid="upstream-billing-next-probe"]')).not.toBeNull()

    await wrapper.setProps({
      globalProbeEnabled: false,
      account: makeAccount({
        extra: { upstream_billing_probe_enabled: false }
      })
    })
    expect(accountState?.querySelector('span')?.className).toContain('text-red-400')
    expect(tooltip.querySelector('[data-testid="upstream-billing-global-probe-state"]')).not.toBeNull()
    expect(tooltip.querySelector('[data-testid="upstream-billing-next-probe"]')).toBeNull()
    wrapper.unmount()
  })

  it('emits manual probe commands only for eligible accounts', async () => {
    const wrapper = mount(UpstreamBillingRateCell, {
      props: { account: makeAccount(), now: Date.now() }
    })
    await wrapper.get('[data-testid="upstream-billing-probe"]').trigger('click')
    expect(wrapper.emitted('probe')).toHaveLength(1)

    // 探测已放宽到全部 API-key 平台：grok API-key 账号同样可探测。
    await wrapper.setProps({ account: makeAccount({ platform: 'grok' }) })
    await wrapper.get('[data-testid="upstream-billing-probe"]').trigger('click')
    expect(wrapper.emitted('probe')).toHaveLength(2)

    await wrapper.setProps({ account: makeAccount({ type: 'oauth' }) })
    expect(wrapper.findAll('button')).toHaveLength(0)
    expect(wrapper.text()).toBe('-')
  })

  it('fails neutral for malformed data and timestamps', async () => {
    const malformedAccount = (
      dataOverrides: Partial<typeof billingData> = {},
      snapshotOverrides: Record<string, unknown> = {}
    ) => makeAccount({
      extra: {
        upstream_billing_probe: {
          status: 'ok',
          data: { ...billingData, ...dataOverrides },
          received_at: '2026-07-13T00:00:00Z',
          fresh_until: '2026-07-13T01:00:00Z',
          last_attempt_at: '2026-07-13T00:00:00Z',
          next_probe_at: '2026-07-13T01:00:00Z',
          ...snapshotOverrides
        }
      }
    })
    const wrapper = mount(UpstreamBillingRateCell, {
      props: {
        account: malformedAccount({
          resolved_rate_multiplier: -1,
          peak_rate_enabled: false,
          effective_rate_multiplier: -1
        }),
        now: Date.now()
      }
    })

    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('-')
    await wrapper.setProps({ account: malformedAccount({ billing_scope: 'request' as 'token' }) })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('-')
    await wrapper.setProps({ account: malformedAccount({}, { received_at: 'not-a-time' }) })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.stale')
    await wrapper.setProps({ account: malformedAccount({}, { received_at: '2026-07-13T00:31:00Z' }) })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('0.60x')
    await wrapper.setProps({ account: malformedAccount({}, { received_at: '2026-07-13T00:36:00Z' }) })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.stale')
    await wrapper.setProps({ account: malformedAccount({}, { fresh_until: '2026-07-12T23:59:00Z' }) })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.stale')

    await wrapper.setProps({
      account: makeAccount({
        extra: {
          upstream_billing_probe: {
            status: 'failed',
            last_attempt_at: '2026-07-13T00:00:00Z',
            next_probe_at: '2026-07-13T01:00:00Z',
            last_error: 'network_error'
          }
        }
      })
    })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('admin.accounts.upstreamBilling.failed')
    expect(wrapper.text()).toContain('admin.accounts.upstreamBilling.failed')
    expect(wrapper.text()).not.toContain('admin.accounts.upstreamBilling.stale')
  })

  it('uses unsupported as the primary tooltip trigger without a dash', () => {
    const wrapper = mount(UpstreamBillingRateCell, {
      props: {
        account: makeAccount({
          extra: {
            upstream_billing_probe: {
              status: 'unsupported',
              last_attempt_at: '2026-07-13T00:00:00Z',
              next_probe_at: '2026-07-13T00:30:00Z',
              last_error: 'unsupported'
            }
          }
        }),
        now: Date.now()
      }
    })

    expect(wrapper.get('[data-testid="upstream-billing-configure"]').text()).toBe(
      'admin.accounts.upstreamBilling.unsupported'
    )
    expect(wrapper.text()).not.toContain('-admin.accounts.upstreamBilling.unsupported')
  })

  const balanceSnapshot = (
    overrides: Partial<UpstreamBalanceSnapshot> = {},
    data: Record<string, unknown> = {}
  ): UpstreamBalanceSnapshot => ({
    status: 'ok',
    data: {
      is_valid: true,
      mode: 'unrestricted',
      plan_name: '钱包余额',
      unit: 'USD',
      remaining: 12.34,
      wallet_balance: 12.34,
      ...data
    } as UpstreamBalanceSnapshot['data'],
    received_at: '2026-07-13T00:00:00Z',
    fresh_until: '2026-07-13T01:00:00Z',
    last_attempt_at: '2026-07-13T00:00:00Z',
    ...overrides
  })

  const accountWithBalance = (
    balance?: UpstreamBalanceSnapshot,
    probeOverrides: Record<string, unknown> = {}
  ) => makeAccount({
    extra: {
      upstream_billing_probe: {
        status: 'ok',
        data: billingData,
        received_at: '2026-07-13T00:00:00Z',
        fresh_until: '2026-07-14T00:00:00Z',
        last_attempt_at: '2026-07-13T00:00:00Z',
        next_probe_at: '2026-07-13T00:30:00Z',
        balance,
        ...probeOverrides
      }
    }
  })

  const openBalanceTooltip = async (wrapper: ReturnType<typeof mount>) => {
    await wrapper.get('[data-testid="upstream-balance-details"]').trigger('mouseenter')
    await flushPromises()
    const tooltip = Array.from(document.body.querySelectorAll<HTMLElement>('[role="tooltip"]'))
      .find(element => element.style.display !== 'none')
    expect(tooltip).toBeDefined()
    return tooltip as HTMLElement
  }

  it('shows the upstream balance on a second line below the rate', async () => {
    const wrapper = mount(UpstreamBillingRateCell, {
      attachTo: document.body,
      props: { account: accountWithBalance(balanceSnapshot()), now: Date.now() }
    })

    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('0.60x')
    expect(wrapper.get('[data-testid="upstream-balance"]').text()).toContain('admin.accounts.upstreamBilling.balance.label')
    const value = wrapper.get('[data-testid="upstream-balance-value"]')
    expect(value.text()).toContain('12.34')
    expect(value.classes()).toContain('font-mono')
    expect(value.classes()).not.toContain('text-red-600')
    expect(wrapper.get('[data-testid="upstream-balance"]').text()).not.toContain('admin.accounts.upstreamBilling.failed')

    const tooltip = await openBalanceTooltip(wrapper)
    expect(tooltip.textContent).toContain('admin.accounts.upstreamBilling.balance.kindWallet')
    expect(tooltip.textContent).toContain('admin.accounts.upstreamBilling.balance.remaining:')
    expect(tooltip.textContent).toContain('admin.accounts.upstreamBilling.updatedAt:')
    expect(tooltip.querySelector('[data-testid="upstream-balance-error"]')).toBeNull()
    wrapper.unmount()
  })

  it('marks depleted or unusable balances and shows unlimited subscriptions', async () => {
    const wrapper = mount(UpstreamBillingRateCell, {
      props: {
        account: accountWithBalance(balanceSnapshot({}, { remaining: 0, wallet_balance: 0 })),
        now: Date.now()
      }
    })
    const value = () => wrapper.get('[data-testid="upstream-balance-value"]')
    expect(value().text()).toContain('0.00')
    expect(value().classes()).toContain('text-red-600')

    await wrapper.setProps({ account: accountWithBalance(balanceSnapshot({}, { is_valid: false })) })
    expect(value().text()).toContain('12.34')
    expect(value().classes()).toContain('text-red-600')

    await wrapper.setProps({
      account: accountWithBalance(balanceSnapshot({}, {
        plan_name: 'Claude Max',
        remaining: undefined,
        wallet_balance: undefined,
        unlimited: true
      }))
    })
    expect(value().text()).toBe('admin.accounts.upstreamBilling.balance.unlimited')
    expect(value().classes()).not.toContain('text-red-600')
    expect(value().classes()).not.toContain('font-mono')
  })

  it('keeps a failed balance while it is fresh and reports it as stale afterwards', async () => {
    const wrapper = mount(UpstreamBillingRateCell, {
      attachTo: document.body,
      props: {
        account: accountWithBalance(balanceSnapshot({
          status: 'failed',
          last_attempt_at: '2026-07-13T00:30:00Z',
          http_status: 502,
          last_error: 'http_error'
        })),
        now: Date.now()
      }
    })
    const line = () => wrapper.get('[data-testid="upstream-balance"]')
    const value = () => wrapper.get('[data-testid="upstream-balance-value"]')
    expect(value().text()).toContain('12.34')
    expect(line().text()).toContain('admin.accounts.upstreamBilling.failed')

    const tooltip = await openBalanceTooltip(wrapper)
    expect(tooltip.querySelector('[data-testid="upstream-balance-error"]')?.textContent).toContain('http_error (HTTP 502)')

    await wrapper.setProps({ now: Date.parse('2026-07-13T01:00:00Z') })
    expect(value().text()).toContain('12.34')

    await wrapper.setProps({ now: Date.parse('2026-07-13T01:00:00.001Z') })
    expect(wrapper.get('[data-testid="upstream-billing-rate"]').text()).toBe('0.90x')
    expect(value().text()).toBe('admin.accounts.upstreamBilling.stale')
    expect(value().classes()).toContain('text-amber-600')
    expect(line().text()).not.toContain('admin.accounts.upstreamBilling.failed')
    expect(tooltip.querySelector('[data-testid="upstream-balance-last-value"]')?.textContent).toContain('12.34')

    await wrapper.setProps({
      now: Date.now(),
      account: accountWithBalance({
        status: 'failed',
        last_attempt_at: '2026-07-13T00:30:00Z',
        http_status: 200,
        last_error: 'invalid_response'
      })
    })
    expect(value().text()).toBe('admin.accounts.upstreamBilling.failed')
    expect(value().classes()).toContain('text-red-600')
    expect(tooltip.querySelector('[data-testid="upstream-balance-error"]')?.textContent).toContain('invalid_response (HTTP 200)')
    wrapper.unmount()
  })

  it('only shows an unsupported balance when the rate endpoint answers', async () => {
    const unsupported: UpstreamBalanceSnapshot = {
      status: 'unsupported',
      last_attempt_at: '2026-07-13T00:00:00Z',
      http_status: 404,
      last_error: 'unsupported'
    }
    const unsupportedRate = { status: 'unsupported', data: undefined, received_at: undefined, fresh_until: undefined }
    const wrapper = mount(UpstreamBillingRateCell, {
      props: { account: accountWithBalance(unsupported, unsupportedRate), now: Date.now() }
    })
    expect(wrapper.get('[data-testid="upstream-billing-configure"]').text()).toBe('admin.accounts.upstreamBilling.unsupported')
    expect(wrapper.find('[data-testid="upstream-balance"]').exists()).toBe(false)

    await wrapper.setProps({ account: accountWithBalance(unsupported) })
    expect(wrapper.get('[data-testid="upstream-balance-value"]').text()).toBe('admin.accounts.upstreamBilling.unsupported')

    // An upstream that predates the billing endpoint can still report a balance.
    await wrapper.setProps({ account: accountWithBalance(balanceSnapshot(), unsupportedRate) })
    expect(wrapper.get('[data-testid="upstream-billing-configure"]').text()).toBe('admin.accounts.upstreamBilling.unsupported')
    expect(wrapper.get('[data-testid="upstream-balance-value"]').text()).toContain('12.34')

    await wrapper.setProps({ account: accountWithBalance(undefined) })
    expect(wrapper.find('[data-testid="upstream-balance"]').exists()).toBe(false)
  })

  it('lists quota, windows, expiry and key state in the balance tooltip', async () => {
    const wrapper = mount(UpstreamBillingRateCell, {
      attachTo: document.body,
      props: {
        account: accountWithBalance(balanceSnapshot({}, {
          mode: 'quota_limited',
          plan_name: undefined,
          wallet_balance: undefined,
          key_status: 'quota_exhausted',
          is_valid: false,
          remaining: 0,
          quota_limit: 100,
          quota_used: 100,
          windows: [
            { window: '5h', limit: 5, used: 1.25, reset_at: '2026-07-13T05:00:00Z' },
            { window: 'daily', limit: 10 },
            { window: 'custom_window', limit: 3, used: 1 },
            { window: 7, limit: 1 },
            { window: '7d', limit: 'x' },
            null
          ],
          expires_at: '2026-09-01T00:00:00Z'
        })),
        now: Date.now()
      }
    })

    const tooltip = await openBalanceTooltip(wrapper)
    const text = tooltip.textContent ?? ''
    expect(text).toContain('admin.accounts.upstreamBilling.balance.kindKeyQuota')
    expect(text).toContain('admin.accounts.upstreamBilling.balance.keyQuota:')
    const windows = Array.from(tooltip.querySelectorAll('[data-testid="upstream-balance-window"]')).map(item => item.textContent ?? '')
    expect(windows).toHaveLength(3)
    expect(windows[0]).toContain('admin.accounts.upstreamBilling.balance.windowWithReset:admin.accounts.upstreamBilling.balance.windows.hours5')
    expect(windows[1]).toContain('admin.accounts.upstreamBilling.balance.window:admin.accounts.upstreamBilling.balance.windows.daily')
    expect(windows[2]).toContain('admin.accounts.upstreamBilling.balance.window:custom_window')
    expect(text).toContain('admin.accounts.upstreamBilling.balance.expiresAt:')
    expect(text).toContain('admin.accounts.upstreamBilling.balance.keyStatus:admin.accounts.upstreamBilling.balance.keyStatuses.quotaExhausted')
    expect(text).toContain('admin.accounts.upstreamBilling.balance.invalid')
    wrapper.unmount()
  })

  it('fails neutral for malformed balance data', async () => {
    const wrapper = mount(UpstreamBillingRateCell, {
      props: {
        account: accountWithBalance(balanceSnapshot({}, { remaining: '12.34', unit: 'POINTS' })),
        now: Date.now()
      }
    })
    const value = () => wrapper.get('[data-testid="upstream-balance-value"]')
    expect(value().text()).toBe('-')

    await wrapper.setProps({ account: accountWithBalance(balanceSnapshot({}, { unit: 'POINTS' })) })
    expect(value().text()).toBe('12.34 POINTS')

    await wrapper.setProps({ account: accountWithBalance(balanceSnapshot({ received_at: 'not-a-time' })) })
    expect(value().text()).toBe('admin.accounts.upstreamBilling.stale')

    await wrapper.setProps({ account: accountWithBalance(balanceSnapshot({ fresh_until: '2026-07-12T23:00:00Z' })) })
    expect(value().text()).toBe('admin.accounts.upstreamBilling.stale')
  })
})
