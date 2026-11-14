// Remote CI only. Complete production frontend, real auth/admin HTTP handlers,
// services/repositories and PostgreSQL/Redis. No persistence endpoint interception.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createServer, request as httpRequest } from 'node:http'
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { resolve, join, extname } from 'node:path'
import { pathToFileURL } from 'node:url'

const output = process.env.EVIDENCE_DIR, origin = 'http://127.0.0.1:4198'
if (!output || process.env.GITHUB_ACTIONS !== 'true') throw Error('Isolated CI only')
const appOrigin = process.env.SUB2API_TEST_APP_ORIGIN
if (!appOrigin) throw Error('Disposable application origin required')
assert.equal(JSON.parse(readFileSync(join(output, 'network-internal.json'))), true)
const git = (...args) => execFileSync('git', args, { encoding: 'utf8' }).trim()
const manifest = {
  candidate: git('rev-parse', 'HEAD'), baseline: process.env.BASELINE,
  boundary: 'Complete unmodified before/after frontend entries and project Vite/Tailwind/plugins. Both use candidate backend through a loopback HTTP proxy, real authentication, service/repository and disposable PostgreSQL/Redis on a Docker internal network. Before screenshots are UI comparison only, not baseline backend behavior. No live upstream, SMTP, production or paid calls. Future notice C is a test-only announcement using the real badge/store.',
  screenshots: [], scenarios: [], persistence: [], http: [], errors: [], external: [], complete: false,
}
for (const ref of [manifest.baseline, manifest.candidate]) {
  manifest.source_blobs ??= {}
  manifest.source_blobs[ref] = Object.fromEntries([
    'frontend/src/components/account/CreateAccountModal.vue', 'frontend/src/components/account/EditAccountModal.vue',
    'frontend/src/components/common/BaseDialog.vue', 'frontend/src/components/common/NewFeatureBadge.vue',
    'frontend/src/stores/newFeatureNotices.ts', 'frontend/src/style.css', 'frontend/tailwind.config.js', 'frontend/vite.config.ts',
  ].map(path => [path, git('rev-parse', `${ref}:${path}`)]))
}
let mode = 'before', browser, page
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2' }
const server = createServer((req, res) => {
  const pathname = new URL(req.url, origin).pathname
  if (/^\/(?:api\/|setup\/|health$)/.test(pathname)) {
    const upstream = httpRequest(appOrigin + req.url, { method: req.method, headers: { ...req.headers, host: new URL(appOrigin).host } }, response => {
      if (/^\/api\/v1\/admin\/accounts(?:\/\d+)?$/.test(pathname)) manifest.http.push({ mode, method: req.method, path: pathname, status: response.statusCode })
      res.writeHead(response.statusCode, response.headers); response.pipe(res)
    })
    upstream.on('error', error => { manifest.errors.push({ proxy: error.code }); res.writeHead(502); res.end() })
    req.pipe(upstream)
    return
  }
  const root = resolve(output, mode + '-dist')
  let file = resolve(root, '.' + decodeURIComponent(pathname))
  if (!file.startsWith(root + '/')) { res.writeHead(403); res.end(); return }
  if (!existsSync(file) || !extname(file)) file = join(root, 'index.html')
  res.setHeader('Content-Type', mime[extname(file)] || 'application/octet-stream')
  res.end(readFileSync(file))
})
await new Promise(resolveReady => server.listen(4198, '127.0.0.1', resolveReady))
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE))
const B = 'temp-unsched-stream-timing-count-window'
const text = {
  en: { create: 'Create Account', edit: 'Edit', temp: 'Temp Unschedulable', add: 'Add Rule', presets: ['529 Overloaded', '429 Rate Limit', '503 Unavailable'], rule: n => `Rule #${n}` },
  zh: { create: '添加账号', edit: '编辑', temp: '临时不可调度', add: '添加规则', presets: ['529 过载', '429 限流', '503 维护'], rule: n => `规则 #${n}` },
}
function pg(id) {
  assert(Number.isSafeInteger(id) && id > 0)
  return JSON.parse(execFileSync('docker', ['exec', 'pr838-ui-pg', 'psql', '-U', 'postgres', '-d', 'sub2api_test', '-tAX', '-c',
    `SELECT json_build_object('id',id,'name',name,'rules',credentials->'temp_unschedulable_rules') FROM accounts WHERE id=${id}`], { encoding: 'utf8' }).trim())
}
const dialog = () => page.getByRole('dialog').filter({ has: page.locator('#create-account-form, #edit-account-form') })
const section = labels => dialog().locator('label').filter({ hasText: new RegExp(`^${labels.temp}$`) }).locator('..').locator('..').locator('..')
const close = async () => { await dialog().getByRole('button', { name: 'Close modal', exact: true }).click(); await dialog().waitFor({ state: 'hidden' }) }
async function screenshot(name, locator) {
  await locator.scrollIntoViewIfNeeded()
  await page.evaluate(async () => { await document.fonts.ready; await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))) })
  await page.waitForFunction(() => !document.querySelector('.modal-enter-active'))
  await locator.screenshot({ path: join(output, name), animations: 'disabled' })
  manifest.screenshots.push({ name, mode, frontend: mode === 'before' ? manifest.baseline : manifest.candidate, backend: manifest.candidate })
}
async function login(labels) {
  // Complete first-login acknowledgment on the ordinary profile page before
  // entering administration. The baseline accounts page starts protected API
  // requests immediately and produces a 423 rejection before acknowledgment.
  await page.goto(origin + '/login?redirect=/profile')
  await page.locator('#email').fill('reviewer@example.invalid')
  await page.locator('#password').fill('Fixture-Only-838-Password!')
  const response = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/auth/login' && r.request().method() === 'POST')
  await page.locator('button[type=submit]').click()
  assert.equal((await response).status(), 200)
  await page.waitForURL(url => url.pathname === '/profile')
  // Actual first-login tour and compliance UI; no injected auth/consent state.
  const tour = page.locator('.driver-popover')
  if (await tour.waitFor({ state: 'visible', timeout: 3000 }).then(() => true, () => false)) {
    await page.keyboard.press('Escape'); await tour.waitFor({ state: 'hidden' })
  }
  const compliance = page.getByRole('dialog').filter({ has: page.locator('#admin-compliance-phrase') })
  if (await compliance.isVisible()) {
    const phrase = await compliance.locator('label[for="admin-compliance-phrase"] + div').innerText()
    await compliance.locator('#admin-compliance-phrase').fill(phrase)
    const accepted = page.waitForResponse(r => new URL(r.url()).pathname === '/api/v1/admin/compliance/accept')
    await compliance.locator('button.btn-primary').click()
    assert.equal((await accepted).status(), 200); await compliance.waitFor({ state: 'hidden' })
  }
  await page.goto(origin + '/admin/accounts')
  if (await tour.waitFor({ state: 'visible', timeout: 3000 }).then(() => true, () => false)) {
    await page.keyboard.press('Escape'); await tour.waitFor({ state: 'hidden' })
  }
  await page.getByRole('button', { name: labels.create, exact: true }).waitFor()
}
async function api(method, path, body) {
  const token = await page.evaluate(() => localStorage.getItem('auth_token'))
  assert(token, 'real login token required')
  const response = await page.request.fetch(origin + '/api/v1' + path, { method, headers: { Authorization: `Bearer ${token}` }, data: body })
  assert.equal(response.status(), 200, `${method} ${path}`)
  const value = await response.json(); assert.equal(value.code, 0); return value.data
}
async function openEdit(name, labels) {
  const row = page.getByRole('row').filter({ hasText: name })
  await row.getByRole('button', { name: labels.edit, exact: true }).click()
  await page.locator('#edit-account-form').waitFor()
}
async function openCreate(labels) {
  await page.getByRole('button', { name: labels.create, exact: true }).click()
  await page.locator('#create-account-form').waitFor()
  await dialog().getByRole('button', { name: /Claude Console/ }).click()
  const header = section(labels).locator(':scope > div').first()
  await header.getByRole('button').click()
  await section(labels).getByRole('button', { name: labels.add, exact: true }).waitFor()
}
async function checkFields(before, counts, windows) {
  assert.deepEqual(await dialog().getByTestId('temp-unsched-stream-before_stream').evaluateAll(nodes => nodes.map(n => n.getAttribute('aria-checked') === 'true')), before)
  assert.deepEqual(await dialog().getByTestId('temp-unsched-trigger-count').evaluateAll(nodes => nodes.map(n => n.value)), counts)
  assert.deepEqual(await dialog().getByTestId('temp-unsched-trigger-window').evaluateAll(nodes => nodes.map(n => n.value)), windows)
}
async function save(method) {
  const response = page.waitForResponse(r => /^\/api\/v1\/admin\/accounts(?:\/\d+)?$/.test(new URL(r.url()).pathname) && r.request().method() === method)
  await page.locator(`button[type=submit][form="${method === 'POST' ? 'create' : 'edit'}-account-form"]`).click()
  const actual = await response; assert.equal(actual.status(), 200)
  const sent = actual.request().postDataJSON(), body = await actual.json(); assert.equal(body.code, 0)
  await dialog().waitFor({ state: 'hidden' })
  return { id: body.data.id, sent: { name: sent.name, rules: sent.credentials?.temp_unschedulable_rules } }
}
async function readback(id, expected, label) {
  const value = await api('GET', '/admin/accounts/' + id), row = pg(id)
  assert.deepEqual(value.credentials.temp_unschedulable_rules, expected)
  assert.deepEqual(row.rules, expected)
  manifest.persistence.push({ label, id, api: { name: value.name, rules: value.credentials.temp_unschedulable_rules }, pg: row })
}
try {
  browser = await chromium.launch({ headless: true })
  for (const lang of ['en', 'zh']) {
    const labels = text[lang]
    const context = await browser.newContext({ viewport: { width: 1440, height: 1800 }, locale: lang === 'en' ? 'en-US' : 'zh-CN', serviceWorkers: 'block' })
    await context.addInitScript(locale => localStorage.setItem('sub2api_locale', locale), lang)
    await context.route('**/*', route => {
      const url = new URL(route.request().url())
      if (url.origin === origin || ['data:', 'blob:', 'about:'].includes(url.protocol)) return route.continue()
      manifest.external.push({ origin: url.origin, path: url.pathname }); return route.abort('blockedbyclient')
    })
    page = await context.newPage(); page.setDefaultTimeout(20000)
    page.on('pageerror', error => manifest.errors.push({ lang, mode, name: error.name, message: error.message, stack: error.stack, path: new URL(page.url()).pathname }))
    mode = 'before'; await login(labels)
    const legacyName = `PR838 ${lang} legacy529`
    const legacy = await api('POST', '/admin/accounts', { name: legacyName, platform: 'anthropic', type: 'apikey',
      credentials: { api_key: 'fixture-only-not-a-real-key', base_url: 'https://upstream.example.invalid', temp_unschedulable_enabled: true,
        temp_unschedulable_rules: [{ error_code: 529, keywords: ['overloaded'], duration_minutes: 15, scope: 'model' }] }, concurrency: 1, priority: 50 })
    assert.equal(Object.hasOwn(pg(legacy.id).rules[0], 'stream_timing'), false)
    await page.reload(); await openCreate(labels)
    await section(labels).getByRole('button', { name: '+ ' + labels.presets[0], exact: true }).click()
    assert.equal(await dialog().getByTestId('temp-unsched-trigger-count').count(), 0)
    await screenshot(`before-create-${lang}.png`, section(labels)); await close()
    await openEdit(legacyName, labels)
    await screenshot(`before-edit-${lang}.png`, section(labels)); await close()

    mode = 'after'; await page.reload()
    const userId = await page.evaluate(() => JSON.parse(localStorage.getItem('auth_user')).id)
    const noticePage = async phase => { await page.goto(`${origin}/pr838-notices.html?phase=${phase}&user=${userId}`); await page.locator('h1').waitFor() }
    if (lang === 'en') {
      await noticePage('a'); await page.getByTestId('notice-A-0').getByTestId('new-feature-badge').click()
      await noticePage('b')
      assert.equal(await page.getByTestId('notice-A-0').getByTestId('new-feature-badge').count(), 0)
      assert.equal(await page.getByTestId('new-feature-badge').count(), 2)
      await screenshot('notice-B-after-A-dismissed.png', page.locator('main'))
      await page.goto(origin + '/admin/accounts')
    }
    await openCreate(labels)
    await section(labels).getByRole('button', { name: labels.add, exact: true }).click()
    await checkFields([true], ['1'], ['5'])
    assert.equal(await dialog().getByTestId('temp-unsched-stream-before_stream').evaluate(el => getComputedStyle(el).borderTopWidth), '2px', 'real Tailwind selection border')
    assert.equal(await dialog().getByTestId('temp-unsched-stream-before_stream').evaluate(el => getComputedStyle(el).display), 'flex', 'real Tailwind layout')
    await screenshot(`after-create-defaults-${lang}.png`, section(labels))
    for (const preset of labels.presets) await section(labels).getByRole('button', { name: '+ ' + preset, exact: true }).click()
    await checkFields([true, false, false, false], ['1', '1', '1', '1'], ['5', '5', '5', '5'])
    assert.equal(await section(labels).getByTestId('new-feature-badge').count(), 4)
    const ruleCard = index => dialog().getByTestId('temp-unsched-trigger-count').nth(index).locator('..').locator('..').locator('..')
    for (let index = 1; index <= 3; index++) await screenshot(`after-create-preset-${[529, 429, 503][index - 1]}-${lang}.png`, ruleCard(index))
    await dialog().locator('[data-tour="account-form-name"]').fill(`PR838 ${lang} created`)
    await dialog().locator('input[type=password][required]').fill('fixture-only-not-a-real-key')
    const firstRule = dialog().getByTestId('temp-unsched-trigger-count').first().locator('..').locator('..').locator('..')
    await firstRule.locator('input[type=number]').nth(0).fill('502')
    await firstRule.locator('input[type=number]').nth(1).fill('30')
    await firstRule.locator('input[type=text]').first().fill('bad gateway')
    const created = await save('POST')
    assert.deepEqual(created.sent.rules.map(r => [r.stream_timing, r.trigger_count, r.trigger_window_minutes]), [
      ['before_stream', 1, 5], ['after_stream', 1, 5], ['after_stream', 1, 5], ['after_stream', 1, 5],
    ])
    await readback(created.id, created.sent.rules, `${lang}: Create POST -> service/repository -> API and PG`)
    await openEdit(`PR838 ${lang} created`, labels)
    await checkFields([true, false, false, false], ['1', '1', '1', '1'], ['5', '5', '5', '5'])
    await screenshot(`after-edit-created-readback-${lang}.png`, ruleCard(0))
    assert.equal(await section(labels).getByTestId('new-feature-badge').count(), 4)
    await section(labels).getByTestId('new-feature-badge').first().click()
    assert.equal(await section(labels).getByTestId('new-feature-badge').count(), 0, 'one click hides all four actual Edit locations')
    await section(labels).getByRole('button', { name: labels.add, exact: true }).click()
    for (const preset of labels.presets) await section(labels).getByRole('button', { name: '+ ' + preset, exact: true }).click()
    await checkFields([true, false, false, false, true, false, false, false], Array(8).fill('1'), Array(8).fill('5'))
    await screenshot(`after-edit-new-defaults-${lang}.png`, ruleCard(4))
    for (let index = 5; index <= 7; index++) await screenshot(`after-edit-preset-${[529, 429, 503][index - 5]}-${lang}.png`, ruleCard(index))
    await close()
    await readback(created.id, created.sent.rules, `${lang}: Edit cancel discards unsaved new custom/preset rules`)

    await openEdit(legacyName, labels)
    await checkFields([false], ['1'], ['1'])
    await screenshot(`after-edit-legacy-defaults-${lang}.png`, section(labels))
    await dialog().locator('[data-tour="edit-account-form-name"]').fill(legacyName + ' renamed')
    const renamed = await save('PUT')
    assert.equal(Object.hasOwn(renamed.sent.rules[0], 'stream_timing'), false, 'rename-only request must omit legacy timing')
    await readback(legacy.id, renamed.sent.rules, `${lang}: rename-only PUT keeps omitted legacy529 timing`)
    await openEdit(legacyName + ' renamed', labels)
    await dialog().getByTestId('temp-unsched-stream-before_stream').click()
    await dialog().getByTestId('temp-unsched-trigger-count').fill('3')
    await dialog().getByTestId('temp-unsched-trigger-window').fill('7')
    const explicit = await save('PUT')
    assert.deepEqual([explicit.sent.rules[0].stream_timing, explicit.sent.rules[0].trigger_count, explicit.sent.rules[0].trigger_window_minutes], ['before_stream', 3, 7])
    await readback(legacy.id, explicit.sent.rules, `${lang}: explicit timing/count/window PUT readback`)
    await openEdit(legacyName + ' renamed', labels); await checkFields([true], ['3'], ['7'])
    await screenshot(`after-edit-saved-readback-${lang}.png`, section(labels))
    // Real close/open interactions preserve one ID across all cards and Create/Edit.
    assert.equal(await section(labels).getByTestId('new-feature-badge').count(), 0)
    await close(); await openCreate(labels)
    await section(labels).getByRole('button', { name: labels.add, exact: true }).click()
    assert.equal(await section(labels).getByTestId('new-feature-badge').count(), 0)
    await close(); await page.reload(); await openEdit(legacyName + ' renamed', labels)
    assert.equal(await section(labels).getByTestId('new-feature-badge').count(), 0)
    await screenshot(`after-edit-notice-hidden-reload-${lang}.png`, section(labels)); await close()

    if (lang === 'en') {
      await noticePage('c')
      assert.equal(await page.getByTestId('notice-B-1').getByTestId('new-feature-badge').count(), 0, 'actual administrator B remains hidden')
      assert.equal(await page.getByTestId('notice-A-0').getByTestId('new-feature-badge').count(), 0)
      assert.equal(await page.getByTestId('new-feature-badge').count(), 1)
      assert.equal(await page.getByTestId('notice-C-3').getByTestId('new-feature-badge').count(), 1)
      await screenshot('notice-C-independent-A-B-stay-hidden.png', page.locator('main'))
      await page.reload(); await page.locator('h1').waitFor()
      assert.equal(await page.getByTestId('new-feature-badge').count(), 1)
      const dismissed = await page.evaluate(id => JSON.parse(localStorage.getItem('sub2api:new-feature-notices:v1:' + id)), userId)
      assert(dismissed.includes(B))
      await page.goto(origin + '/admin/accounts')
      await page.getByRole('button', { name: 'User menu', exact: true }).click()
      await page.getByRole('button', { name: 'Logout', exact: true }).click()
      await page.waitForURL(url => url.pathname === '/login')
      await login(labels); await openEdit(legacyName + ' renamed', labels)
      assert.equal(await section(labels).getByTestId('new-feature-badge').count(), 0)
      await close()
      manifest.scenarios.push({ name: 'A -> B multiple locations -> reload -> future C; actual B Create/Edit/reload/same-admin-login', passed: true, futureC: 'test-only registry; actual badge/store', actualUser: userId })
    }
    manifest.scenarios.push({ lang, name: 'full administration Create/Edit, defaults/presets, rename-only omitted timing, explicit save, API + SQL readback', passed: true })
    await context.close()
  }
  assert.deepEqual(manifest.external, []); assert.deepEqual(manifest.errors, [])
  manifest.complete = true
} catch (error) {
  manifest.failure = { name: error.name, message: error.message, stack: error.stack }
  if (page && !page.isClosed()) await page.screenshot({ path: join(output, 'failure.png'), fullPage: true }).catch(() => {})
  throw error
} finally {
  writeFileSync(join(output, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n')
  await browser?.close(); await new Promise(done => server.close(done))
}
console.log('PR838 full administration and persistence acceptance passed.')
