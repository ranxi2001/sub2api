// CI rendering of the actual GroupsView and controls with synthetic API data.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import fs from 'node:fs/promises'
import path from 'node:path'
import { pathToFileURL } from 'node:url'

assert(process.env.CI && process.env.SCREENSHOT_DIR && process.env.SOURCE_BASE && process.env.PLAYWRIGHT_ROOT)
const root = process.cwd(), frontend = path.join(root, 'frontend'), output = process.env.SCREENSHOT_DIR
const fixture = path.join(frontend, '.free-ultrafast-fixture')
const beforePath = path.join(frontend, 'src/views/admin/.FreeUltrafastBefore.vue')
const head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
await fs.mkdir(output, { recursive: true }); await fs.mkdir(fixture, { recursive: true })
await fs.writeFile(beforePath, execFileSync('git', ['show', `${process.env.SOURCE_BASE}:frontend/src/views/admin/GroupsView.vue`]))
await fs.writeFile(path.join(fixture, 'layout.js'), `import {h} from 'vue';export default {setup(_,c){return()=>h('main',{class:'p-4'},c.slots.default?.())}}`)
await fs.writeFile(path.join(fixture, 'stores.js'), `import {reactive} from 'vue';
const auth=reactive({user:{id:41,role:'admin',username:'演示管理员'},isAdmin:true,isSimpleMode:false});
const app=reactive({cachedPublicSettings:{},showError:m=>{throw Error(m)},showSuccess:()=>{}});
export const useAuthStore=()=>auth;export const useAppStore=()=>app;
export const useOnboardingStore=()=>({isCurrentStep:()=>false,nextStep:()=>{},setReplayCallback:()=>{},replay:()=>{}});`)
await fs.writeFile(path.join(fixture, 'index.html'), `<!doctype html><html><meta charset="utf-8"><div id="app"></div><script type="module">
import {createApp} from 'vue';import {createPinia} from 'pinia';import {apiClient} from '/src/api/client.ts';
import Before from '/src/views/admin/.FreeUltrafastBefore.vue';import After from '/src/views/admin/GroupsView.vue';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';import '/src/style.css';
const lang=new URLSearchParams(location.search).get('lang')||'zh';await loadLocaleMessages(lang);i18n.global.locale.value=lang;
const initial={id:42,name:'UltraFast 演示分组',description:'合成数据，仅用于界面验收',platform:'openai',status:'active',subscription_type:'standard',rate_multiplier:.6,
 is_exclusive:false,force_openai_fast:false,free_openai_fast:true,free_openai_ultrafast:false,account_count:1,active_account_count:1,sort_order:1,
 allow_image_generation:false,supported_model_scopes:[],model_pricing:[],model_allowlist:{enabled:false,models:[]},created_at:'2026-01-01T00:00:00Z',updated_at:'2026-01-01T00:00:00Z'};
const state={group:JSON.parse(localStorage.getItem('ultra-fixture-group')||'null')||initial,writes:[]};window.__ultraFixture=state;
apiClient.defaults.adapter=async request=>{
 const p=request.url.split('?')[0];let data;
 if(request.method==='put'&&p==='/admin/groups/42'){const body=typeof request.data==='string'?JSON.parse(request.data):request.data;state.group={...state.group,...body};state.writes.push(body);localStorage.setItem('ultra-fixture-group',JSON.stringify(state.group));data=state.group}
 else if(p==='/admin/groups')data={items:[state.group],total:1,page:1,page_size:20,pages:1};
 else if(p.endsWith('/live-capability'))data={supported:false};
 else if(p.includes('model-allowlist-candidates'))data={models:[]};
 else if(p.includes('/accounts'))data={items:[],total:0};
 else data=[];
 return{data:{code:0,data},status:200,statusText:'OK',headers:{},config:request};
};
createApp(new URLSearchParams(location.search).get('mode')==='before'?Before:After).use(createPinia()).use(i18n).mount('#app');
</script></html>`)
const req = createRequire(path.join(frontend, 'package.json'))
const { createServer } = req('vite'), vue = req('@vitejs/plugin-vue').default
const theme = (await import(pathToFileURL(path.join(frontend, 'tailwind.config.js')))).default
const { chromium } = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))('playwright')
const aliases = ['auth', 'app', 'onboarding'].map(name => ({ find: `@/stores/${name}`, replacement: path.join(fixture, 'stores.js') }))
const server = await createServer({ root: frontend, configFile: false, envFile: false,
  plugins: [vue()], resolve: { alias: [{ find: '@/components/layout/AppLayout.vue', replacement: path.join(fixture, 'layout.js') }, ...aliases,
    { find: '@', replacement: path.join(frontend, 'src') }, { find: 'vue-i18n', replacement: 'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js' }] },
  define: { __INTLIFY_JIT_COMPILATION__: true },
  css: { postcss: { plugins: [req('tailwindcss')({ ...theme, content: [path.join(frontend, 'src/**/*.{vue,ts}'), path.join(fixture, '*.html')] }), req('autoprefixer')()] } },
  server: { host: '127.0.0.1', port: 4196, strictPort: true },
  optimizeDeps: { entries: [path.join(fixture, 'index.html')], include: ['vue', 'pinia', 'vue-i18n', 'axios'] },
})
let browser
const errors = [], screenshots = []
try {
  await server.listen(); browser = await chromium.launch({ headless: true })
  for (const lang of ['zh', 'en']) {
    const page = await browser.newPage({ viewport: { width: 1280, height: 950 } })
    page.on('pageerror', e => errors.push(e.message))
    await page.route('**/*', route => new URL(route.request().url()).origin === 'http://127.0.0.1:4196' ? route.continue() : route.abort())
    const url = `http://127.0.0.1:4196/.free-ultrafast-fixture/index.html?lang=${lang}`
    const openEdit = async () => page.getByRole('button', { name: lang === 'zh' ? '编辑' : 'Edit', exact: true }).first().click()
    const section = () => page.locator('[data-testid="edit-free-openai-fast"]').locator('xpath=../..')
    const shot = async name => { const file = `${lang}-${name}.png`; await section().screenshot({ path: path.join(output, file), animations: 'disabled' }); screenshots.push(file) }
    await page.goto(url + '&mode=before'); await openEdit(); await shot('before')
    await page.goto(url + '&mode=after'); await openEdit()
    const toggle = page.locator('[data-testid="edit-free-openai-ultrafast"]')
    await toggle.waitFor(); assert.equal(await toggle.getAttribute('aria-checked'), 'false'); await shot('after-default')
    await toggle.click(); await shot('enabled')
    await page.locator('#edit-group-form').evaluate(form => form.requestSubmit())
    await page.waitForFunction(() => window.__ultraFixture.writes.length === 1)
    const saved = await page.evaluate(() => window.__ultraFixture.writes[0])
    assert.equal(saved.free_openai_ultrafast, true); assert.equal(saved.free_openai_fast, true); assert.equal(saved.force_openai_fast, false)
    await openEdit(); assert.equal(await toggle.getAttribute('aria-checked'), 'true')
    await toggle.locator('xpath=..').locator('[data-testid="new-feature-badge"]').click()
    await page.reload(); await openEdit(); assert.equal(await toggle.getAttribute('aria-checked'), 'true')
    assert.equal(await toggle.locator('xpath=..').locator('[data-testid="new-feature-badge"]').count(), 0); await shot('reloaded')
    await page.evaluate(() => document.documentElement.classList.add('dark')); await shot('dark')
    await page.close()
  }
  assert.deepEqual(errors, [])
  await fs.writeFile(path.join(output, 'manifest.json'), JSON.stringify({ head, base: process.env.SOURCE_BASE, synthetic: true,
    source: 'Real GroupsView, Toggle, NewFeatureBadge and locales; layout/auth/API fixtures; no production access', screenshots, errors }, null, 2))
} finally {
  if (browser) await browser.close(); await server.close(); await fs.rm(fixture, { recursive: true, force: true }); await fs.rm(beforePath, { force: true })
}
