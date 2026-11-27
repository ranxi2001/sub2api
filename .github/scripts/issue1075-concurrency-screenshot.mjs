// Remote CI only: complete fixed base/head frontend trees, real capacity cells,
// locales and Tailwind CSS. The surrounding table and all accounts are fictional.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const output = process.env.SCREENSHOT_DIR
const base = 'd8f00260f44bf6a6d0385eaa92c74841ab5476d5'
if (!process.env.CI || !output || !process.env.BASELINE_ROOT || !process.env.PLAYWRIGHT_ROOT) throw Error('Remote CI configuration required')
assert.equal(process.env.SOURCE_BASE, base)
const head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
assert.equal(head, process.env.EXPECTED_HEAD_SHA)
assert.equal(execFileSync('git', ['-C', process.env.BASELINE_ROOT, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(), base)
mkdirSync(output, { recursive: true })
const manifest = {
  base_commit: base, head_commit: head,
  scope: 'Real AccountCapacityCell and children, complete fixed frontend trees, original Tailwind/global CSS and locales. Synthetic table shell/accounts; no full-page navigation, backend, Redis, upstream, production or paid requests.',
  screenshots: [], observations: [], errors: []
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
    const suffix = `.ci-issue1075-${process.pid}`
    const files = []
    const create = (path, source) => { writeFileSync(path, source, { flag: 'wx' }); files.push(path); return path }
    let server, page
    try {
      const html = create(join(root, suffix + '.html'), `<html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="./${suffix}.js"></script></html>`)
      create(join(root, suffix + '.js'), `
        import {createApp,h} from 'vue';import {createI18n} from 'vue-i18n';
        import Cell from '/src/components/account/AccountCapacityCell.vue';
        import zh from '/src/i18n/locales/zh/index.ts';import en from '/src/i18n/locales/en/index.ts';import '/src/style.css';
        const language=new URLSearchParams(location.search).get('lang')||'zh';
        const rows=[
          ['示例账号 A · OAuth',1,3,'oauth'],['示例账号 B · API Key',2,7,'apikey'],
          ['示例账号 C · 统计不可用',null,3,'oauth'],['示例账号 D · 字段缺失',undefined,7,'apikey'],
          ['示例账号 E · 真实零槽位',0,3,'oauth'],['示例账号 F · 非正容量原值',0,0,'apikey']
        ];
        createApp({render:()=>h('main',{class:'p-6 bg-white text-gray-900 dark:bg-dark-900 dark:text-gray-100 min-h-screen'},[
          h('h1',{class:'text-xl font-semibold mb-2'},'账号管理 · 容量并发占用'),
          h('p',{class:'text-sm text-gray-500 mb-4'},'虚构数据 · 真实容量组件与完整 CSS · ${mode}'),
          h('table',{class:'w-full text-left'},[
            h('thead',h('tr',[h('th',{class:'pb-3'},'示例账号 / 观测状态'),h('th',{class:'pb-3'},'容量 · 占用 / 配置')])),
            h('tbody',rows.map(([name,count,capacity,type],i)=>h('tr',{'data-row':i,class:'border-t border-gray-200 dark:border-gray-700'},[
              h('td',{class:'py-5 pr-10'},name),h('td',{class:'py-5'},h(Cell,{account:{id:i+41,name,platform:'openai',type,concurrency:capacity,current_concurrency:count,extra:{}}}))
            ])))
          ])
        ])}).use(createI18n({legacy:false,locale:language,messages:{zh,en}})).mount('#app');
      `)
      const outDir = join(output, `${mode}-build`)
      await build({
        root, configFile: false, cacheDir: join(output, `${mode}-vite-cache`),
        resolve: { alias: { '@': join(root, 'src') } }, plugins: [vue()],
        css: { postcss: { plugins: [tailwindcss({ ...tailwindConfig, content: [html, join(root, suffix + '.js'), join(root, 'src/**/*.{vue,js,ts}')] }), autoprefixer()] } },
        build: { outDir, emptyOutDir: true, minify: false, rollupOptions: { input: html } }
      })
      server = await preview({ root, configFile: false, build: { outDir }, preview: { host: '127.0.0.1', port: 4198, strictPort: true } })
      page = await browser.newPage({ viewport: { width: 1000, height: 690 }, locale: 'zh-CN' })
      page.on('pageerror', error => manifest.errors.push(error.message))
      await page.route('**/*', route => {
        if (new URL(route.request().url()).origin === 'http://127.0.0.1:4198') return route.continue()
        manifest.errors.push('Unexpected external request'); return route.abort()
      })
      for (const lang of ['zh', 'en']) {
        await page.goto(`http://127.0.0.1:4198/${suffix}.html?lang=${lang}`)
        await page.locator('[data-row="5"]').waitFor()
        const counts = await page.locator('tbody td:nth-child(2)').allTextContents()
        const compact = counts.map(value => value.replace(/\s/g, ''))
        assert.deepEqual(compact, mode === 'before' ? ['1/3', '2/7', '0/3', '0/7', '0/3', '0/0'] : ['1/3', '2/7', '—/3', '—/7', '0/3', '0/0'])
        const style = await page.locator('[data-row="0"] td:nth-child(2) > div > span').evaluate(el => ({ size: getComputedStyle(el).fontSize, background: getComputedStyle(el).backgroundColor }))
        assert.equal(style.size, '10px', 'Actual Tailwind badge CSS must be rendered')
        assert.notEqual(style.background, 'rgba(0, 0, 0, 0)')
        manifest.observations.push({ mode, lang, counts: compact, style })
        if (mode === 'after') {
          const title = await page.locator('[data-row="2"] [data-testid="account-concurrency"]').getAttribute('title')
          assert.equal(title, lang === 'zh' ? '当前并发占用暂不可用，请刷新重试' : 'Current concurrency is unavailable. Refresh to retry.')
        }
        const filename = `${mode}-${lang}.png`
        await page.screenshot({ path: join(output, filename), animations: 'disabled', fullPage: true })
        manifest.screenshots.push(filename)
      }
      await page.evaluate(() => document.documentElement.classList.add('dark'))
      const dark = `${mode}-dark.png`
      await page.screenshot({ path: join(output, dark), animations: 'disabled', fullPage: true })
      manifest.screenshots.push(dark)
    } finally {
      await page?.close()
      await server?.httpServer.close()
      for (const file of files) rmSync(file, { force: true })
    }
  }
  assert.deepEqual(manifest.errors, [])
} finally {
  writeFileSync(join(output, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n')
  await browser.close()
}
