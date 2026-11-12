import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import PaymentProviderList from '@/components/payment/PaymentProviderList.vue'
import PaymentProviderDialog from '@/components/payment/PaymentProviderDialog.vue'
import ProviderCard from '@/components/payment/ProviderCard.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'
import type { ProviderInstance } from '@/types/payment'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, values?: Record<string, unknown>) => `${key}${values && typeof values === 'object' ? ` ${Object.values(values).join(' ')}` : ''}` }),
}))

const provider: ProviderInstance = {
  id: 1, name: 'Example provider', provider_key: 'airwallex', config: {},
  supported_types: ['airwallex'], enabled: true, payment_mode: '',
  refund_enabled: false, allow_user_refund: false, limits: '', sort_order: 0,
  fee_rate: null, method_fee_rates: { airwallex: 0 },
}

describe('provider fee summaries and notices', () => {
  beforeEach(() => localStorage.clear())

  it('distinguishes inherited rates, explicit zero, and method overrides on cards', async () => {
    const wrapper = mount(ProviderCard, { props: { provider, enabled: true, availableTypes: [], globalFeeRate: 2.5 } })
    expect(wrapper.get('[data-testid="provider-card-fee"]').text()).toContain('feeGlobalSummary 2.5')
    expect(wrapper.text()).toContain('feeMethodOverrides 1')
    await wrapper.setProps({ provider: { ...provider, fee_rate: 0 } })
    expect(wrapper.get('[data-testid="provider-card-fee"]').text()).toContain('feeCustomSummary 0')
  })

  it.each([0, 1, 2])('dismisses the fee notice from entry %s across the list, card, and dialog', async (entry) => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const oldNotice = NEW_FEATURE_NOTICES.easyPayRefundSuccessCode
    useNewFeatureNoticeStore().dismiss(41, oldNotice)
    const list = mount(PaymentProviderList, {
      props: { providers: [provider], loading: false, canCreate: true, enabledPaymentTypes: ['airwallex'], allPaymentTypes: [], redirectLabel: '', userId: 41, globalFeeRate: 2.5 },
      global: { plugins: [pinia], stubs: { VueDraggable: { template: '<div><slot /></div>' } } },
    })
    const dialog = mount(PaymentProviderDialog, {
      props: { show: true, saving: false, editing: provider, allKeyOptions: [], enabledKeyOptions: [], allPaymentTypes: [], redirectLabel: '', userId: 41, globalFeeRate: 2.5 },
      global: { plugins: [pinia], stubs: { BaseDialog: { template: '<div><slot /></div>' } } },
    })
    const badges = () => [list, dialog].flatMap(wrapper => wrapper.findAllComponents(NewFeatureBadge)
      .filter(badge => badge.props('noticeId') === NEW_FEATURE_NOTICES.paymentProviderFees)
      .flatMap(badge => badge.findAll('[data-testid="new-feature-badge"]')))
    expect(badges()).toHaveLength(3)
    await badges()[entry]!.trigger('click')
    expect(badges()).toHaveLength(0)
    expect(dialog.emitted('save')).toBeUndefined()
    list.unmount()
    dialog.unmount()
    const reloadedPinia = createPinia()
    setActivePinia(reloadedPinia)
    const store = useNewFeatureNoticeStore()
    expect(store.isDismissed(41, oldNotice)).toBe(true)
    expect(store.isDismissed(41, NEW_FEATURE_NOTICES.paymentProviderFees)).toBe(true)
    const later = mount(NewFeatureBadge, { props: { noticeId: 'future-payment-feature', userId: 41 }, global: { plugins: [reloadedPinia] } })
    expect(later.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
    const reloaded = mount(NewFeatureBadge, { props: { noticeId: NEW_FEATURE_NOTICES.paymentProviderFees, userId: 41 }, global: { plugins: [reloadedPinia] } })
    expect(reloaded.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
  })
})
