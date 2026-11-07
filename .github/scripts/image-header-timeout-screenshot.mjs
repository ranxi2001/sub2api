// CI only: actual SettingsView and controls, synthetic API, no production login.
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const root = resolve('frontend')
const output = process.env.SCREENSHOT_DIR
if (!output || !process.env.SOURCE_BASE || !process.env.PLAYWRIGHT_ROOT) throw Error('Missing CI screenshot configuration')
mkdirSync(output, { recursive: true })
const require = createRequire(join(root, 'package.json'))
const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
const tailwindcss = require('tailwindcss')
const autoprefixer = require('autoprefixer')
const tailwindConfig = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
const suffix = `.ci-image-header-${process.pid}`
const files = []
const create = (path, text) => { writeFileSync(path, text, { flag: 'wx' }); files.push(path); return path }
const previewDirectory = join(output, 'preview-build')
const testSource = readFileSync(join(root, 'src/views/admin/__tests__/SettingsView.spec.ts'), 'utf8')
const fixtureStart = testSource.indexOf('const baseSettingsResponse = {')
const fixtureEnd = testSource.indexOf('\n};', fixtureStart) + 3
if (fixtureStart < 0 || fixtureEnd < fixtureStart) throw Error('Settings fixture not found')
const baseFixture = testSource.slice(fixtureStart, fixtureEnd).replaceAll('sub3.nebula-spaces.com', 'fixture.example.invalid')
const errors = []
let browser, server
try {
  const baseline = create(join(root, 'src/views/admin', suffix + '-before.vue'),
    execFileSync('git', ['show', `${process.env.SOURCE_BASE}:frontend/src/views/admin/SettingsView.vue`], { encoding: 'utf8' }))
  const shell = create(join(root, suffix + '-layout.js'), `import {h} from 'vue';export default {setup(_,ctx){return()=>h('main',{class:'p-4 bg-gray-50 dark:bg-dark-900'},ctx.slots.default?.())}}`)
  const html = create(join(root, suffix + '.html'), `<html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="./${suffix}.js"></script></html>`)
  create(join(root, suffix + '.js'), `
    import {createApp,h} from 'vue';import {createPinia} from 'pinia';import {createRouter,createWebHistory,RouterView} from 'vue-router';
    import {createI18n} from 'vue-i18n';import {apiClient} from '/src/api/client.ts';import {useAuthStore} from '/src/stores/auth.ts';
    import Before from '/src/views/admin/${suffix}-before.vue';import After from '/src/views/admin/SettingsView.vue';
    import zh from '/src/i18n/locales/zh/index.ts';import '/src/style.css';
    ${baseFixture}
    const initial={enabled:true,seconds:60,image_group_ids:[7],image_seconds:180};
    const state={settings:JSON.parse(localStorage.getItem('fixture-image-timeout')||'null')||initial,writes:[]};window.__imageTimeoutFixture=state;
    const groups=[{id:7,name:'虚构生图组 A',platform:'openai',status:'active',rate_multiplier:1,subscription_type:'standard'},
      {id:8,name:'虚构生图组 B',platform:'openai',status:'active',rate_multiplier:1,subscription_type:'standard'}];
    apiClient.defaults.adapter=async request=>{
      const path=request.url.split('?')[0];let data={};
      if(path.endsWith('/openai-response-header-timeout')){
        if(request.method==='put'){state.settings=typeof request.data==='string'?JSON.parse(request.data):structuredClone(request.data);state.writes.push(structuredClone(state.settings));localStorage.setItem('fixture-image-timeout',JSON.stringify(state.settings))}
        data=structuredClone(state.settings);
      }else if(path==='/admin/settings')data=structuredClone(baseSettingsResponse);
      else if(path.includes('/groups'))data=groups;
      else if(path.includes('/proxies'))data={items:[]};
      else if(path.includes('/providers'))data={data:[]};
      else if(path.includes('/web-search'))data={enabled:false,providers:[]};
      else if(path.includes('/admin-api-key'))data={exists:false,masked_key:''};
      else if(path.includes('/serverless'))data={config:{enabled:false,pods:[],regions:[]},pods:[],stats:{}};
      else if(path.includes('/settings')||path.includes('rectifier')||path.includes('beta-policy'))data={enabled:false,seconds:20,interval_minutes:60,debounce_minutes:1,window_minutes:60,rules:[]};
      return{data:{code:0,message:'success',data},status:200,statusText:'OK',headers:{},config:request};
    };
    const mode=new URLSearchParams(location.search).get('mode');
    const pinia=createPinia(),router=createRouter({history:createWebHistory(),routes:[{path:'/:pathMatch(.*)*',component:mode==='before'?Before:After}]});
    const app=createApp(RouterView);app.use(pinia);useAuthStore().user={id:41,role:'admin',status:'active',email:'admin@example.invalid'};
    app.use(router).use(createI18n({legacy:false,locale:'zh-CN',messages:{'zh-CN':zh}})).mount('#app');
  `)
  await build({
    root, configFile: false, cacheDir: join(output, 'vite-cache'),
    resolve: { alias: [{ find: '@/components/layout/AppLayout.vue', replacement: shell }, { find: '@', replacement: join(root, 'src') }] },
    plugins: [vue()],
    css: { postcss: { plugins: [tailwindcss({ ...tailwindConfig, content: [html, join(root, 'src/**/*.{vue,js,ts}')] }), autoprefixer()] } },
    build: { outDir: previewDirectory, emptyOutDir: true, minify: false, rollupOptions: { input: html } },
  })
  server = await preview({ root, configFile: false, build: { outDir: previewDirectory }, preview: { host: '127.0.0.1', port: 4198, strictPort: true } })
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1280, height: 950 }, locale: 'zh-CN', timezoneId: 'UTC' })
  page.on('pageerror', error => errors.push(error.message))
  await page.route('**/*', route => {
    const url = new URL(route.request().url())
    if (url.origin === 'http://127.0.0.1:4198' || url.protocol === 'data:') return route.continue()
    errors.push('Blocked external origin: ' + url.origin)
    return route.abort()
  })
  const url = `http://127.0.0.1:4198/${suffix}.html?tab=gateway`
  const card = () => page.locator('.card').filter({ has: page.locator('#settings-section-openai-response-header-timeout') })
  await page.goto(url + '&mode=before')
  await page.locator('[data-testid="openai-header-timeout-seconds"]').waitFor()
  await card().screenshot({ path: join(output, 'before.png'), animations: 'disabled' })
  await page.goto(url + '&mode=after')
  await page.locator('[data-testid="openai-image-header-timeout-seconds"]').waitFor()
  await card().screenshot({ path: join(output, 'after.png'), animations: 'disabled' })
  await card().locator('input[type="checkbox"][value="8"]').check()
  await page.locator('[data-testid="openai-image-header-timeout-seconds"]').fill('240')
  await page.locator('[data-testid="openai-header-timeout-save"]').click()
  await page.waitForFunction(() => window.__imageTimeoutFixture.writes.length === 1)
  const saved = await page.evaluate(() => window.__imageTimeoutFixture.writes[0])
  if (saved.image_seconds !== 240 || JSON.stringify(saved.image_group_ids) !== '[7,8]') throw Error('Settings save did not retain group choices')
  await card().screenshot({ path: join(output, 'saved.png'), animations: 'disabled' })
  await card().locator('[data-testid="new-feature-badge"]').click()
  await page.reload()
  await page.locator('[data-testid="openai-image-header-timeout-seconds"]').waitFor()
  if (await card().locator('[data-testid="new-feature-badge"]').count()) throw Error('Dismissal did not survive refresh')
  if (await page.locator('[data-testid="openai-image-header-timeout-seconds"]').inputValue() !== '240') throw Error('Saved timeout did not reload')
  await card().screenshot({ path: join(output, 'reloaded.png'), animations: 'disabled' })
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await card().screenshot({ path: join(output, 'dark.png'), animations: 'disabled' })
  writeFileSync(join(output, 'manifest.json'), JSON.stringify({
    source_commit: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(), base_commit: process.env.SOURCE_BASE,
    mode: 'actual SettingsView, Toggle, GroupSelector and NewFeatureBadge; synthetic API and layout shell; no login or production validation',
    screenshots: ['before.png', 'after.png', 'saved.png', 'reloaded.png', 'dark.png'], saved, errors,
  }, null, 2) + '\n')
  if (errors.length) throw Error(errors.join('; '))
} catch (error) {
  if (browser) {
    const pages = browser.contexts().flatMap(context => context.pages())
    if (pages[0]) writeFileSync(join(output, 'failure.txt'), String(error) + '\n' + await pages[0].locator('body').innerText())
  }
  throw error
} finally {
  if (browser) await browser.close()
  if (server) await new Promise((resolve, reject) => server.httpServer.close(error => error ? reject(error) : resolve()))
  for (const file of files) rmSync(file, { force: true })
  rmSync(join(output, 'vite-cache'), { recursive: true, force: true })
  rmSync(previewDirectory, { recursive: true, force: true })
}
