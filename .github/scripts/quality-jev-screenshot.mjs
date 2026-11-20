// CI-only visual evidence: real view, synthetic modules, no application backend.
import { createRequire } from 'node:module'
import path from 'node:path'
import fs from 'node:fs/promises'
const root = process.cwd(), frontend = path.join(root, 'frontend')
const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vueModule = requireFrontend('@vitejs/plugin-vue')
const requireBrowser = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))
const { chromium } = requireBrowser('playwright')
const output = process.env.QUALITY_SCREENSHOT_DIR
await fs.mkdir(output, { recursive: true })
const modules = {
  auth: `import {reactive} from 'vue'; const auth=reactive({user:{id:1,role:'admin'}}); export const useAuthStore=()=>auth;`,
  layout: `import {h} from 'vue'; export default {setup(_, {slots}){return ()=>h('main',{},slots.default?.())}};`,
  nav: `import {h} from 'vue'; export default {render(){return h('nav','质量运维 · 虚构数据')}};`,
  quality: `export const listQualityPlans=async()=>[]; export const listQualityOperations=async()=>({items:[],next_cursor:0}); export const listQualityTemplates=async()=>[]; export const listQualityPresets=async()=>[]; export const runQualityPlan=async()=>{}; export const createQualityPreset=async()=>{}; export const updateQualityPreset=async()=>{}; export const createQualityTemplate=async()=>{}; export const updateQualityTemplate=async()=>{}; export const deleteQualityTemplate=async()=>{}; export const previewQualityOwnership=async()=>({entries:[]}); export const reconcileQualityOwnership=async()=>{}; export const restoreQualityOwnership=async()=>{}; export const removeQualityOwnedModel=async()=>{};`,
  accounts: `export const list=async()=>({items:[{id:1,name:'虚构账号',platform:'openai',type:'oauth',status:'active'}],total:1}); export const getAntigravityDefaultModelMapping=async()=>({}); export const getAvailableModels=async()=>[]; export const getModelReasoning=async()=>({supported_reasoning_levels:[],default_reasoning_level:''}); export const probeOpenAICodexState=async()=>{throw new Error('unexpected_account_diagnostic')}; export const accountsAPI={list,getAntigravityDefaultModelMapping,getAvailableModels,getModelReasoning,probeOpenAICodexState}; export default accountsAPI;`,
  groups: `export const getModelAllowlistCandidates=async()=>['synthetic-judge']; export const getAllIncludingInactive=async()=>[{id:21,name:'虚构普通判题分组',status:'active',platform:'openai'}]; export const groupsAPI={getModelAllowlistCandidates,getAllIncludingInactive}; export default groupsAPI;`,
  scheduled: `export const scheduledTestsAPI={create:async()=>({id:7}),update:async()=>({id:7}),delete:async()=>{},listResults:async()=>[],getResult:async()=>({})}; export default scheduledTestsAPI;`,
}
// Real JS fixtures let Vite's dependency scanner classify imports that replace
// .vue files; a virtual ID there is incorrectly treated as an HTML entry.
const fixtures = path.join(frontend, 'node_modules', '.quality-jev-fixtures')
await fs.mkdir(fixtures, { recursive: true })
for (const [name, source] of Object.entries(modules)) await fs.writeFile(path.join(fixtures, `${name}.mjs`), source)
const aliases = [
  ['@/stores/auth', 'auth'], ['@/components/layout/AppLayout.vue', 'layout'],
  ['@/components/admin/operations/SmartOpsNav.vue', 'nav'],
  ['@/api/admin/accountQuality', 'quality'], ['@/api/admin/accounts', 'accounts'],
  ['@/api/admin/groups', 'groups'], ['@/api/admin/scheduledTests', 'scheduled'],
].map(([find, name]) => ({ find, replacement: path.join(fixtures, `${name}.mjs`) }))
const html = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module">
import {createApp} from 'vue'; import {createPinia} from 'pinia'; import {createI18n} from 'vue-i18n'; import {createRouter,createMemoryHistory} from 'vue-router';
import View from '/src/views/admin/AccountQualityView.vue'; import zh from '/src/i18n/locales/zh/qualityOps.ts'; import en from '/src/i18n/locales/en/qualityOps.ts'; import '/src/style.css';
const i18n=createI18n({legacy:false,locale:'zh',messages:{zh:{qualityOps:zh},en:{qualityOps:en}},missingWarn:false,fallbackWarn:false});window.qualityI18n=i18n;
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/',component:View}]});
createApp(View).use(createPinia()).use(i18n).use(router).mount('#app');</script>`
process.chdir(frontend)
const server = await createServer({
  root: frontend, configFile: false,
  plugins: [(vueModule.default || vueModule)(), {
    name: 'quality-jev-offline',
    resolveId(id, importer) {
      if (id === './auth' && importer?.endsWith('/stores/accountQuality.ts')) return path.join(fixtures, 'auth.mjs')
    },
    configureServer(vite) { vite.middlewares.use((req, res, next) => {
      if (req.url === '/') { res.setHeader('Content-Type', 'text/html'); void vite.transformIndexHtml('/', html).then(value => res.end(value)); return }
      next()
    }) },
  }],
  resolve: { alias: [...aliases, { find: '@', replacement: path.join(frontend, 'src') }] },
  css: { postcss: { plugins: [requireFrontend('tailwindcss')({ config: path.join(frontend, 'tailwind.config.js') }), requireFrontend('autoprefixer')()] } },
  server: { host: '127.0.0.1', port: 4181, strictPort: true },
  optimizeDeps: { include: ['vue', 'vue-i18n', 'vue-router', 'pinia'] },
})
await server.listen()
const browser = await chromium.launch({ headless: true })
const failures = [], blocked = []
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 1100 } })
  page.on('pageerror', error => failures.push(error.message))
  // Any accidental backend or external request fails the evidence job.
  await page.route('**/*', route => {
    const url = new URL(route.request().url())
    if (url.origin !== 'http://127.0.0.1:4181' || url.pathname.startsWith('/api/')) { blocked.push(url.pathname); return route.abort() }
    return route.continue()
  })
  await page.goto('http://127.0.0.1:4181/', { waitUntil: 'networkidle' })
  await page.getByTestId('quality-create-rule').click()
  const kind = page.getByTestId('quality-judge-kind')
  await kind.waitFor()
  if (await kind.inputValue() !== 'llm') throw new Error('legacy_default_changed')
  const grading = kind.locator('..').locator('..')
  await grading.screenshot({ path: path.join(output, 'zh-default-llm.png') })
  await kind.selectOption('jev')
  await page.getByTestId('quality-jev-hint').waitFor()
  if (await page.locator('#quality-judge-models').count()) throw new Error('jev_shows_ordinary_routing')
  await grading.screenshot({ path: path.join(output, 'zh-jev.png') })
  await page.evaluate(() => { window.qualityI18n.global.locale.value = 'en' })
  await grading.screenshot({ path: path.join(output, 'en-jev.png') })
  await page.setViewportSize({ width: 440, height: 1000 })
  await grading.screenshot({ path: path.join(output, 'en-jev-mobile.png') })
  await page.evaluate(() => { window.qualityI18n.global.locale.value = 'zh' })
  const question = page.getByTestId('quality-question-kind')
  for (const [device, width] of [['mobile', 390], ['desktop', 1280]]) {
    await page.setViewportSize({ width, height: 1000 })
    await question.selectOption('candy')
    await question.scrollIntoViewIfNeeded()
    await page.screenshot({ path: path.join(output, `zh-quality-candy-${device}.png`) })
    await question.selectOption('ciallo')
    await page.getByTestId('quality-ciallo-hint').waitFor()
    if (await kind.count()) throw new Error('ciallo_requires_external_judge')
    await question.scrollIntoViewIfNeeded()
    await page.screenshot({ path: path.join(output, `zh-quality-ciallo-${device}.png`) })
  }
  // Use the real account modal, locales and model picker. Its model directory
  // is supplied by the local adapter; screenshots never start a model call.
  const accountHtml = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module">
    import {createApp,h} from 'vue'; import {createPinia} from 'pinia'; import {createI18n} from 'vue-i18n';
    import IQ from '/src/components/admin/account/IQTestModal.vue'; import zh from '/src/i18n/locales/zh'; import '/src/style.css';
    import api from '/src/api/client'; api.defaults.adapter=async req=>({data:{code:0,data:req.url.endsWith('/reasoning')?{supported_reasoning_levels:['medium'],default_reasoning_level:'medium'}:[{id:'gpt-6-astra'}]},status:200,statusText:'OK',config:req,headers:{}});
    const app=createApp({render:()=>h(IQ,{show:true,account:{id:1,name:'虚构账号',platform:'openai',type:'apikey',credentials:{},status:'active'}})});
    app.use(createPinia()).use(createI18n({legacy:false,locale:'zh',messages:{zh}})).mount('#app');</script>`
  const accountServer = await createServer({
    root: frontend, configFile: false, cacheDir: path.join(fixtures, 'iq-vite'),
    plugins: [(vueModule.default || vueModule)(), { name:'iq-preview', configureServer(vite) {
      vite.middlewares.use((req, res, next) => {
        if (req.url !== '/') return next()
        res.setHeader('Content-Type', 'text/html')
        void vite.transformIndexHtml('/', accountHtml).then(html => res.end(html))
      })
    } }],
    resolve: { alias: { '@': path.join(frontend, 'src') } },
    css: { postcss: { plugins: [requireFrontend('tailwindcss')({ config: path.join(frontend, 'tailwind.config.js') }), requireFrontend('autoprefixer')()] } },
    server: { host:'127.0.0.1', port:4182, strictPort:true },
  })
  try {
    await accountServer.listen()
    const iq = await browser.newPage({locale:'zh-CN'})
    iq.on('pageerror', err => failures.push(err.message))
    await iq.route('**/*', route => {
      const u = new URL(route.request().url())
      if (u.origin !== 'http://127.0.0.1:4182' || u.pathname.startsWith('/api/')) { blocked.push(u.pathname); return route.abort() }
      return route.continue()
    })
    for (const [device,width] of [['mobile',390],['desktop',1280]]) {
      await iq.setViewportSize({width,height:1000})
      await iq.goto('http://127.0.0.1:4182/')
      const selector = iq.getByTestId('question-select')
      await selector.waitFor()
      await iq.screenshot({path:path.join(output,`zh-account-candy-${device}.png`),animations:'disabled'})
      await selector.getByRole('button').first().click()
      await iq.getByRole('option', { name:'Ciallo 检测' }).click()
      await iq.getByTestId('ciallo-panel').waitFor()
      await iq.screenshot({path:path.join(output,`zh-account-ciallo-${device}.png`),animations:'disabled'})
    }
  } finally { await accountServer.close() }
  if (failures.length || blocked.length) throw new Error('offline_render_failed')
  await fs.writeFile(path.join(output, 'verification.json'), JSON.stringify({ commit: process.env.QUALITY_HEAD_SHA, component: 'AccountQualityView.vue', synthetic: true, paidRequests: 0, applicationApiCalls: 0, failures, blocked }, null, 2))
} catch (err) {
  await fs.writeFile(path.join(output, 'failure.json'), JSON.stringify({ message: err.message, failures, blocked }, null, 2))
  throw err
} finally { await browser.close(); await server.close() }
