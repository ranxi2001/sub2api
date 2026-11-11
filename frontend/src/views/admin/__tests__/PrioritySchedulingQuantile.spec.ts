import { describe, expect, it } from 'vitest'
import { client as apiClient, settle as flushPromises, render, wrapper } from './prioritySchedulingFixture'
import zh from '@/i18n/locales/zh/priorityScheduling'
import en from '@/i18n/locales/en/priorityScheduling'

describe('priority performance controls replace legacy quantile controls', () => {
  it('offers TPS and average TTFT targets, preserves legacy stored fields without displaying them', async () => {
    render(); await flushPromises()
    expect(wrapper!.find('[data-testid="latency-percentile"]').exists()).toBe(false)
    expect(wrapper!.find('[data-testid="latency-measurement"]').exists()).toBe(false)
    expect(wrapper!.find('[data-testid="error_rate_prior_samples"]').exists()).toBe(false)
    expect(wrapper!.find('[data-testid="quality_max_age_hours"]').exists()).toBe(false)
    await wrapper!.get('[data-testid="target_tps"]').setValue(60)
    await wrapper!.get('[data-testid="target_ttft_ms"]').setValue(5000)
    await wrapper!.get('form').trigger('submit'); await flushPromises()
    expect(apiClient.put).toHaveBeenCalledWith('/admin/priority-scheduling/config',expect.objectContaining({target_tps:60,target_ttft_ms:5000}))
  })
  it('labels the same shared metrics and sources in both languages', () => {
    expect(zh.load_weight).toBe('平均 TPS'); expect(en.load_weight).toBe('Average TPS')
    expect(zh.analysis.notice).toContain('账号管理'); expect(en.analysis.notice).toContain('account management')
    expect(zh.errorRateHint).toContain('HTTP'); expect(en.errorRateHint).toContain('HTTP')
  })
})
