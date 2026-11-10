// Actual AutoConfigView, its model component and API modules; synthetic API only.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import path from 'node:path'
import fs from 'node:fs/promises'
const root = process.cwd(), frontend = path.join(root, 'frontend')
const head = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim()
const baselineSHA = '7eb2e271acdc4b36d62f1b542b16b5abb5b1bb09'
const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vueModule = requireFrontend('@vitejs/plugin-vue'), vue = vueModule.default || vueModule
const { chromium } = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))('playwright')
const output = process.env.OAUTH_SELECTION_SCREENSHOT_DIR
await fs.mkdir(output, { recursive: true })
const baselinePath = path.join(frontend, 'src/views/admin/OAuthSelectionBaseline.vue')
await fs.writeFile(baselinePath, execFileSync('git', ['show', baselineSHA + ':frontend/src/views/admin/AutoConfigView.vue'], { cwd: root }))
const virtual = {
 'fixture:auth': `export const useAuthStore=()=>({user:{id:41,role:'admin'}})`,
 'fixture:layout': `import {h} from 'vue';export default {setup:(_,ctx)=>()=>h('main',{class:'mx-auto max-w-6xl p-5'},ctx.slots.default?.())}`,
 'fixture:nav': `export default {render:()=>null}`,
 'fixture:whitelist': `export default {render:()=>null}`
}
const html = `<!doctype html><meta charset="utf-8"><div id="app"></div><script type="module">
import {createApp} from 'vue';import {createPinia} from 'pinia';
const {default:Page}=new URLSearchParams(location.search).has('baseline')?await import('/src/views/admin/OAuthSelectionBaseline.vue'):await import('/src/views/admin/AutoConfigView.vue');
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {defaultExcelBPSDefaults} from '/src/utils/excelBPSDefaults.ts';
import {defaultOpenAITwoFAAdmissionConfig} from '/src/utils/openaiTwoFAAdmission.ts';
import {defaultOpenAIAPIKeyFallbackConfig} from '/src/utils/openaiAPIKeyFallback.ts';
import '/src/style.css';
window.fixture={enabled:true,platform:'openai',priority:50,load_factor:1,concurrency:3,cost_multiplier:0.07,group_ids:[5],upgrade_enabled:false,upgrade_group_ids:[],successes_per_step:20,upgrade_step:1,max_concurrency:100,cooldown_seconds:60,revision:'synthetic',excel_bps:defaultExcelBPSDefaults(),openai_two_fa_admission:defaultOpenAITwoFAAdmissionConfig(),openai_apikey_fallback:defaultOpenAIAPIKeyFallbackConfig(),openai_disable_scheduling_on_429_7d_threshold_percent:98,model_mappings:[],model_billing:{enabled:false,rules:[]},selected_models:null};
const lang=new URLSearchParams(location.search).get('lang')||'zh';await loadLocaleMessages(lang);i18n.global.locale.value=lang;
createApp(Page).use(i18n).use(createPinia()).mount('#app');</script>`
process.chdir(frontend) // Tailwind/PostCSS resolve the actual frontend config.
const server = await createServer({
 root: frontend, configFile: false, envFile: false,
 define: { __INTLIFY_JIT_COMPILATION__: true, 'import.meta.env.VITE_API_BASE_URL': JSON.stringify('/api/v1') },
 plugins: [vue(), {
  name: 'oauth-selection-offline-page',
  resolveId(id) { if (id in virtual) return '\0' + id },
  load(id) { return virtual[id.slice(1)] },
  configureServer(vite) { vite.middlewares.use((req, res, next) => {
   if (req.url?.split('?')[0] !== '/') return next()
   res.setHeader('Content-Type', 'text/html')
   void vite.transformIndexHtml('/', html).then(value => res.end(value))
  }) }
 }],
 resolve: { alias: [
  { find: '@/stores/auth', replacement: 'fixture:auth' },
  { find: '@/components/layout/AppLayout.vue', replacement: 'fixture:layout' },
  { find: '@/components/admin/operations/SmartOpsNav.vue', replacement: 'fixture:nav' },
  { find: '@/components/account/ModelWhitelistSelector.vue', replacement: 'fixture:whitelist' },
  { find: '@', replacement: path.join(frontend, 'src') },
  { find: 'vue-i18n', replacement: 'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js' }
 ] },
 server: { host: '127.0.0.1', port: 4183, strictPort: true },
 // This fixture injects its HTML; unrelated repository HTML cannot load its virtual modules during dep scanning.
 optimizeDeps: { entries: [], include: ['vue', 'pinia', 'vue-i18n', 'axios'] }
})
await server.listen()
const browser = await chromium.launch({ headless: true })
const errors = [], external = [], saves = [], requests = []
let saved
const groups = [{ id: 5, name: 'OpenAI 示例组', platform: 'openai', status: 'active' }, { id: 6, name: 'Anthropic 示例组', platform: 'anthropic', status: 'active' }]
try {
 const context = await browser.newContext({ viewport: { width: 1280, height: 1000 }, timezoneId: 'UTC' })
 await context.route('**/*', async route => {
  const url = new URL(route.request().url())
  if (url.origin !== 'http://127.0.0.1:4183') { external.push(url.origin); return route.abort() }
  if (!url.pathname.startsWith('/api/v1/')) return route.continue()
  requests.push({ path: url.pathname, method: route.request().method() })
  let data
  if (url.pathname === '/api/v1/admin/account-ops/auto-config') {
   if (route.request().method() === 'PUT') { saved = route.request().postDataJSON(); saves.push(structuredClone(saved)) }
   data = saved ?? await route.request().frame().evaluate(() => window.fixture)
  } else if (url.pathname === '/api/v1/admin/groups/all') data = groups
  else if (url.pathname === '/api/v1/admin/accounts') {
   assert.equal(route.request().method(), 'GET')
   // This page's account GET is the fallback picker: paused raw-active keys must reach its local eligibility filter.
   assert.deepEqual(Object.fromEntries(url.searchParams), { page: '1', page_size: '100', platform: 'openai', type: 'apikey', lite: '1', sort_by: 'id', sort_order: 'asc', timezone: 'UTC' })
   data = { items: [], page: 1, page_size: 100, pages: 1, total: 0 }
  }
  else if (url.pathname === '/api/v1/admin/groups') data = { items: groups, page: 1, page_size: 100, pages: 1, total: 2 }
  else if (url.pathname === '/api/v1/admin/account-quality-plans') data = []
  else if (url.pathname === '/api/v1/admin/account-ops/auto-config/events') data = { items: [], has_more: false }
  else throw new Error('Unexpected mock API: ' + url.pathname)
  await route.fulfill({ json: { code: 0, message: '', data } })
 })
 const page = await context.newPage()
 page.on('pageerror', error => errors.push(error.message))
 const open = async suffix => { await page.goto('http://127.0.0.1:4183/' + (suffix ?? ''), { waitUntil: 'networkidle' }); await page.getByTestId('initial-enabled').waitFor() }
 const save = async () => {
  const response = page.waitForResponse(r => r.url().includes('/account-ops/auto-config') && r.request().method() === 'PUT')
  await page.locator('button[type=submit]').click(); await response
  await page.waitForFunction(() => !document.querySelector('button[type=submit]')?.disabled)
 }
 await open('?baseline=1')
 assert.equal(await page.getByTestId('initial-model-selection').count(), 0)
 await page.getByTestId('initial-enabled').locator('xpath=ancestor::section').screenshot({ path: path.join(output, 'before-initial-template-zh.png') })
 await open()
 await page.getByTestId('apikey-fallback-loading').waitFor({ state: 'detached' })
 assert.equal(await page.getByTestId('apikey-fallback-accounts-error').count(), 0)
 assert.equal(await page.getByTestId('apikey-fallback-enabled').isChecked(), false)
 assert(requests.some(request => request.path === '/api/v1/admin/accounts' && request.method === 'GET'))
 const scope = page.getByTestId('initial-model-selection')
 const initialSection = page.getByTestId('initial-enabled').locator('xpath=ancestor::section')
 const rotation = page.getByTestId('totp-rotate-on-429')
 const disableScheduling = page.getByTestId('disable-scheduling-on-429')
 assert.equal(await rotation.isChecked(), false)
 assert.equal(await disableScheduling.isChecked(), false)
 await initialSection.screenshot({ path: path.join(output, '429-off-desktop-zh.png') })
 // The scheduling policy is independent of credential rotation. Exercise the
 // real checkbox, API payload and reloaded page; do not infer this from labels.
 await disableScheduling.check()
 await save()
 assert.equal(saved.openai_disable_scheduling_on_429, true)
 assert.equal(saved.openai_totp_rotate_on_429, false)
 await page.reload({ waitUntil: 'networkidle' })
 assert.equal(await disableScheduling.isChecked(), true)
 assert.equal(await rotation.isChecked(), false)
 await initialSection.screenshot({ path: path.join(output, 'scheduling429-only-desktop-zh.png') })
 assert.equal(await page.getByTestId('model-selection-enabled').isChecked(), false)
 await scope.screenshot({ path: path.join(output, 'unset-desktop-zh.png') })
 await page.getByTestId('model-selection-enabled').check()
 await scope.locator('[data-model="gpt-6-luna"]').uncheck()
 await rotation.check()
 await save()
 assert(saved.selected_models.includes('gpt-6.1-sol')); assert(!saved.selected_models.includes('gpt-6-luna'))
 assert.equal(saved.openai_totp_rotate_on_429, true)
 assert.equal(saved.openai_disable_scheduling_on_429, true)
 await initialSection.screenshot({ path: path.join(output, '429-and-selection-desktop-zh.png') })
 await scope.screenshot({ path: path.join(output, 'excluded-luna-desktop-zh.png') })
 await page.setViewportSize({ width: 390, height: 1000 })
 await initialSection.screenshot({ path: path.join(output, '429-and-selection-mobile-zh.png') })
 await scope.screenshot({ path: path.join(output, 'excluded-luna-mobile-zh.png') })
 assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
 await page.reload({ waitUntil: 'networkidle' })
 assert.equal(await rotation.isChecked(), true)
 assert.equal(await disableScheduling.isChecked(), true)
 assert.equal(await scope.locator('[data-model="gpt-6-luna"]').isChecked(), false)
 await initialSection.screenshot({ path: path.join(output, '429-reloaded-mobile-zh.png') })
 await disableScheduling.uncheck()
 await save()
 assert.equal(saved.openai_disable_scheduling_on_429, false)
 assert.equal(saved.openai_totp_rotate_on_429, true)
 await page.reload({ waitUntil: 'networkidle' })
 assert.equal(await disableScheduling.isChecked(), false)
 assert.equal(await rotation.isChecked(), true)
 await disableScheduling.check()
 await page.getByTestId('model-selection-none').click()
 await page.getByTestId('model-selection-empty').waitFor()
 await save(); assert.deepEqual(saved.selected_models, [])
 await scope.screenshot({ path: path.join(output, 'none-mobile-zh.png') })
 await page.getByTestId('model-selection-enabled').uncheck(); await save(); assert.equal(saved.selected_models, null)
 await page.getByTestId('platform').selectOption('anthropic')
 assert.equal(await rotation.count(), 0)
 assert.equal(await disableScheduling.count(), 0)
 await page.getByTestId('initial-group-6').check()
 await page.getByTestId('model-selection-enabled').check()
 assert.equal(await scope.locator('[data-model="gpt-6-luna"]').count(), 0)
 assert.equal(await scope.locator('[data-model="claude-sonnet-4-6"]').isChecked(), true)
 await page.getByTestId('model-selection-custom').fill('custom-model')
 await page.getByTestId('model-selection-add').click(); await save()
 assert(saved.selected_models.includes('custom-model'))
 assert.equal(saved.openai_totp_rotate_on_429, false)
 assert.equal(saved.openai_disable_scheduling_on_429, false)
 await page.setViewportSize({ width: 1280, height: 1000 })
 await open('?lang=en')
 await scope.screenshot({ path: path.join(output, 'anthropic-custom-desktop-en.png') })
 await page.getByTestId('platform').selectOption('openai')
 await page.getByTestId('initial-group-5').check()
 assert.equal(await disableScheduling.isChecked(), false)
 await disableScheduling.check()
 await save()
 assert.equal(saved.openai_disable_scheduling_on_429, true)
 assert.equal(saved.openai_totp_rotate_on_429, false)
 await initialSection.screenshot({ path: path.join(output, 'scheduling429-only-desktop-en.png') })
 await page.setViewportSize({ width: 390, height: 1000 })
 await initialSection.screenshot({ path: path.join(output, 'scheduling429-only-mobile-en.png') })
 assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
 assert.deepEqual(errors, []); assert.deepEqual(external, [])
 await fs.writeFile(path.join(output, 'verification.json'), JSON.stringify({ head, baselineSHA, synthetic: true, actualPage: 'AutoConfigView.vue', actualComponent: 'OAuthInitialModelSelection.vue', cssRoot: frontend, checks: ['old template before screenshot', 'unset', 'select catalog and exclude Luna', 'save and reload', 'empty explicitly denies all', 'turn off null', '429 scheduling policy independent of rotation; both save/reload together', '429 scheduling opt-out preserves rotation after reload', '429 desktop/mobile screenshots', 'platform switch clears both 429 policies', 'custom exact name', 'desktop and mobile', 'no horizontal overflow', 'zh and en', 'no external requests'], requests, saves, errors, external }, null, 2))
} catch (error) {
 await fs.writeFile(path.join(output, 'failure.json'), JSON.stringify({ head, error: String(error), errors, external, requests }, null, 2)); throw error
} finally { await browser.close(); await server.close(); await fs.rm(baselinePath, { force: true }) }
