// Isolated CI: full original base/head trees, real AutoConfigView and controls.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const output = process.env.SCREENSHOT_DIR
if (!output || !process.env.SOURCE_BASE || !process.env.BASELINE_ROOT || !process.env.PLAYWRIGHT_ROOT) throw Error('Missing isolated CI configuration')
const fallbackEvidence = process.env.EVIDENCE_SCENARIO === 'apikey-fallback'
if (fallbackEvidence) assert.equal(process.env.SOURCE_BASE, '8545bf9ffac5a870ade18fbe7c4b61a48755c8db')
mkdirSync(output, { recursive: true })
const manifest = {
  base_commit: process.env.SOURCE_BASE, head_commit: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(),
  scenario: fallbackEvidence ? 'apikey-fallback' : 'oauth-initial-ws',
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
        if(${fallbackEvidence})groups.push({id:7,name:'虚构 OpenAI 第二组',platform:'openai',status:'active',rate_multiplier:1,subscription_type:'standard'},
          {id:6,name:'虚构 Anthropic 组',platform:'anthropic',status:'active',rate_multiplier:1,subscription_type:'standard'});
        const accounts=Array.from({length:121},(_,i)=>({id:i+1,name:(i<20?'Other':'Matching')+' 示例 '+(i+1),platform:'openai',type:'apikey',status:'active',schedulable:i%2===1,parent_account_id:null,expires_at:null,group_ids:i%3===0?[5]:i%3===1?[7]:[]}));
        apiClient.defaults.adapter=async request=>{
          const path=request.url.split('?')[0];state.requests.push({method:request.method,path,params:structuredClone(request.params||{})});let data;
          if(path==='/admin/account-ops/auto-config'){
            if(request.method==='put'){state.config=JSON.parse(request.data);state.writes.push(structuredClone(state.config));localStorage.setItem('fixture-oauth-ws',JSON.stringify(state.config))}
            data=structuredClone(state.config);
          }else if(path==='/admin/account-ops/auto-config/events')data={items:[],has_more:false};
          else if(path==='/admin/accounts'&&request.method==='get'){
            const params=request.params||{};
            // Only this page's fallback picker uses this endpoint. Ordinary account lists retain their own status contracts.
            const expected={page:params.page,page_size:100,platform:'openai',type:'apikey',lite:'1',sort_by:'id',sort_order:'asc',timezone:'UTC',${fallbackEvidence && mode === 'before' ? "status:'active'" : ''}};
            if(!Number.isSafeInteger(params.page)||params.page<1||Object.keys(params).length!==Object.keys(expected).length||Object.entries(expected).some(([key,value])=>params[key]!==value)){
              state.unexpected.push({path,params});throw Error('Unexpected fallback account filters');
            }
            // These fictional records have no rate/temporary limits; the old API active filter excludes paused keys.
            const matches=${fallbackEvidence}?accounts.filter(account=>params.status!=='active'||account.schedulable):[];
            data={items:matches.slice((params.page-1)*100,params.page*100),total:matches.length,page:params.page,page_size:100,pages:Math.max(1,Math.ceil(matches.length/100))};
          }
          else if(path==='/admin/groups/all')data=groups;
          else if(path==='/admin/groups')data={items:groups,total:groups.length,page:1,page_size:100,pages:1};
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
      const card = () => fallbackEvidence ? page.getByTestId('apikey-fallback') : page.locator('section').filter({ has: page.getByTestId('initial-enabled') })
      const screenshot = async name => {
        await page.evaluate(async () => { await document.fonts.ready; await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))) })
        await card().screenshot({ path: join(output, name), animations: 'disabled' });manifest.screenshots.push(name)
      }
      await page.goto(url)
      await page.getByTestId('initial-enabled').waitFor()
      assert.deepEqual(await page.evaluate(() => window.__oauthWSFixture.unexpected), [])
      if (fallbackEvidence) {
        const loaded = async () => {
          await card().waitFor()
          await page.getByTestId('apikey-fallback-loading').waitFor({ state: 'detached' })
          assert.equal(await page.getByTestId('apikey-fallback-accounts-error').count(), 0)
        }
        await loaded()
        // Confirm the original Tailwind CSS is applied in both complete source trees.
        assert.equal(await card().evaluate(element => getComputedStyle(element).borderRadius), '16px')
        if (mode === 'before') {
          assert.equal(await card().getByTestId('apikey-fallback-account-picker').count(), 0)
          assert.equal(await card().getByTestId('apikey-fallback-account-1').count(), 0)
          assert.equal(await card().getByTestId('apikey-fallback-account-2').count(), 1)
          await screenshot('before-fallback-desktop-zh.png')
          await page.setViewportSize({ width: 390, height: 1100 })
          await screenshot('before-fallback-mobile-zh.png')
          manifest.results.push({ mode, derived_active_excludes_paused: true, account_requests: await page.evaluate(() => window.__oauthWSFixture.requests.filter(request => request.path === '/admin/accounts')) })
        } else {
          const picker = () => card().getByTestId('apikey-fallback-account-picker')
          const row = id => picker().locator(`[data-account-id="${id}"]`)
          const checkbox = id => row(id).locator('input[type="checkbox"]')
          const search = async value => {
            await page.locator('#apikey-fallback-account-search').fill(value)
            await page.locator('#apikey-fallback-account-search').press('Enter')
          }
          const count = async expected => assert.equal(await picker().getByTestId('quality-account-count').innerText(), `已选择 ${expected} 个账户`)
          const next = () => picker().getByRole('button', { name: '下一页账户', exact: true }).click()
          const previous = () => picker().getByRole('button', { name: '上一页账户', exact: true }).click()
          const accountRequests = await page.evaluate(() => window.__oauthWSFixture.requests.filter(request => request.path === '/admin/accounts'))
          assert.deepEqual(accountRequests.map(request => request.params.page), [1, 2])
          assert(accountRequests.every(request => !Object.hasOwn(request.params, 'status')))
          assert.equal(await picker().locator('[data-account-id]').count(), 20)
          assert.match(await row(1).innerText(), /调度已关闭/)
          assert.match(await row(2).innerText(), /调度已开启/)
          assert.equal(await picker().getByTestId('quality-account-type').count(), 0)
          assert.equal(await picker().locator('[data-testid^="quality-account-status-"]').count(), 0)
          assert.deepEqual(await picker().getByTestId('quality-account-group').locator('option').evaluateAll(options => options.map(option => option.value)), ['', 'ungrouped', '5', '7'])
          await screenshot('after-fallback-paused-desktop-zh.png')
          await picker().getByTestId('quality-select-page').click(); await count(20)
          await next(); await checkbox(21).check(); await count(21)
          await previous(); assert.equal(await checkbox(1).isChecked(), true)
          await screenshot('after-fallback-page-selection-zh.png')
          await picker().getByTestId('quality-account-group').selectOption('5')
          assert.deepEqual(await picker().locator('[data-account-id]').evaluateAll(rows => rows.map(row => Number(row.dataset.accountId))), Array.from({ length: 20 }, (_, i) => i * 3 + 1))
          await search('#1')
          assert.equal(await checkbox(1).isChecked(), true)
          await screenshot('after-fallback-search-group-zh.png')
          await picker().getByTestId('quality-account-group').selectOption('ungrouped')
          assert.equal(await row(1).count(), 0)
          await search(''); assert.equal(await row(3).count(), 1)
          await picker().getByTestId('quality-account-group').selectOption('')
          await picker().getByTestId('quality-clear-selection').click(); await count(0)
          await search('Matching'); assert.equal(await row(21).count(), 1)
          await picker().getByTestId('quality-select-all').click(); await count(100)
          for (let i = 0; i < 5; i++) await next()
          assert.equal(await checkbox(121).isDisabled(), true)
          await screenshot('after-fallback-limit-zh.png')
          await previous(); assert.equal(await checkbox(120).isChecked(), true)
          assert.equal(await checkbox(120).isEnabled(), true)
          await checkbox(120).uncheck(); await count(99)
          await next(); assert.equal(await checkbox(121).isEnabled(), true)
          await checkbox(121).check(); await count(100)
          await screenshot('after-fallback-deselect-reselect-zh.png')
          await card().getByTestId('apikey-fallback-enabled').check()
          await card().getByTestId('apikey-fallback-metric').selectOption('health_score')
          await card().getByTestId('apikey-fallback-threshold').fill('82.5')
          await card().getByTestId('apikey-fallback-window').fill('15')
          const save = async expectedWrites => {
            await page.locator('form button[type="submit"]').click()
            await page.waitForFunction(count => window.__oauthWSFixture.writes.length === count, expectedWrites)
            await page.waitForFunction(() => !document.querySelector('form button[type="submit"]').disabled)
          }
          await save(1)
          const saved = await page.evaluate(() => window.__oauthWSFixture.writes[0])
          assert.deepEqual(saved.openai_apikey_fallback, { enabled: true, metric: 'health_score', threshold: 82.5, window_minutes: 15, account_ids: [...Array.from({ length: 99 }, (_, i) => i + 21), 121] })
          await screenshot('after-fallback-saved-zh.png')
          await page.reload(); await loaded()
          await search('#121'); assert.equal(await checkbox(121).isChecked(), true); await count(100)
          assert.equal(await card().getByTestId('apikey-fallback-enabled').isChecked(), true)
          assert.equal(await card().getByTestId('apikey-fallback-metric').inputValue(), 'health_score')
          assert.equal(await card().getByTestId('apikey-fallback-threshold').inputValue(), '82.5')
          assert.equal(await card().getByTestId('apikey-fallback-window').inputValue(), '15')
          await page.setViewportSize({ width: 390, height: 1100 })
          assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
          await screenshot('after-fallback-reloaded-mobile-zh.png')
          // Discarding an unsaved change must preserve the stored selection.
          await checkbox(121).uncheck(); await page.reload()
          await loaded()
          await search('#121'); assert.equal(await checkbox(121).isChecked(), true)
          await picker().getByTestId('quality-clear-selection').click(); await count(0)
          await card().getByTestId('apikey-fallback-enabled').uncheck(); await save(1)
          assert.deepEqual(await page.evaluate(() => window.__oauthWSFixture.writes[0].openai_apikey_fallback.account_ids), [])
          await screenshot('after-fallback-cleared-mobile-zh.png')
          manifest.results.push({ mode, saved, account_requests: accountRequests, checks: ['paused candidates', 'API and UI pagination', 'page selection retained across pages', 'search by name and ID', 'OpenAI group and ungrouped filters', '100 account cap', 'deselect and reselect at cap', 'save and reload', 'discard unsaved selection', 'clear selection', 'mobile no overflow'] })
        }
      } else if (mode === 'before') {
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
