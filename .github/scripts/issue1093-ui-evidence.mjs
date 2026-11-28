// Remote CI only. Full original trees, identical fictional fixture, actual CSS.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { createRequire } from 'node:module'
import { copyFileSync, mkdirSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, relative, resolve, sep } from 'node:path'
import { pathToFileURL } from 'node:url'

assert.equal(process.env.CI, 'true', 'Only execute this harness in remote CI')
const git = (...args) => execFileSync('git', args, { encoding: 'utf8' }).trim()
const hash = data => createHash('sha256').update(data).digest('hex')
const base = '76a2e0f308219781833f560e5b642368cd106ad8'
const head = process.env.CANDIDATE_SHA
assert.match(head || '', /^[0-9a-f]{40}$/)
assert.equal(git('rev-parse', 'HEAD'), head)
assert.equal(git('status', '--porcelain'), '')
const temp = resolve(process.env.RUNNER_TEMP), output = resolve(process.env.EVIDENCE_DIR)
assert(output.startsWith(temp + sep))
mkdirSync(output, { recursive: true })
const manifest = { base, head, sources: {}, scenarios: [], screenshots: [], errors: [], passed: false,
  fixture_sha256: hash(readFileSync('.github/scripts/issue1093-ui-fixture.mjs')),
  harness_sha256: hash(readFileSync('.github/scripts/issue1093-ui-evidence.mjs')),
  run_id: process.env.GITHUB_RUN_ID, run_attempt: process.env.GITHUB_RUN_ATTEMPT,
  boundary: 'Actual AccountUsageCell and children, repository locales, full CSS/Tailwind theme in complete immutable archives; same synthetic fixture in both. No component stubs, source overlays, backend, login, full page navigation, real upstream or production. Browser network allows only exact built static files. PNGs require parent inspection and native PR attachment upload.' }
