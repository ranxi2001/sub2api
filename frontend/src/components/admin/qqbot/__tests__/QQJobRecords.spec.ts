import { mount, flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import QQJobRecords from '../QQJobRecords.vue'

const api = vi.hoisted(() => ({ jobs: vi.fn(), members: vi.fn().mockResolvedValue({ app_id: 'app', items: [], total: 0, page: 1, page_size: 20 }) }))
vi.mock('@/api/qqbot', () => ({ qqbotAPI: api }))

describe('image intent receipts', () => {
  it.each(['zh', 'en'])('shows retry diagnostics without treating missing usage as confirmed zero in %s', async locale => {
    api.jobs.mockResolvedValue({ items: [{
      id: 'retry-job', group_id: 'group', member_id: 'member', kind: 'chat', status: 'completed', error: '',
      input_tokens: 0, output_tokens: 0, created_at: '2026-10-30T08:00:00Z',
      chat_execution: { attempts: 3, retries: 2, usage_complete: false, last_error_code: 'http_503' }
    }, {
      id: 'known-job', group_id: 'group', member_id: 'member', kind: 'chat', status: 'completed', error: '',
      input_tokens: 40, output_tokens: 20, created_at: '2026-10-30T07:00:00Z',
      chat_execution: { attempts: 2, retries: 1, usage_complete: true, last_error_code: 'no_action' }
    }, {
      id: 'legacy-job', group_id: 'group', member_id: 'member', kind: 'chat', status: 'completed', error: '',
      input_tokens: 10, output_tokens: 5, created_at: '2026-10-29T07:00:00Z'
    }], total: 3, page: 1, page_size: 20 })
    const i18n = createI18n({ legacy: false, locale, messages: { zh, en } })
    const w = mount(QQJobRecords, { global: { plugins: [i18n] } }); await flushPromises()
    const [incomplete, complete, legacy] = w.findAll('article')
    expect(incomplete.get('[data-testid="qq-job-chat-execution"]').text()).toContain(i18n.global.t('qqbot.chatExecution.attempts', { attempts: 3, retries: 2 }))
    expect(incomplete.text()).toContain('http_503')
    expect(incomplete.text()).toContain(`${i18n.global.t('qqbot.chatExecution.knownTokens')}: 0 / 0`)
    expect(incomplete.get('[data-testid="qq-job-usage-incomplete"]').text()).toBe(i18n.global.t('qqbot.chatExecution.incompleteUsage'))
    expect(complete.text()).toContain('Tokens: 40 / 20')
    expect(complete.find('[data-testid="qq-job-usage-incomplete"]').exists()).toBe(false)
    expect(legacy.find('[data-testid="qq-job-chat-execution"]').exists()).toBe(false)
    expect(legacy.find('[data-testid="qq-job-usage-incomplete"]').exists()).toBe(false)
    w.unmount()
  })
  it.each(['zh', 'en'])('filters and separates site query outcome from delivery state in %s', async locale => {
    api.jobs.mockResolvedValue({ items: [{
      id: 'query-job', group_id: 'group', member_id: 'member', kind: 'chat', status: 'completed', error: '',
      input_tokens: 22, output_tokens: 14, created_at: '2026-10-30T08:00:00Z',
      site_query: { kind: 'model_price', model: 'model-test', activity: '', page: 2, status: 'failed', error_code: 'provider_error', duration_ms: 15 }
    }], total: 1, page: 1, page_size: 20 })
    const i18n = createI18n({ legacy: false, locale, messages: { zh, en } })
    const w = mount(QQJobRecords, { global: { plugins: [i18n] } }); await flushPromises()
    await w.get('[data-testid="qq-job-query-kind"]').setValue('model_price')
    await w.get('[data-testid="qq-job-query-status"]').setValue('failed')
    await w.get('form').trigger('submit'); await flushPromises()
    expect(api.jobs).toHaveBeenLastCalledWith(expect.objectContaining({ query_kind: 'model_price', query_status: 'failed', page: 1 }))
    expect(w.get('article strong').text()).toContain('completed')
    const query = w.get('[data-testid="qq-job-site-query"]')
    expect(query.text()).toContain(i18n.global.t('qqbot.siteQuery.statuses.failed'))
    expect(query.text()).toContain('model-test')
    expect(query.text()).toContain('provider_error')
    expect(query.text()).toContain(i18n.global.t('qqbot.siteQuery.help'))
    w.unmount()
  })
  it.each(['zh', 'en'])('filters and labels personal read-only query records in %s', async locale => {
    const queryKinds = ['account_balance', 'today_usage', 'lottery_chances']
    api.jobs.mockResolvedValue({ items: queryKinds.map(kind => ({
      id: kind, group_id: 'group', member_id: 'member', kind: 'chat', status: 'completed', error: '',
      input_tokens: 0, output_tokens: 0, created_at: '2026-11-02T08:00:00Z',
      site_query: { kind, model: '', activity: '', page: 1, status: 'succeeded', error_code: '', duration_ms: 5 }
    })), total: 3, page: 1, page_size: 20 })
    const i18n = createI18n({ legacy: false, locale, messages: { zh, en } })
    const w = mount(QQJobRecords, { global: { plugins: [i18n] } }); await flushPromises()
    const filter = w.get('[data-testid="qq-job-query-kind"]')
    for (const [index, kind] of queryKinds.entries()) {
      const key = 'qqbot.siteQuery.kinds.' + kind
      const label = i18n.global.t(key)
      expect(label).not.toBe(key)
      expect(filter.get('option[value="' + kind + '"]').text()).toBe(label)
      expect(w.findAll('[data-testid="qq-job-site-query"]')[index]!.text()).toContain(label)
      await filter.setValue(kind)
      await w.get('form').trigger('submit'); await flushPromises()
      expect(api.jobs).toHaveBeenLastCalledWith(expect.objectContaining({ query_kind: kind, page: 1 }))
    }
    w.unmount()
  })
  it.each(['zh', 'en'])('renders review, execution and scoped references in %s', async locale => {
    api.jobs.mockResolvedValue({ items: [{
      id: 'job-id', group_id: 'group', member_id: 'member', kind: 'chat', status: 'completed',
      error: '', input_tokens: 22, output_tokens: 14, created_at: '2026-10-23T08:00:00Z',
      image_decision: { tool: 'generate_image', review: 'text', basis: 'none', reason: 'not_image_request',
        execution: 'not_started', message_ref: 'message-hash', plan_ref: 'plan-hash' }
    }, { id: 'legacy', group_id: 'group', member_id: 'member', kind: 'chat', status: 'completed', error: '',
      input_tokens: 11, output_tokens: 7, created_at: '2026-10-23T07:00:00Z', image_decision: {} }], total: 2, page: 1, page_size: 20 })
    const i18n = createI18n({ legacy: false, locale, messages: { zh, en } })
    const w = mount(QQJobRecords, { global: { plugins: [i18n] } })
    await flushPromises()
    expect(w.findAll('article')).toHaveLength(2)
    expect(w.text()).toContain(i18n.global.t('qqbot.workspace.imageDecision'))
    expect(w.text()).toContain('generate_image → text → not_started')
    expect(w.text()).toContain('not_image_request')
    expect(w.text()).toContain('message-hash')
    expect(w.text()).toContain('plan-hash')
    expect(w.findAll('article')[1].text()).not.toContain(i18n.global.t('qqbot.workspace.imageDecision'))
    w.unmount()
  })
  it.each(['zh', 'en'])('filters spam report reviews and displays decisions, tokens and execution caveats in %s', async locale => {
    api.jobs.mockResolvedValue({ items: [{
      id: 'report-job', group_id: 'group', member_id: 'reporter', kind: 'spam_report', status: 'completed',
      trigger_reason: 'quoted_message_report', decision_reason: 'spam_phishing', error: '',
      input_tokens: 220, output_tokens: 40, created_at: '2026-10-24T01:00:00Z'
    }], total: 1, page: 1, page_size: 20 })
    const i18n = createI18n({ legacy: false, locale, messages: { zh, en } })
    const w = mount(QQJobRecords, { global: { plugins: [i18n] } })
    await flushPromises()
    const filter = w.get('[data-testid="qq-job-kind"]')
    expect(filter.get('option[value="spam_report"]').text()).toBe(i18n.global.t('qqbot.workspace.jobKinds.spam_report'))
    await filter.setValue('spam_report')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(api.jobs).toHaveBeenLastCalledWith(expect.objectContaining({ kind: 'spam_report', page: 1 }))
    const record = w.get('article')
    expect(record.text()).toContain(i18n.global.t('qqbot.workspace.jobKinds.spam_report'))
    expect(record.text()).toContain('quoted_message_report → spam_phishing')
    expect(record.text()).toContain('Tokens: 220 / 40')
    expect(record.text()).toContain(i18n.global.t('qqbot.workspace.reportReviewHelp'))
    w.unmount()
  })
})
