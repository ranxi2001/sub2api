// Actual account setting component with synthetic state; no production access.
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
const root = resolve('frontend'), output = process.env.SCREENSHOT_DIR
if (!output || !process.env.PLAYWRIGHT_ROOT) throw Error('Missing screenshot configuration')
mkdirSync(output, { recursive: true })
const require = createRequire(join(root, 'package.json'))
const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
const tailwind = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
const prefix = `.probe-penalty-${process.pid}`, files = []
const create = (name, text) => { const path = join(root, name); writeFileSync(path, text, { flag: 'wx' }); files.push(path); return path }
let server, browser
try {
  const html = create(prefix + '.html', `<html lang="zh"><meta charset="utf-8"><div id="app"></div><script type="module" src="./${prefix}.js"></script></html>`)
  create(prefix + '.js', `
import {createApp,defineComponent,ref,h} from 'vue'; import {createPinia} from 'pinia'; import {createI18n} from 'vue-i18n';
import {useAuthStore} from '/src/stores/auth.ts'; import Setting from '/src/components/account/ProbePenaltySettings.vue';
import zh from '/src/i18n/locales/zh/index.ts'; import '/src/style.css';
const app=createApp(defineComponent({setup(){const enabled=ref(false),minimum=ref(.001);return()=>h('main',{class:'p-6 bg-white dark:bg-dark-900'},[h('h1',{class:'text-lg mb-4 dark:text-white'},'账号管理 / 编辑虚构 Claude 账号'),h(Setting,{enabled:enabled.value,minimum:minimum.value,'onUpdate:enabled':v=>enabled.value=v,'onUpdate:minimum':v=>minimum.value=v})])}}));
const pinia=createPinia();app.use(pinia);useAuthStore().user={id:41,role:'admin',email:'admin@example.invalid'};
app.use(createI18n({legacy:false,locale:'zh-CN',messages:{'zh-CN':zh}})).mount('#app');
`)
  await build({ root, configFile: false, base: './', resolve: { alias: { '@': join(root, 'src') } }, plugins: [vue()],
    css: { postcss: { plugins: [require('tailwindcss')({ ...tailwind, content: [html, join(root, 'src/**/*.{vue,js,ts}')] }), require('autoprefixer')()] } },
    build: { outDir: join(output, 'build'), emptyOutDir: true, rollupOptions: { input: html } } })
  server = await preview({ root, configFile: false, build: { outDir: join(output, 'build') }, preview: { host: '127.0.0.1', port: 4198, strictPort: true } })
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1000, height: 620 } }), errors = []
  page.on('pageerror', e => errors.push(e.message))
  await page.route('**/*', route => new URL(route.request().url()).origin === 'http://127.0.0.1:4198' ? route.continue() : route.abort())
  await page.goto(`http://127.0.0.1:4198/${prefix}.html`)
  const card = page.locator('[data-testid="probe-penalty-settings"]')
  await card.waitFor()
  await page.locator('main').screenshot({ path: join(output, 'disabled.png') })
  await page.locator('[data-testid="probe-penalty-enabled"]').check()
  if (await page.locator('[data-testid="probe-penalty-minimum"]').inputValue() !== '0.001') throw Error('Wrong default minimum')
  await page.locator('main').screenshot({ path: join(output, 'enabled.png') })
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await page.locator('main').screenshot({ path: join(output, 'dark.png') })
  await card.locator('[data-testid="new-feature-badge"]').click()
  await page.reload(); await card.waitFor()
  if (await card.locator('[data-testid="new-feature-badge"]').count()) throw Error('Dismissal did not persist')
  if (errors.length) throw Error(errors.join('\n'))
  writeFileSync(join(output, 'manifest.json'), JSON.stringify({ commit: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(), scope: 'actual ProbePenaltySettings component with synthetic parent state, not full account page or production', screenshots: ['disabled.png', 'enabled.png', 'dark.png'], errors }, null, 2))
} finally {
  if (browser) await browser.close()
  if (server) await new Promise(resolve => server.httpServer.close(resolve))
  for (const file of files) rmSync(file, { force: true })
  rmSync(join(output, 'build'), { recursive: true, force: true })
}
