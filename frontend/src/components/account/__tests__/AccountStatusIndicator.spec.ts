import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountStatusIndicator from '../AccountStatusIndicator.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

vi.mock('@/utils/format', async () => {
  const actual = await vi.importActual<typeof import('@/utils/format')>('@/utils/format')
  return {
    ...actual,
    formatCountdown: () => '1h'
  }
})

function makeAccount(overrides: Partial<Account>): Account {
  return {
    id: 1,
    name: 'account',
    platform: 'antigravity',
    type: 'oauth',
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: true,
    created_at: '2026-03-15T00:00:00Z',
    updated_at: '2026-03-15T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    ...overrides,
  }
}

describe('AccountStatusIndicator', () => {
  it.each([
    [{}, 'active'],
    [{ status: 'error', error_message: 'Upstream unavailable' }, 'error'],
    [{ rate_limit_reset_at: '2099-01-01T00:00:00Z' }, 'rateLimited'],
    [{ overload_until: '2099-01-01T00:00:00Z' }, 'overloaded'],
    [{ temp_unschedulable_until: '2099-01-01T00:00:00Z' }, 'tempUnschedulable'],
    [{ schedulable: false }, 'paused'],
  ] as [Partial<Account>, string][])('keeps the BPS badge above the %s status', (overrides, status) => {
    const wrapper = mount(AccountStatusIndicator, {
      props: { account: makeAccount({ platform: 'openai', extra: { openai_excel_bps: true }, ...overrides }) },
      global: { stubs: { Icon: true } },
    })

    const badge = wrapper.get('[data-testid="bps-status-badge"]')
    expect(badge.text()).toBe('bps')
    expect(badge.attributes('title')).toBe('admin.accounts.openai.excelBPS')
    expect(wrapper.element.firstElementChild).toBe(badge.element)
    expect(badge.element.nextElementSibling?.textContent).toContain(`admin.accounts.status.${status}`)
  })

  it.each([
    { extra: undefined },
    { extra: { openai_excel_bps: false } },
    { extra: { openai_excel_bps: 'true' } },
    { platform: 'anthropic' },
    { type: 'apikey' },
    { parent_account_id: 2 },
    { credentials: { plan_type: ' Free ' } },
    { credentials: { auth_mode: ' agentIdentity ' } },
    { credentials: { auth_mode: 'personalAccessToken' } },
    { credentials: { openai_auth_mode: ' PERSONAL_ACCESS_TOKEN ' } },
  ] as Partial<Account>[])('hides BPS for disabled or unsupported accounts: %j', overrides => {
    const wrapper = mount(AccountStatusIndicator, {
      props: { account: makeAccount({ platform: 'openai', extra: { openai_excel_bps: true }, ...overrides }) },
    })

    expect(wrapper.find('[data-testid="bps-status-badge"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.status.active')
  })

  it('updates the BPS badge when the account setting changes', async () => {
    const account = makeAccount({ platform: 'openai', extra: { openai_excel_bps: false } })
    const wrapper = mount(AccountStatusIndicator, { props: { account } })
    expect(wrapper.find('[data-testid="bps-status-badge"]').exists()).toBe(false)

    await wrapper.setProps({ account: { ...account, extra: { openai_excel_bps: true } } })
    expect(wrapper.find('[data-testid="bps-status-badge"]').exists()).toBe(true)

    await wrapper.setProps({ account })
    expect(wrapper.find('[data-testid="bps-status-badge"]').exists()).toBe(false)
  })

  it('hides the BPS badge when the global protocol switch is off', () => {
    const wrapper = mount(AccountStatusIndicator, {
      props: {
        account: makeAccount({ platform: 'openai', extra: { openai_excel_bps: true } }),
        globalBpsEnabled: false,
      },
    })

    expect(wrapper.find('[data-testid="bps-status-badge"]').exists()).toBe(false)
  })

  it('shows the RPM pause reason and clears it when refreshed after reset', async () => {
    const account = makeAccount({ platform: 'openai', base_rpm: 10, current_rpm: 10, rpm_paused: true, rpm_reset_at: 1_900_000_020 })
    const wrapper = mount(AccountStatusIndicator, { props: { account }, global: { stubs: { Icon: true } } })
    expect(wrapper.text()).toContain('admin.accounts.status.rpmPaused')
    expect(wrapper.text()).toContain('admin.accounts.status.rpmPausedUntil')
    await wrapper.setProps({ account: { ...account, current_rpm: 0, rpm_paused: false, rpm_reset_at: undefined } })
    expect(wrapper.text()).not.toContain('admin.accounts.status.rpmPaused')
  })
  it('Claude 5 系列模型限流时显示 Opus 和 Sonnet 的短别名', () => {
    const wrapper = mount(AccountStatusIndicator, {
      props: {
        account: makeAccount({
          extra: {
            model_rate_limits: {
              'claude-opus-5': {
                rate_limited_at: '2026-07-28T00:00:00Z',
                rate_limit_reset_at: '2099-07-28T00:00:00Z'
              },
              'claude-sonnet-5': {
                rate_limited_at: '2026-07-28T00:00:00Z',
                rate_limit_reset_at: '2099-07-28T00:00:00Z'
              },
              'claude-sonnet-5-5': {
                rate_limited_at: '2026-09-28T00:00:00Z',
                rate_limit_reset_at: '2099-09-28T00:00:00Z'
              }
            }
          }
        })
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('COpus5')
    expect(wrapper.text()).toContain('CSon5')
    expect(wrapper.text()).toContain('CSon55')
    expect(wrapper.text()).not.toContain('claude-sonnet-5')
  })

  it('Grok 账号额度限流时显示自动恢复时间而非临时不可调度', () => {
    const wrapper = mount(AccountStatusIndicator, {
      props: {
        account: makeAccount({
          id: 5,
          name: 'grok-free-1',
          platform: 'grok',
          rate_limited_at: '2026-07-11T12:00:00Z',
          rate_limit_reset_at: '2099-07-11T13:00:00Z',
          temp_unschedulable_until: '2099-07-11T12:30:00Z',
          temp_unschedulable_reason: 'legacy grok rate limited'
        })
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.find('.badge-warning').text()).toBe('admin.accounts.status.rateLimited')
    expect(wrapper.text()).toContain('admin.accounts.status.rateLimitedAutoResume')
    expect(wrapper.text()).not.toContain('admin.accounts.status.tempUnschedulable')
  })

  it('模型限流 + overages 启用 + 无 AICredits key → 显示 ⚡ (credits_active)', () => {
    const wrapper = mount(AccountStatusIndicator, {
      props: {
        account: makeAccount({
          id: 1,
          name: 'ag-1',
          extra: {
            allow_overages: true,
            model_rate_limits: {
              'claude-sonnet-4-5': {
                rate_limited_at: '2026-03-15T00:00:00Z',
                rate_limit_reset_at: '2099-03-15T00:00:00Z'
              }
            }
          }
        })
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('⚡')
    expect(wrapper.text()).toContain('CSon45')
  })

  it('模型限流 + overages 未启用 → 普通限流样式（无 ⚡）', () => {
    const wrapper = mount(AccountStatusIndicator, {
      props: {
        account: makeAccount({
          id: 2,
          name: 'ag-2',
          extra: {
            model_rate_limits: {
              'claude-sonnet-4-5': {
                rate_limited_at: '2026-03-15T00:00:00Z',
                rate_limit_reset_at: '2099-03-15T00:00:00Z'
              }
            }
          }
        })
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('CSon45')
    expect(wrapper.text()).not.toContain('⚡')
  })

  it('AICredits key 生效 → 显示积分已用尽 (credits_exhausted)', () => {
    const wrapper = mount(AccountStatusIndicator, {
      props: {
        account: makeAccount({
          id: 3,
          name: 'ag-3',
          extra: {
            allow_overages: true,
            model_rate_limits: {
              'AICredits': {
                rate_limited_at: '2026-03-15T00:00:00Z',
                rate_limit_reset_at: '2099-03-15T00:00:00Z'
              }
            }
          }
        })
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('admin.accounts.status.creditsExhausted')
  })

  it('模型限流 + overages 启用 + AICredits key 生效 → 普通限流样式（积分耗尽，无 ⚡）', () => {
    const wrapper = mount(AccountStatusIndicator, {
      props: {
        account: makeAccount({
          id: 4,
          name: 'ag-4',
          extra: {
            allow_overages: true,
            model_rate_limits: {
              'claude-sonnet-4-5': {
                rate_limited_at: '2026-03-15T00:00:00Z',
                rate_limit_reset_at: '2099-03-15T00:00:00Z'
              },
              'AICredits': {
                rate_limited_at: '2026-03-15T00:00:00Z',
                rate_limit_reset_at: '2099-03-15T00:00:00Z'
              }
            }
          }
        })
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    // 模型限流 + 积分耗尽 → 不应显示 ⚡
    expect(wrapper.text()).toContain('CSon45')
    expect(wrapper.text()).not.toContain('⚡')
    // AICredits 积分耗尽状态应显示
    expect(wrapper.text()).toContain('admin.accounts.status.creditsExhausted')
  })
})
