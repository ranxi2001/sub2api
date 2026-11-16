// Remote CI only. Actual fixed base/head UsersView, DataTable, cells, badges and
// CSS; offline handler JSON, synthetic admin, layout shell and closed dialogs.
import assert from 'node:assert/strict'
import fs from 'node:fs/promises'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import os from 'node:os'
import path from 'node:path'
import { pathToFileURL } from 'node:url'

assert.equal(process.env.CI, 'true', 'This capture must run in remote CI')
const headRoot = process.cwd()
const deps = path.join(headRoot, 'frontend')
const output = process.env.USER_LIVE_LOAD_EVIDENCE
const requireDeps = createRequire(path.join(deps, 'package.json'))
const { createServer } = requireDeps('vite')
const vueModule = requireDeps('@vitejs/plugin-vue')
const vue = vueModule.default || vueModule
const tailwind = requireDeps('tailwindcss')
const autoprefixer = requireDeps('autoprefixer')
const requireBrowser = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))
const { chromium } = requireBrowser('playwright')
const scenarios = ['active', 'real-zero', 'read-error', 'timeout', 'missing-service']
const rows = await Promise.all(scenarios.map(async (name, index) => {
  const payload = JSON.parse(await fs.readFile(path.join(process.env.USER_LIVE_LOAD_CONTRACT_DIR, `${name}.json`), 'utf8'))
  return { ...payload.data.items[0], id: 101 + index, email: `${name}@example.test`, username: name }
}))
await fs.mkdir(output, { recursive: true })
const evidence = { synthetic: true, paidRequests: 0, liveApiCalls: 0, sources: [], checks: [] }
const browser = await chromium.launch({ headless: true })

