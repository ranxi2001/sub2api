// Render the real account usage cell with synthetic data; no backend or login.
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const root = resolve('frontend')
const output = process.env.SCREENSHOT_DIR
const baseline = process.env.SOURCE_BASE
if (!output || !baseline || !process.env.PLAYWRIGHT_ROOT) throw Error('Missing CI screenshot configuration')
mkdirSync(output, { recursive: true })
const require = createRequire(join(root, 'package.json'))
const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
const tailwindcss = require('tailwindcss')
const autoprefixer = require('autoprefixer')
const tailwindConfig = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
const suffix = `.ci-mistral-${process.pid}`
const accountDirectory = join(root, 'src/components/account')
const entry = join(root, suffix + '.js')
const html = join(root, suffix + '.html')
const api = join(root, suffix + '-api.js')
const locale = join(root, suffix + '-locale.js')
const files = [entry, html, api, locale]
const before = new Map()
for (const name of ['AccountUsageCell.vue', 'CNProviderQuotaCell.vue', 'CNProviderBalanceCell.vue', 'credentialsBuilder.ts']) {
  const target = join(accountDirectory, suffix + '-' + name)
  before.set(name, target)
  files.push(target)
}
const previewDirectory = join(output, 'preview-build')
const errors = []
let server, browser
try {
  for (const [name, target] of before) {
    writeFileSync(target, execFileSync('git', ['show', `${baseline}:frontend/src/components/account/${name}`]), { flag: 'wx' })
  }
  writeFileSync(api, `
    const balance={provider:'mistral',success:true,balance:18,currency:'USD',balances:[{currency:'USD',balance:18}],available:true,persisted:true,fetched_at:Math.floor(Date.now()/1000)};
    export const adminAPI={cnProviders:{queryQuota:async()=>({provider:'mistral',success:true,persisted:true,credential_valid:true,tiers:[{window:'monthly',used_percent:40}],balance}),queryBalance:async()=>balance}};
  `, { flag: 'wx' })
  writeFileSync(locale, `
    import {createI18n} from 'vue-i18n';
    import zh from '/src/i18n/locales/zh/index.ts';
    export const i18n=createI18n({legacy:false,locale:'zh-CN',messages:{'zh-CN':zh}});
    export const getLocale=()=> 'zh-CN';
  `, { flag: 'wx' })
  writeFileSync(entry, `
    import {createApp,h} from 'vue';
    import {createPinia} from 'pinia';
    import {i18n} from '@fixture-i18n';
    import Before from '/src/components/account/${suffix}-AccountUsageCell.vue';
    import After from '/src/components/account/AccountUsageCell.vue';
    import '/src/style.css';
    const account={id:9106,name:'虚构 Mistral 账号',platform:'mistral',type:'apikey',status:'active',schedulable:true,concurrency:1,credentials:{api_key:'fixture-only-never-issued',base_url:'https://api.mistral.ai/v1'},extra:{mistral_monthly_used_percent:25,mistral_usage_updated_at:new Date().toISOString(),mistral_balance:22.5,mistral_balance_currency:'USD',mistral_balance_available:true,mistral_balance_low:false}};
    const mode=new URLSearchParams(location.search).get('mode');
    createApp({render:()=>h('main',{class:'min-h-screen bg-gray-50 p-8 text-gray-900 dark:bg-dark-900 dark:text-gray-100'},[
      h('h1',{class:'text-xl font-semibold mb-2'},'账号管理 / Mistral 用量'),
      h('p',{class:'text-sm text-gray-500 mb-5'},'实际 AccountUsageCell · CI 离线组件预览 · 虚构数据'),
      h('table',{class:'w-full rounded-lg border bg-white dark:bg-dark-800'},[
        h('thead',[h('tr',[h('th',{class:'p-4 text-left'},'账号'),h('th',{class:'p-4 text-left'},'用量与余额')])]),
        h('tbody',[h('tr',[h('td',{class:'p-4 align-top'},account.name),h('td',{class:'p-4',id:'usage-cell'},[h(mode==='before'?Before:After,{account,noticeUserId:41})])])])
      ])
    ])}).use(createPinia()).use(i18n).mount('#app');
  `, { flag: 'wx' })
  writeFileSync(html, `<html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="./${suffix}.js"></script></html>`, { flag: 'wx' })
  await build({
    root, configFile: false, cacheDir: join(output, 'vite-cache'),
    resolve: { alias: [
      { find: /^@\/api\/admin$/, replacement: api },
      { find: /^@\/i18n$/, replacement: locale },
      { find: '@fixture-i18n', replacement: locale },
      { find: '@', replacement: join(root, 'src') },
    ] },
    plugins: [{
      name: 'mistral-baseline-dependencies',
      enforce: 'pre',
      resolveId(id, importer) {
        if (!importer?.includes(suffix) || !importer.includes('/components/account/')) return
        const name = id.replace(/^\.\//, '')
        return before.get(name) || (name === 'credentialsBuilder' ? before.get('credentialsBuilder.ts') : undefined)
      },
    }, vue()],
    css: { postcss: { plugins: [tailwindcss({ ...tailwindConfig, content: [html, join(root, 'src/**/*.{vue,js,ts}')] }), autoprefixer()] } },
    build: { outDir: previewDirectory, emptyOutDir: true, minify: false, rollupOptions: { input: html } },
  })
  server = await preview({ root, configFile: false, build: { outDir: previewDirectory }, preview: { host: '127.0.0.1', port: 4197, strictPort: true } })
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1100, height: 620 }, locale: 'zh-CN', timezoneId: 'UTC' })
  page.on('pageerror', error => errors.push(error.message))
  page.on('response', response => { if (response.status() >= 400) errors.push(`HTTP ${response.status()} ${new URL(response.url()).pathname}`) })
  await page.route('**/*', route => {
    const url = new URL(route.request().url())
    if (url.origin === 'http://127.0.0.1:4197' || url.protocol === 'data:') return route.continue()
    errors.push('Blocked external origin: ' + url.origin)
    return route.abort()
  })
  const url = `http://127.0.0.1:4197/${suffix}.html`
  await page.goto(url + '?mode=before')
  await page.locator('#usage-cell').waitFor()
  if (await page.locator('[data-test="cn-provider-quota"]').count()) throw Error('Baseline unexpectedly exposes Mistral quota')
  await page.screenshot({ path: join(output, 'before.png'), fullPage: true, animations: 'disabled' })
  await page.goto(url + '?mode=after')
  await page.locator('[data-test="cn-provider-quota"]').waitFor()
  await page.getByText('25%', { exact: true }).waitFor()
  await page.screenshot({ path: join(output, 'after.png'), fullPage: true, animations: 'disabled' })
  await page.locator('[data-test="cn-provider-quota-probe"]').click()
  await page.getByText('40%', { exact: true }).waitFor()
  await page.getByText('USD 18.00', { exact: true }).waitFor()
  await page.screenshot({ path: join(output, 'probed.png'), fullPage: true, animations: 'disabled' })
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await page.screenshot({ path: join(output, 'dark.png'), fullPage: true, animations: 'disabled' })
  writeFileSync(join(output, 'manifest.json'), JSON.stringify({
    source_commit: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(), base_commit: baseline,
    mode: 'real AccountUsageCell with synthetic account and mocked quota API; no complete account page, authentication or production validation',
    screenshots: ['before.png', 'after.png', 'probed.png', 'dark.png'], errors,
  }, null, 2) + '\n')
  if (errors.length) throw Error(errors.join('; '))
} finally {
  if (browser) await browser.close()
  if (server) await new Promise((resolve, reject) => server.httpServer.close(error => error ? reject(error) : resolve()))
  for (const file of files) rmSync(file, { force: true })
  rmSync(join(output, 'vite-cache'), { recursive: true, force: true })
  rmSync(previewDirectory, { recursive: true, force: true })
}
