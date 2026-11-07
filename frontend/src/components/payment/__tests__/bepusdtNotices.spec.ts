import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { nextTick } from 'vue'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import PaymentProviderDialog from '../PaymentProviderDialog.vue'
import ProviderCard from '../ProviderCard.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

enableAutoUnmount(afterEach)
beforeEach(() => localStorage.clear())

describe('BEpusdt announcement at provider entries', () => {
  it.each(['card', 'dialog'])('dismisses from the %s without editing the provider and persists across sessions', async (entry) => {
    const oldId = NEW_FEATURE_NOTICES.protocolFeatureSwitches
    localStorage.setItem('sub2api:new-feature-notices:v1:41', JSON.stringify([oldId]))
    const pinia = createPinia()
    const card = mount(ProviderCard, {
      props: {
        userId: 41,
        provider: {
          id: 1, provider_key: 'bepusdt', name: 'BEP20 collection',
          config: { mode: 'embedded', currency: 'CNY' }, supported_types: ['usdt.bep20'],
          enabled: true, payment_mode: '', refund_enabled: false, allow_user_refund: false,
          limits: '', sort_order: 0,
        },
        enabled: true,
        availableTypes: [{ value: 'usdt.bep20', label: 'USDT (BEP20)' }],
      },
      global: { plugins: [pinia] },
    })
    const dialog = mount(PaymentProviderDialog, {
      props: {
        userId: 41, show: true, saving: false, editing: null,
        allKeyOptions: [{ value: 'bepusdt', label: 'BEpusdt' }],
        enabledKeyOptions: [{ value: 'bepusdt', label: 'BEpusdt' }],
        allPaymentTypes: [{ value: 'bepusdt', label: 'BEpusdt' }], redirectLabel: 'Redirect',
      },
      global: {
        plugins: [pinia],
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          Select: true, ToggleSwitch: true,
        },
      },
    })
    ;(dialog.vm as unknown as { reset: (key: string) => void }).reset('bepusdt')
    await nextTick()
    expect(card.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
    expect(dialog.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
    await (entry === 'card' ? card : dialog).get('[data-testid="new-feature-badge"]').trigger('click')
    await nextTick()
    expect(card.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
    expect(dialog.find('[data-testid="new-feature-badge"]').exists()).toBe(false)
    for (const event of ['toggleField', 'toggleType', 'edit', 'delete']) {
      expect(card.emitted(event)).toBeUndefined()
    }
    expect(dialog.emitted('save')).toBeUndefined()
    expect((dialog.get('[data-testid="bepusdt-mode"]').element as HTMLSelectElement).value).toBe('embedded')
    card.unmount()
    dialog.unmount()

    const newSession = createPinia()
    for (const noticeId of [oldId, NEW_FEATURE_NOTICES.bepusdtPayment, 'future-payment-announcement-C']) {
      const badge = mount(NewFeatureBadge, {
        props: { userId: 41, noticeId }, global: { plugins: [newSession] },
      })
      expect(badge.find('[data-testid="new-feature-badge"]').exists()).toBe(noticeId === 'future-payment-announcement-C')
    }
    const otherAdmin = mount(NewFeatureBadge, {
      props: { userId: 42, noticeId: NEW_FEATURE_NOTICES.bepusdtPayment }, global: { plugins: [newSession] },
    })
    expect(otherAdmin.find('[data-testid="new-feature-badge"]').exists()).toBe(true)
  })
})
