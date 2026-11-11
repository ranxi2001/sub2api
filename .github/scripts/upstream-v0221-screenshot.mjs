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
      for (const scenario of ['pricing', 'usage', 'payment', 'settings', 'keys', 'accounts']) {
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
          if (scenario === 'usage') await page.waitForFunction(() => window.__v0221.requests.some(row => /\/usage$/.test(row.path)) && document.body.innerText.includes('71'))
          if (scenario === 'keys') await page.waitForFunction(() => window.__v0221.pending.length > 0)
          if (scenario === 'settings') {
            await page.locator('form').first().waitFor()
            await page.evaluate(() => window.scrollTo({ top: document.body.scrollHeight * 0.55, behavior: 'instant' }))
            await page.evaluate(async () => { await document.fonts.ready; await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))) })
          }
          let originalAccount
          if (scenario === 'accounts') {
            await page.getByText('Synthetic quota account', { exact: true }).first().waitFor()
            await page.waitForFunction(() => document.body.innerText.includes('71%'))
            originalAccount = await page.getByText('Synthetic quota account', { exact: true }).first().elementHandle()
            await page.getByRole('button', { name: '添加账号', exact: true }).locator('..').locator('button.btn-secondary').first().click()
            await page.waitForFunction(() => window.__v0221.pendingAccounts.length > 0)
            if (mode === 'after') {
              assert.equal(await originalAccount.evaluate(element => element.isConnected), true)
              assert.equal(await page.locator('[aria-busy="true"][inert]').count() > 0, true)
            } else assert.equal(await page.getByText('Synthetic quota account', { exact: true }).count(), 0)
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
            await page.waitForFunction(() => window.__v0221.requests.some(row => row.params?.force === 'true' && row.params?.source === 'active'))
            await page.waitForFunction(() => document.body.innerText.includes('9%'))
            await shot('query-refreshed')
          }
          if (mode === 'after' && scenario === 'settings') {
            const dock = page.locator('.settings-save-dock')
            assert.ok(await dock.count())
            const bounds = await dock.evaluate(element => { const r = element.getBoundingClientRect(); return { top: r.top, bottom: r.bottom, height: innerHeight, position: getComputedStyle(element).position } })
            assert.equal(bounds.position, 'sticky')
            assert.ok(bounds.top >= 0 && bounds.bottom <= bounds.height)
            manifest.results.push({ mode, scenario, dock: bounds })
          }
          const measureDock = async phase => {
            if (scenario !== 'settings' || mode !== 'after') return
            const observed = await page.evaluate(() => {
              const dock = document.querySelector('.settings-save-dock'), button = dock.querySelector('button[type="submit"]')
              const rect = element => { const r = element.getBoundingClientRect(); return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height } }
              const buttonRect = rect(button), hit = document.elementFromPoint((buttonRect.left + buttonRect.right) / 2, (buttonRect.top + buttonRect.bottom) / 2)
              const ancestors = []; for (let node = dock.parentElement; node; node = node.parentElement) { const css = getComputedStyle(node); ancestors.push({ tag: node.tagName, overflowX: css.overflowX, overflowY: css.overflowY, transform: css.transform, contain: css.contain }) }
              return { dock: rect(dock), button: buttonRect, width: innerWidth, height: innerHeight, scrollY, scrollHeight: document.scrollingElement.scrollHeight, position: getComputedStyle(dock).position, hit: button === hit || button.contains(hit), ancestors }
            })
            manifest.results.push({ mode, scenario, phase, observed })
            for (const rect of [observed.dock, observed.button]) assert.ok(rect.left >= 0 && rect.right <= observed.width && rect.top >= 0 && rect.bottom <= observed.height, `${phase}: ${JSON.stringify(observed)}`)
            assert.equal(observed.hit, true, `${phase}: actual submit centre must remain clickable`)
            return observed.button
          }
          await measureDock('desktop-light-settled')
          await page.evaluate(() => document.documentElement.classList.add('dark'))
          await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
          await measureDock('desktop-dark-settled')
          await shot('dark')
          await page.setViewportSize({ width: 430, height: 950 })
          if (scenario === 'settings') {
            await page.evaluate(() => window.scrollTo({ top: document.body.scrollHeight * 0.55, behavior: 'instant' }))
            await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
          }
          await measureDock('mobile-resized-settled')
          await shot('mobile')
          if (scenario === 'settings' && mode === 'after') {
            await page.reload()
            await page.waitForFunction(() => window.__v0221Ready)
            await page.locator('form').first().waitFor()
            await page.evaluate(async () => { await document.fonts.ready; await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))) })
            for (const [phase, ratio] of [['top', 0], ['middle', 0.55], ['end', 1]]) {
              await page.evaluate(value => window.scrollTo({ top: document.body.scrollHeight * value, behavior: 'instant' }), ratio)
              await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
              const rect = await measureDock('mobile-fresh-' + phase)
              if (phase === 'middle') {
                await page.mouse.click((rect.left + rect.right) / 2, (rect.top + rect.bottom) / 2)
                await page.waitForFunction(() => window.__v0221.pendingSettings.length > 0)
                assert.equal(await page.locator('.settings-save-dock button[type="submit"]').isDisabled(), true)
                await page.evaluate(() => window.__v0221.pendingSettings.splice(0).forEach(resolve => resolve()))
                await page.waitForFunction(() => !document.querySelector('.settings-save-dock button[type="submit"]').disabled)
                assert.ok(await page.evaluate(() => window.__v0221.saved.some(row => row.path === '/admin/settings')))
                await shot('mobile-saved')
              }
            }
          }
          if (scenario === 'keys') {
            await page.evaluate(() => window.__v0221.pending.splice(0).forEach(resolve => resolve()))
            await page.waitForFunction(() => document.body.innerText.includes('$3.7500'))
            await shot('usage-loaded')
          }
          if (scenario === 'accounts') {
            await page.evaluate(() => window.__v0221.pendingAccounts.splice(0).forEach(resolve => resolve()))
            await page.getByText('Synthetic refreshed account', { exact: true }).first().waitFor()
            await shot('refreshed')
            manifest.results.push({ mode, scenario, old_row_retained: mode === 'after' })
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
