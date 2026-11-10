// Isolated CI: full original base/head trees, real AutoConfigView and controls.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const output = process.env.SCREENSHOT_DIR
if (!output || !process.env.SOURCE_BASE || !process.env.BASELINE_ROOT || !process.env.PLAYWRIGHT_ROOT) throw Error('Missing isolated CI configuration')
mkdirSync(output, { recursive: true })
const manifest = {
  base_commit: process.env.SOURCE_BASE, head_commit: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(),
  boundary: 'Original complete base/head frontend trees, actual AutoConfigView and child controls/locales/CSS. Synthetic administrator/configuration and in-browser API adapter with localStorage persistence; layout shell only. No real backend, login, account configuration, upstream or production/paid calls.',
  results: [], screenshots: [], errors: [],
}
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
const browser = await chromium.launch({ headless: true })
try {
  for (const mode of ['before', 'after']) {
    const root = resolve(mode === 'before' ? process.env.BASELINE_ROOT : 'frontend')
    const require = createRequire(join(root, 'package.json'))
    const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
    const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
    const tailwindcss = require('tailwindcss'), autoprefixer = require('autoprefixer')
    const tailwindConfig = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
    const suffix = `.ci-oauth-ws-${process.pid}`, files = []
    const create = (path, text) => { writeFileSync(path, text, { flag: 'wx' }); files.push(path); return path }
    const buildDir = join(output, `${mode}-build`)
    let server, page
    try {
      const shell = create(join(root, suffix + '-layout.js'), `import {h} from 'vue';export default {setup(_,ctx){return()=>h('main',{class:'p-6 bg-gray-50 dark:bg-dark-900'},ctx.slots.default?.())}}`)
      const html = create(join(root, suffix + '.html'), `<html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="./${suffix}.js"></script></html>`)
      create(join(root, suffix + '.js'), `
        import {createApp} from 'vue';import {createPinia} from 'pinia';import {createRouter,createMemoryHistory} from 'vue-router';
        import {createI18n} from 'vue-i18n';import {apiClient} from '/src/api/client.ts';import {useAuthStore} from '/src/stores/auth.ts';
        import View from '/src/views/admin/AutoConfigView.vue';import zh from '/src/i18n/locales/zh/index.ts';import en from '/src/i18n/locales/en/index.ts';import '/src/style.css';
        const legacy={enabled:false,platform:'openai',priority:50,load_factor:1,concurrency:3,cost_multiplier:0.07,group_ids:[],upgrade_enabled:false,upgrade_group_ids:[],successes_per_step:20,upgrade_step:1,max_concurrency:100,cooldown_seconds:60,revision:''};
        const state={config:JSON.parse(localStorage.getItem('fixture-oauth-ws')||'null')||legacy,writes:[],requests:[],unexpected:[]};window.__oauthWSFixture=state;
        const groups=[{id:5,name:'虚构 OpenAI 组',platform:'openai',status:'active',rate_multiplier:1,subscription_type:'standard'}];
        apiClient.defaults.adapter=async request=>{
          const path=request.url.split('?')[0];state.requests.push({method:request.method,path});let data;
          if(path==='/admin/account-ops/auto-config'){
            if(request.method==='put'){state.config=JSON.parse(request.data);state.writes.push(structuredClone(state.config));localStorage.setItem('fixture-oauth-ws',JSON.stringify(state.config))}
            data=structuredClone(state.config);
          }else if(path==='/admin/account-ops/auto-config/events')data={items:[],has_more:false};
          else if(path==='/admin/accounts'&&request.method==='get'){
            const params=request.params||{};
            if(params.platform!=='openai'||params.type!=='apikey'||params.status!=='active'||params.lite!=='1')throw Error('Unexpected fallback account filters');
            data={items:[],total:0,page:1,page_size:100,pages:1};
          }
          else if(path==='/admin/groups/all')data=groups;
          else if(path==='/admin/groups')data={items:groups,total:1,page:1,page_size:100,pages:1};
          else if(path==='/admin/account-quality-plans')data=[];
          else {state.unexpected.push(path);throw Error('Unexpected fixture endpoint '+path)}
          return{data:{code:0,message:'success',data},status:200,statusText:'OK',headers:{},config:request};
        };
        const app=createApp(View),pinia=createPinia();app.use(pinia);useAuthStore().user={id:41,role:'admin',status:'active',email:'admin@example.invalid'};
        const router=createRouter({history:createMemoryHistory(),routes:[{path:'/:pathMatch(.*)*',component:{render:()=>null}}]});await router.push('/admin/auto-config');await router.isReady();
        app.use(router).use(createI18n({legacy:false,locale:new URLSearchParams(location.search).get('lang')||'zh',messages:{zh,en}})).mount('#app');
      `)
      await build({ root, configFile: false, cacheDir: join(output, `${mode}-cache`),
        resolve: { alias: [{ find: '@/components/layout/AppLayout.vue', replacement: shell }, { find: '@', replacement: join(root, 'src') }] }, plugins: [vue()],
        css: { postcss: { plugins: [tailwindcss({ ...tailwindConfig, content: [html, join(root, 'src/**/*.{vue,js,ts}')] }), autoprefixer()] } },
        build: { outDir: buildDir, emptyOutDir: true, minify: false, target: 'esnext', rollupOptions: { input: html } },
      })
      server = await preview({ root, configFile: false, build: { outDir: buildDir }, preview: { host: '127.0.0.1', port: 4198, strictPort: true } })
      page = await browser.newPage({ viewport: { width: 1280, height: 1100 }, locale: 'zh-CN', timezoneId: 'UTC' })
      page.on('pageerror', error => manifest.errors.push(`${mode}: ${error.message}`))
      await page.route('**/*', route => {
        const url = new URL(route.request().url())
        if (url.origin === 'http://127.0.0.1:4198' || url.protocol === 'data:') return route.continue()
        manifest.errors.push(`Blocked external origin ${url.origin}`);return route.abort()
      })
      const url = `http://127.0.0.1:4198/${suffix}.html`
      const card = () => page.locator('section').filter({ has: page.getByTestId('initial-enabled') })
      const screenshot = async name => { await card().screenshot({ path: join(output, name), animations: 'disabled' });manifest.screenshots.push(name) }
      await page.goto(url)
      await page.getByTestId('initial-enabled').waitFor()
      assert.deepEqual(await page.evaluate(() => window.__oauthWSFixture.unexpected), [])
      if (mode === 'before') {
        assert.equal(await page.getByTestId('initial-ws-mode-select').count(), 0)
        assert.equal(await page.getByTestId('initial-ws-sse-acceleration-enabled').count(), 0)
        await screenshot('before.png')
        manifest.results.push({ mode, ws_controls_present: false })
      } else {
        await page.getByTestId('apikey-fallback-loading').waitFor({state:'detached'})
        assert.equal(await page.getByTestId('apikey-fallback-accounts-error').count(), 0)
        assert.equal(await page.getByTestId('apikey-fallback-enabled').isChecked(), false)
        assert.equal(await page.evaluate(() => window.__oauthWSFixture.requests.some(request => request.method==='get'&&request.path==='/admin/accounts')), true)
        const select = () => page.getByTestId('initial-ws-mode-select')
        const acceleration = () => page.getByTestId('initial-ws-sse-acceleration-enabled')
        assert.equal(await select().inputValue(), 'ctx_pool')
        assert.equal(await acceleration().isChecked(), false)
        await screenshot('after.png')
        await select().selectOption('http_bridge');await acceleration().check()
        await page.getByTestId('totp-rotate-on-429').check();await page.getByTestId('disable-scheduling-on-429').check()
        await page.locator('form button[type="submit"]').click()
        await page.waitForFunction(() => window.__oauthWSFixture.writes.length === 1)
        const saved = await page.evaluate(() => window.__oauthWSFixture.writes[0])
        assert.equal(saved.openai_ws_mode, 'http_bridge');assert.equal(saved.openai_ws_sse_acceleration, true)
        assert.equal(saved.openai_totp_rotate_on_429, true);assert.equal(saved.openai_disable_scheduling_on_429, true)
        await screenshot('after-saved.png')
        await page.reload();await select().waitFor()
        assert.equal(await select().inputValue(), 'http_bridge');assert.equal(await acceleration().isChecked(), true)
        const wsBadges = () => page.getByTestId('initial-ws-mode').getByTestId('new-feature-badge')
        assert.equal(await wsBadges().count(), 1)
        await wsBadges().click()
        assert.equal(await page.getByTestId('initial-ws-sse-acceleration').getByTestId('new-feature-badge').count(), 0)
        await page.reload();await select().waitFor()
        assert.equal(await wsBadges().count(), 0)
        await screenshot('after-reloaded.png')
        await page.getByTestId('platform').selectOption('anthropic')
        assert.equal(await select().count(), 0);assert.equal(await acceleration().count(), 0)
        await screenshot('after-anthropic.png')
        await page.getByTestId('platform').selectOption('openai')
        assert.equal(await select().inputValue(), 'ctx_pool');assert.equal(await acceleration().isChecked(), false)
        await page.goto(url + '?lang=en');await select().waitFor();await screenshot('after-en.png')
        await page.evaluate(() => document.documentElement.classList.add('dark'));await screenshot('after-dark.png')
        await page.setViewportSize({ width: 430, height: 1000 });await screenshot('after-mobile.png')
        manifest.results.push({ mode, defaults: { ws_mode: 'ctx_pool', acceleration: false }, saved, reload_preserved: true, platform_reset: true, shared_badge_dismissal_persisted: true })
      }
      assert.deepEqual(await page.evaluate(() => window.__oauthWSFixture.unexpected), [])
    } catch (error) {
      manifest.errors.push(`${mode}: ${error.stack}`)
      if (page) { writeFileSync(join(output, `${mode}-failure.txt`), await page.locator('body').innerText());await page.screenshot({ path: join(output, `${mode}-failure.png`) }) }
      throw error
    } finally {
      if (page) await page.close()
      if (server) await new Promise((res, rej) => server.httpServer.close(error => error ? rej(error) : res()))
      for (const file of files) rmSync(file, { force: true })
      rmSync(buildDir, { recursive: true, force: true });rmSync(join(output, `${mode}-cache`), { recursive: true, force: true })
    }
  }
  assert.deepEqual(manifest.errors, [])
} finally { writeFileSync(join(output, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n');await browser.close() }