const paths = ['package.json', 'pnpm-lock.yaml', 'tailwind.config.js', 'postcss.config.js', 'src/style.css',
  'src/components/account/AccountUsageCell.vue', 'src/components/account/CNProviderBalanceCell.vue',
  'src/components/account/credentialsBuilder.ts', 'src/api/admin/cnProviders.ts',
  'src/i18n/locales/zh/admin/accounts.ts', 'src/i18n/locales/en/admin/accounts.ts']
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
const origin = 'http://127.0.0.1:4193', archives = []
let browser, activePage
try {
  browser = await chromium.launch({ headless: true })
  manifest.browser_version = browser.version()
  for (const mode of ['before', 'after']) {
    const revision = mode === 'before' ? base : head
    const tree = join(temp, `issue1093-render-${mode}-${head}`)
    mkdirSync(tree); archives.push(tree)
    const tar = join(tree, 'source.tar')
    execFileSync('git', ['archive', '--format=tar', `--output=${tar}`, revision])
    execFileSync('tar', ['-xf', tar, '-C', tree])
    const root = join(tree, 'frontend')
    // Never share dependencies unless both package inputs are identical.
    for (const path of ['package.json', 'pnpm-lock.yaml']) assert(readFileSync(join(root, path)).equals(readFileSync(join('frontend', path))), 'Dependency mismatch: ' + path)
    symlinkSync(resolve('frontend/node_modules'), join(root, 'node_modules'), 'dir')
    const identity = manifest.sources[mode] = { revision, tree: git('rev-parse', `${revision}^{tree}`), files: {}, css: [] }
    for (const path of paths) {
      const file = join(root, path), blob = git('rev-parse', `${revision}:frontend/${path}`)
      assert.equal(git('hash-object', file), blob, 'Original source mismatch: ' + path)
      identity.files[path] = { blob, sha256: hash(readFileSync(file)) }
    }
    const require = createRequire(join(root, 'package.json'))
    const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
    const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
    const theme = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
    const html = join(root, '.ci-issue1093.html'), entry = join(root, '.ci-issue1093.mjs'), built = join(tree, 'rendered')
    copyFileSync('.github/scripts/issue1093-ui-fixture.mjs', entry)
    writeFileSync(html, '<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"></head><body><div id="app"></div><script type="module" src="./.ci-issue1093.mjs"></script></body></html>', { flag: 'wx' })
    await build({ root, configFile: false, cacheDir: join(tree, 'cache'), plugins: [vue()],
      resolve: { alias: { '@': join(root, 'src') } },
      css: { postcss: { plugins: [require('tailwindcss')({ ...theme, content: [html, entry, join(root, 'src/**/*.{vue,js,ts}')] }), require('autoprefixer')()] } },
      build: { target: 'esnext', minify: false, outDir: built, emptyOutDir: true, rollupOptions: { input: html } } })
    mkdirSync(join(output, 'css', mode), { recursive: true })
    for (const file of readdirSync(join(built, 'assets')).filter(file => file.endsWith('.css'))) {
      const css = readFileSync(join(built, 'assets', file))
      copyFileSync(join(built, 'assets', file), join(output, 'css', mode, file))
      identity.css.push({ file, sha256: hash(css) })
    }
    assert(identity.css.length > 0, 'Missing real compiled CSS')
    const allowed = new Set(readdirSync(built, { recursive: true, withFileTypes: true }).filter(file => file.isFile())
      .map(file => '/' + relative(built, join(file.parentPath, file.name)).split(sep).join('/')))
    const server = await preview({ root, configFile: false, build: { outDir: built }, preview: { host: '127.0.0.1', port: 4193, strictPort: true } })
    try {
      for (const locale of ['zh', 'en']) for (const [style, viewport] of [
        ['light-desktop', { width: 1100, height: 1200 }], ['dark-desktop', { width: 1100, height: 1200 }], ['light-mobile', { width: 430, height: 932 }]
      ]) {
        const label = `${mode}-${locale}-${style}`
        const context = await browser.newContext({ viewport, locale: locale === 'zh' ? 'zh-CN' : 'en-US', timezoneId: 'UTC', serviceWorkers: 'block' })
        await context.addInitScript(locale => {
          localStorage.setItem('sub2api_locale', locale)
        }, locale)
        await context.route('**/*', route => {
          const url = new URL(route.request().url())
          if (url.protocol === 'data:' || (url.origin === origin && route.request().method() === 'GET' && allowed.has(decodeURIComponent(url.pathname)))) return route.continue()
          manifest.errors.push(`${label}: blocked ${route.request().method()} ${url.origin}${url.pathname}`)
          return route.abort()
        })
        const page = activePage = await context.newPage()
        page.setDefaultTimeout(15000)
        page.on('pageerror', error => manifest.errors.push(`${label}: ${error.message}`))
        page.on('console', message => { if (message.type() === 'error') manifest.errors.push(`${label}: ${message.text()}`) })
        await page.goto(origin + '/.ci-issue1093.html?theme=' + (style.startsWith('dark') ? 'dark' : 'light'))
        await page.waitForFunction(() => window.__issue1093?.ready)
        const row = key => page.locator(`[data-case="${key}"]`)
        const value = scope => scope.locator('[data-test="cn-provider-balance-value"]')
        const probe = scope => scope.locator('[data-test="cn-provider-balance-probe"]')
        const snapshot = async scenario => {
          const state = await page.evaluate(() => window.__issue1093)
          assert.deepEqual(state.unexpected, [])
          manifest.scenarios.push({ mode, locale, style, scenario, state })
          await page.evaluate(async () => { await document.fonts.ready; await new Promise(done => requestAnimationFrame(() => requestAnimationFrame(done))) })
          const file = `${label}-${scenario}.png`
          await page.screenshot({ path: join(output, file), fullPage: true, animations: 'disabled' })
          manifest.screenshots.push({ file, revision, sha256: hash(readFileSync(join(output, file))) })
        }
        assert.equal(await page.evaluate(() => window.__issue1093.requests.length), 0, 'Mount must never probe')
        for (const key of ['official', 'unsupported', 'openai']) {
          const text = await row(key).innerText()
          for (const part of ['1.2K req', '75.1M', 'A $67.83', 'U $15.63']) assert(text.includes(part), 'Generic usage missing: ' + key)
        }
        // Same actual-render behavioral oracle for both immutable sources.
        const restoredBalance = async () => {
          assert.equal(await value(row('official')).count(), 1, 'ISSUE1093_OFFICIAL_BALANCE_MISSING')
          assert.equal(await value(row('official')).innerText(), 'CNY 12.34')
          assert.equal(await probe(row('official')).count(), 1)
        }
        if (mode === 'before') {
          let defect
          try { await restoredBalance() } catch (error) { defect = error }
          assert(defect instanceof assert.AssertionError && defect.message.includes('ISSUE1093_OFFICIAL_BALANCE_MISSING'), 'Baseline must fail the named behavioral oracle, not setup')
          manifest.scenarios.push({ mode, locale, style, expected_behavioral_red: defect.message })
        } else {
          await restoredBalance()
          assert.equal(await value(row('zero')).innerText(), 'CNY 0.00')
          for (const key of ['zero', 'low']) assert((await row(key).innerText()).includes(locale === 'zh' ? '余额不足' : 'Insufficient balance'))
          assert.equal(await value(row('missing')).count(), 1)
          assert.equal(await probe(row('missing')).count(), 1)
          assert.equal(await value(row('custom')).innerText(), 'CNY 4.00 · USD 0.00')
          assert.equal(await probe(row('custom')).count(), 0)
          assert.equal(await value(row('unsupported')).count(), 0)
          assert.equal(await probe(row('adaptive')).count(), 1)
          // Verify theme utility CSS actually reached the real value node.
          const size = await value(row('official')).evaluate(el => getComputedStyle(el).fontSize)
          assert.equal(size, '10px')
        }
        await snapshot('initial')
        if (mode === 'after') {
          await probe(row('official')).click()
          await page.waitForFunction(() => document.querySelector('[data-case="official"] [data-test="cn-provider-balance-value"]')?.textContent.trim() === 'CNY 0.00')
          await page.evaluate(() => { window.__issue1093.queryFailure = true })
          await probe(row('official')).click()
          await row('official').getByText('Fictional HTTP 401', { exact: true }).waitFor()
          assert.equal(await value(row('official')).innerText(), 'CNY 0.00')
          assert.equal(await page.evaluate(() => window.__issue1093.requests.length), 2)
          await snapshot('query-failure-retains-zero')
          await page.evaluate(() => window.__issue1093ReplaceOfficial())
          await page.waitForFunction(() => document.querySelector('[data-case="official"] [data-test="cn-provider-balance-value"]')?.textContent.trim() === 'CNY 4.00')
          assert.equal(await probe(row('official')).count(), 0)
          assert(!(await row('official').innerText()).includes('Fictional HTTP 401'))
          assert.equal(await page.evaluate(() => window.__issue1093.requests.length), 2)
          await snapshot('changed-endpoint-read-only')
        }
        await context.close(); activePage = null
      }
    } finally { await new Promise((done, fail) => server.httpServer.close(error => error ? fail(error) : done())) }
  }
  assert.deepEqual(manifest.errors, [])
  manifest.passed = true
} catch (error) {
  manifest.errors.push(String(error.stack || error))
  if (activePage) await activePage.screenshot({ path: join(output, 'failure.png'), fullPage: true }).catch(() => {})
  throw error
} finally {
  writeFileSync(join(output, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n')
  if (browser) await browser.close()
  for (const tree of archives) rmSync(tree, { recursive: true, force: true })
}
