// Actual AutoConfigView, its model component and API modules; synthetic API only.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import path from 'node:path'
import fs from 'node:fs/promises'
const root = process.cwd(), frontend = path.join(root, 'frontend')
const head = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim()
const baselineSHA = '1411d0fc6e1fca51d4b657c21837cd74c87bf84e'
const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vueModule = requireFrontend('@vitejs/plugin-vue'), vue = vueModule.default || vueModule
const { chromium } = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))('playwright')
const output = process.env.OAUTH_DEGRADATION_SCREENSHOT_DIR
await fs.mkdir(output, { recursive: true })
const baselinePath = path.join(frontend, 'src/views/admin/OAuthDegradationBaseline.vue')
await fs.writeFile(baselinePath, execFileSync('git', ['show', baselineSHA + ':frontend/src/views/admin/AutoConfigView.vue'], { cwd: root }))
const virtual = {
 'fixture:auth': `export const useAuthStore=()=>({user:{id:41,role:'admin'}})`,
 'fixture:layout': `import {h} from 'vue';export default {setup:(_,ctx)=>()=>h('main',{class:'mx-auto max-w-6xl p-5'},[h('p',{class:'mb-4 text-sm text-amber-700'},'CI fixture · 虚构数据 · 非生产'),ctx.slots.default?.()])}`,
 'fixture:nav': `export default {render:()=>null}`,
 'fixture:whitelist': `export default {render:()=>null}`
}
const html = `<!doctype html><meta charset="utf-8"><div id="app"></div><script type="module">
import {createApp} from 'vue';import {createPinia} from 'pinia';
const {default:Page}=new URLSearchParams(location.search).has('baseline')?await import('/src/views/admin/OAuthDegradationBaseline.vue'):await import('/src/views/admin/AutoConfigView.vue');
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {defaultExcelBPSDefaults} from '/src/utils/excelBPSDefaults.ts';
import {defaultOpenAITwoFAAdmissionConfig} from '/src/utils/openaiTwoFAAdmission.ts';
import '/src/style.css';
window.fixture={enabled:true,platform:'openai',priority:50,load_factor:1,concurrency:3,cost_multiplier:0.07,group_ids:[5],upgrade_enabled:false,upgrade_group_ids:[],successes_per_step:20,upgrade_step:1,max_concurrency:100,cooldown_seconds:60,revision:'synthetic',excel_bps:defaultExcelBPSDefaults(),openai_two_fa_admission:defaultOpenAITwoFAAdmissionConfig(),model_mappings:[],model_billing:{enabled:false,rules:[]},selected_models:null};
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
  { find: '@', replacement: path.join(frontend, 'src') },
  { find: 'vue-i18n', replacement: 'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js' }
 ] },
 server: { host: '127.0.0.1', port: 4183, strictPort: true },
 optimizeDeps: { include: ['vue', 'pinia', 'vue-i18n', 'axios'] }
})
await server.listen()
const browser = await chromium.launch({ headless: true })
const errors = [], external = [], saves = [], requests = []
let saved
const groups = [{ id: 5, name: 'OpenAI 示例组', platform: 'openai', status: 'active' }, { id: 6, name: 'Anthropic 示例组', platform: 'anthropic', status: 'active' }]
try {
 const context = await browser.newContext({ viewport: { width: 1280, height: 1000 } })
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
 assert.equal(await page.getByTestId('degradation-action').count(), 0)
 await page.screenshot({ path: path.join(output, 'before-zh.png'), fullPage: true })
 await open()
 const card=page.getByTestId('degradation-action')
 const annotate=()=>page.addStyleTag({content:'[data-testid=degradation-action]::before{content:"CI fixture · 虚构数据 · 非生产";display:block;color:#92400e;font-size:12px}'})
 assert.equal(await page.getByTestId('degradation-enabled').isChecked(),false)
 assert.equal(await page.getByTestId('degradation-threshold').inputValue(),'')
 assert.equal(await page.getByTestId('degradation-concurrency').inputValue(),'10')
 assert.equal(await page.getByTestId('degradation-limit-concurrency').isChecked(),true)
 assert.equal(await page.getByTestId('degradation-limit-models').isChecked(),true)
 assert.equal(await page.getByTestId('degradation-limit-groups').isChecked(),false)
 await annotate(); await card.screenshot({path:path.join(output,'after-disabled-zh.png')})
 const picker=card.getByTestId('degradation-models')
 await picker.locator('.cursor-pointer').click()
 await picker.getByTestId('model-option').filter({hasText:'gpt-6-luna'}).getByTestId('select-model').click()
 await picker.locator('.cursor-pointer').click()
 await page.getByTestId('degradation-threshold').fill('25')
 await page.getByTestId('degradation-limit-groups').check()
 await page.getByTestId('degradation-group-5').check()
 await page.getByTestId('degradation-enabled').check()
 await save()
 assert.equal(saved.degradation_action.threshold_percent,25)
 assert.equal(saved.degradation_action.enabled,true)
 assert.equal(saved.degradation_action.target_concurrency,10)
 assert.equal(saved.degradation_action.limit_concurrency,true)
 assert.equal(saved.degradation_action.limit_models,true)
 assert.equal(saved.degradation_action.limit_groups,true)
 assert.deepEqual(saved.degradation_action.group_ids,[5])
 assert(!saved.degradation_action.models.includes('gpt-6-luna'))
 await page.reload({waitUntil:'networkidle'})
 assert.equal(await page.getByTestId('degradation-enabled').isChecked(),true)
 assert.equal(await page.getByTestId('degradation-threshold').inputValue(),'25')
 await annotate(); await card.screenshot({path:path.join(output,'after-enabled-zh.png')})
 await page.setViewportSize({width:390,height:1000})
 await annotate(); await card.screenshot({path:path.join(output,'after-mobile-zh.png')})
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))
 await card.getByTestId('new-feature-badge').click()
 await page.reload({waitUntil:'networkidle'})
 assert.equal(await card.getByTestId('new-feature-badge').count(),0)
 await page.setViewportSize({width:1280,height:1000})
 await page.getByTestId('degradation-limit-concurrency').uncheck()
 await page.getByTestId('degradation-limit-models').uncheck()
 await save()
 assert.equal(saved.degradation_action.limit_concurrency,false)
 assert.equal(saved.degradation_action.limit_models,false)
 assert.equal(saved.degradation_action.limit_groups,true)
 await page.reload({waitUntil:'networkidle'})
 assert.equal(await page.getByTestId('degradation-concurrency').count(),0)
 assert.equal(await page.getByTestId('degradation-models').count(),0)
 assert.equal(await page.getByTestId('degradation-group-5').isChecked(),true)
 await annotate(); await card.screenshot({path:path.join(output,'after-groups-only-zh.png')})
 await open('?lang=en')
 await annotate(); await card.screenshot({path:path.join(output,'after-enabled-en.png')})
 assert.deepEqual(external,[]); assert.deepEqual(errors,[])
 await fs.writeFile(path.join(output,'evidence.json'),JSON.stringify({head,baselineSHA,fixture:true,external,errors,saves,requests},null,2))
} finally {
 await browser.close();await server.close();await fs.rm(baselinePath,{force:true})
}
