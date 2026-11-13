// Synthetic data only; both source trees use this identical component harness.
import { createApp, h } from 'vue'
import { createI18n } from 'vue-i18n'
import Table from './src/components/modelPlaza/PlazaModelPricingTable.vue'
import zh from './src/i18n/locales/zh/dashboard.ts'
import en from './src/i18n/locales/en/dashboard.ts'
import './src/style.css'

const locale = new URLSearchParams(location.search).get('lang') || 'zh'
document.documentElement.lang = locale
const price = (billing_mode, per_request_price) => ({
  billing_mode, per_request_price, input_price: null, output_price: null,
  cache_write_price: null, cache_read_price: null, image_input_price: null,
  image_output_price: null, intervals: []
})
const model = (name, pricing) => ({ name, platform: 'openai', pricing, official_pricing: null })
const models = [
  model('demo-video-base', price('video', 0.54)),
  model('demo-video-resolution', {
    ...price('video', 0.54),
    intervals: [0.54, 1.08].map((value, index) => ({
      ...price('video', value), min_tokens: 0, max_tokens: null,
      tier_label: index === 0 ? '480p' : '720p'
    }))
  }),
  model('demo-image', price('image', 0.2)),
  model('demo-request', price('per_request', 0.04)),
  model('demo-token', {
    ...price('token', null), input_price: 2e-6, output_price: 8e-6,
    cache_write_price: 4e-6, cache_read_price: 1e-6
  })
]
const groups = [
  { id: 'group', title: 'Synthetic group rate 0.5', props: {} },
  { id: 'user', title: 'Synthetic user rate 0.25', props: { userRateMultiplier: 0.25 } },
  { id: 'independent', title: 'Synthetic video rate 2 / image rate 3', props: {
    userRateMultiplier: 0.25, videoRateIndependent: true, videoRateMultiplier: 2,
    imageRateIndependent: true, imageRateMultiplier: 3
  } }
]
const app = createApp({
  render: () => h('main', { class: 'min-h-screen bg-white p-6 text-gray-900 dark:bg-dark-900 dark:text-white' }, [
    h('h1', { class: 'mb-2 text-xl font-bold' }, `Model Plaza · ${locale} · synthetic component preview`),
    h('p', { class: 'mb-4 text-sm text-gray-500' }, 'Actual Vue component, locale and application CSS. No API calls.'),
    ...groups.map(group => h('section', { 'data-scenario': group.id, class: 'mb-5' }, [
      h('h2', { class: 'mb-2 font-semibold' }, group.title),
      h(Table, { models, platform: 'openai', rateMultiplier: 0.5, ...group.props })
    ]))
  ])
})
app.use(createI18n({ legacy: false, locale, messages: { zh, en } }))
app.mount('#app')
window.__issue825Ready = true
