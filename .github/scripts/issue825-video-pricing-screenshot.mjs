// Remote-only: same full-tree / actual CSS rendering pattern as upstream-v0221-screenshot.mjs.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const output = process.env.SCREENSHOT_DIR
assert.ok(output && process.env.SOURCE_BASE && process.env.BASELINE_ROOT && process.env.PLAYWRIGHT_ROOT)
const head = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
assert.equal(head, process.env.EXPECTED_HEAD_SHA)
mkdirSync(output, { recursive: true })
const digest = path => createHash('sha256').update(readFileSync(path)).digest('hex')
const manifest = {
  baseline: process.env.SOURCE_BASE, candidate: head,
  boundary: 'Full baseline archive and exact candidate; actual Vue component, locales, style.css and original Tailwind. Identical synthetic props, no API calls or production data. Component preview only.',
  screenshots: [], observations: [], sources: [], errors: []
}
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
const browser = await chromium.launch({ headless: true })
try {
  for (const mode of ['before', 'after']) {
    const root = resolve(mode === 'before' ? process.env.BASELINE_ROOT : 'frontend')
    const require = createRequire(join(root, 'package.json'))
    const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
    const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
    const tailwind = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
    const tag = '.ci-issue825-' + process.pid
    const paths = [join(root, tag + '.html'), join(root, tag + '.mjs')]
    writeFileSync(paths[0], `<html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="./${tag}.mjs"></script></html>`)
    writeFileSync(paths[1], readFileSync('.github/scripts/issue825-video-pricing-fixture.mjs'))
    const sourcePaths = ['src/components/modelPlaza/PlazaModelPricingTable.vue', 'src/style.css', 'tailwind.config.js', 'src/i18n/locales/zh/dashboard.ts', 'src/i18n/locales/en/dashboard.ts']
    manifest.sources.push({ mode, sha: mode === 'before' ? process.env.SOURCE_BASE : head, files: sourcePaths.map(path => ({ path, sha256: digest(join(root, path)) })) })
    const buildDir = join(output, mode + '-build')
    let server
    try {
      await build({
        root, configFile: false, cacheDir: join(output, mode + '-cache'), plugins: [vue()],
        resolve: { alias: { '@': join(root, 'src'), 'vue-i18n': require.resolve('vue-i18n/dist/vue-i18n.runtime.esm-bundler.js') } },
        define: { __INTLIFY_JIT_COMPILATION__: true },
        css: { postcss: { plugins: [require('tailwindcss')({ ...tailwind, content: [...paths, join(root, 'src/**/*.{vue,js,ts}')] }), require('autoprefixer')()] } },
        build: { outDir: buildDir, emptyOutDir: true, minify: false, target: 'esnext', rollupOptions: { input: paths[0] } }
      })
      server = await preview({ root, configFile: false, build: { outDir: buildDir }, preview: { host: '127.0.0.1', port: 4199, strictPort: true } })
      for (const lang of ['zh', 'en']) {
        const page = await browser.newPage({ viewport: { width: 1440, height: 1100 }, locale: lang === 'zh' ? 'zh-CN' : 'en-US', timezoneId: 'UTC' })
        page.on('pageerror', error => manifest.errors.push(`${mode}/${lang}: ${error.message}`))
        await page.route('**/*', route => new URL(route.request().url()).origin === 'http://127.0.0.1:4199' ? route.continue() : route.abort())
        try {
          await page.goto(`http://127.0.0.1:4199/${tag}.html?lang=${lang}`)
          await page.waitForFunction(() => window.__issue825Ready)
          await page.evaluate(async () => { await document.fonts.ready; await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))) })
          const observed = await page.locator('section').evaluateAll(sections => sections.map(section => ({
            scenario: section.dataset.scenario,
            rows: [...section.querySelectorAll('tbody tr')].map(row => [...row.querySelectorAll('td')].map(cell => cell.innerText.trim()))
          })))
          const label = lang === 'zh' ? (mode === 'before' ? '按次计费' : '按秒计费') : (mode === 'before' ? 'Per request' : 'Per second')
          const suffix = lang === 'zh' ? (mode === 'before' ? '/ 次' : '/ 秒') : (mode === 'before' ? '/ request' : '/ second')
          const prices = { group: ['$0.27', '$0.54'], user: ['$0.135', '$0.27'], independent: ['$1.08', '$2.16'] }
          for (const section of observed) {
            const videos = section.rows.filter(row => row[0].startsWith('demo-video'))
            assert.equal(videos.length, 2)
            for (const row of videos) {
              assert.ok(row[0].includes(label), JSON.stringify(row))
              assert.ok(row[1].includes(suffix), JSON.stringify(row))
              assert.ok(row[1].includes(prices[section.scenario][0]), JSON.stringify(row))
              if (row[0].startsWith('demo-video-resolution')) {
                assert.ok(row[1].includes('480p') && row[1].includes('720p'))
                assert.ok(row[1].includes(prices[section.scenario][1]))
              }
            }
          }
          const css = await page.locator('table').first().evaluate(table => ({ fontSize: getComputedStyle(table).fontSize, minWidth: getComputedStyle(table).minWidth, borderCollapse: getComputedStyle(table).borderCollapse }))
          assert.equal(css.minWidth, '1000px')
          assert.equal(css.fontSize, '14px')
          manifest.observations.push({ mode, lang, css, sections: observed })
          for (const theme of ['light', 'dark']) {
            await page.evaluate(value => document.documentElement.classList.toggle('dark', value === 'dark'), theme)
            await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
            const name = `${mode}-${lang}-${theme}.png`
            await page.screenshot({ path: join(output, name), fullPage: true, animations: 'disabled' })
            manifest.screenshots.push({ name, sha: mode === 'before' ? process.env.SOURCE_BASE : head, sha256: digest(join(output, name)) })
          }
          console.log(JSON.stringify({ mode, lang, label, suffix, css, scenarios: observed.length }))
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
} finally {
  writeFileSync(join(output, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n')
  await browser.close()
}
