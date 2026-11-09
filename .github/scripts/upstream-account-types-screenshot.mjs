// Isolated remote evidence: real AccountsView, separate full source trees and locales.
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
  boundary: 'Original complete base/head frontend trees and locales, actual AccountsView/table/refresh controls and real timer. Synthetic admin and six fictional accounts, in-browser API adapter (sync calls recorded, no actual upstream discovery). Only AppLayout replaced with an isolated layout shell; unrelated dialogs remain closed and untested. No login, real backend, credentials or production/paid calls.',
  results: [], screenshots: [], errors: [],
}
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
const browser = await chromium.launch({ headless: true })
try {
  for (const mode of ['before', 'after']) {
    const root = resolve(mode === 'before' ? process.env.BASELINE_ROOT : 'frontend'), require = createRequire(join(root, 'package.json'))
    const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
    const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
    const tailwindcss = require('tailwindcss'), autoprefixer = require('autoprefixer')
    const tailwindConfig = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
    const suffix = `.ci-account-types-${process.pid}`, files = [], buildDir = join(output, `${mode}-build`)
    const create = (path, text) => { writeFileSync(path, text, { flag: 'wx' }); files.push(path); return path }
    let page, server
    try {
      const shell = create(join(root, suffix + '-layout.js'), `import {h} from 'vue';export default {setup(_,ctx){return()=>h('main',{class:'p-6 bg-gray-50 min-h-screen dark:bg-dark-900'},ctx.slots.default?.())}}`)
      const html = create(join(root, suffix + '.html'), `<html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="./${suffix}.js"></script></html>`)
      const entry = create(join(root, suffix + '.js'), `
        import {createApp} from 'vue';import {createPinia} from 'pinia';import {createRouter,createMemoryHistory} from 'vue-router';
        import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import {apiClient} from '/src/api/client.ts';import {useAuthStore} from '/src/stores/auth.ts';
        import View from '/src/views/admin/AccountsView.vue';import '/src/style.css';
        await loadLocaleMessages('zh');i18n.global.locale.value='zh';
        const params=new URLSearchParams(location.search),scenario=params.get('scenario')||'default';
        if(!localStorage.getItem('account-auto-refresh')){
          const setting={enabled:true,interval_seconds:5};
          if(scenario==='legacy'||scenario==='explicit')setting.sync_upstream_models=true;
          if(scenario==='explicit')setting.sync_upstream_models_oauth=false;
          localStorage.setItem('account-auto-refresh',JSON.stringify(setting));
        }
        localStorage.setItem('account-hidden-columns',JSON.stringify(['usage','today_stats','proxy','notes','scheduler_score','rate_multiplier']));
        const account=(id,platform,type,extra={})=>({id,name:'虚构账号 '+id,platform,type,status:'active',schedulable:true,concurrency:2,priority:1,rate_multiplier:1,group_ids:[],credentials:{},extra:{},proxy_id:null,created_at:'2026-11-01T00:00:00Z',updated_at:'2026-11-01T00:00:00Z',...extra});
        const rows=[account(41,'openai','oauth'),account(42,'openai','apikey'),account(43,'anthropic','setup-token'),account(44,'gemini','service_account'),account(45,'openai','oauth',{credentials:{model_mapping_mode:'aliases'}}),account(46,'openai','apikey',{schedulable:false})];
        const state={requests:[],syncs:[],unexpected:[]};window.__accountTypesFixture=state;
        apiClient.defaults.adapter=async request=>{
          const path=request.url.split('?')[0];state.requests.push({method:request.method,path});let data;
          if(path==='/admin/accounts')data={items:structuredClone(rows),total:rows.length,page:1,page_size:20,pages:1};
          else if(path==='/admin/accounts/management-capabilities')data={concurrency_upgrade_enabled:false};
          else if(path==='/admin/accounts/upstream-billing-probe/settings')data={enabled:true};
          else if(['/admin/proxies/all','/admin/tls-fingerprint-profiles','/admin/groups/all'].includes(path))data=[];
          else if(path==='/admin/accounts/performance')data={};
          else if(path==='/admin/accounts/today-stats/batch')data={stats:{}};
          else if(path==='/admin/ops/settings/metric-thresholds')data={upstream_error_rate_percent_max:null};
          else if(/\\/accounts\\/\\d+\\/models\\/sync-upstream$/.test(path)){state.syncs.push(Number(path.match(/accounts\\/(\\d+)/)[1]));data={models:['fixture-model'],applied:true,changed:false};}
          else if(/\\/accounts\\/\\d+\\/schedulable-timers$/.test(path))data={tasks:[],history:[],server_now:new Date().toISOString(),poll_seconds:30,open_grace_seconds:900,max_delay_minutes:100};
          else {state.unexpected.push(path);throw Error('Unexpected fixture endpoint '+path)}
          return{data:{code:0,message:'success',data},status:200,statusText:'OK',headers:{etag:'"fixture-1"'},config:request};
        };
        const pinia=createPinia();useAuthStore(pinia).user={id:41,role:'admin',status:'active',email:'admin@example.invalid'};
        const router=createRouter({history:createMemoryHistory(),routes:[{path:'/:pathMatch(.*)*',component:{render:()=>null}}]});await router.push('/admin/accounts');await router.isReady();
        createApp(View).use(pinia).use(i18n).use(router).mount('#app');
      `)
      await build({ root, configFile: false, cacheDir: join(output, `${mode}-cache`),
        resolve: { alias: [{ find: '@/components/layout/AppLayout.vue', replacement: shell }, { find: '@', replacement: join(root, 'src') }] }, plugins: [vue()],
        css: { postcss: { plugins: [tailwindcss({ ...tailwindConfig, content: [html, entry, shell, join(root, 'src/**/*.{vue,js,ts}')] }), autoprefixer()] } },
        build: { outDir: buildDir, emptyOutDir: true, minify: false, target: 'esnext', rollupOptions: { input: html } },
      })
      server = await preview({ root, configFile: false, build: { outDir: buildDir }, preview: { host: '127.0.0.1', port: 4198, strictPort: true } })
      page = await browser.newPage({ viewport: { width: 1440, height: 1080 }, locale: 'zh-CN', timezoneId: 'UTC' })
      page.on('pageerror', error => manifest.errors.push(`${mode}: ${error.message}`))
      await page.route('**/*', route => {
        const url = new URL(route.request().url())
        if (url.origin === 'http://127.0.0.1:4198' || url.protocol === 'data:') return route.continue()
        manifest.errors.push(`Blocked external origin ${url.origin}`);return route.abort()
      })
      const url = `http://127.0.0.1:4198/${suffix}.html`
      const opener = () => page.getByTitle('自动刷新', { exact: true })
      const oauth = () => page.getByTestId('auto-refresh-upstream-models-oauth')
      const apikey = () => page.getByTestId('auto-refresh-upstream-models-apikey')
      const assertMenuVisible = async () => {
        const bounds = await page.getByTestId('auto-refresh-menu').evaluate(element => {
          const rect = element.getBoundingClientRect()
          return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: innerWidth, height: innerHeight }
        })
        assert.ok(bounds.left >= 0 && bounds.right <= bounds.width, `Menu outside horizontal viewport: ${JSON.stringify(bounds)}`)
        assert.ok(bounds.top >= 0 && bounds.bottom <= bounds.height, `Menu outside vertical viewport: ${JSON.stringify(bounds)}`)
        return bounds
      }
      const screenshot = async name => { await page.screenshot({ path: join(output, name), animations: 'disabled' });manifest.screenshots.push(name) }
      const open = async scenario => {
        await page.goto(`${url}?scenario=${scenario}`);await page.getByText('虚构账号 41', { exact: true }).first().waitFor();await opener().click()
        const observed = await page.evaluate(() => ({ requests: window.__accountTypesFixture.requests, unexpected: window.__accountTypesFixture.unexpected }))
        manifest.results.push({ mode, scenario, observed })
        assert.deepEqual(observed.unexpected, [])
      }
      await open('default')
      if (mode === 'before') {
        assert.equal(await page.getByTestId('auto-refresh-upstream-models').count(), 1)
        assert.equal(await oauth().count(), 0);await screenshot('before.png')
        manifest.results.push({ mode, single_control: true })
      } else {
        assert.equal(await oauth().isChecked(), false);assert.equal(await apikey().isChecked(), false)
        await assertMenuVisible()
        await screenshot('after.png')
        await oauth().check();await screenshot('after-oauth-only.png');await opener().click()
        await page.waitForFunction(() => window.__accountTypesFixture.syncs.length >= 3)
        assert.deepEqual(await page.evaluate(() => [...window.__accountTypesFixture.syncs].sort((a,b)=>a-b)), [41,43,44])
        await opener().click();await oauth().uncheck();await apikey().check();await screenshot('after-apikey-only.png');await opener().click()
        await page.waitForFunction(() => window.__accountTypesFixture.syncs.includes(42))
        const synced = await page.evaluate(() => [...window.__accountTypesFixture.syncs].sort((a,b)=>a-b))
        assert.deepEqual(synced, [41,42,43,44])
        await page.reload();await opener().waitFor();await opener().click()
        assert.equal(await oauth().isChecked(), false);assert.equal(await apikey().isChecked(), true)
        await screenshot('after-reloaded.png')
        manifest.results.push({ mode, oauth_only: [41,43,44], apikey_only: [42], excluded: [45,46], persisted: true })
        await page.evaluate(() => localStorage.removeItem('account-auto-refresh'));await open('legacy')
        assert.equal(await oauth().isChecked(), true);assert.equal(await apikey().isChecked(), true)
        await screenshot('after-legacy-migrated.png')
        await page.evaluate(() => localStorage.removeItem('account-auto-refresh'));await open('explicit')
        assert.equal(await oauth().isChecked(), false);assert.equal(await apikey().isChecked(), true)
        await screenshot('after-explicit-false.png')
        await page.evaluate(() => document.documentElement.classList.add('dark'));await screenshot('after-dark.png')
        await page.setViewportSize({ width: 430, height: 1000 })
        await page.waitForFunction(() => {
          const rect = document.querySelector('[data-testid="auto-refresh-menu"]').getBoundingClientRect()
          return rect.left >= 0 && rect.right <= innerWidth
        })
        const mobileBounds = await assertMenuVisible()
        await oauth().check();await apikey().uncheck()
        assert.equal(await oauth().isChecked(), true);assert.equal(await apikey().isChecked(), false)
        await screenshot('after-mobile.png')
        manifest.results.push({ mobile_menu_bounds: mobileBounds, mobile_checkboxes_clickable: true })
        manifest.results.push({ legacy_true_migrates_both: true, explicit_false_takes_precedence: true })
      }
      const unexpected = await page.evaluate(() => window.__accountTypesFixture.unexpected)
      assert.deepEqual(unexpected, [])
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
