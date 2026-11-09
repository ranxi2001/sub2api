// CI-only evidence: unmodified Vue components/stores from separate full source trees.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const output = process.env.SCREENSHOT_DIR
const base = process.env.SOURCE_BASE
const head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
if (!output || !base || !process.env.BASELINE_ROOT || !process.env.PLAYWRIGHT_ROOT) throw Error('Missing isolated CI configuration')
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
mkdirSync(output, { recursive: true })
const manifest = {
  base_commit: base, head_commit: head,
  boundary: 'Separate complete base/head frontend trees; original AppSidebar, SmartOpsSidebarSettings, adminSettings store, Toggle, FeatureSearch, locales and CSS. Synthetic admin/API and component harness with explicit fixture apply (not the real SettingsView save). No backend, login, production or paid calls. Counts cover the 14 configurable paths; the unrelated AI operations link remains visible.',
  results: [], errors: [], screenshots: [],
}
const browser = await chromium.launch({ headless: true })
try {
  for (const mode of ['before', 'after']) {
    const root = resolve(mode === 'before' ? process.env.BASELINE_ROOT : 'frontend')
    const require = createRequire(join(root, 'package.json'))
    const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
    const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
    const tailwindcss = require('tailwindcss')
    const autoprefixer = require('autoprefixer')
    const tailwindConfig = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
    const suffix = `.ci-smart-ops-${process.pid}`
    const files = []
    const create = (path, text) => { writeFileSync(path, text, { flag: 'wx' }); files.push(path); return path }
    let server, page
    const buildDir = join(output, `${mode}-build`)
    try {
      const html = create(join(root, suffix + '.html'), `<html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="./${suffix}.js"></script></html>`)
      const entry = create(join(root, suffix + '.js'), `
        import {createApp,h,ref} from 'vue';import {createPinia} from 'pinia';
        import {createRouter,createMemoryHistory} from 'vue-router';import {createI18n} from 'vue-i18n';
        import {apiClient} from '/src/api/client.ts';import {useAuthStore} from '/src/stores/auth.ts';
        import {useAppStore} from '/src/stores/app.ts';import {useAdminSettingsStore} from '/src/stores/adminSettings.ts';
        import Panel from '/src/components/settings/SmartOpsSidebarSettings.vue';import Sidebar from '/src/components/layout/AppSidebar.vue';
        import zh from '/src/i18n/locales/zh/index.ts';import en from '/src/i18n/locales/en/index.ts';import '/src/style.css';
        const params=new URLSearchParams(location.search),scenario=params.get('scenario')||'missing';
        const settings={request_capture_enabled:true};
        if(scenario==='empty')settings.smart_ops_hidden_menu_items=[];
        if(scenario==='saved')settings.smart_ops_hidden_menu_items=['/admin/token-guard'];
        const state={requests:[],unexpected:[]};window.__sidebarFixture=state;
        apiClient.defaults.adapter=async request=>{
          const path=request.url.split('?')[0];state.requests.push({method:request.method,path});let data;
          if(path==='/admin/settings'){
            if(request.method==='put')Object.assign(settings,typeof request.data==='string'?JSON.parse(request.data):request.data);
            data=structuredClone(settings);
          }
          else if(path==='/admin/payment/config')data={enabled:false};
          else if(path==='/keys')data={items:[],pages:1};
          else {state.unexpected.push(path);throw Error('Unexpected fixture endpoint '+path)}
          return{data:{code:0,message:'success',data},status:200,statusText:'OK',headers:{},config:request};
        };
        const pinia=createPinia();const app=createApp({setup(){const store=useAdminSettingsStore(),draft=ref([...store.smartOpsHiddenMenuItems]);return()=>h('div',[
          h(Sidebar),h('main',{class:'ml-64 p-8 bg-gray-50 min-h-screen dark:bg-dark-900'},[
            h(Panel,{modelValue:draft.value,'onUpdate:modelValue':value=>{draft.value=value}}),
            h('button',{class:'btn btn-primary mt-4','data-testid':'fixture-apply',onClick:async()=>{
              await apiClient.put('/admin/settings',{smart_ops_hidden_menu_items:[...draft.value]});await store.fetch(true);
            }},'应用虚构设置（验证用）')
          ])])}});app.use(pinia);
        useAuthStore().user={id:41,role:'admin',status:'active',email:'admin@example.invalid'};
        useAuthStore().token='synthetic-fixture';
        const appStore=useAppStore();appStore.publicSettingsLoaded=true;appStore.siteName='Sub2API';appStore.siteLogo='/logo.svg';
        appStore.cachedPublicSettings={custom_menu_items:[]};appStore.versionLoaded=true;appStore.currentVersion='fixture';appStore.siteVersion='fixture';
        const router=createRouter({history:createMemoryHistory(),routes:[{path:'/:pathMatch(.*)*',component:{render:()=>null}}]});
        await router.push('/admin/auto-config');await router.isReady();
        app.use(router).use(createI18n({legacy:false,locale:params.get('lang')||'zh',messages:{zh,en}}));
        const store=useAdminSettingsStore();await store.fetch();
        window.__sidebarFixture.snapshot=()=>({hidden:[...store.smartOpsHiddenMenuItems],loaded:store.loaded,unexpected:state.unexpected,requests:state.requests});
        app.mount('#app');window.__sidebarFixture.ready=true;
      `)
      await build({ root, configFile: false, cacheDir: join(output, `${mode}-cache`),
        resolve: { alias: { '@': join(root, 'src') } }, plugins: [vue()],
        css: { postcss: { plugins: [tailwindcss({ ...tailwindConfig, content: [html, entry, join(root, 'src/**/*.{vue,js,ts}')] }), autoprefixer()] } },
        build: { outDir: buildDir, emptyOutDir: true, minify: false, target: 'esnext', rollupOptions: { input: html } },
      })
      server = await preview({ root, configFile: false, build: { outDir: buildDir }, preview: { host: '127.0.0.1', port: 4198, strictPort: true } })
      page = await browser.newPage({ viewport: { width: 1440, height: 1080 }, locale: 'zh-CN', timezoneId: 'UTC' })
      page.on('pageerror', error => manifest.errors.push(`${mode}: ${error.message}`))
      await page.route('**/*', route => {
        const url = new URL(route.request().url())
        if (url.origin === 'http://127.0.0.1:4198' || url.protocol === 'data:') return route.continue()
        manifest.errors.push(`Blocked external origin ${url.origin}`)
        return route.abort()
      })
      const url = `http://127.0.0.1:4198/${suffix}.html`
      const panel = () => page.getByTestId('smart-ops-sidebar-settings')
      const visibleCount = () => panel().locator('[role="switch"][aria-checked="true"]').count()
      const paths = [
        '/admin/auto-config', '/admin/priority-scheduling', '/admin/profit-operations', '/admin/account-quality',
        '/admin/token-guard-v2', '/admin/pelican-tests', '/admin/lottery', '/admin/channel-operations',
        '/admin/controlled-experiments', '/admin/account-ops', '/admin/token-guard', '/admin/group-cache-tests',
        '/admin/request-captures', '/admin/harvest-flow',
      ]
      const sidebarPaths = () => page.locator('aside nav a').evaluateAll((links, candidates) => links.map(a => a.getAttribute('href')).filter(path => candidates.includes(path)), paths)
      const screenshot = async name => {
        await page.screenshot({ path: join(output, name), animations: 'disabled' })
        manifest.screenshots.push(name)
      }
      for (const scenario of ['missing', 'empty', 'saved']) {
        await page.goto(`${url}?scenario=${scenario}`)
        await page.waitForFunction(() => window.__sidebarFixture?.ready)
        const expected = scenario === 'missing' && mode === 'after' ? 6 : scenario === 'saved' ? 13 : 14
        assert.equal(await visibleCount(), expected, `${mode}/${scenario} selected entries`)
        const snapshot = await page.evaluate(() => window.__sidebarFixture.snapshot())
        assert.equal(snapshot.loaded, true)
        assert.deepEqual(snapshot.unexpected, [])
        assert.equal((await sidebarPaths()).length, expected, `${mode}/${scenario} actual sidebar links`)
        if (mode === 'after' && scenario === 'missing') assert.deepEqual(await sidebarPaths(), paths.slice(0, 6))
        assert.equal(await page.locator('aside nav a[href="/admin/ai-ops"]').count(), 1)
        manifest.results.push({ mode, scenario, selected: expected, sidebar_paths: await sidebarPaths(), ...snapshot })
        await screenshot(`${mode}-${scenario}.png`)
        const label = await panel().locator('label[for="smart-ops-sidebar-token-guard"]').innerText()
        await page.locator('aside button[aria-haspopup="dialog"]').click()
        await page.getByRole('combobox').fill('/admin/token-guard')
        const result = page.getByRole('option').filter({ has: page.getByText(label, { exact: true }) })
        assert.equal(await result.count(), snapshot.hidden.includes('/admin/token-guard') ? 0 : 1)
        await page.keyboard.press('Escape')
        await page.getByRole('combobox').waitFor({ state: 'hidden' })
      }
      if (mode === 'after') {
        await page.goto(`${url}?scenario=missing`)
        await page.waitForFunction(() => window.__sidebarFixture?.ready)
        await page.locator('#smart-ops-sidebar-token-guard').click()
        assert.equal(await visibleCount(), 7)
        assert.equal((await sidebarPaths()).length, 6, 'unsaved draft does not change sidebar')
        await page.getByTestId('fixture-apply').click()
        await page.waitForFunction(() => !window.__sidebarFixture.snapshot().hidden.includes('/admin/token-guard'))
        assert.ok((await sidebarPaths()).includes('/admin/token-guard'))
        await screenshot('after-enable-one.png')
        await panel().getByRole('button', { name: '全部隐藏', exact: true }).click()
        assert.equal(await visibleCount(), 0)
        await page.getByTestId('fixture-apply').click()
        await page.waitForFunction(() => window.__sidebarFixture.snapshot().hidden.length === 14)
        assert.deepEqual(await sidebarPaths(), [])
        await screenshot('after-hide-all.png')
        await panel().getByRole('button', { name: '全部显示', exact: true }).click()
        assert.equal(await visibleCount(), 14)
        await page.getByTestId('fixture-apply').click()
        await page.waitForFunction(() => window.__sidebarFixture.snapshot().hidden.length === 0)
        assert.equal((await sidebarPaths()).length, 14)
        await screenshot('after-show-all.png')
        manifest.results.push({ interaction: 'enable token-guard -> seven; hide all -> zero; show all -> fourteen', passed: true })
        await page.goto(`${url}?scenario=missing&lang=en`)
        await page.waitForFunction(() => window.__sidebarFixture?.ready)
        assert.equal(await visibleCount(), 6)
        await screenshot('after-en.png')
        await page.evaluate(() => document.documentElement.classList.add('dark'))
        await screenshot('after-dark.png')
        await page.setViewportSize({ width: 820, height: 1000 })
        await panel().screenshot({ path: join(output, 'after-narrow-panel.png'), animations: 'disabled' })
        manifest.screenshots.push('after-narrow-panel.png')
      }
    } catch (error) {
      manifest.errors.push(`${mode}: ${error.stack}`)
      if (page) {
        writeFileSync(join(output, `${mode}-failure.txt`), await page.locator('body').innerText())
        await page.screenshot({ path: join(output, `${mode}-failure.png`) })
      }
      throw error
    } finally {
      if (page) await page.close()
      if (server) await new Promise((res, rej) => server.httpServer.close(error => error ? rej(error) : res()))
      for (const file of files) rmSync(file, { force: true })
      rmSync(buildDir, { recursive: true, force: true })
      rmSync(join(output, `${mode}-cache`), { recursive: true, force: true })
    }
  }
  assert.deepEqual(manifest.errors, [])
} finally {
  writeFileSync(join(output, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n')
  await browser.close()
}
