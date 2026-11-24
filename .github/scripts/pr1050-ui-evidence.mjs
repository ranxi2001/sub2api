// CI-only screenshots of real archived Vue components. No historical overlays.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { createRequire } from 'node:module'
import { copyFileSync, existsSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, relative, resolve, sep } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const git = (...args) => execFileSync('git', args, { cwd: repo, encoding: 'utf8' }).trim()
const sha256 = bytes => createHash('sha256').update(bytes).digest('hex')
const base = 'c3f5cbde082f8785aaa9eae59ce9e6bc2542706c'
const head = process.env.EXPECTED_HEAD_SHA
const output = resolve(process.env.EVIDENCE_DIR || '')
const temp = resolve(process.env.RUNNER_TEMP || '')
const assertTemporary = path => assert.ok(path.startsWith(temp + sep), `Must use RUNNER_TEMP: ${path}`)
assert.equal(process.env.CI, 'true', 'Only run this build/browser harness in CI')
assert.match(head || '', /^[0-9a-f]{40}$/)
assert.equal(git('rev-parse', 'HEAD'), head)
assert.equal(git('status', '--porcelain'), '')
assertTemporary(output)
mkdirSync(output, { recursive: true })
const manifest = {
  base, capture_sha: head, candidate_tree: git('rev-parse', `${head}^{tree}`),
  boundary: 'Complete git archive base/head trees; real CreateAccountModal, EditAccountModal, HeaderOverrideEditor and child components, native Tailwind theme/plugins/CSS and locales. Synthetic admin/account/credentials and a strict in-browser Axios adapter. Form-only group preview/toggle interactions and shared notice localStorage persistence. No full application navigation, login, server writes, real database, upstream discovery or paid requests. Baseline absence assertions are not business RED tests.',
  fixture_sha256: sha256(readFileSync(join(repo, '.github/scripts/pr1050-ui-fixture.mjs'))),
  sources: {}, results: [], screenshots: [], console: [], errors: [], passed: false,
}
const components = ['src/components/account/EditAccountModal.vue', 'src/components/account/CreateAccountModal.vue',
  'src/components/account/HeaderOverrideEditor.vue', 'src/components/account/UpstreamGroupSyncButton.vue',
  'src/components/common/BaseDialog.vue', 'src/components/common/NewFeatureBadge.vue', 'src/stores/newFeatureNotices.ts',
  'src/constants/newFeatureNotices.ts', 'src/style.css', 'tailwind.config.js', 'package.json', 'pnpm-lock.yaml']
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
let browser
const settle = async page => {
  await page.evaluate(async () => {
    await document.fonts.ready
    await Promise.all(document.getAnimations().filter(a => a.effect?.getComputedTiming().iterations !== Infinity).map(a => a.finished.catch(() => {})))
    await new Promise(done => requestAnimationFrame(() => requestAnimationFrame(done)))
  })
}
try {
  browser = await chromium.launch({ headless: true })
  for (const mode of ['before', 'after']) {
    const ref = mode === 'before' ? base : head
    const root = resolve(process.env[mode === 'before' ? 'BASELINE_ROOT' : 'CANDIDATE_ROOT'])
    assertTemporary(root)
    const sources = manifest.sources[mode] = { capture_sha: ref, tree: git('rev-parse', `${ref}^{tree}`), files: {} }
    for (const path of components) {
      const file = join(root, path)
      if (!existsSync(file)) {
        assert.equal(mode, 'before')
        assert.equal(path, 'src/components/account/UpstreamGroupSyncButton.vue')
        sources.files[path] = { absent: true }
        continue
      }
      const blob = git('rev-parse', `${ref}:frontend/${path}`)
      assert.equal(git('hash-object', file), blob, `${mode} source mismatch: ${path}`)
      sources.files[path] = { blob, sha256: sha256(readFileSync(file)) }
    }
    const require = createRequire(join(root, 'package.json'))
    const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
    const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
    const tailwindcss = require('tailwindcss'), autoprefixer = require('autoprefixer')
    const theme = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
    const entry = join(root, '.ci-pr1050.mjs'), html = join(root, '.ci-pr1050.html')
    const buildDir = join(temp, `pr1050-${mode}-build`), cacheDir = join(temp, `pr1050-${mode}-cache`)
    let server
    try {
      assert.ok(!existsSync(entry) && !existsSync(html), 'Fixture must not replace archived files')
      copyFileSync(join(repo, '.github/scripts/pr1050-ui-fixture.mjs'), entry)
      writeFileSync(html, '<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"></head><body><div id="app"></div><script type="module" src="./.ci-pr1050.mjs"></script></body></html>', { flag: 'wx' })
      await build({ root, configFile: false, cacheDir, resolve: { alias: { '@': join(root, 'src') } }, plugins: [vue()],
        css: { postcss: { plugins: [tailwindcss({ ...theme, content: [html, entry, join(root, 'src/**/*.{vue,js,ts}')] }), autoprefixer()] } },
        build: { outDir: buildDir, emptyOutDir: true, minify: false, target: 'esnext', rollupOptions: { input: html } },
      })
      const allowedPaths = new Set(readdirSync(buildDir, { recursive: true, withFileTypes: true })
        .filter(item => item.isFile()).map(item => '/' + relative(buildDir, join(item.parentPath, item.name)).split(sep).join('/')))
      server = await preview({ root, configFile: false, build: { outDir: buildDir }, preview: { host: '127.0.0.1', port: 4195, strictPort: true } })
      for (const locale of ['zh', 'en']) {
        for (const [viewportName, viewport] of Object.entries({ desktop: { width: 1440, height: 1080 }, mobile: { width: 430, height: 932 } })) {
          const label = `${mode}-${locale}-${viewportName}`
          const context = await browser.newContext({ viewport, locale: locale === 'zh' ? 'zh-CN' : 'en-US', timezoneId: 'UTC', serviceWorkers: 'block' })
          const page = await context.newPage()
          page.setDefaultTimeout(15000)
          const result = { mode, locale, viewport, capture_sha: ref, observations: [], sessions: [] }
          manifest.results.push(result)
          page.on('pageerror', error => manifest.errors.push(`${label}: pageerror: ${error.message}`))
          page.on('console', message => {
            manifest.console.push({ label, type: message.type(), text: message.text() })
            if (['error', 'warning'].includes(message.type())) manifest.errors.push(`${label}: console ${message.type()}: ${message.text()}`)
          })
          await context.route('**/*', route => {
            const url = new URL(route.request().url())
            if (url.protocol === 'data:') return route.continue()
            if (url.origin === 'http://127.0.0.1:4195' && route.request().method() === 'GET' && allowedPaths.has(decodeURIComponent(url.pathname))) return route.continue()
            manifest.errors.push(`${label}: blocked request ${route.request().method()} ${url.origin}${url.pathname}`)
            return route.abort()
          })
          const snapshotState = async () => {
            const state = await page.evaluate(() => window.__pr1050)
            assert.ok(state?.ready, 'Fixture did not mount')
            result.sessions.push(state)
            assert.deepEqual(state.unexpected, [])
            return state
          }
          const capture = async (surface, focus) => {
            if (focus) { await focus.waitFor({ state: 'visible' }); await focus.scrollIntoViewIfNeeded() }
            await settle(page)
            const name = `${label}-${surface}.png`
            await page.screenshot({ path: join(output, name), animations: 'disabled' })
            manifest.screenshots.push({ name, mode, locale, viewport, surface, capture_sha: ref, base,
              sha256: sha256(readFileSync(join(output, name))) })
          }
          const dialog = () => page.getByRole('dialog')
          const open = async which => {
            await page.getByTestId(`fixture-open-${which}`).click()
            await dialog().waitFor({ state: 'visible' })
            await dialog().locator(`#${which}-account-form`).waitFor({ state: 'visible' })
            await settle(page)
            const bounds = await dialog().locator('.modal-content').boundingBox()
            assert.ok(bounds && bounds.width > 200 && bounds.x >= -1 && bounds.x + bounds.width <= viewport.width + 1, 'Dialog must fit the viewport')
          }
          const close = async () => {
            await dialog().getByRole('button', { name: 'Close modal', exact: true }).click()
            await dialog().waitFor({ state: 'hidden' })
            await settle(page)
          }
          const groupToggle = () => dialog().getByTestId('upstream-billing-group-sync')
          const absentGroupControls = async () => {
            assert.equal(await groupToggle().count(), 0)
            const text = locale === 'zh' ? '同步上游分组名称与备注' : 'Sync upstream group name and notes'
            assert.equal(await dialog().getByRole('button', { name: text, exact: true }).count(), 0)
          }
          const groupBadge = () => groupToggle().locator('xpath=..').locator('xpath=..').getByTestId('new-feature-badge')
          const headerBadge = scope => scope.getByTestId('header-override-variables').getByTestId('new-feature-badge')
          const fillCreate = async () => {
            await dialog().locator('[data-tour="account-form-platform"]').getByRole('button', { name: 'OpenAI', exact: true }).click()
            await dialog().locator('[data-tour="account-form-type"]').getByRole('button', { name: /API Key/ }).click()
            const baseLabel = await page.evaluate(() => window.__pr1050.labels.baseURL)
            await dialog().locator('label').filter({ hasText: new RegExp(`^${baseLabel}$`) }).locator('..').locator('input').fill('https://relay.example.invalid/v1')
            await dialog().locator('input[type="password"][required]').fill('synthetic-fixture-key')
          }
          const sync = async which => {
            const syncLabel = await page.evaluate(() => window.__pr1050.labels.sync)
            const prior = await page.evaluate(() => window.__pr1050.previews.length)
            await dialog().getByRole('button', { name: syncLabel, exact: true }).click()
            await page.waitForFunction(count => window.__pr1050.previews.length === count + 1, prior)
            const name = dialog().locator(`[data-tour="${which === 'edit' ? 'edit-account' : 'account'}-form-name"]`)
            await page.waitForFunction(selector => document.querySelector(selector)?.value === 'Fixture upstream group', `[data-tour="${which === 'edit' ? 'edit-account' : 'account'}-form-name"]`)
            assert.equal(await name.inputValue(), 'Fixture upstream group')
            assert.equal(await dialog().locator('textarea').first().inputValue(), 'Fixture upstream notes')
            assert.equal(await groupToggle().getAttribute('aria-checked'), 'false')
            for (const value of ['true', 'false', 'true']) {
              await groupToggle().click()
              assert.equal(await groupToggle().getAttribute('aria-checked'), value)
            }
            result.observations.push(`${which}: real preview API mapping fills name/notes; form auto-sync toggles on/off/on`)
          }
          try {
            await page.goto(`http://127.0.0.1:4195/.ci-pr1050.html?locale=${locale}&mode=${mode}`)
            await page.waitForFunction(() => window.__pr1050?.ready)
            const header = page.getByTestId('fixture-header')
            if (mode === 'before') {
              assert.equal(await header.getByTestId('header-override-variables').count(), 0)
              assert.equal(await header.locator('input').count(), 2)
              result.observations.push('Original historical HeaderOverrideEditor has no identity variable picker')
            } else {
              const value = header.getByTestId('header-override-value')
              let expected = 'fixture-'
              for (const variable of ['user_id', 'api_key_id', 'group_id', 'request_id']) {
                await value.focus()
                await value.evaluate(input => input.setSelectionRange(input.value.length, input.value.length))
                await header.getByTestId(`header-override-variable-${variable}`).click()
                expected += `{{${variable}}}`
                assert.equal(await value.inputValue(), expected)
              }
              await header.getByTestId('header-override-row-preview').waitFor({ state: 'visible' })
              result.observations.push('All four identity variables inserted through native buttons at the caret; real preview visible')
            }
            await capture('header', header)
            await open('edit')
            const editName = dialog().locator('[data-tour="edit-account-form-name"]')
            assert.equal(await editName.inputValue(), 'Fixture local group')
            if (mode === 'before') {
              await absentGroupControls()
              assert.equal(await dialog().getByTestId('header-override-variables').count(), 0)
              result.observations.push('Original historical EditAccountModal has no group-sync switch or identity picker')
            } else {
              assert.equal(await groupBadge().count(), 1)
              assert.equal(await headerBadge(dialog()).count(), 1)
              await sync('edit')
            }
            await capture('edit-group', editName)
            if (mode === 'after') {
              await capture('edit-header', dialog().getByTestId('header-override-variables'))
              await headerBadge(dialog()).click()
              assert.equal(await headerBadge(page.getByTestId('fixture-header')).count(), 0)
              assert.equal(await groupBadge().count(), 1, 'Header dismissal must leave group notice visible')
              await groupBadge().click()
              assert.equal(await groupBadge().count(), 0)
              result.observations.push('Header notice dismissal shared by real editor instances; group notice stays independent')
            }
            await close()
            await open('create')
            await fillCreate()
            const createName = dialog().locator('[data-tour="account-form-name"]')
            if (mode === 'before') {
              await absentGroupControls()
              result.observations.push('Original historical CreateAccountModal has no group-sync control')
            } else {
              assert.equal(await groupBadge().count(), 0, 'Group dismissal must carry from Edit to Create')
              await sync('create')
            }
            await capture('create-group', createName)
            await close()
            const state = await snapshotState()
            assert.equal(state.previews.length, mode === 'after' ? 2 : 0)
            if (mode === 'after') {
              const dismissed = await page.evaluate(() => JSON.parse(localStorage.getItem('sub2api:new-feature-notices:v1:1050')))
              assert.ok(dismissed.includes('header-override-identity-variables'))
              assert.ok(dismissed.includes('upstream-billing-group-sync'))
              await page.reload()
              await page.waitForFunction(() => window.__pr1050?.ready)
              assert.equal(await headerBadge(page.getByTestId('fixture-header')).count(), 0)
              await open('edit')
              assert.equal(await groupBadge().count(), 0)
              assert.equal(await headerBadge(dialog()).count(), 0)
              await capture('notices-reloaded', dialog().locator('[data-tour="edit-account-form-name"]'))
              await close()
              await open('create')
              await fillCreate()
              assert.equal(await groupBadge().count(), 0)
              await close()
              await snapshotState()
              result.observations.push('Same synthetic admin after full page reload: both notice IDs remain dismissed in Edit/Create/Header entries')
            }
            result.passed = true
          } catch (error) {
            manifest.errors.push(`${label}: ${error.stack}`)
            writeFileSync(join(output, `${label}-failure.txt`), await page.locator('body').innerText())
            await capture('failure').catch(() => {})
            throw error
          } finally {
            const state = await page.evaluate(() => window.__pr1050).catch(() => null)
            if (state) result.final_state = state
            await context.close()
          }
        }
      }
    } finally {
      if (server) await new Promise((done, fail) => server.httpServer.close(error => error ? fail(error) : done()))
      for (const file of [entry, html]) rmSync(file, { force: true })
      for (const path of [buildDir, cacheDir]) rmSync(path, { recursive: true, force: true })
    }
    for (const [path, identity] of Object.entries(sources.files)) {
      if (!identity.absent) assert.equal(git('hash-object', join(root, path)), identity.blob, `Build mutated ${path}`)
    }
  }
  assert.equal(manifest.results.length, 8)
  assert.ok(manifest.results.every(result => result.passed))
  assert.deepEqual(manifest.errors, [])
  assert.equal(git('rev-parse', 'HEAD'), head)
  assert.equal(git('status', '--porcelain'), '')
  manifest.passed = true
} catch (error) {
  manifest.errors.push(String(error.stack || error))
  throw error
} finally {
  writeFileSync(join(output, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n')
  if (browser) await browser.close()
}
