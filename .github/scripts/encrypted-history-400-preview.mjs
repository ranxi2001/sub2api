// Render the production Vue setting with synthetic state. No external API.
import { mkdirSync, mkdtempSync, writeFileSync, unlinkSync, rmSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const [repoArg, outputArg] = process.argv.slice(2)
if (!repoArg || !outputArg) throw Error('Usage: node encrypted-history-400-preview.mjs REPO OUTPUT')
const repo = resolve(repoArg), root = join(repo, 'frontend'), output = resolve(outputArg)
const require = createRequire(join(root, 'package.json'))
const { createServer } = await import(pathToFileURL(join(dirname(require.resolve('vite/package.json')), 'dist/node/index.js')).href)
const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
const { chromium } = await import(pathToFileURL(process.env.PLAYWRIGHT_MODULE).href)
const cache = mkdtempSync(join(tmpdir(), 'encrypted-history-400-'))
const entryName = `.encrypted-history-400-${process.pid}.js`, entry = join(root, entryName)
const origin = 'http://127.0.0.1:4297', errors = []
let server, browser
mkdirSync(output, { recursive: true })
try {
  writeFileSync(entry, `
import {createApp,ref} from 'vue';
import {createPinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import Setting from '/src/components/settings/EncryptedHistory400Settings.vue';
import PriorSetting from '/src/components/settings/EncryptedHistory409Settings.vue';
import '/src/style.css';
const locale=new URL(location.href).searchParams.get('locale')||'zh';
await loadLocaleMessages(locale); i18n.global.locale.value=locale;
createApp({components:{Setting,PriorSetting},setup(){return {enabled:ref(false),prior:ref(false)}},template:'<main class="mx-auto max-w-4xl p-8"><p class="mb-4 text-sm text-gray-500">System settings → Gateway · Actual component / fictional data</p><section class="card p-6"><PriorSetting v-model="prior" :user-id="99999" /><Setting v-model="enabled" :user-id="99999" /></section></main>'}).use(createPinia()).use(i18n).mount('#app');
`, { flag: 'wx' })
  process.chdir(root)
  server = await createServer({ root, configFile: false, cacheDir: cache,
    resolve: { alias: { '@': join(root, 'src'), vue: 'vue/dist/vue.esm-bundler.js' } },
    plugins: [{ name: 'encrypted-history-preview', configureServer(s) {
      s.middlewares.use((req, res, next) => {
        if (req.url === '/' || req.url?.startsWith('/?')) {
          res.setHeader('Content-Type', 'text/html')
          res.end(`<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/${entryName}"></script></html>`)
        } else if (req.url === '/favicon.ico') { res.statusCode = 204; res.end() } else next()
      })
    } }, vue()],
    server: { host: '127.0.0.1', port: 4297, strictPort: true },
  })
  await server.listen()
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1100, height: 620 } })
  page.on('pageerror', e => errors.push(e.message))
  await page.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort())
  await page.goto(origin, { waitUntil: 'networkidle' })
  const toggle = page.getByTestId('openai-400-omit-encrypted-history-toggle')
  await toggle.waitFor()
  await page.screenshot({ path: join(output, 'default-off.png'), fullPage: true, animations: 'disabled' })
  await toggle.click()
  if (await page.getByTestId('openai-409-omit-encrypted-history-toggle').isChecked()) throw Error('400 toggle changed 409')
  await page.screenshot({ path: join(output, 'enabled.png'), fullPage: true, animations: 'disabled' })
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await page.setViewportSize({ width: 430, height: 700 })
  await page.screenshot({ path: join(output, 'mobile-dark.png'), fullPage: true, animations: 'disabled' })
  await page.setViewportSize({ width: 1100, height: 620 })
  await page.goto(origin + '/?locale=en', { waitUntil: 'networkidle' })
  await toggle.waitFor()
  await page.screenshot({ path: join(output, 'english.png'), fullPage: true, animations: 'disabled' })
  writeFileSync(join(output, 'manifest.json'), JSON.stringify({ source: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: repo, encoding: 'utf8' }).trim(), boundary: 'Actual Vue setting component with fictional state; no full-page navigation, backend or production acceptance', errors }, null, 2))
  if (errors.length) throw Error(errors.join('\n'))
} finally {
  await browser?.close()
  await server?.close()
  unlinkSync(entry)
  rmSync(cache, { recursive: true, force: true })
}
