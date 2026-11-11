// CI component preview: real AccountPerformanceCell, synthetic metrics, no backend.
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const root = resolve('frontend')
const output = process.env.SCREENSHOT_DIR
if (!output || !process.env.SOURCE_BASE || !process.env.PLAYWRIGHT_ROOT) throw Error('Missing screenshot configuration')
mkdirSync(output, { recursive: true })
const require = createRequire(join(root, 'package.json'))
const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
const tailwindcss = require('tailwindcss'), autoprefixer = require('autoprefixer')
const tailwindConfig = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
const suffix = `.ci-account-cache-${process.pid}`
const files = []
const create = (path, text) => { writeFileSync(path, text, { flag: 'wx' }); files.push(path); return path }
const errors = [], screenshots = []
let browser, server
try {
  create(join(root, 'src/components/account', suffix + '-before.vue'), execFileSync('git', ['show', `${process.env.SOURCE_BASE}:frontend/src/components/account/AccountPerformanceCell.vue`], { encoding: 'utf8' }))
  const html = create(join(root, suffix + '.html'), `<html lang="zh"><meta charset="utf-8"><div id="app"></div><script type="module" src="./${suffix}.js"></script></html>`)
  create(join(root, suffix + '.js'), `
    import {createApp,h} from 'vue';import {createPinia} from 'pinia';import {createI18n} from 'vue-i18n';
    import Before from '/src/components/account/${suffix}-before.vue';import After from '/src/components/account/AccountPerformanceCell.vue';
    import zh from '/src/i18n/locales/zh/index.ts';import '/src/style.css';
    const mode=new URLSearchParams(location.search).get('mode');
    const rows=[['示例账号 A',70,2],['示例账号 B',0,3],['示例账号 C',null,0]];
    createApp({render:()=>h('main',{class:'p-6 bg-white text-gray-900 dark:bg-dark-900 dark:text-gray-100'},[
      h('h1',{class:'text-xl font-semibold mb-2'},'账号管理 · 性能列组件预览'),
      h('p',{class:'text-sm text-gray-500 mb-4'},'虚构数据 · 实际组件渲染'),
      h('table',{class:'w-full'},[
        h('thead',h('tr',[h('th',{class:'text-left pb-3'},'账号'),h('th',{class:'text-left pb-3'},'性能 · 近60分钟')])),
        h('tbody',rows.map(([name,cache,samples])=>h('tr',{class:'border-t border-gray-200 dark:border-gray-700'},[
          h('td',{class:'py-4 align-top pr-12'},name),
          h('td',{class:'py-4'},h(mode==='before'?Before:After,{noticeUserId:41,metrics:{
            avg_tps:42,avg_ttft_ms:1200,degradation_percent:0,avg_cache_percent:cache,cache_samples:samples,
            window_start:'2026-11-12T00:00:00Z',window_end:'2026-11-12T01:00:00Z'
          }}))
        ])))
      ])
    ])}).use(createPinia()).use(createI18n({legacy:false,locale:'zh',messages:{zh}})).mount('#app');
  `)
  const outDir = join(output, 'preview-build')
  await build({ root, configFile: false, cacheDir: join(output, 'vite-cache'), resolve: { alias: { '@': join(root, 'src') } }, plugins: [vue()], css: { postcss: { plugins: [tailwindcss({ ...tailwindConfig, content: [html, join(root, suffix + '.js'), join(root, 'src/**/*.{vue,js,ts}')] }), autoprefixer()] } }, build: { outDir, emptyOutDir: true, minify: false, rollupOptions: { input: html } } })
  server = await preview({ root, configFile: false, build: { outDir }, preview: { host: '127.0.0.1', port: 4198, strictPort: true } })
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 760, height: 850 }, locale: 'zh-CN' })
  page.on('pageerror', error => errors.push(error.message))
  await page.route('**/*', route => {
    if (new URL(route.request().url()).origin === 'http://127.0.0.1:4198') return route.continue()
    errors.push('Unexpected external request'); return route.abort()
  })
  const capture = async name => { await page.screenshot({ path: join(output, name), animations: 'disabled' }); screenshots.push(name) }
  const url = `http://127.0.0.1:4198/${suffix}.html`
  await page.goto(url + '?mode=before')
  await page.locator('[data-metric="tps"]').first().waitFor()
  await capture('before.png')
  await page.goto(url + '?mode=after')
  await page.locator('[data-metric="cache"]').first().waitFor()
  const text = await page.locator('[data-metric="cache"]').allTextContents()
  if (!text[0].includes('70.0%') || !text[1].includes('0.0%') || !text[2].includes('未采集')) throw Error('Cache states rendered incorrectly')
  await capture('after.png')
  await page.locator('[data-testid="cache-help"]').first().click()
  await page.getByRole('tooltip').filter({ hasText: '最近60分钟有效请求缓存命中率的算术平均' }).waitFor()
  await capture('formula.png')
  await page.keyboard.press('Escape')
  await page.locator('[data-metric="cache"] [data-testid="new-feature-badge"]').first().click()
  if (await page.locator('[data-metric="cache"] [data-testid="new-feature-badge"]').count()) throw Error('Notice dismissal not shared')
  await page.reload()
  await page.locator('[data-metric="cache"]').first().waitFor()
  if (await page.locator('[data-metric="cache"] [data-testid="new-feature-badge"]').count()) throw Error('Notice dismissal not persisted')
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await capture('dark.png')
  if (errors.length) throw Error(errors.join('; '))
} finally {
  writeFileSync(join(output, 'manifest.json'), JSON.stringify({ source_commit: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(), base_commit: process.env.SOURCE_BASE, scope: 'Real AccountPerformanceCell in synthetic table shell, no AccountsView navigation, backend or production validation', screenshots, errors }, null, 2) + '\n')
  await browser?.close()
  await server?.httpServer.close()
  for (const file of files) rmSync(file, { force: true })
}
