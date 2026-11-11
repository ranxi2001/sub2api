// Isolated screenshot fixture: actual component and project CSS, fictional data.
// The browser harness intercepts /api/*; no real backend or paid API is used.
import { createApp, h, ref } from 'vue'
import { createPinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import UserAutoCreditModal from '@/components/admin/user/UserAutoCreditModal.vue'
import { useAuthStore } from '@/stores/auth'
import { NEW_FEATURE_NOTICES } from '@/constants/newFeatureNotices'
import NewFeatureBadge from '@/components/common/NewFeatureBadge.vue'
import en from '@/i18n/locales/en/autoCredit'
import zh from '@/i18n/locales/zh/autoCredit'
import type { AdminUser } from '@/types'
import '../style.css'
localStorage.setItem('auth_user', JSON.stringify({ id: 41, role: 'admin', email: 'admin@example.invalid' }))
localStorage.setItem('auth_token', 'offline-fixture-only')
const pinia = createPinia()
const app = createApp({
  setup() {
    const auth = useAuthStore()
    auth.$patch({ user: { id: 41, role: 'admin', email: 'admin@example.invalid' }, token: 'offline-fixture-only' })
    const show = ref(true)
    const user = { id: 2, email: 'vip-fixture@example.invalid' } as AdminUser
    return () => h('main', { class: 'min-h-screen bg-gray-100 p-6' }, [
      h('p', { class: 'mb-4' }, 'Isolated CSS fixture — fictional records'),
      h('div', { class: 'flex items-center gap-2' }, [
        h('button', { class: 'btn btn-primary', onClick: () => { show.value = true } }, 'VIP credit'),
        h(NewFeatureBadge, { noticeId: NEW_FEATURE_NOTICES.vipAutoCredit, userId: auth.user?.id }),
      ]),
      h(UserAutoCreditModal, { show: show.value, user, onClose: () => { show.value = false } }),
    ])
  },
})
const locale = new URLSearchParams(location.search).get('lang') === 'zh' ? 'zh' : 'en'
app.use(pinia).use(createI18n({ legacy: false, locale, messages: { en: { autoCredit: en, common: { newFeature: 'New', dismissNewFeature: 'Dismiss new feature' } }, zh: { autoCredit: zh, common: { newFeature: '新功能', dismissNewFeature: '关闭新功能提示' } } } })).mount('#app')
