// Remote CI only. Actual components/CSS; synthetic Axios adapter and browser-local persistence.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { createRequire } from 'node:module'
import { mkdirSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const root = resolve('frontend')
const output = process.env.SCREENSHOT_DIR
const baseline = 'd7feb61edabc2858800c78dc0ef963ddb989e9f8'
const sourceFix = '3d6c47692f60e09d5549cf5a5e760cca5e2afad0'
const candidate = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
assert(output && process.env.PLAYWRIGHT_ROOT && process.env.CANDIDATE_SHA === candidate, 'Missing exact-head CI configuration')
execFileSync('git', ['merge-base', '--is-ancestor', sourceFix, candidate])
assert.equal(execFileSync('git', ['status', '--porcelain'], { encoding: 'utf8' }), '')
mkdirSync(output, { recursive: true })
const sha256 = value => createHash('sha256').update(value).digest('hex')
const require = createRequire(join(root, 'package.json'))
const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
const tailwindcss = require('tailwindcss')
const autoprefixer = require('autoprefixer')
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
const suffix = `.ci-claude-rectifier-${process.pid}`
const temporary = []
const create = (path, contents) => { writeFileSync(path, contents, { flag: 'wx' }); temporary.push(path); return path }
const builds = join(output, 'preview-build')
const beforeTree = join(output, 'baseline-source')
const origin = 'http://127.0.0.1:4198'
const sources = {}
const captures = []
const errors = []
const testSource = readFileSync(join(root, 'src/views/admin/__tests__/SettingsView.spec.ts'), 'utf8')
const start = testSource.indexOf('const baseSettingsResponse = {')
const end = testSource.indexOf('\n};', start) + 3
assert(start >= 0 && end > start, 'Missing shared synthetic settings fixture')
const settingsFixture = testSource.slice(start, end).replaceAll('sub3.nebula-spaces.com', 'fixture.example.invalid')
const entry = `
import {createApp} from 'vue';import {createPinia} from 'pinia';
import {createRouter,createWebHistory,RouterView} from 'vue-router';
import {i18n,initI18n} from '/src/i18n/index.ts';
import {apiClient} from '/src/api/client.ts';import {adminAPI} from '/src/api/index.ts';
import {useAuthStore} from '/src/stores/auth.ts';import {useAppStore} from '/src/stores/app.ts';
import View from '/src/views/admin/SettingsView.vue';import '/src/style.css';
${settingsFixture}
const mode=location.pathname.split('/')[1],scenario=new URL(location.href).searchParams.get('scenario');
if(!['before','after'].includes(mode)||!['unconfigured','stored_false'].includes(scenario))throw Error('Unknown fixture');
const storageKey='ci-claude-rectifier:'+mode+':'+scenario;
const common={enabled:true,thinking_signature_enabled:true,thinking_budget_enabled:true,apikey_signature_patterns:[]};
const stored=localStorage.getItem(storageKey);
const state={mode,scenario,settings:stored?JSON.parse(stored):(scenario==='stored_false'?{...common,apikey_signature_enabled:false}:null),reads:[],writes:[],requests:[]};
window.__rectifierEvidence=state;
apiClient.defaults.adapter=async request=>{
 const path=request.url.split('?')[0],method=request.method||'get';state.requests.push({path,method});
 if(method!=='get'&&!(method==='put'&&path==='/admin/settings/rectifier'))throw Error('Unexpected fixture write: '+method+' '+path);
 let data={};
 if(path==='/admin/settings/rectifier'){
  if(method==='put'){
   state.settings=typeof request.data==='string'?JSON.parse(request.data):structuredClone(request.data);
   state.writes.push(structuredClone(state.settings));localStorage.setItem(storageKey,JSON.stringify(state.settings));
  }
  // Omit this field deliberately: test the real frontend initializer, not a synthetic server default.
  data=state.settings?structuredClone(state.settings):structuredClone(common);
  if(method==='get')state.reads.push(structuredClone(data));
 }else if(path==='/admin/settings'||path==='/settings/public')data={...structuredClone(baseSettingsResponse),site_logo:'/'+mode+'/logo.svg',subscription_enabled:false,lottery_enabled:false};
 else if(path==='/admin/system/check-updates')data={current_version:'fixture',latest_version:'fixture',has_update:false,build_type:'source'};
 else if(path.includes('/announcements')||path.includes('/subscriptions/active'))data=[];
 else if(path.includes('/groups'))data=[];
 else if(path.includes('/proxies')||path==='/keys')data={items:[],total:0};
 else if(path.includes('/providers'))data={data:[]};
 else if(path.includes('/web-search'))data={enabled:false,providers:[]};
 else if(path.includes('/admin-api-key'))data={exists:false,masked_key:''};
 else if(path.includes('/serverless'))data={config:{enabled:false,pods:[],regions:[]},pods:[],stats:{}};
 else if(path.includes('/settings'))data={enabled:false,seconds:20,interval_minutes:60,debounce_minutes:1,window_minutes:60,rules:[]};
 return{data:{code:0,message:'success',data},status:200,statusText:'OK',headers:{},config:request};
};
state.readback=()=>adminAPI.settings.getRectifierSettings();
const pinia=createPinia();const router=createRouter({history:createWebHistory(),routes:[{path:'/:pathMatch(.*)*',component:View}]});
const app=createApp(RouterView);app.use(pinia);
useAuthStore(pinia).user={id:41,username:'fixture-admin',role:'admin',status:'active',email:'admin@example.invalid',balance:0,frozen_balance:0};
await initI18n();await useAppStore(pinia).fetchPublicSettings();app.use(router).use(i18n);await router.isReady();app.mount('#app');
`
const identityPaths = ['package.json', 'pnpm-lock.yaml', 'tailwind.config.js', 'postcss.config.js', 'src/style.css',
  'src/styles/onboarding.css', 'src/views/admin/SettingsView.vue', 'src/components/common/Toggle.vue',
  'src/components/layout/AppLayout.vue', 'src/components/layout/AppSidebar.vue', 'src/components/layout/AppHeader.vue',
  'src/api/admin/settings.ts', 'src/api/client.ts', 'src/i18n/locales/zh/admin/settings.ts']
let browser, server, activePage
try {
  mkdirSync(beforeTree)
  const archive = join(beforeTree, 'source.tar')
  execFileSync('git', ['archive', '--format=tar', `--output=${archive}`, baseline])
  execFileSync('tar', ['-xf', archive, '-C', beforeTree])
  const beforeRoot = join(beforeTree, 'frontend')
  for (const path of ['package.json', 'pnpm-lock.yaml']) {
    assert(readFileSync(join(beforeRoot, path)).equals(readFileSync(join(root, path))), `Dependency identity differs: ${path}`)
  }
  symlinkSync(join(root, 'node_modules'), join(beforeRoot, 'node_modules'), 'dir')
  for (const mode of ['before', 'after']) {
    const sourceRoot = mode === 'before' ? beforeRoot : root
    const revision = mode === 'before' ? baseline : candidate
    sources[mode] = { revision, files: {} }
    for (const path of identityPaths) {
      sources[mode].files[path] = {
        blob: execFileSync('git', ['rev-parse', revision + ':frontend/' + path], { encoding: 'utf8' }).trim(),
        sha256: sha256(readFileSync(join(sourceRoot, path))),
      }
    }
    const tailwindConfig = (await import(pathToFileURL(join(sourceRoot, 'tailwind.config.js')).href)).default
    const html = create(join(sourceRoot, suffix + '.html'), `<html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>请求整流器 CI fixture</title><div id="app"></div><script type="module" src="./${suffix}.js"></script></html>`)
    create(join(sourceRoot, suffix + '.js'), entry)
    // No component aliases: SettingsView, AppLayout, Toggle and their styles are real imports.
    await build({
      root: sourceRoot, base: './', configFile: false, cacheDir: join(output, 'vite-cache', mode),
      resolve: { alias: { '@': join(sourceRoot, 'src') } }, plugins: [vue()],
      css: { postcss: { plugins: [tailwindcss({ ...tailwindConfig, content: [html, join(sourceRoot, 'src/**/*.{vue,js,ts}')] }), autoprefixer()] } },
      build: { target: 'esnext', outDir: join(builds, mode), emptyOutDir: true, minify: false, rollupOptions: { input: html } },
    })
    sources[mode].compiled_css = readdirSync(join(builds, mode, 'assets')).filter(name => name.endsWith('.css')).map(name => ({
      file: name, sha256: sha256(readFileSync(join(builds, mode, 'assets', name))),
    }))
    assert(sources[mode].compiled_css.length > 0, 'Missing actual compiled CSS')
    mkdirSync(join(output, 'css', mode), { recursive: true })
    for (const css of sources[mode].compiled_css) {
      writeFileSync(join(output, 'css', mode, css.file), readFileSync(join(builds, mode, 'assets', css.file)))
      css.artifact_path = `css/${mode}/${css.file}`
    }
  }
  server = await preview({ root, base: '/', configFile: false, build: { outDir: builds }, preview: { host: '127.0.0.1', port: 4198, strictPort: true } })
  browser = await chromium.launch({ headless: true })
  for (const [device, viewport] of [['desktop', { width: 1280, height: 1000 }], ['mobile', { width: 390, height: 844 }]]) {
    const context = await browser.newContext({ viewport, locale: 'zh-CN', timezoneId: 'UTC', serviceWorkers: 'block' })
    await context.addInitScript(() => {
      localStorage.setItem('sub2api_locale', 'zh')
      localStorage.setItem('admin_guide_41_admin_v4_interactive', 'true')
    })
    await context.route('**/*', route => {
      const request = route.request(), url = new URL(request.url())
      if (url.origin === origin && request.method() === 'GET') return route.continue()
      errors.push('Blocked browser network: ' + request.method() + ' ' + url.origin)
      return route.abort()
    })
    const page = await context.newPage()
    activePage = page
    page.on('pageerror', error => errors.push(error.message))
    page.on('console', message => { if (message.type() === 'error') errors.push(message.text()) })
    page.on('response', response => { if (response.status() >= 400) errors.push('HTTP ' + response.status() + ' ' + response.url()) })
    const card = () => page.locator('.card').filter({ has: page.locator('#settings-section-rectifier') })
    const row = () => card().locator('div.flex.items-center.justify-between').filter({ has: page.locator('label').filter({ hasText: /^API Key 签名整流$/ }) })
    const toggle = () => row().getByRole('switch')
    const waitState = async expected => {
      await page.waitForFunction(() => window.__rectifierEvidence?.reads.length > 0)
      await toggle().waitFor({ state: 'visible' })
      await page.waitForFunction(value => {
        const label = [...document.querySelectorAll('#settings-section-rectifier ~ * label, .card label')].find(el => el.textContent.trim() === 'API Key 签名整流')
        return label?.parentElement.parentElement.querySelector('[role="switch"]')?.getAttribute('aria-checked') === String(value)
      }, expected)
      assert(await toggle().isEnabled(), 'Actual toggle disabled')
    }
    const go = async (mode, scenario, expected) => {
      await page.goto(`${origin}/${mode}/${suffix}.html?tab=gateway&scenario=${scenario}`)
      await waitState(expected)
    }
    const capture = async name => {
      await toggle().scrollIntoViewIfNeeded()
      await page.evaluate(() => document.fonts.ready)
      const layout = await row().evaluate(element => {
        const toggle = element.querySelector('[role="switch"]'), label = element.querySelector('label'), hint = element.querySelector('p')
        const bounds = el => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } }
        const style = el => { const s = getComputedStyle(el); return { color: s.color, backgroundColor: s.backgroundColor, fontSize: s.fontSize, display: s.display, opacity: s.opacity } }
        return { label: label.textContent.trim(), hint: hint.textContent.trim(), checked: toggle.getAttribute('aria-checked'),
          row: bounds(element), toggle: bounds(toggle), labelBox: bounds(label), hintBox: bounds(hint),
          switchStyle: style(toggle), labelStyle: style(label), hintStyle: style(hint),
          viewportWidth: innerWidth, scrollWidth: document.documentElement.scrollWidth }
      })
      assert.equal(layout.label, 'API Key 签名整流')
      assert(layout.hint.length > 10 && parseFloat(layout.labelStyle.fontSize) >= 12 && parseFloat(layout.hintStyle.fontSize) >= 12, 'Unreadable labels')
      for (const style of [layout.labelStyle, layout.hintStyle]) {
        assert(style.display !== 'none' && style.opacity === '1' && !['transparent', 'rgba(0, 0, 0, 0)'].includes(style.color), 'Hidden label styling')
      }
      assert.equal(layout.row.width > 0 && layout.toggle.width >= 40 && layout.toggle.height >= 20, true, 'Missing Tailwind layout')
      for (const box of [layout.toggle, layout.labelBox, layout.hintBox]) {
        assert(box.x >= 0 && box.x + box.width <= viewport.width + 1 && box.height > 0, 'Control/label overflows viewport')
      }
      assert(layout.labelBox.x + layout.labelBox.width <= layout.toggle.x + 1, 'Switch overlaps label')
      const state = await page.evaluate(() => {
        const { readback, ...state } = window.__rectifierEvidence
        return structuredClone(state)
      })
      const file = `${device}-${name}.png`
      await card().screenshot({ path: join(output, file), animations: 'disabled' })
      captures.push({ file, sha256: sha256(readFileSync(join(output, file))), device, viewport, layout, fixture: state })
      return layout
    }
    await go('before', 'unconfigured', false)
    assert.equal(await page.evaluate(() => Object.hasOwn(window.__rectifierEvidence.reads[0], 'apikey_signature_enabled')), false)
    const before = await capture('before-unconfigured')
    await go('after', 'unconfigured', true)
    assert.equal(await page.evaluate(() => Object.hasOwn(window.__rectifierEvidence.reads[0], 'apikey_signature_enabled')), false)
    const after = await capture('after-unconfigured')
    assert.notEqual(before.switchStyle.backgroundColor, after.switchStyle.backgroundColor, 'On/off actual CSS indistinguishable')
    await go('after', 'stored_false', false)
    assert.equal(await page.evaluate(() => window.__rectifierEvidence.reads[0].apikey_signature_enabled), false)
    await capture('stored-false')
    await toggle().click()
    await waitState(true)
    await capture('clicked-on')
    await toggle().click()
    await waitState(false)
    await card().getByRole('button', { name: '保存', exact: true }).click()
    await page.waitForFunction(() => window.__rectifierEvidence.writes.length === 1)
    const saved = await page.evaluate(() => window.__rectifierEvidence.writes[0])
    assert.deepEqual(saved, { enabled: true, thinking_signature_enabled: true, thinking_budget_enabled: true, apikey_signature_enabled: false, apikey_signature_patterns: [] })
    const readback = await page.evaluate(() => window.__rectifierEvidence.readback())
    assert.deepEqual(readback, saved, 'Actual settings GET did not read synthetic saved payload')
    await capture('saved-false')
    await page.reload()
    await waitState(false)
    assert.deepEqual(await page.evaluate(() => window.__rectifierEvidence.reads[0]), saved, 'Refresh lost synthetic persisted false')
    await capture('reloaded-false')
    await context.close()
    activePage = null
  }
  assert.deepEqual(errors, [], 'Browser/render errors')
  writeFileSync(join(output, 'manifest.json'), JSON.stringify({
    candidate_sha: candidate, baseline_sha: baseline, source_fix_sha: sourceFix,
    github: { run_id: process.env.GITHUB_RUN_ID, run_attempt: process.env.GITHUB_RUN_ATTEMPT, job: process.env.GITHUB_JOB },
    browser_version: browser.version(), sources, common_fixture_sha256: sha256(settingsFixture), generated_entry_sha256: sha256(entry),
    boundaries: 'Actual SettingsView, AppLayout, Toggle, Chinese labels and repository CSS/Tailwind theme/plugins. Minimal fixture router and synthetic admin identity/Axios data; localStorage simulates persistence. Unconfigured GET deliberately omits API Key field to isolate frontend initializer; real backend default response and database are not exercised. No component/style mocks, force clicks, provider requests, production login or external browser network.',
    network_policy: 'Browser permits GET only to 127.0.0.1:4198; synthetic PUT/GET uses Axios adapter without network.',
    captures, errors,
  }, null, 2) + '\n')
} catch (error) {
  writeFileSync(join(output, 'failure.txt'), String(error) + '\n' + JSON.stringify(errors) + '\n' + (activePage ? await activePage.locator('body').innerText().catch(() => '') : ''))
  throw error
} finally {
  if (browser) await browser.close()
  if (server) await new Promise((resolve, reject) => server.httpServer.close(error => error ? reject(error) : resolve()))
  for (const file of temporary) rmSync(file, { force: true })
  for (const directory of [builds, beforeTree, join(output, 'vite-cache')]) rmSync(directory, { recursive: true, force: true })
}
