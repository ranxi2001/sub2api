// CI-only rendering of the real Vue settings component with fictional API data.
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const root = resolve('frontend')
const output = process.env.SCREENSHOT_DIR
if (!output || !process.env.PLAYWRIGHT_ROOT || !process.env.SOURCE_BASE) throw Error('Missing CI screenshot paths or base SHA')
mkdirSync(output, { recursive: true })
const require = createRequire(join(root, 'package.json'))
const { createServer } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
const tailwindcss = require('tailwindcss')
const autoprefixer = require('autoprefixer')
const tailwindConfig = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
const suffix = `.ci-serverless-${process.pid}`
const baselineName = suffix + '-before.vue'
const entryNames = [suffix + '-before.js', suffix + '-after.js']
const files = [baselineName, ...entryNames].map(name => join(root, name))
const baseline = execFileSync('git', ['show', `${process.env.SOURCE_BASE}:frontend/src/components/settings/ServerlessSettings.vue`], { encoding: 'utf8' })
const snapshot = {
  config: { enabled: false, pods: [{ id: 'cn-gateway', endpoint: 'https://cn.example.com', enabled: true }], regions: [{ country: 'CN', pod_ids: ['cn-gateway'] }] },
  pods: [{ id: 'cn-gateway', endpoint: 'https://cn.example.com', region: 'CN', version: 'preview', ready: true, at: 1790802000 }],
  stats: { 'cn-gateway|CN|region|requests': '128', 'primary|US|default|requests': '42' },
}
let server, browser
const errors = []
try {
  writeFileSync(files[0], baseline, { flag: 'wx' })
  for (const [i, name] of entryNames.entries()) {
    writeFileSync(join(root, name), `
      import { createApp, h } from 'vue';
      import { createI18n } from 'vue-i18n';
      import { createPinia } from 'pinia';
      import zh from '/src/i18n/locales/zh/common.ts';
      import Panel from '${i === 0 ? '/' + baselineName : '/src/components/settings/ServerlessSettings.vue'}';
      import '/src/style.css';
      createApp({render:()=>h('main',{class:'min-h-screen bg-gray-50 p-6 text-gray-900 dark:bg-dark-900 dark:text-gray-100'},[
        h('div',{class:'mx-auto max-w-5xl space-y-4'},[
          h('h1',{class:'text-xl font-semibold'},'设置 / 网关服务'),
          h('p',{class:'text-sm text-gray-500'},'实际组件 · CI 离线预览 · 全部为虚构数据'),h(Panel,{userId:41})])])
      }).use(createPinia()).use(createI18n({legacy:false,locale:'zh-CN',messages:{'zh-CN':zh}})).mount('#app');
    `, { flag: 'wx' })
  }
  server = await createServer({
    root, configFile: false, cacheDir: join(output, 'vite-cache'),
    resolve: { alias: { '@': join(root, 'src') } },
    css: { postcss: { plugins: [
      tailwindcss({ ...tailwindConfig, content: [join(root, 'index.html'), join(root, 'src/**/*.{vue,js,ts,jsx,tsx}'), files[0]] }),
      autoprefixer(),
    ] } },
    plugins: [vue(), {
      name: 'serverless-offline-preview',
      resolveId(id) {
        if (id === '@/api/client' || id === join(root, 'src/api/client') || id === join(root, 'src/api/client.ts')) return '\0serverless-api'
      },
      load(id) {
        if (id === '\0serverless-api') return `let snapshot=${JSON.stringify(snapshot)};export const apiClient={get:async()=>({data:structuredClone(snapshot)}),put:async(_u,c)=>{snapshot.config=structuredClone(c);return{data:structuredClone(c)}},post:async()=>({data:{ready:true}})};`
      },
      configureServer(vite) {
        vite.middlewares.use((req, res, next) => {
          if (!['/before', '/after'].includes(req.url)) return next()
          const entry = entryNames[req.url === '/before' ? 0 : 1]
          res.setHeader('Content-Type', 'text/html; charset=utf-8')
          res.end(`<html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/${entry}"></script></html>`)
        })
      },
    }],
    server: { host: '127.0.0.1', port: 4191, strictPort: true },
    optimizeDeps: { entries: [], include: ['vue', 'vue-i18n', 'pinia'] },
  })
  await server.listen()
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1280, height: 1100 }, locale: 'zh-CN', timezoneId: 'UTC' })
  await page.route('**/*', route => {
    const url = new URL(route.request().url())
    if (url.origin === 'http://127.0.0.1:4191' || url.protocol === 'data:') return route.continue()
    errors.push('Blocked external origin: ' + url.origin)
    return route.abort()
  })
  page.on('pageerror', error => errors.push(error.message))
  page.on('response', response => { if (response.status() >= 400) errors.push(`HTTP ${response.status()} ${new URL(response.url()).pathname}`) })
  for (const name of ['before', 'after']) {
    await page.goto('http://127.0.0.1:4191/' + name)
    await page.locator('summary').first().click()
    await page.waitForFunction(() => !document.querySelector('[data-testid="serverless-enabled"]').disabled)
    await page.screenshot({ path: join(output, name + '.png'), fullPage: true })
  }
  await page.locator('[data-testid="serverless-affinity-scope"]').selectOption('api_key_ip')
  await page.locator('[data-testid="serverless-primary-fallback"]').check()
  await page.getByRole('button', { name: '保存 Serverless 配置', exact: true }).click()
  await page.getByRole('status').waitFor()
  await page.screenshot({ path: join(output, 'ip-fallback-saved.png'), fullPage: true })
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await page.screenshot({ path: join(output, 'dark.png'), fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.screenshot({ path: join(output, 'mobile.png'), fullPage: true })
  writeFileSync(join(output, 'manifest.json'), JSON.stringify({
    source_commit: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(),
    base_commit: process.env.SOURCE_BASE,
    mode: 'actual component with mocked API; no full Settings navigation or production validation',
    fixtures: 'fictional example.com gateway and synthetic counters',
    screenshots: ['before.png', 'after.png', 'ip-fallback-saved.png', 'dark.png', 'mobile.png'], errors,
  }, null, 2) + '\n')
  if (errors.length) throw Error('Offline rendering errors: ' + errors.join('; '))
} finally {
  if (browser) await browser.close()
  if (server) await server.close()
  for (const file of files) rmSync(file, { force: true })
  rmSync(join(output, 'vite-cache'), { recursive: true, force: true })
}
