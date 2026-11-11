// Remote-only rendering of complete immutable source trees, real components and CSS.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
const output = process.env.SCREENSHOT_DIR
assert.ok(output && process.env.SOURCE_BASE && process.env.BASELINE_ROOT && process.env.PLAYWRIGHT_ROOT)
const head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
assert.equal(head, process.env.EXPECTED_HEAD_SHA)
mkdirSync(output, { recursive: true })
const manifest = { baseline: process.env.SOURCE_BASE, candidate: head, boundary: 'Full baseline/candidate source archives; actual Vue components/locales/style.css and original Tailwind. Only AppLayout shell and HTTP adapter use synthetic fixtures. No production/paid requests.', screenshots: [], results: [], errors: [] }
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
const browser = await chromium.launch({ headless: true })
try {
  for (const mode of ['before', 'after']) {
    const root = resolve(mode === 'before' ? process.env.BASELINE_ROOT : 'frontend')
    const require = createRequire(join(root, 'package.json'))
    const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
    const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
    const tailwind = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
    const tag = '.ci-v0221-' + process.pid
    const paths = [join(root, tag + '.html'), join(root, tag + '.mjs'), join(root, tag + '-layout.mjs')]
    writeFileSync(paths[0], `<html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="./${tag}.mjs"></script></html>`)
    writeFileSync(paths[1], readFileSync('.github/scripts/upstream-v0221-ui-fixture.mjs'))
    writeFileSync(paths[2], "import {h} from 'vue'; export default{setup(_,ctx){return()=>h('main',{class:'p-6 min-h-screen bg-gray-50 dark:bg-dark-900'},ctx.slots.default?.())}}")
    const buildDir = join(output, mode + '-build')
    let server
    try {
      await build({ root, configFile: false, cacheDir: join(output, mode + '-cache'), plugins: [vue()],
        resolve: { alias: [{ find: '@/components/layout/AppLayout.vue', replacement: paths[2] }, { find: '@', replacement: join(root, 'src') }] },
        css: { postcss: { plugins: [require('tailwindcss')({ ...tailwind, content: [...paths, join(root, 'src/**/*.{vue,js,ts}')] }), require('autoprefixer')()] } },
        build: { outDir: buildDir, emptyOutDir: true, minify: false, target: 'esnext', rollupOptions: { input: paths[0] } } })
      server = await preview({ root, configFile: false, build: { outDir: buildDir }, preview: { host: '127.0.0.1', port: 4199, strictPort: true } })
      for (const scenario of ['pricing', 'usage', 'payment', 'settings', 'keys']) {
        const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, locale: 'zh-CN', timezoneId: 'UTC' })
        page.on('pageerror', error => manifest.errors.push(`${mode}/${scenario}: ${error.message}`))
        await page.route('**/*', route => {
          const url = new URL(route.request().url())
          if (url.origin === 'http://127.0.0.1:4199' || url.protocol === 'data:') return route.continue()
          return route.abort()
        })
        const shot = async suffix => {
          const name = `${mode}-${scenario}-${suffix}.png`, path = join(output, name)
          await page.screenshot({ path, animations: 'disabled' })
          manifest.screenshots.push({ name, sha256: createHash('sha256').update(readFileSync(path)).digest('hex') })
        }
        try {
          await page.goto(`http://127.0.0.1:4199/${tag}.html?scenario=${scenario}`)
          await page.waitForFunction(() => window.__v0221Ready)
          if (scenario === 'pricing') await page.locator('.cursor-pointer.select-none').first().click()
          if (scenario === 'usage') await page.waitForFunction(() => window.__v0221.requests.some(row => /\/usage$/.test(row.path)))
          if (scenario === 'keys') await page.waitForFunction(() => window.__v0221.pending.length > 0)
          if (scenario === 'settings') {
            await page.locator('form').first().waitFor()
            await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight * 0.55))
          }
          await shot('light')
          if (mode === 'after' && scenario === 'pricing') {
            const label = page.locator('label').filter({ hasText: '12h' }).first()
            await label.waitFor()
            await label.locator('..').locator('input').fill('0')
            await page.waitForFunction(() => window.__v0221.saved.some(row => row.pricing?.cache_write_12h_price === '0'))
          }
          if (mode === 'after' && scenario === 'usage') {
            await page.getByTestId('antigravity-active-query').click()
            await page.waitForFunction(() => window.__v0221.requests.some(row => row.params?.force === true && row.params?.source === 'active'))
          }
          if (mode === 'after' && scenario === 'settings') {
            const dock = page.locator('.settings-save-dock')
            assert.ok(await dock.count())
            const bounds = await dock.evaluate(element => { const r = element.getBoundingClientRect(); return { top: r.top, bottom: r.bottom, height: innerHeight, position: getComputedStyle(element).position } })
            assert.equal(bounds.position, 'sticky')
            assert.ok(bounds.top >= 0 && bounds.bottom <= bounds.height)
            manifest.results.push({ mode, scenario, dock: bounds })
          }
          await page.evaluate(() => document.documentElement.classList.add('dark'))
          await shot('dark')
          await page.setViewportSize({ width: 430, height: 950 })
          await shot('mobile')
          if (scenario === 'keys') {
            await page.evaluate(() => window.__v0221.pending.splice(0).forEach(resolve => resolve()))
            await shot('usage-loaded')
          }
          manifest.results.push({ mode, scenario, requests: await page.evaluate(() => window.__v0221.requests), saved: await page.evaluate(() => window.__v0221.saved) })
        } catch (error) {
          writeFileSync(join(output, `${mode}-${scenario}-failure.txt`), await page.locator('body').innerText())
          await shot('failure')
          throw error
        } finally { await page.close() }
      }
    } finally {
      if (server) await new Promise(resolve => server.httpServer.close(resolve))
      paths.forEach(path => rmSync(path, { force: true }))
      rmSync(buildDir, { recursive: true, force: true })
      rmSync(join(output, mode + '-cache'), { recursive: true, force: true })
    }
  }
  assert.deepEqual(manifest.errors, [])
} finally { writeFileSync(join(output, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n'); await browser.close() }
