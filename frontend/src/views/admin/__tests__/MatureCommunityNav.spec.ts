import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { createMemoryHistory, createRouter } from 'vue-router'
import { expect, it } from 'vitest'
import SmartOpsNav from '@/components/admin/operations/SmartOpsNav.vue'
import { useAuthStore } from '@/stores/auth'
import type { User } from '@/types'

it('exposes the QQ community overview to administrators', async () => {
  const pinia = createPinia(); setActivePinia(pinia)
  useAuthStore().user = { id: 1, role: 'admin' } as User
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] })
  await router.push('/admin/qqbot/overview'); await router.isReady()
  const i18n = createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: { en: {} } })
  const w = mount(SmartOpsNav, { global: { plugins: [pinia, router, i18n] } })
  expect(w.find('a[href="/admin/qqbot/overview"]').exists(), 'community operations must be reachable from the existing operations navigation').toBe(true)
  w.unmount()
})
