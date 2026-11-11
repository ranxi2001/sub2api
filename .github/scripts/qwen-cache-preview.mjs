// Actual shared Qwen component and project styles, synthetic admin state only.
import { createRequire } from 'node:module'
import { mkdir, writeFile, rm, mkdtemp } from 'node:fs/promises'
import { execFileSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { pathToFileURL } from 'node:url'

const repo = process.cwd(), root = path.join(repo, 'frontend')
const output = process.env.EVIDENCE_DIR
if (!output || !process.env.PLAYWRIGHT_ROOT) throw Error('Evidence and browser paths are required')
const requireFrontend = createRequire(path.join(root, 'package.json'))
const { createServer } = await import(pathToFileURL(path.join(path.dirname(requireFrontend.resolve('vite/package.json')), 'dist/node/index.js')).href)
const vue = (await import(pathToFileURL(requireFrontend.resolve('@vitejs/plugin-vue')).href)).default
const { chromium } = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))('playwright')
const entryName = `.qwen-cache-preview-${process.pid}.js`, entry = path.join(root, entryName)
const cacheDir = await mkdtemp(path.join(tmpdir(), 'qwen-cache-preview-'))
let browser, server
const errors = []
await mkdir(output, { recursive: true })
try {
  await writeFile(entry, `
import {createApp,h,ref} from 'vue';
import {createPinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import Settings from '/src/components/account/QwenCacheSettings.vue';
import Badge from '/src/components/common/NewFeatureBadge.vue';
import {useNewFeatureNoticeStore} from '/src/stores/newFeatureNotices.ts';
import {NEW_FEATURE_NOTICES} from '/src/constants/newFeatureNotices.ts';
import '/src/style.css';
const lang=new URL(location.href).searchParams.get('lang')||'zh';
await loadLocaleMessages(lang);i18n.global.locale.value=lang;
const pinia=createPinia();const notices=useNewFeatureNoticeStore(pinia);
notices.dismiss(99999,NEW_FEATURE_NOTICES.qwenPlatform);
const app=createApp({setup(){const cache=ref(false),strict=ref(false),url=ref('');
return()=>h('main',{class:'mx-auto max-w-3xl space-y-5 p-6 text-gray-900 dark:text-gray-100'},[
h('p',{},lang==='zh'?'千问账号设置 · 实际共享组件 / 合成管理员数据':'Qwen settings · Actual shared component / synthetic admin'),
...['create','edit'].map(id=>h('section',{'data-preview':id},[
h('h2',{class:'mb-2 font-semibold'},id==='create'?'Create account':'Edit account'),
h(Settings,{sessionCache:cache.value,strictAffinity:strict.value,userId:99999,
'onUpdate:sessionCache':v=>cache.value=v,'onUpdate:strictAffinity':v=>strict.value=v,onBaseUrl:v=>url.value=v})])),
h('output',{'data-testid':'chosen-endpoint'},url.value),
h('div',{'data-preview':'future'},[h(Badge,{noticeId:'qwen-cache-preview-future',userId:99999})])]);}});
app.use(pinia);app.use(i18n);app.mount('#app');
`)
  process.chdir(root)
  server = await createServer({ root, configFile: false, cacheDir,
    resolve: { alias: { '@': path.join(root, 'src') } },
    plugins: [vue(), { name: 'qwen-cache-preview', configureServer(vite) {
      vite.middlewares.use((request, response, next) => {
        if (request.url === '/' || request.url?.startsWith('/?')) {
          response.setHeader('Content-Type', 'text/html')
          response.end(`<!doctype html><html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><body><div id="app"></div><script type="module" src="/${entryName}"></script></body></html>`)
        } else next()
      })
    } }], server: { host: '127.0.0.1', port: 4298, strictPort: true } })
  await server.listen()
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1080, height: 980 } })
  page.on('pageerror', e => errors.push(e.message))
  await page.goto('http://127.0.0.1:4298/', { waitUntil: 'networkidle' })
  const cards = page.getByTestId('qwen-cache-settings')
  await cards.first().waitFor()
  if (await cards.getByTestId('new-feature-badge').count() !== 2) throw Error('New announcement missing after old announcement was dismissed')
  await page.screenshot({ path: path.join(output, 'defaults-synthetic.png'), fullPage: true })
  await cards.first().getByTestId('qwen-session-cache').check()
  await cards.first().getByTestId('qwen-strict-affinity').check()
  await cards.first().getByTestId('qwen-token-plan-url').click()
  if (await page.getByTestId('chosen-endpoint').innerText() !== 'https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1') throw Error('Token Plan preset mismatch')
  await page.screenshot({ path: path.join(output, 'enabled-synthetic.png'), fullPage: true })
  await cards.first().getByTestId('new-feature-badge').click()
  await page.waitForFunction(() => !document.querySelector('[data-testid="qwen-cache-settings"] [data-testid="new-feature-badge"]'))
  await page.reload({ waitUntil: 'networkidle' })
  if (await cards.getByTestId('new-feature-badge').count()) throw Error('Dismissal did not survive reload')
  if (await page.locator('[data-preview="future"] [data-testid="new-feature-badge"]').count() !== 1) throw Error('Future announcement was incorrectly dismissed')
  await page.goto('http://127.0.0.1:4298/?lang=en', { waitUntil: 'networkidle' })
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await page.setViewportSize({ width: 390, height: 844 })
  await page.screenshot({ path: path.join(output, 'english-mobile-dark-synthetic.png'), fullPage: true })
  if (errors.length) throw Error(errors.join('\n'))
  await writeFile(path.join(output, 'manifest.json'), JSON.stringify({
    sha: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: repo, encoding: 'utf8' }).trim(),
    errors, announcementChecks: ['old dismissed/new visible', 'shared dismissal', 'reload persistence', 'future ID visible'],
    boundary: 'Actual QwenCacheSettings and NewFeatureBadge with project styles; synthetic state. Not a full account page, API save or production test.'
  }, null, 2))
} finally {
  await browser?.close()
  await server?.close()
  await rm(entry, { force: true })
  await rm(cacheDir, { recursive: true, force: true })
}
