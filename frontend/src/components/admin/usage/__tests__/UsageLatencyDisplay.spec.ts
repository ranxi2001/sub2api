import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import UsageTable from '../UsageTable.vue'
import UsageTimingDialog from '../UsageTimingDialog.vue'
import enDashboard from '@/i18n/locales/en/dashboard'
import zhDashboard from '@/i18n/locales/zh/dashboard'
import enTiming from '@/i18n/locales/en/requestTiming'
import zhTiming from '@/i18n/locales/zh/requestTiming'
import adminPage from '@/views/admin/UsageView.vue?raw'
import userPage from '@/views/user/UsageView.vue?raw'
import { serverLatency } from './fixtures/serverLatency'

vi.mock('@/stores/app', () => ({ useAppStore: () => ({}) }))
vi.mock('@/api/admin/usageTiming', () => ({ getUsageTiming: vi.fn(async () => ({ traces: [], retention_days: 30 })) }))

const wrappers: ReturnType<typeof mount>[] = []
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()); document.body.innerHTML = '' })

const locales = {
  en: { usage: enDashboard.usage, requestTiming: enTiming, empty: { noData: 'No data' }, common: { noData: 'No data' } },
  zh: { usage: zhDashboard.usage, requestTiming: zhTiming, empty: { noData: '暂无数据' }, common: { noData: '暂无数据' } },
}
function global(locale: 'en' | 'zh') {
  return { plugins: [createI18n({ legacy: false, locale, messages: locales })] }
}

describe('server latency display without pre-forward wait disclosure', () => {
  it('is shared by both admin and user usage pages', () => {
    for (const page of [adminPage, userPage]) {
      expect(page).toContain("import UsageTable from '@/components/admin/usage/UsageTable.vue'")
      expect(page).toContain('<UsageTable')
    }
    expect(adminPage).toContain('enable-timing-details')
    expect(userPage).not.toContain('enable-timing-details')
  })

  for (const locale of ['en', 'zh'] as const) {
    for (const audience of ['admin', 'user'] as const) {
      it(`keeps 2020/18950ms unchanged without 97ms in ${locale} ${audience} table or tooltip`, () => {
        const wrapper = mount(UsageTable, {
          props: {
            data: [serverLatency], columns: [{ key: 'latency', label: 'Latency' }],
            enableTimingDetails: audience === 'admin', cacheRateAudience: audience,
            showAccountBilling: audience === 'admin',
          }, global: global(locale),
        })
        wrappers.push(wrapper)
        const latency = wrapper.get('[data-testid="latency-bar"]').element.parentElement!
        expect(latency.textContent).toContain('2.02s')
        expect(latency.textContent).toContain('18.95s')
        expect(latency.textContent).not.toMatch(/97ms|pre-forward|转发前|queuePortion/)
        const titles = Array.from(latency.querySelectorAll('[title]')).map(node => node.getAttribute('title')).join(' ')
        expect(titles).not.toMatch(/97ms|0\.097|1\.92s|18\.85s|\{queue\}/)
        expect(titles).toContain(locale === 'zh' ? '服务端' : 'Server')
        expect(latency.querySelector('.row-start-1.col-start-3')?.textContent?.trim()).toBe('2.02s')
        expect(latency.querySelector('.row-start-2.col-start-3')?.textContent?.trim()).toBe('18.95s')
        expect(wrapper.get('th').text()).toBe('Latency')
        expect(wrapper.get('[data-testid="latency-tps"]').text()).toBe('10.6 t/s')
      })
    }

    it(`keeps empty/unknown and historical values in ${locale}`, () => {
      const wrapper = mount(UsageTable, {
        props: { columns: [{ key: 'latency', label: 'Latency' }], data: [{
          ...serverLatency, queue_timing: { ...serverLatency.queue_timing!, status: 'unknown' },
          first_token_ms: null, duration_ms: null, output_tokens: 0,
        }] }, global: global(locale),
      })
      wrappers.push(wrapper)
      const latency = wrapper.get('[data-testid="latency-bar"]').element.parentElement!
      expect(latency.querySelector('.row-start-1.col-start-3')?.textContent).toBe('-')
      expect(latency.querySelector('.row-start-2.col-start-3')?.textContent?.trim()).toBe('-')
      expect(latency.textContent).not.toContain('97ms')
      expect(latency.textContent).toContain(locale === 'zh' ? '旧口径' : 'legacy')
      const empty = mount(UsageTable, { props: { data: [], columns: [{ key: 'latency', label: 'Latency' }] }, global: global(locale) })
      wrappers.push(empty)
      expect(empty.text()).toContain(locales[locale].empty.noData)
    })

    it(`uses concise server titles and no wait summary in the ${locale} timing dialog`, async () => {
      const wrapper = mount(UsageTimingDialog, {
        props: { record: serverLatency }, attachTo: document.body, global: global(locale),
      })
      wrappers.push(wrapper)
      await flushPromises()
      const body = document.body.querySelector('.modal-body')!
      const labels = Array.from(body.querySelectorAll('.rounded-xl.border.p-4 > .text-sm')).map(node => node.textContent)
      expect(labels).toContain(locale === 'zh' ? '服务端首字' : 'Server TTFT')
      expect(labels).toContain(locale === 'zh' ? '服务端总耗时' : 'Server total')
      expect(labels.join(' ')).not.toMatch(/pre-forward|转发前/)
      expect(body.textContent).toContain('2.02s')
      expect(body.textContent).toContain('18.95s')
      expect(body.textContent).not.toContain('97ms')
      expect(body.textContent).toContain(locales[locale].requestTiming.empty)
      expect(body.textContent).toContain(locales[locale].requestTiming.missing)
    })
  }
})
