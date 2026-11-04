import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { describe, expect, it } from 'vitest'
import NewFeatureBadge from '../NewFeatureBadge.vue'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import { useNewFeatureNoticeStore } from '@/stores/newFeatureNotices'

describe('VIP auto-credit notice lifecycle', () => {
  it('keeps A dismissed, synchronizes B entries, persists refresh and isolates C/admin/browser boundaries', async () => {
    localStorage.clear()
    let pinia = createPinia(); setActivePinia(pinia)
    const a = NEW_FEATURE_NOTICES.protocolFeatureSwitches
    const b = NEW_FEATURE_NOTICES.vipAutoCredit
    const c = 'fixture-next-feature-C'
    expect(new Set(Object.values(NEW_FEATURE_NOTICES)).size).toBe(Object.values(NEW_FEATURE_NOTICES).length)
    useNewFeatureNoticeStore().dismiss(41, a)
    const i18n = createI18n({ legacy: false, locale: 'en', messages: { en: { common: { dismissNewFeature: 'Dismiss', newFeature: 'New' } } } })
    const render = (id: string, admin = 41) => mount(NewFeatureBadge, { props: { noticeId: id, userId: admin }, global: { plugins: [pinia, i18n] } })
    const old = render(a); const entry = render(b); const dialog = render(b)
    expect(old.find('button').exists()).toBe(false)
    expect(entry.find('button').exists()).toBe(true)
    await entry.get('button').trigger('click')
    expect(dialog.find('button').exists()).toBe(false)
    old.unmount(); entry.unmount(); dialog.unmount()
    pinia = createPinia(); setActivePinia(pinia)
    const refreshed = render(b); const otherAdmin = render(b, 42); const next = render(c); const stillOld = render(a)
    expect(refreshed.find('button').exists()).toBe(false)
    expect(otherAdmin.find('button').exists()).toBe(true)
    expect(next.find('button').exists()).toBe(true)
    expect(stillOld.find('button').exists()).toBe(false)
    for (const w of [refreshed, otherAdmin, next, stillOld]) w.unmount()
    localStorage.clear(); pinia = createPinia(); setActivePinia(pinia)
    const otherBrowser = render(b); expect(otherBrowser.find('button').exists()).toBe(true); otherBrowser.unmount()
  })
})
