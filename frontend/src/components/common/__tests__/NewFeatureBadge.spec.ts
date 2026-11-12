import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it } from 'vitest'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import NewFeatureBadge from '../NewFeatureBadge.vue'
import zhCommon from '@/i18n/locales/zh/common'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'

describe('NewFeatureBadge', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
  })

  function mountBadge(userId = 41) {
    const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh: zhCommon } })
    return mount(NewFeatureBadge, {
      props: { noticeId: 'protocol-switches', userId },
      global: { plugins: [i18n, createPinia()] },
    })
  }


  it.each([
    ['user ID copy', NEW_FEATURE_NOTICES.userIdCopy],
    ['Antigravity usage query', NEW_FEATURE_NOTICES.antigravityUsageQuery],
    ['OpenAI cache write TTL', NEW_FEATURE_NOTICES.openaiCacheWriteTTL12h],
    ['EasyPay refund code', NEW_FEATURE_NOTICES.easyPayRefundSuccessCode],
    ['payment provider fees', NEW_FEATURE_NOTICES.paymentProviderFees],
    ['settings save dock', NEW_FEATURE_NOTICES.settingsSaveDock],
    ['VIP automatic credit email', NEW_FEATURE_NOTICES.vipAutoCreditEmail],
    ['priority account performance', NEW_FEATURE_NOTICES.priorityAccountPerformance],
    ['dashboard account concurrency', NEW_FEATURE_NOTICES.dashboardAccountConcurrency],
    ['key risk', NEW_FEATURE_NOTICES.keyRisk],
    ['key source restrictions', NEW_FEATURE_NOTICES.keyRiskSources],
    ['API key usage dates', NEW_FEATURE_NOTICES.apiKeyUsageDateRange],
    ['pricing transfer', NEW_FEATURE_NOTICES.channelPricingTransfer],
    ['stream event timing', NEW_FEATURE_NOTICES.streamFirstEventTiming],
    ['Mistral budget usage', NEW_FEATURE_NOTICES.mistralBudgetUsage],
    ['Qwen platform', NEW_FEATURE_NOTICES.qwenPlatform],
    ['image header timeout groups', NEW_FEATURE_NOTICES.openaiImageHeaderTimeoutGroups],
    ['profit operations time ranges', NEW_FEATURE_NOTICES.profitOperationsTimeRanges],
    ['quality retest rest', NEW_FEATURE_NOTICES.qualityRetestRest],
    ['OAuth 401 quality cleanup', NEW_FEATURE_NOTICES.qualityOAuth401Cleanup],
    ['AI operations proposal cancellation', NEW_FEATURE_NOTICES.aiOpsProposalCancel],
  ])('keeps %s dismissal shared and persistent across feature announcements', async (_feature, noticeId) => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const store = useNewFeatureNoticeStore()
    const oldId = NEW_FEATURE_NOTICES.vipAutoCredit
    store.dismiss(41, oldId)
    const i18n = createI18n({ legacy: false, locale: 'zh', messages: { zh: zhCommon } })
    const badge = () => mount(NewFeatureBadge, {
      props: { noticeId, userId: 41 }, global: { plugins: [pinia, i18n] },
    })
    const first = badge()
    const second = badge()
    expect(first.find('button').exists()).toBe(true)
    await first.get('button').trigger('click')
    expect(second.find('button').exists()).toBe(false)
    first.unmount()
    second.unmount()
    setActivePinia(createPinia())
    const reloaded = useNewFeatureNoticeStore()
    expect(reloaded.isDismissed(41, noticeId)).toBe(true)
    expect(reloaded.isDismissed(41, oldId)).toBe(true)
    expect(reloaded.isDismissed(41, 'future-feature-C')).toBe(false)
  })

  it('hides a shared notice for the current admin when clicked', async () => {
    const wrapper = mountBadge()
    expect(wrapper.get('[data-testid="new-feature-badge"]').text()).toBe('新功能')

    await wrapper.get('[data-testid="new-feature-badge"]').trigger('click')

    expect(wrapper.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
    expect(useNewFeatureNoticeStore().isDismissed(41, 'protocol-switches')).toBe(true)
    expect(mountBadge().find('[data-testid="new-feature-badge"]').exists()).toBe(false)
    expect(mountBadge(42).find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  })
})