async function capture(root, sha, label) {
  assert.equal(execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim(), sha)
  const frontend = path.join(root, 'frontend')
  const modulesPath = path.join(frontend, 'node_modules')
  let linked = false
  if (root !== headRoot) {
    await fs.symlink(path.join(deps, 'node_modules'), modulesPath, 'dir')
    linked = true
  }
  const cacheDir = await fs.mkdtemp(path.join(os.tmpdir(), 'user-live-capture-'))
  const { default: theme } = await import(pathToFileURL(path.join(frontend, 'tailwind.config.js')))
  const prefix = 'virtual:user-live-review/'
  const modules = {
    api: `export const adminAPI = {
      users: { list: async () => ({ items: ${JSON.stringify(rows)}, total: ${rows.length}, pages: 1, page: 1, page_size: 20 }) },
      groups: { getAll: async () => [], getAllIncludingInactive: async () => [] },
      dashboard: { getBatchUsersUsage: async () => ({ stats: {} }) },
      userAttributes: { listEnabledDefinitions: async () => [], getBatchUserAttributes: async () => ({ values: {} }) }
    };`,
    auth: `export const useAuthStore = () => ({ user: { id: 41, role: 'admin' }, isAdmin: true });`,
    app: `export const useAppStore = () => ({ showError: message => { throw new Error(message) }, showSuccess: () => {} });`,
    layout: `import { h } from 'vue'; export default { setup(_, { slots }) { return () => h('main', { class: 'min-h-screen bg-gray-50 p-4 text-gray-900 dark:bg-dark-950 dark:text-gray-100' }, slots.default?.()); } };`,
    dialog: `export default { render: () => null };`,
    entry: `import { createApp, h } from 'vue';
      import { createPinia } from 'pinia'; import { createI18n } from 'vue-i18n';
      import Users from '/src/views/admin/UsersView.vue';
      import { NEW_FEATURE_NOTICES } from '/src/constants/newFeatureNotices.ts';
      import { useNewFeatureNoticeStore } from '/src/stores/newFeatureNotices.ts';
      import zh from '/src/i18n/locales/zh/index.ts'; import '/src/style.css';
      const pinia = createPinia(); const notices = useNewFeatureNoticeStore(pinia);
      notices.dismiss(41, NEW_FEATURE_NOTICES.vipAutoCredit);
      window.userLiveReview = { notices, ids: NEW_FEATURE_NOTICES };
      createApp({ render: () => h(Users) }).use(pinia).use(createI18n({ legacy: false, locale: 'zh', messages: { zh } })).mount('#app');`,
  }
  const virtual = {
    name: 'user-live-offline-review', enforce: 'pre',
    resolveId(id) { return id.startsWith(prefix) ? `\0${id}` : null },
    load(id) { return id.startsWith(`\0${prefix}`) ? modules[id.slice(prefix.length + 1)] : null },
    configureServer(vite) {
      vite.middlewares.use(async (req, res, next) => {
        const pathname = new URL(req.url, 'http://127.0.0.1:4198').pathname
        if (pathname.startsWith('/api/')) { res.statusCode = 503; res.end('Offline fixture'); return }
        if (pathname !== '/') return next()
        res.setHeader('Content-Type', 'text/html')
        res.end(await vite.transformIndexHtml(req.url, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><div id="app"></div><script type="module">import '${prefix}entry'</script></body></html>`))
      })
    },
  }
  const alias = (find, name) => ({ find, replacement: `${prefix}${name}` })
  const server = await createServer({
    root: frontend, configFile: false, cacheDir, plugins: [virtual, vue()],
    define: { __INTLIFY_JIT_COMPILATION__: true },
    resolve: { dedupe: ['vue', 'pinia', 'vue-i18n'], alias: [
      alias(/^@\/api\/admin$/, 'api'), alias('@/stores/auth', 'auth'), alias('@/stores/app', 'app'),
      alias('@/components/layout/AppLayout.vue', 'layout'),
      alias(/^@\/components\/(admin\/user\/.*Modal|user\/UserAttributesConfigModal)\.vue$/, 'dialog'),
      { find: '@', replacement: path.join(frontend, 'src') },
    ] },
    css: { postcss: { plugins: [tailwind({ ...theme, content: [path.join(frontend, 'src/**/*.{vue,ts}'), { raw: modules.layout, extension: 'js' }] }), autoprefixer()] } },
    server: { host: '127.0.0.1', port: 4198, strictPort: true, fs: { allow: [root, headRoot, cacheDir] } },
  })
  let context
  try {
    await server.listen()
    context = await browser.newContext({ viewport: { width: 1440, height: 1100 } })
    await context.addInitScript(() => {
      // Keep table focus identical on both fixed sources; no product CSS overlays.
      localStorage.setItem('user-hidden-columns', JSON.stringify(['notes', 'role', 'groups', 'subscriptions', 'balance', 'balance_platform_quota', 'usage', 'usage_anthropic', 'usage_openai', 'usage_gemini', 'usage_antigravity', 'concurrency', 'status', 'last_active_at', 'last_used_at', 'created_at']))
      localStorage.setItem('user-column-settings-version', '3')
    })
    const page = await context.newPage()
    const errors = []
    page.on('pageerror', error => errors.push(error.message))
    page.on('console', message => { if (message.type() === 'error') errors.push(message.text()) })
    await page.route('**/api/**', route => { evidence.liveApiCalls++; return route.abort() })
    await page.goto('http://127.0.0.1:4198/', { waitUntil: 'networkidle' })
    await page.locator('tbody tr').first().waitFor()
    assert.equal(await page.locator('tbody tr').count(), rows.length)
    const desktop = page.locator('thead [data-testid="new-feature-badge"]')
    assert.equal(await desktop.count(), 1)
    for (const dark of [false, true]) {
      await page.evaluate(dark => document.documentElement.classList.toggle('dark', dark), dark)
      await page.screenshot({ path: path.join(output, `${label}-desktop-${dark ? 'dark' : 'light'}.png`), fullPage: true })
    }
    if (label === 'after') {
      const cells = await page.locator('tbody tr').allTextContents()
      for (let i = 2; i < rows.length; i++) assert.ok(cells[i].includes('—'), scenarios[i])
    }
    await page.setViewportSize({ width: 390, height: 1000 })
    await page.locator('[data-field="live_usage"]').first().waitFor()
    const mobile = page.locator('[data-field="live_usage"] [data-testid="new-feature-badge"]')
    assert.equal(await mobile.count(), label === 'before' ? 0 : rows.length)
    for (const dark of [false, true]) {
      await page.evaluate(dark => document.documentElement.classList.toggle('dark', dark), dark)
      await page.screenshot({ path: path.join(output, `${label}-mobile-${dark ? 'dark' : 'light'}.png`), fullPage: true })
    }
    if (label === 'after') {
      assert.ok(await mobile.first().isVisible())
      await mobile.first().click()
      assert.equal(await mobile.count(), 0, 'all mobile instances dismiss together')
      await page.setViewportSize({ width: 1440, height: 1100 })
      await page.locator('thead').waitFor()
      assert.equal(await desktop.count(), 0, 'desktop shares the same dismissal')
      await page.reload({ waitUntil: 'networkidle' })
      assert.equal(await desktop.count(), 0, 'dismissal survives reload')
      // A later unrelated announcement is independent of the dismissed IDs.
      assert.equal(await page.evaluate(() => window.userLiveReview.notices.isDismissed(41, 'user-live-review-future-fixture')), false)
      await page.screenshot({ path: path.join(output, 'after-shared-dismissal.png'), fullPage: true })
      evidence.checks.push('mobile-visible', 'mobile-dismiss-all', 'desktop-shared-dismissal', 'reload-persistence', 'independent-future-id', 'unknown-is-not-zero')
    }
    assert.deepEqual(errors, [])
    evidence.sources.push({ label, sha, view: 'UsersView.vue', realDataTable: true, realCSS: true, handlerFixturesFrom: process.env.USER_LIVE_LOAD_HEAD })
  } finally {
    await context?.close()
    await server.close()
    await fs.rm(cacheDir, { recursive: true, force: true })
    if (linked) await fs.unlink(modulesPath)
  }
}

try {
  // Preserve the entire before set even when head assertions subsequently fail.
  await capture(process.env.USER_LIVE_LOAD_BEFORE_DIR, process.env.USER_LIVE_LOAD_BASE, 'before')
  await fs.writeFile(path.join(output, 'before-preserved.json'), JSON.stringify(evidence, null, 2))
  await capture(headRoot, process.env.USER_LIVE_LOAD_HEAD, 'after')
  assert.equal(evidence.liveApiCalls, 0)
  await fs.writeFile(path.join(output, 'verification.json'), JSON.stringify(evidence, null, 2))
} finally { await browser.close() }
