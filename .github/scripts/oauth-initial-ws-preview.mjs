// Render the actual AutoConfigView with fictional data; PNGs stay outside Git.
import { existsSync, mkdirSync, mkdtempSync, realpathSync, rmSync, symlinkSync, unlinkSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const [repoArg, outputArg, baseline = '1c3882e935a01b376f9aefdf844863125fff7c2b'] = process.argv.slice(2)
if (!repoArg || !outputArg) throw Error('Usage: node oauth-initial-ws-preview.mjs REPO OUTPUT [BASELINE_SHA]')
const repo = realpathSync(repoArg), root = join(repo, 'frontend'), output = resolve(outputArg)
const deps = resolve(process.env.SUB2API_PREVIEW_DEPS || root)
const req = createRequire(join(deps, 'package.json'))
const { createServer } = await import(pathToFileURL(join(dirname(req.resolve('vite/package.json')), 'dist/node/index.js')).href)
const vue = (await import(pathToFileURL(req.resolve('@vitejs/plugin-vue')).href)).default
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE || req.resolve('playwright')).href)
const git = (...args) => execFileSync('git', args, { cwd: repo, encoding: 'utf8' }).trimEnd()
const sourceHead = git('rev-parse', 'HEAD'), baseHead = git('rev-parse', baseline)
const cacheDir = mkdtempSync(join(tmpdir(), 'oauth-initial-ws-preview-'))
const port = Number(process.env.SUB2API_PREVIEW_PORT || 4299), origin = `http://127.0.0.1:${port}`
const entryName = `.oauth-ws-preview-${process.pid}.js`, entry = join(root, entryName)
const errors = [], browserWarnings = [], captures = [], requests = []
let server, browser, context, linked = false, before = false
mkdirSync(output, { recursive: true })
try {
  if (!existsSync(join(root, 'node_modules'))) {
    symlinkSync(join(deps, 'node_modules'), join(root, 'node_modules'), 'dir')
    linked = true
  }
  writeFileSync(entry, `
import {mount} from '@vue/test-utils';
import {h} from 'vue';
import {createPinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {apiClient} from '/src/api/client.ts';
import {defaultExcelBPSDefaults} from '/src/utils/excelBPSDefaults.ts';
import {defaultOpenAITwoFAAdmissionConfig} from '/src/utils/openaiTwoFAAdmission.ts';
import {defaultModelBillingConfig} from '/src/utils/modelBilling.ts';
import {defaultOAuthModelMappings} from '/src/utils/oauthModelMappings.ts';
import View from '/src/views/admin/AutoConfigView.vue';
import '/src/style.css';
const locale=new URL(location.href).searchParams.get('locale')||'zh';
await loadLocaleMessages(locale);i18n.global.locale.value=locale;
let config={enabled:false,platform:'openai',priority:50,load_factor:1,concurrency:3,cost_multiplier:0.07,group_ids:[],upgrade_enabled:false,upgrade_group_ids:[],successes_per_step:20,upgrade_step:1,max_concurrency:100,cooldown_seconds:60,revision:'synthetic-preview',openai_totp_rotate_on_429:false,openai_disable_scheduling_on_429:false,openai_two_fa_admission:defaultOpenAITwoFAAdmissionConfig(),excel_bps:defaultExcelBPSDefaults(),model_billing:defaultModelBillingConfig(),model_mappings:defaultOAuthModelMappings('openai')};
const groups=[{id:5,name:'OpenAI Demo',platform:'openai',status:'active'},{id:6,name:'Anthropic Demo',platform:'anthropic',status:'active'}];
window.__previewRequests=[];
apiClient.defaults.adapter=async request=>{let data;window.__previewRequests.push({method:request.method,url:request.url});switch(request.url){
case '/admin/account-ops/auto-config':if(request.method==='put'){config=typeof request.data==='string'?JSON.parse(request.data):request.data;window.__saved=structuredClone(config)}data=structuredClone(config);break;
case '/admin/groups/all':data=groups;break;
case '/admin/groups':data={items:groups,page:1,pages:1,page_size:100,total:groups.length};break;
case '/admin/account-quality-plans':data=[];break;
default:throw Error('Unexpected preview API: '+request.url);
}return {config:request,status:200,statusText:'OK',headers:{},data:{code:0,data}}};
document.getElementById('app').innerHTML='<p class="p-5 text-sm text-gray-500">'+(locale==='en'?'Smart operations → Auto configuration · Actual component with fictional data':'智能运维 → 自动配置 · 实际组件 / 虚构数据')+'</p><div id="preview"></div>';
window.preview=mount(View,{attachTo:'#preview',global:{plugins:[createPinia(),i18n],stubs:{AppLayout:{setup(_, {slots}){return()=>h('main',{class:'min-h-screen bg-gray-50 p-5 text-gray-900 dark:bg-dark-950 dark:text-gray-100'},slots.default?.())}},SmartOpsNav:true,BPSDefaultsCard:true,OpenAITwoFAAdmissionCard:true,AutoConfigHistory:true,OAuthInitialModelSelection:true}}});
`, { flag: 'wx' })
  process.chdir(root)
  server = await createServer({
    root, configFile: false, cacheDir,
    resolve: { alias: { '@': join(root, 'src') } },
    plugins: [{ name: 'oauth-ws-preview', enforce: 'pre', load(id) {
      const clean = id.split('?')[0]
      if (clean === join(root, 'src/stores/auth.ts')) return 'export const useAuthStore=()=>({user:{id:99999,role:"admin"},isAdmin:true})'
      if (before && !id.includes('?') && clean === join(root, 'src/views/admin/AutoConfigView.vue')) return git('show', baseHead + ':frontend/src/views/admin/AutoConfigView.vue')
    }, configureServer(vite) {
      vite.middlewares.use((request, response, next) => {
        if (request.url === '/' || request.url?.startsWith('/?')) {
          response.setHeader('Content-Type', 'text/html')
          response.end('<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/' + entryName + '"></script></html>')
        } else if (request.url === '/favicon.ico') { response.statusCode = 204; response.end() } else next()
      })
    } }, vue()],
    server: { host: '127.0.0.1', port, strictPort: true, fs: { allow: [root, deps, realpathSync(join(root, 'node_modules'))] } },
    optimizeDeps: { entries: [], include: ['vue', 'pinia', 'vue-i18n', 'vue-router', 'axios', '@vue/test-utils', 'marked', 'dompurify'] }
  })
  await server.listen()
  browser = process.env.SUB2API_CDP_ENDPOINT ? await chromium.connectOverCDP(process.env.SUB2API_CDP_ENDPOINT) : await chromium.launch({ headless: true })
  context = await browser.newContext({ viewport: { width: 1180, height: 1000 }, locale: 'zh-CN' })
  const page = await context.newPage()
  page.on('pageerror', error => { errors.push(error.message); console.error(error.message) })
  page.on('console', message => {
    if (message.type() !== 'error') return
    const text = message.text()
    // Chromium's unload policy warning is unrelated to rendering or fixture APIs.
    if (text === 'Permissions policy violation: unload is not allowed in this document.') browserWarnings.push(text)
    else { errors.push(text); console.error(text) }
  })
  await page.route('**/*', route => {
    const url = new URL(route.request().url())
    if (url.origin === origin || url.protocol === 'data:') return route.continue()
    errors.push('External request blocked: ' + url.origin)
    return route.abort()
  })
  const card = () => page.getByTestId('initial-enabled').locator('xpath=ancestor::section[1]')
  const capture = async name => { await card().screenshot({ path: join(output, name) }); captures.push(name) }
  before = true
  await page.goto(origin, { waitUntil: 'networkidle', timeout: 90000 })
  await page.getByTestId('initial-enabled').waitFor({ timeout: 60000 })
  await capture('before-initial-config.png')
  before = false
  server.moduleGraph.invalidateAll()
  await page.reload({ waitUntil: 'networkidle' })
  await page.getByTestId('initial-ws-mode-select').waitFor({ timeout: 60000 })
  await capture('after-defaults.png')
  await page.getByTestId('initial-enabled').check()
  await page.getByTestId('initial-group-5').check()
  await page.getByTestId('initial-ws-mode-select').selectOption('http_bridge')
  await page.getByTestId('initial-ws-sse-acceleration-enabled').check()
  await page.locator('form').evaluate(form => form.requestSubmit())
  await page.waitForFunction(() => window.__saved?.openai_ws_mode === 'http_bridge' && window.__saved?.openai_ws_sse_acceleration === true)
  await capture('after-saved-http-bridge.png')
  await page.goto(origin + '/?locale=en', { waitUntil: 'networkidle' })
  await page.getByTestId('initial-ws-mode-select').waitFor()
  await capture('after-english.png')
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await page.setViewportSize({ width: 390, height: 844 })
  await capture('after-mobile-dark.png')
  requests.push(...await page.evaluate(() => window.__previewRequests))
  writeFileSync(join(output, 'manifest.json'), JSON.stringify({ sourceHead, baseline: baseHead, captures, requests, errors, browserWarnings, boundary: 'Actual AutoConfigView and project styles; synthetic API/auth and unrelated child components are stubbed. Component preview only, no production, real upstream or full navigation acceptance.' }, null, 2) + '\n')
  if (errors.length) throw Error(errors.join('\n'))
  console.log('Captured OAuth initial WS component evidence at ' + output)
} finally {
  await context?.close()
  if (!process.env.SUB2API_CDP_ENDPOINT) await browser?.close()
  await server?.close()
  if (existsSync(entry)) unlinkSync(entry)
  if (linked) unlinkSync(join(root, 'node_modules'))
  rmSync(cacheDir, { recursive: true, force: true })
}
