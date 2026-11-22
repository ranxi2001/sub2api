// Remote CI only: original full application entries/CSS, canonical mocked HTTP.
// This proves browser behavior and request contracts, never real DB persistence.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createServer } from 'node:http'
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { resolve, join, extname } from 'node:path'
import { pathToFileURL } from 'node:url'

const output = process.env.EVIDENCE_DIR, origin = 'http://127.0.0.1:4198'
if (process.env.GITHUB_ACTIONS !== 'true' || !output || !process.env.BASELINE || !process.env.CANDIDATE) throw Error('Exact remote CI inputs required')
const git = (...args) => execFileSync('git', args, { encoding: 'utf8' }).trim()
const candidate = git('rev-parse', 'HEAD'), baseline = git('-C', '.gateway-models-baseline', 'rev-parse', 'HEAD')
assert.equal(candidate, process.env.CANDIDATE)
assert.equal(baseline, process.env.BASELINE)
const clone = value => structuredClone(value)
const A = 'cross-platform-gateway-accounts', B = 'gateway-account-model-sync-20261122'
const manifest = {
  candidate, baseline, complete: false,
  notice_ids: { A, B, C: 'fixture-gateway-models-future-announcement-C' },
  boundary: 'Unmodified complete baseline/candidate index.html, App, router, AccountsView, Create/Edit dialogs, shared components, stores and project Vite/Tailwind/CSS. Playwright intercepts canonical same-origin APIs with synthetic data, including auth and a server-side saved-key fixture. Saves/readbacks are mock contract assertions, not real service/repository/PostgreSQL evidence. Backend persistence/security are separate remote Go tests. No production, real upstream, paid model calls or external requests. The auxiliary announcement epoch page renders the real badge/store; actual B checks use all three product entrypoints. No target component stubs or source overlays.',
  source_blobs: {}, screenshots: [], assertions: [], requests: [], saves: [], readbacks: [], computed_css: [], errors: [], unexpected: [], external: [],
}
const sourcePaths = [
  'frontend/index.html', 'frontend/src/main.ts', 'frontend/src/App.vue',
  'frontend/src/views/admin/AccountsView.vue', 'frontend/src/components/account/CreateAccountModal.vue',
  'frontend/src/components/account/EditAccountModal.vue', 'frontend/src/components/account/GatewayAccountFields.vue',
  'frontend/src/components/account/GatewayModelSettings.vue', 'frontend/src/components/account/ModelWhitelistSelector.vue',
  'frontend/src/components/common/BaseDialog.vue', 'frontend/src/components/common/NewFeatureBadge.vue',
  'frontend/src/stores/newFeatureNotices.ts', 'frontend/src/constants/newFeatureNotices.ts',
  'frontend/src/composables/useModelWhitelist.ts', 'frontend/src/i18n/locales/en/admin/accounts.ts', 'frontend/src/i18n/locales/zh/admin/accounts.ts',
  'frontend/src/style.css', 'frontend/tailwind.config.js', 'frontend/vite.config.ts',
]
for (const [mode, ref] of [['before', baseline], ['after', candidate]]) {
  const paths = new Set(git('ls-tree', '-r', '--name-only', ref).split('\n'))
  manifest.source_blobs[mode] = { commit: ref, paths: Object.fromEntries(sourcePaths.map(path => [path, paths.has(path) ? git('rev-parse', `${ref}:${path}`) : 'absent'])) }
}
let mode = 'before', browser, activePage, activeCase
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2' }
const server = createServer((req, res) => {
  const pathname = new URL(req.url, origin).pathname
  if (/^\/(?:api\/|setup\/)/.test(pathname)) { res.writeHead(500); res.end('API must be intercepted by the audited fixture'); return }
  const root = resolve(output, mode + '-dist')
  let file = resolve(root, '.' + decodeURIComponent(pathname))
  if (!file.startsWith(root + '/')) { res.writeHead(403); res.end(); return }
  if (!existsSync(file) || !extname(file)) file = join(root, 'index.html')
  res.setHeader('Content-Type', mime[extname(file)] || 'application/octet-stream')
  res.setHeader('Cache-Control', 'no-store')
  res.end(readFileSync(file))
})
await new Promise(done => server.listen(4198, '127.0.0.1', done))
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE))
const user = { id: 1003, role: 'admin', status: 'active', email: 'reviewer@example.invalid', username: 'Fixture administrator', balance: 0, concurrency: 1, run_mode: 'standard' }
const groups = [
  { id: 101, name: 'Fixture OpenAI restricted', platform: 'openai', model_allowlist: { enabled: true, models: ['live'] } },
  { id: 102, name: 'Fixture Anthropic', platform: 'anthropic', model_allowlist: { enabled: false, models: [] } },
  { id: 103, name: 'Fixture OAuth only', platform: 'openai', require_oauth_only: true },
].map(group => ({ status: 'active', rate_multiplier: 1, account_count: 1, subscription_type: 'standard', require_oauth_only: false, ...group }))
const proxies = [7, 8].map(id => ({ id, name: `Fixture proxy ${id}`, protocol: 'http', host: 'proxy.example.invalid', port: 8080, status: 'active' }))
const publicSettings = { site_name: 'Sub2API', site_logo: '', subscription_enabled: false, support_ticket_enabled: false,
  payment_enabled: false, risk_control_enabled: false, custom_menu_items: [], custom_endpoints: [], registration_enabled: false }
