// CI-only evidence from the real admin key list, policy dialog and project CSS.
// Identity and API adapters are fictional. No admin login or backend request runs.
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const root = resolve('frontend')
const output = process.env.SCREENSHOT_DIR
if (!output || !process.env.PLAYWRIGHT_ROOT) throw Error('Missing isolated screenshot paths')
mkdirSync(output, { recursive: true })
const require = createRequire(join(root, 'package.json'))
const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
const tailwindcss = require('tailwindcss')
const autoprefixer = require('autoprefixer')
const tailwindConfig = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
const suffix = `.ci-key-policy-${process.pid}`
const entry = join(root, suffix + '.js')
const html = join(root, suffix + '.html')
const api = join(root, suffix + '-api.js')
const auth = join(root, suffix + '-auth.js')
const app = join(root, suffix + '-app.js')
const localeAdapter = join(root, suffix + '-i18n.js')
const files = [entry, html, api, auth, app, localeAdapter]
const previewDir = join(output, 'preview-build')
const sourceCommit = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
const errors = []
const screenshots = []
const interactions = []
let browser, server

try {
  writeFileSync(api, `
    const initialKeys = [
      {id:17,user_id:99,name:'Fictional Key A',key:'fixture-only-key-a-never-issued',status:'active',group_id:null,group_ids:[],created_at:'2026-11-04T00:00:00Z',model_mapping:{'gpt-6.1-sol':'gpt-5.6-sol'},reasoning_effort:'high'},
      {id:18,user_id:99,name:'Fictional Key B',key:'fixture-only-key-b-never-issued',status:'active',group_id:null,group_ids:[],created_at:'2026-11-04T00:00:00Z',model_mapping:{},reasoning_effort:''},
    ];
    const state={keys:structuredClone(initialKeys),writes:[],failNext:false,successes:[]};
    window.__keyPolicyFixture=state;
    export const adminAPI={
      users:{getUserApiKeys:async(id)=>{if(id!==99)throw Error('Unexpected fixture user');return{items:structuredClone(state.keys)}}},
      groups:{getAll:async()=>[]},
      apiKeys:{updateApiKeyModelPolicy:async(id,policy)=>{
        if(id!==17)throw Error('Unexpected fixture key');
        if(state.failNext){state.failNext=false;throw Error('Fictional save failure')}
        state.writes.push({id,policy:structuredClone(policy)});
        state.keys=state.keys.map(key=>key.id===id?{...key,...structuredClone(policy)}:key);
        return{api_key:structuredClone(state.keys.find(key=>key.id===id)),auto_granted_group_access:false};
      }},
    };
  `, { flag: 'wx' })
  writeFileSync(auth, `export const useAuthStore=()=>({isAdmin:true,user:{id:41,role:'admin'}});`, { flag: 'wx' })
  writeFileSync(app, `export const useAppStore=()=>({showSuccess:message=>window.__keyPolicyFixture.successes.push(message),showError:()=>{}});`, { flag: 'wx' })
  writeFileSync(localeAdapter, `
    import {createI18n} from 'vue-i18n';
    import zh from '/src/i18n/locales/zh/index.ts';
    import en from '/src/i18n/locales/en/index.ts';
    const locale=new URLSearchParams(location.search).get('lang')||'zh';
    export const i18n=createI18n({legacy:false,locale,messages:{zh,en}});
    export const getLocale=()=>locale;
  `, { flag: 'wx' })
  writeFileSync(entry, `
    import {createApp,h,onMounted,ref} from 'vue';
    import {createPinia} from 'pinia';
    import {i18n} from '@fixture-i18n';
    import Keys from '/src/components/admin/user/UserApiKeysModal.vue';
    import '/src/style.css';
    const locale=new URLSearchParams(location.search).get('lang')||'zh';
    document.documentElement.lang=locale;
    createApp({setup(){
      const show=ref(false);
      onMounted(()=>{show.value=true});
      return()=>h('main',{class:'min-h-screen bg-gray-50 p-6 text-gray-900 dark:bg-dark-900 dark:text-gray-100'},[
        h('h1',{class:'text-xl font-semibold'},'用户管理 / API Keys'),
        h('p','CI offline component preview · fictional identity and API data'),
        h(Keys,{show:show.value,user:{id:99,email:'customer@example.invalid',username:'Fictional customer'},onClose:()=>show.value=false}),
      ]);
    }}).use(createPinia()).use(i18n).mount('#app');
  `, { flag: 'wx' })
  writeFileSync(html, `<html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="./${suffix}.js"></script></html>`, { flag: 'wx' })
  await build({
    root, configFile: false, cacheDir: join(output, 'vite-cache'),
    resolve: { alias: [
      { find: '@/api/admin', replacement: api },
      { find: '@/stores/auth', replacement: auth },
      { find: '@/stores/app', replacement: app },
      { find: /^@\/i18n$/, replacement: localeAdapter },
      { find: '@fixture-i18n', replacement: localeAdapter },
      { find: '@', replacement: join(root, 'src') },
    ] },
    css: { postcss: { plugins: [
      tailwindcss({ ...tailwindConfig, content: [html, join(root, 'src/**/*.{vue,js,ts,jsx,tsx}')] }), autoprefixer(),
    ] } },
    plugins: [vue()],
    build: { outDir: previewDir, emptyOutDir: true, minify: false, rollupOptions: { input: html } },
  })
  server = await preview({ root, configFile: false, build: { outDir: previewDir }, preview: { host: '127.0.0.1', port: 4193, strictPort: true } })
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1280, height: 1100 }, timezoneId: 'UTC' })
  await page.route('**/*', route => {
    const url = new URL(route.request().url())
    if (url.origin === 'http://127.0.0.1:4193' || url.protocol === 'data:') return route.continue()
    errors.push('Blocked external origin: ' + url.origin)
    return route.abort()
  })
  page.on('pageerror', error => errors.push(error.message))
  page.on('response', response => { if (response.status() >= 400) errors.push(`HTTP ${response.status()} ${new URL(response.url()).pathname}`) })
  async function capture(name) {
    const geometry = await page.evaluate(() => {
      const dialogs = [...document.querySelectorAll('.modal-content')]
      const dialog = dialogs.at(-1)
      const rect = dialog?.getBoundingClientRect()
      return { overflow: document.documentElement.scrollWidth > innerWidth, left: rect?.left, right: rect?.right, viewport: innerWidth }
    })
    if (geometry.overflow || geometry.left < 0 || geometry.right > geometry.viewport) throw Error(name + ': horizontal overflow')
    await page.screenshot({ path: join(output, name + '.png'), fullPage: true, animations: 'disabled' })
    screenshots.push(name + '.png')
  }
  async function openPolicy() {
    await page.locator('[data-test="edit-key-model-policy"]').first().click()
    await page.locator('#api-key-model-policy-form').waitFor()
  }
  async function savePolicy() {
    await page.locator('button[type="submit"][form="api-key-model-policy-form"]').click()
    await page.locator('#api-key-model-policy-form').waitFor({ state: 'hidden' })
  }
  for (const lang of ['zh', 'en']) {
    await page.setViewportSize({ width: 1280, height: 1100 })
    await page.goto(`http://127.0.0.1:4193/${suffix}.html?lang=${lang}`)
    await page.locator('[data-test="edit-key-model-policy"]').first().waitFor()
    if (await page.locator('[data-test="edit-key-model-policy"]').count() !== 2) throw Error('Missing real key entry')
    await capture(lang + '-entry')
    await openPolicy()
    if (await page.locator('[data-test="mapping-source"]').inputValue() !== 'gpt-6.1-sol') throw Error('Saved source was not loaded')
    await page.locator('[data-test="mapping-target"]').fill('gpt-5.6-sol-fast')
    await page.locator('[data-test="reasoning-effort"]').selectOption('xhigh')
    await capture(lang + '-configured')
    await savePolicy()
    const saved = await page.evaluate(() => window.__keyPolicyFixture.writes.at(-1))
    if (saved.id !== 17 || JSON.stringify(saved.policy) !== JSON.stringify({ model_mapping: { 'gpt-6.1-sol': 'gpt-5.6-sol-fast' }, reasoning_effort: 'xhigh' })) throw Error('Unexpected saved policy')
    await openPolicy()
    if (await page.locator('[data-test="mapping-target"]').inputValue() !== 'gpt-5.6-sol-fast') throw Error('Saved mapping did not return on reopen')
    if (await page.locator('[data-test="reasoning-effort"]').inputValue() !== 'xhigh') throw Error('Saved effort did not return on reopen')
    await capture(lang + '-saved')
    await page.setViewportSize({ width: 390, height: 844 })
    await capture(lang + '-mobile')
    await page.setViewportSize({ width: 1280, height: 1100 })
    await page.locator('[data-test="model-mapping-row"] button').click()
    await page.locator('[data-test="reasoning-effort"]').selectOption('')
    await savePolicy()
    const cleared = await page.evaluate(() => window.__keyPolicyFixture.writes.at(-1))
    if (JSON.stringify(cleared.policy) !== JSON.stringify({ model_mapping: {}, reasoning_effort: '' })) throw Error('Policy clear did not send explicit empty values')
    const untouched = await page.evaluate(() => window.__keyPolicyFixture.keys.find(key => key.id === 18))
    if (Object.keys(untouched.model_mapping).length || untouched.reasoning_effort !== '') throw Error('Another key changed')
    await openPolicy()
    if (await page.locator('[data-test="model-mapping-row"]').count() !== 0) throw Error('Cleared mapping returned after reopen')
    await capture(lang + '-cleared')
    await page.locator('[data-test="reasoning-effort"]').selectOption('low')
    await page.evaluate(() => { window.__keyPolicyFixture.failNext = true })
    await page.locator('button[type="submit"][form="api-key-model-policy-form"]').click()
    await page.getByRole('alert').waitFor()
    if (await page.getByRole('alert').textContent() !== 'Fictional save failure') throw Error('Save failure was not shown')
    if (await page.locator('[data-test="reasoning-effort"]').inputValue() !== 'low') throw Error('Failed save discarded edits')
    await capture(lang + '-save-error')
    interactions.push({ lang, real_entry: true, saved_mapping_and_effort: true, reopen_readback: true, explicit_clear: true, other_key_unchanged: true, failed_save_retains_edit: true })
  }
  writeFileSync(join(output, 'manifest.json'), JSON.stringify({
    source_commit: sourceCommit,
    mode: 'actual UserApiKeysModal → ApiKeyModelPolicyDialog → BaseDialog and project CSS; fictional identity and in-memory API adapters; no full Users navigation, admin login, backend API or production validation',
    fixtures: 'fictional customer@example.invalid and two never-issued fixture keys; no real credentials',
    screenshots, interactions, errors,
  }, null, 2) + '\n')
  if (errors.length) throw Error(errors.join('; '))
} catch (error) {
  writeFileSync(join(output, 'failure.json'), JSON.stringify({ source_commit: sourceCommit, message: String(error), errors }, null, 2) + '\n')
  throw error
} finally {
  if (browser) await browser.close()
  if (server) await new Promise((resolve, reject) => server.httpServer.close(error => error ? reject(error) : resolve()))
  for (const file of files) rmSync(file, { force: true })
  rmSync(join(output, 'vite-cache'), { recursive: true, force: true })
  rmSync(previewDir, { recursive: true, force: true })
}