const initialAccount = (kind, id) => ({ id, name: `Fixture ${kind}`, platform: 'gateway', type: 'gateway', status: 'active',
  schedulable: true, concurrency: 2, priority: 50, rate_multiplier: 1, cost_multiplier: 1, proxy_id: 7, group_ids: [101],
  groups: [groups[0]], account_groups: [{ group_id: 101, priority: 50, allowed_models: [] }],
  credentials: { upstream_kind: kind, base_url: `https://${kind.replace('_', '-')}.example.invalid/tenant`, api_key: `synthetic-saved-${kind}`, model_mapping: { live: 'live', public: 'live' }, unrelated_option: { keep: true } },
  extra: {}, notes: '', auto_pause_on_expired: false, created_at: '2026-11-22T00:00:00Z', updated_at: '2026-11-22T00:00:00Z' })
const labels = {
  en: { create: 'Create Account', edit: 'Edit', from: 'Request model', to: 'Target model', addMapping: 'Add Mapping', custom: 'Enter custom model name', add: 'Add' },
  zh: { create: '添加账号', edit: '编辑', from: '请求模型', to: '目标模型', addMapping: '添加映射', custom: '输入自定义模型名称', add: '填入' },
}
const tick = page => page.evaluate(() => new Promise(done => requestAnimationFrame(() => requestAnimationFrame(done))))
function stateForCase() {
  return { rows: new Map([['new_api', 51], ['sub2api', 52]].map(([kind, id]) => [id, initialAccount(kind, id)])), nextID: 60,
    previewPlan: null, applyPlan: null, pending: null, previews: [], applies: [], groupSnapshot: clone(groups) }
}
const redacted = value => { const copy = clone(value); delete copy.credentials.api_key; return copy }
function recordRequest(request, path) {
  const data = request.postData() ? request.postDataJSON() : undefined
  const safe = data ? clone(data) : undefined
  if (safe?.api_key) safe.api_key = '<synthetic supplied key>'
  if (safe?.credentials?.api_key) safe.credentials.api_key = '<synthetic supplied key>'
  if (safe?.password) safe.password = '<synthetic password>'
  manifest.requests.push({ case: activeCase, mode, method: request.method(), path, body: safe })
  return data
}
async function routeAPI(route, state) {
  const request = route.request(), url = new URL(request.url()), path = url.pathname.replace(/^\/api\/v1/, ''), method = request.method()
  const body = recordRequest(request, path)
  const ok = data => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ code: 0, message: 'success', data }) })
  const fail = (status, message) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify({ code: status, message }) })
  if (path === '/setup/status') return ok({ needs_setup: false, step: 'complete' })
  if (path === '/auth/me') return ok(user)
  if (path === '/auth/login') return ok({ access_token: 'synthetic-ui-session', token_type: 'Bearer', expires_in: 3600, user })
  if (path === '/settings/public') return ok(publicSettings)
  if (path === '/admin/system/check-updates') return ok({ current_version: 'fixture', latest_version: 'fixture', has_update: false, cached: true, build_type: 'source' })
  if (path === '/admin/accounts/antigravity/default-model-mapping') return ok({})
  if (path === '/keys') return ok({ items: [], total: 0, page: 1, pages: 0, page_size: 20 })
  if (path === '/admin/compliance') return ok({ required: false, version: 'fixture' })
  if (path === '/announcements' || path === '/subscriptions/active') return ok([])
  if (path === '/admin/settings') return ok({ ops_monitoring_enabled: false, ops_realtime_monitoring_enabled: false, custom_menu_items: [], web_search_emulation_enabled: false })
  if (path === '/admin/payment/config') return ok({ enabled: false })
  if (path === '/admin/settings/web-search-emulation') return ok({ enabled: false, providers: [] })
  if (path === '/admin/account-ops/token-guard-v2/encryption') return ok({ configured: true, source: 'server_config' })
  if (path === '/admin/account-ops/auto-config') return ok({ enabled: false, platform: 'openai', group_ids: [], concurrency: 1, priority: 50, load_factor: 1, revision: 'fixture' })
  if (path === '/admin/accounts/management-capabilities') return ok({ web_search_enabled: false, account_quota_notify_enabled: false, concurrency_upgrade_enabled: false })
  if (path === '/admin/accounts/upstream-billing-probe/settings') return ok({ enabled: false, interval_minutes: 30 })
  if (path === '/admin/accounts/upstream-billing-rates') return ok({ rates: {}, account_rates: {}, items: [], enabled: false })
  if (path === '/admin/ops/settings/metric-thresholds') return ok({ upstream_error_rate_percent_max: null })
  if (path === '/admin/accounts/performance') return ok({})
  if (path === '/admin/accounts/today-stats/batch') return ok({ stats: {} })
  if (path === '/admin/accounts/usage/batch') return ok({ accounts: {}, usage: {} })
  if (path === '/admin/proxies/all') return ok(proxies)
  if (path === '/admin/groups/all') return ok(groups)
  if (path === '/admin/groups') return ok({ items: groups, total: groups.length, page: 1, pages: 1, page_size: 100 })
  if (path === '/admin/tls-fingerprint-profiles') return ok([])
  if (path === '/admin/upstream-channels') return ok({ items: [], total: 0 })
  if (path === '/admin/upstream-channel-bindings') return ok({ items: [] })
  if (path === '/admin/accounts/check-mixed-channel') return ok({ has_risk: false })
  if (/^\/admin\/accounts\/\d+\/schedulable-timers$/.test(path)) return ok({ tasks: [], history: [], server_now: '2026-11-22T00:00:00Z', poll_seconds: 30, open_grace_seconds: 900, max_delay_minutes: 100 })
  if (path === '/admin/accounts/models/sync-upstream-preview') {
    assert.equal(body.platform, 'gateway'); assert.equal(body.type, 'gateway')
    assert(['new_api', 'sub2api'].includes(body.upstream_kind))
    const saved = body.account_id ? state.rows.get(body.account_id) : null
    const resolvedKey = body.api_key || saved?.credentials.api_key
    assert(resolvedKey, 'the synthetic server resolves a saved key when the browser leaves it blank')
    assert(/^https:\/\/[^/]+\.example\.invalid(?:\/|$)/.test(body.base_url))
    assert([7, 8].includes(body.proxy_id), 'the selected fixture proxy must reach the HTTP contract')
    state.previews.push({ ...clone(body), resolvedKey })
    const plan = state.previewPlan; state.previewPlan = null
    if (plan === 'hold') await new Promise(done => { state.pending = done })
    if (plan === 'error') return fail(502, 'Synthetic upstream catalog unavailable')
    return ok({ models: plan === 'hold' ? ['stale-result'] : ['live', 'removable'], discovery_source: 'upstream' })
  }
  const sync = path.match(/^\/admin\/accounts\/(\d+)\/models\/sync-upstream$/)
  if (sync) {
    const row = state.rows.get(Number(sync[1])); assert(row)
    assert.deepEqual(body, { apply: true, whitelist_only: true })
    if (state.applyPlan === 'hold') {
      state.applyPlan = null
      await new Promise(done => { state.pending = done })
    }
    const mapping = { live: 'live', 'saved-fetched': 'saved-fetched' }
    for (const [from, to] of Object.entries(row.credentials.model_mapping)) if (from !== to && to === 'live') mapping[from] = to
    row.credentials.model_mapping = mapping
    state.applies.push(row.id)
    return ok({ models: ['live', 'saved-fetched'], applied: true, changed: true, discovery_source: 'upstream' })
  }
  if (path === '/admin/accounts' && method === 'GET') {
    const rows = [...state.rows.values()].map(redacted)
    return ok({ items: rows, total: rows.length, page: 1, pages: 1, page_size: 20 })
  }
  const accountPath = path.match(/^\/admin\/accounts\/(\d+)$/)
  if (accountPath && method === 'GET') {
    const row = state.rows.get(Number(accountPath[1])); assert(row)
    manifest.readbacks.push({ case: activeCase, id: row.id, boundary: 'canonical HTTP fixture', mapping: clone(row.credentials.model_mapping), group_ids: clone(row.group_ids) })
    return ok(redacted(row))
  }
  if ((path === '/admin/accounts' && method === 'POST') || (accountPath && method === 'PUT')) {
    assert.equal(body.credentials.upstream_kind === 'new_api' || body.credentials.upstream_kind === 'sub2api', true)
    const previous = accountPath ? state.rows.get(Number(accountPath[1])) : null
    const id = previous?.id || state.nextID++
    const row = { ...(previous || initialAccount(body.credentials.upstream_kind, id)), ...clone(body), id,
      credentials: { ...previous?.credentials, ...clone(body.credentials) } }
    row.account_groups = row.group_ids.map(group_id => ({ group_id, priority: 50, allowed_models: clone(body.group_allowed_models?.[group_id] || []) }))
    row.groups = groups.filter(group => row.group_ids.includes(group.id))
    assert(!row.group_ids.includes(103), 'OAuth-only groups are excluded by the actual selector')
    state.rows.set(id, row)
    manifest.saves.push({ case: activeCase, method, id, name: row.name, group_ids: clone(row.group_ids), mapping: clone(row.credentials.model_mapping), group_allowed_models: clone(body.group_allowed_models || {}), unrelated_preserved: row.credentials.unrelated_option?.keep === true, boundary: 'mock HTTP save, not database evidence' })
    return ok(redacted(row))
  }
  manifest.unexpected.push({ case: activeCase, method, path })
  return fail(500, `Unexpected fixture endpoint: ${method} ${path}`)
}
const dialog = page => page.getByRole('dialog').filter({ has: page.locator('#create-account-form, #edit-account-form') })
const settings = page => dialog(page).getByTestId('gateway-model-settings')
const fields = page => dialog(page).getByTestId('gateway-account-fields')
const rowFor = (page, name) => page.getByRole('row').filter({ hasText: name })
const modelChip = (page, model) => settings(page).locator('span.inline-flex').filter({ has: page.getByText(model, { exact: true }) })
const modelValues = page => settings(page).locator('span.inline-flex > span > span.truncate').allTextContents()
async function settle(page) {
  await tick(page)
  await page.waitForFunction(() => !document.querySelector('.modal-enter-active, .modal-leave-active'))
}
async function shot(page, name, locator) {
  await locator.scrollIntoViewIfNeeded(); await settle(page)
  await page.evaluate(() => document.fonts.ready)
  await locator.screenshot({ path: join(output, name), animations: 'disabled' })
  manifest.screenshots.push({ name, case: activeCase, mode, actual_frontend_sha: mode === 'before' ? baseline : candidate, api: 'synthetic fixture' })
}
async function openCreate(page, lang) {
  assert.equal(await page.locator('#create-account-form').count(), 0, 'the real dialog starts closed')
  await page.getByRole('button', { name: labels[lang].create, exact: true }).click()
  await page.locator('#create-account-form').waitFor()
  await page.getByTestId('platform-button-gateway').click()
  await fields(page).waitFor(); await settle(page)
}
async function close(page) {
  await dialog(page).getByRole('button', { name: 'Close modal', exact: true }).click()
  await dialog(page).waitFor({ state: 'hidden' }); await settle(page)
}
async function openEdit(page, lang, name) {
  assert.equal(await page.locator('#edit-account-form').count(), 0)
  await rowFor(page, name).getByRole('button', { name: labels[lang].edit, exact: true }).click()
  await page.locator('#edit-account-form').waitFor(); await fields(page).waitFor(); await settle(page)
  assert.equal(await fields(page).locator('input[type=password]').inputValue(), '', 'saved calling key is never returned to the browser')
}
async function selectProxy(page, id) {
  await dialog(page).locator('button.select-trigger').filter({ hasText: /Fixture proxy|No Proxy|不使用代理|无代理/ }).first().click()
  await dialog(page).locator('.select-option').filter({ hasText: `Fixture proxy ${id}` }).click()
}
async function syncPreview(page, state) {
  const count = state.previews.length
  const response = page.waitForResponse(r => r.url().endsWith('/models/sync-upstream-preview'))
  await settings(page).getByTestId('sync-upstream-models').click()
  await response; await settle(page)
  assert.equal(state.previews.length, count + 1)
}
async function save(page, method) {
  const response = page.waitForResponse(r => /^\/api\/v1\/admin\/accounts(?:\/\d+)?$/.test(new URL(r.url()).pathname) && r.request().method() === method)
  await page.locator(`button[type=submit][form="${method === 'POST' ? 'create' : 'edit'}-account-form"]`).click()
  const actual = await response; assert.equal(actual.status(), 200)
  const body = await actual.json(); assert.equal(body.code, 0)
  await dialog(page).waitFor({ state: 'hidden' }); await settle(page)
  return body.data.id
}
async function css(page, tag) {
  const values = await settings(page).evaluate(node => {
    const input = node.querySelector('input'), action = node.querySelector('[data-testid="sync-upstream-models"]')
    const style = getComputedStyle(node), rect = node.getBoundingClientRect(), inputStyle = getComputedStyle(input)
    return { width: rect.width, left: rect.left, right: rect.right, viewport: innerWidth, display: style.display,
      inputBorder: inputStyle.borderTopWidth, inputBackground: inputStyle.backgroundColor, actionEnabled: !action.disabled,
      dark: document.documentElement.classList.contains('dark') }
  })
  assert(values.width > 250); assert(values.left >= 0 && values.right <= values.viewport)
  assert.notEqual(values.inputBorder, '0px'); assert.equal(values.display, 'block'); assert(values.actionEnabled)
  manifest.computed_css.push({ case: activeCase, tag, ...values })
  return values
}
try {
  browser = await chromium.launch({ headless: true })
  for (const lang of ['en', 'zh']) {
    activeCase = `${lang}-desktop`
    const state = stateForCase()
    const context = await browser.newContext({ viewport: { width: 1440, height: 1050 }, locale: lang === 'en' ? 'en-US' : 'zh-CN', serviceWorkers: 'block' })
    await context.addInitScript(({ user, lang, publicSettings }) => {
      // Synthetic authenticated browser fixture, not a claim of real login.
      if (!sessionStorage.getItem('gateway-fixture-initialized')) {
        localStorage.setItem('auth_token', 'synthetic-ui-session'); localStorage.setItem('auth_user', JSON.stringify(user))
        localStorage.setItem('theme', 'light'); sessionStorage.setItem('gateway-fixture-initialized', 'true')
      }
      localStorage.setItem('sub2api_locale', lang)
      localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: false }))
      localStorage.setItem('admin_guide_1003_admin_v4_interactive', 'true')
      localStorage.setItem('account-hidden-columns', JSON.stringify(['usage', 'today_stats', 'upstream_billing_rate', 'notes', 'scheduler_score', 'rate_multiplier']))
      window.__APP_CONFIG__ = publicSettings
    }, { user, lang, publicSettings })
    await context.route('**/*', async route => {
      const url = new URL(route.request().url())
      if (url.origin !== origin && !['data:', 'blob:', 'about:'].includes(url.protocol)) {
        manifest.external.push({ origin: url.origin, path: url.pathname }); return route.abort('blockedbyclient')
      }
      if (url.pathname.startsWith('/api/v1/') || url.pathname.startsWith('/setup/')) {
        try {
          return await routeAPI(route, state)
        } catch (error) {
          manifest.errors.push({ case: activeCase, mode, path: url.pathname, message: error.message, stack: error.stack })
          return route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ code: 500, message: 'Fixture contract assertion failed' }) })
        }
      }
      return route.continue()
    })
    const page = await context.newPage(); activePage = page; page.setDefaultTimeout(20000)
    page.on('pageerror', error => manifest.errors.push({ case: activeCase, mode, message: error.message, stack: error.stack }))
    mode = 'before'; await page.goto(origin + '/admin/accounts')
    await page.getByRole('button', { name: labels[lang].create, exact: true }).waitFor()
    for (const kind of ['new_api', 'sub2api']) {
      await openCreate(page, lang); await fields(page).locator('select').selectOption(kind)
      assert.equal(await settings(page).count(), 0)
      assert.equal(await dialog(page).getByTestId('sync-upstream-models').count(), 0)
      await shot(page, `before-create-${kind}-${lang}.png`, fields(page)); await close(page)
      await openEdit(page, lang, `Fixture ${kind}`)
      assert.equal(await settings(page).count(), 0)
      await shot(page, `before-edit-${kind}-${lang}.png`, fields(page)); await close(page)
    }
    assert.equal(await page.getByTestId('gateway-sync-saved').count(), 0)
    manifest.assertions.push({ case: activeCase, baseline_gateway_controls_absent: true })

    mode = 'after'; await page.reload()
    await page.getByTestId('gateway-sync-saved').first().waitFor()
    if (lang === 'en') {
      await page.goto(`${origin}/pr838-notices.html?phase=a&user=1003`)
      await page.getByTestId('notice-A-0').getByTestId('new-feature-badge').click()
      await page.goto(origin + '/admin/accounts')
    }
    for (const kind of ['new_api', 'sub2api']) {
      await openCreate(page, lang)
      await fields(page).locator('select').selectOption(kind)
      const name = `Untouched ${kind} ${lang}`
      await page.locator('[data-tour="account-form-name"]').fill(name)
      await fields(page).locator('input[type=url]').fill('https://untouched.example.invalid')
      await fields(page).locator('input[type=password]').fill('synthetic-untouched-key')
      await selectProxy(page, 7)
      await dialog(page).locator('[data-tour="account-form-groups"] input[value="101"]').check()
      const previewsBefore = state.previews.length
      assert.deepEqual(await modelValues(page), [], 'gateway model defaults must remain unrestricted')
      assert.equal(await settings(page).getByRole('textbox', { name: labels[lang].from, exact: true }).count(), 0)
      await shot(page, `after-create-untouched-${kind}-${lang}.png`, settings(page))
      const id = await save(page, 'POST')
      assert.deepEqual(state.rows.get(id).credentials.model_mapping, {})
      assert.equal(state.previews.length, previewsBefore, 'untouched create must not fetch models')
      await openEdit(page, lang, name)
      assert.deepEqual(await modelValues(page), [])
      await close(page)
      manifest.assertions.push({ lang, kind, untouched_create_empty_mapping: true, mock_readback_unrestricted: true })
    }
    for (const kind of ['new_api', 'sub2api']) {
      await openCreate(page, lang)
      await fields(page).locator('select').selectOption(kind)
      await page.locator('[data-tour="account-form-name"]').fill(`Created ${kind} ${lang}`)
      await fields(page).locator('input[type=url]').fill(`https://${kind.replace('_', '-')}.example.invalid/create`)
      await fields(page).locator('input[type=password]').fill(`synthetic-create-${kind}`)
      await selectProxy(page, 7)
      const groupScope = dialog(page).locator('[data-tour="account-form-groups"]')
      assert.equal(await groupScope.locator('input[value="103"]').count(), 0)
      await groupScope.locator('input[value="101"]').check()
      assert.equal(await settings(page).getByTestId('sync-upstream-models').isEnabled(), true)
      if (kind === 'new_api') {
        state.previewPlan = 'hold'
        const received = page.waitForRequest(r => r.url().endsWith('/models/sync-upstream-preview'))
        await settings(page).getByTestId('sync-upstream-models').click(); await received
        await page.waitForFunction(() => document.querySelector('[data-testid="gateway-model-settings"] [data-testid="sync-upstream-models"]').disabled)
        await shot(page, `after-loading-${lang}.png`, settings(page))
        await fields(page).locator('input[type=url]').fill('https://changed.example.invalid/create')
        assert(await settings(page).getByTestId('sync-upstream-models').isEnabled())
        const staleResponse = page.waitForResponse(r => r.url().endsWith('/models/sync-upstream-preview'))
        assert(state.pending); state.pending(); state.pending = null; await staleResponse; await settle(page)
        assert.deepEqual(await modelValues(page), [], 'old connection response must not alter the new draft')
        state.previewPlan = 'error'; await syncPreview(page, state)
        assert.deepEqual(await modelValues(page), [])
        await page.getByText(/Synthetic upstream catalog unavailable/).waitFor()
        await shot(page, `after-error-${lang}.png`, page.locator('body'))
      }
      await syncPreview(page, state)
      assert.equal(state.previews.at(-1).upstream_kind, kind)
      assert.equal(state.previews.at(-1).api_key, `synthetic-create-${kind}`)
      assert.deepEqual(await modelValues(page), ['live', 'removable'])
      await modelChip(page, 'removable').getByRole('button').click()
      await settings(page).getByPlaceholder(labels[lang].custom, { exact: true }).fill('manual')
      await settings(page).getByRole('button', { name: labels[lang].add, exact: true }).click()
      await settings(page).getByRole('button', { name: labels[lang].addMapping, exact: true }).click()
      await settings(page).getByRole('textbox', { name: labels[lang].from, exact: true }).fill('public')
      await settings(page).getByRole('textbox', { name: labels[lang].to, exact: true }).fill('live')
      assert.deepEqual(await modelValues(page), ['live', 'manual'])
      await css(page, 'desktop-light')
      await shot(page, `after-create-configured-${kind}-${lang}.png`, settings(page))
      const id = await save(page, 'POST')
      assert.deepEqual(state.rows.get(id).credentials.model_mapping, { live: 'live', manual: 'manual', public: 'live' })
      await openEdit(page, lang, `Created ${kind} ${lang}`)
      assert.deepEqual(await modelValues(page), ['live', 'manual'])
      assert.equal(await settings(page).getByRole('textbox', { name: labels[lang].from, exact: true }).inputValue(), 'public')
      await close(page)
      await openEdit(page, lang, `Fixture ${kind}`)
      await fields(page).locator('input[type=url]').fill(`https://edited.example.invalid/${kind}`)
      await selectProxy(page, 8)
      await syncPreview(page, state)
      const preview = state.previews.at(-1)
      assert.equal(preview.account_id, kind === 'new_api' ? 51 : 52)
      assert.equal(preview.api_key, ''); assert.equal(preview.resolvedKey, `synthetic-saved-${kind}`)
      assert.equal(preview.proxy_id, 8)
      await fields(page).locator('input[type=password]').fill(`synthetic-rotated-${kind}`)
      await syncPreview(page, state)
      assert.equal(state.previews.at(-1).resolvedKey, `synthetic-rotated-${kind}`)
      await settings(page).getByRole('textbox', { name: labels[lang].from, exact: true }).fill('public-edited')
      await modelChip(page, 'removable').getByRole('button').click()
      await dialog(page).locator('[data-tour="account-form-groups"] input[value="101"]').uncheck()
      await dialog(page).locator('[data-tour="account-form-groups"] input[value="102"]').check()
      const binding = dialog(page).getByTestId('group-model-limit-102')
      await binding.getByTestId('group-model-limit-selected').click()
      await binding.getByPlaceholder(labels[lang].custom, { exact: true }).fill('live')
      await binding.getByRole('button', { name: labels[lang].add, exact: true }).click()
      await shot(page, `after-edit-configured-${kind}-${lang}.png`, settings(page))
      const editedID = await save(page, 'PUT')
      assert.deepEqual(state.rows.get(editedID).credentials.model_mapping, { live: 'live', 'public-edited': 'live' })
      assert.deepEqual(state.rows.get(editedID).credentials.unrelated_option, { keep: true })
      assert.deepEqual(state.rows.get(editedID).group_ids, [102])
      assert.deepEqual(state.rows.get(editedID).account_groups[0].allowed_models, ['live'])
      await openEdit(page, lang, `Fixture ${kind}`)
      assert.deepEqual(await modelValues(page), ['live'])
      assert.equal(await settings(page).getByRole('textbox', { name: labels[lang].from, exact: true }).inputValue(), 'public-edited')
      assert.equal(await dialog(page).getByTestId('group-model-limit-102').getByText('live', { exact: true }).count(), 1)
      await close(page)
      const before = state.applies.length
      const applied = page.waitForResponse(r => r.url().includes(`/${editedID}/models/sync-upstream`))
      if (kind === 'new_api') state.applyPlan = 'hold'
      await rowFor(page, `Fixture ${kind}`).getByTestId('gateway-sync-saved').click()
      if (kind === 'new_api') {
        await page.waitForFunction(name => [...document.querySelectorAll('[data-testid="gateway-sync-saved"]')]
          .some(button => button.closest('tr')?.textContent.includes(name) && button.disabled), `Fixture ${kind}`)
        await shot(page, `after-saved-action-loading-${lang}.png`, rowFor(page, `Fixture ${kind}`))
        assert(state.pending); state.pending(); state.pending = null
      }
      await applied
      await settle(page); assert.equal(state.applies.length, before + 1)
      await openEdit(page, lang, `Fixture ${kind}`)
      assert.deepEqual(await modelValues(page), ['live', 'saved-fetched'])
      assert.equal(await settings(page).getByRole('textbox', { name: labels[lang].from, exact: true }).inputValue(), 'public-edited')
      await shot(page, `after-saved-action-readback-${kind}-${lang}.png`, settings(page)); await close(page)
    }
    assert.deepEqual(groups, state.groupSnapshot, 'HTTP fixture never distributes a fetched list to joined groups')
    await openCreate(page, lang)
    await fields(page).locator('input[type=password]').fill('synthetic-style-key')
    await selectProxy(page, 7)
    const light = await css(page, 'light-before-dark')
    if (lang === 'en') {
      assert.equal(await settings(page).getByTestId('new-feature-badge').count(), 1, 'new B remains visible after actual A dismissal')
      const listNotice = rowFor(page, 'Fixture new_api').locator('[data-testid="gateway-sync-saved"] + [data-testid="new-feature-badge"]')
      assert.equal(await listNotice.count(), 1, 'list entry shares B before dismissal')
      await settings(page).getByTestId('new-feature-badge').click(); await close(page)
      await listNotice.waitFor({ state: 'detached' })
      await openEdit(page, lang, 'Fixture new_api')
      assert.equal(await settings(page).getByTestId('new-feature-badge').count(), 0, 'same dismissal hides actual Edit entry')
      await close(page); await page.reload(); await openCreate(page, lang)
      assert.equal(await settings(page).getByTestId('new-feature-badge').count(), 0)
      await close(page)
      await page.evaluate(() => { localStorage.removeItem('auth_token'); localStorage.removeItem('auth_user') })
      await page.goto(origin + '/login?redirect=/admin/accounts')
      await page.locator('#email').fill('reviewer@example.invalid'); await page.locator('#password').fill('synthetic-login-password')
      await page.locator('button[type=submit]').click(); await page.waitForURL(url => url.pathname !== '/login')
      await page.goto(origin + '/admin/accounts'); await openEdit(page, lang, 'Fixture new_api')
      assert.equal(await settings(page).getByTestId('new-feature-badge').count(), 0)
      await close(page)
      await page.goto(`${origin}/pr838-notices.html?phase=c&user=1003`)
      assert.equal(await page.getByTestId('notice-A-0').getByTestId('new-feature-badge').count(), 0)
      assert.equal(await page.getByTestId('notice-B-1').getByTestId('new-feature-badge').count(), 0)
      await page.getByTestId('notice-C-3').getByTestId('new-feature-badge').waitFor()
      await shot(page, 'after-notice-future-C.png', page.locator('main'))
      await page.goto(origin + '/admin/accounts'); await openCreate(page, lang)
      manifest.assertions.push({ notice_actual_create_edit_list_shared: true, refresh_and_mock_relogin_persistent: true, A_B_C_independent: true })
    }
    await fields(page).locator('input[type=url]').fill('https://narrow.example.invalid')
    await fields(page).locator('input[type=password]').fill('synthetic-narrow-key'); await selectProxy(page, 7)
    await page.evaluate(() => { localStorage.setItem('theme', 'dark'); document.documentElement.classList.add('dark') })
    await page.setViewportSize({ width: 430, height: 950 }); activeCase = `${lang}-narrow-dark`
    // Wait for the input's real color transition before sampling and capturing it.
    await settings(page).locator('input').first().evaluate(async input => {
      getComputedStyle(input).backgroundColor
      await Promise.all(input.getAnimations().map(animation => animation.finished))
    })
    const dark = await css(page, 'narrow-dark'); assert(dark.dark); assert.notEqual(light.inputBackground, dark.inputBackground)
    await shot(page, `after-narrow-dark-${lang}.png`, settings(page))
    await syncPreview(page, state); assert.deepEqual(await modelValues(page), ['live', 'removable'])
    await close(page)
    manifest.assertions.push({ lang, both_kinds: true, closed_open_transitions: true, previews_own_connection: true,
      whitelist_remove_add: true, alias_edit: true, mock_save_readback: true, saved_action: true,
      errors_loading_stale_response: true, OAuth_only_group_hidden: true, group_config_unchanged: true })
    await context.close(); activePage = null
  }
  assert.deepEqual(manifest.unexpected, []); assert.deepEqual(manifest.errors, []); assert.deepEqual(manifest.external, [])
  manifest.complete = true
} catch (error) {
  manifest.errors.push({ case: activeCase, mode, message: error.message, stack: error.stack })
  if (activePage) {
    writeFileSync(join(output, 'failure.txt'), await activePage.locator('body').innerText())
    await activePage.screenshot({ path: join(output, 'failure.png'), fullPage: true })
  }
  throw error
} finally {
  writeFileSync(join(output, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n')
  if (browser) await browser.close()
  await new Promise(done => server.close(done))
}
