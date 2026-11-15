// Actual application and SettingsView from full base/head trees; all APIs use fictional fixtures.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
const output = process.env.SCREENSHOT_DIR
if (!output || !process.env.PLAYWRIGHT_ROOT || !process.env.BASELINE_ROOT || !process.env.SOURCE_BASE) throw Error('Missing CI fixture paths')
mkdirSync(output, { recursive: true })
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
const manifest = {
  base: process.env.SOURCE_BASE, head: execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(),
  scope: 'Actual full frontend, SettingsView email tab and CSS; synthetic auth and API fixtures. No backend, SMTP or production validation.',
  screenshots: [], errors: [], fixturePaths: [],
}
const browser = await chromium.launch({ headless: true })
try {
  for (const mode of ['before', 'after']) {
    const root = resolve(mode === 'before' ? process.env.BASELINE_ROOT : 'frontend')
    const require = createRequire(join(root, 'package.json'))
    const { createServer } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
    const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
    const tailwind = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
    const server = await createServer({ root, configFile: false, resolve: { alias: { '@': join(root, 'src') } }, plugins: [vue()],
      css: { postcss: { plugins: [require('tailwindcss')({ ...tailwind, content: [join(root, 'src/**/*.{vue,js,ts}')] }), require('autoprefixer')()] } },
      server: { host: '127.0.0.1', port: 4192, strictPort: true },
    })
    await server.listen()
    const page = await browser.newPage({ viewport: { width: 1440, height: 1080 } })
    page.on('pageerror', err => manifest.errors.push(`${mode}: ${err.message}`))
    const user = { id: 41, role: 'admin', status: 'active', email: 'admin@example.invalid', username: 'Fixture administrator', balance: 100, concurrency: 5 }
    await page.addInitScript(user => {
      localStorage.setItem('auth_token', 'synthetic-fixture')
      localStorage.setItem('auth_user', JSON.stringify(user))
      localStorage.setItem('sub2api_locale', 'zh')
      localStorage.setItem('admin_guide_41_admin_v4_interactive', 'true')
    }, user)
    await page.route('**/*', async route => {
      const url = new URL(route.request().url())
      if (url.origin !== 'http://127.0.0.1:4192') return route.abort()
      if (!url.pathname.startsWith('/api/')) return route.continue()
      const path = url.pathname.replace(/^\/api\/v1/, '')
      manifest.fixturePaths.push(`${mode}:${path}`)
      let data = {}
      if (path === '/auth/me') data = user
      else if (path === '/admin/settings') data = { site_name: 'Sub2API', balance_low_notify_enabled: true, balance_low_notify_threshold: 25,
        ...(mode === 'before' ? { balance_forecast_enabled: true, balance_forecast_threshold_hours: 12, balance_forecast_max_notifications: 3 } : {}) }
      else if (path === '/admin/backups/image-storage') data = { config: { enabled: false, prefix: 'images/', region: 'auto' }, secret_configured: false }
      else if (path === '/admin/settings/email-templates') data = { events: [], locales: ['zh', 'en'], templates: [], placeholders: [] }
      else if (path === '/admin/groups/all' || path === '/admin/payment/providers') data = []
      else if (path === '/keys' || path === '/admin/proxies') data = { items: [], total: 0, pages: 1 }
      else if (path === '/settings/public') data = { site_name: 'Sub2API', custom_menu_items: [], registration_enabled: false }
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ code: 0, message: 'success', data }) })
    })
    try {
      await page.goto('http://127.0.0.1:4192/admin/settings', { waitUntil: 'networkidle' })
      await page.locator('#settings-tab-email').click()
      const forecast = page.locator('[data-testid="balance-forecast-settings"]')
      if (mode === 'before') await forecast.waitFor({ state: 'visible' })
      else assert.equal(await forecast.count(), 0)
      const low = page.locator('h3').filter({ hasText: /余额不足提醒|Balance Low Notification/ }).first()
      await low.waitFor({ state: 'visible' })
      assert.equal((await page.locator('[aria-live="polite"].pointer-events-none').allTextContents()).join('').trim(), '', 'fixture must not produce error toasts')
      if (mode === 'before') await forecast.scrollIntoViewIfNeeded()
      else await low.scrollIntoViewIfNeeded()
      await page.screenshot({ path: join(output, `${mode}-email.png`), animations: 'disabled' })
      manifest.screenshots.push(`${mode}-email.png`)
    } catch (error) {
      await page.screenshot({ path: join(output, `${mode}-failure.png`), fullPage: true })
      writeFileSync(join(output, `${mode}-failure.txt`), await page.locator('body').innerText())
      throw error
    } finally { await page.close(); await server.close() }
  }
  assert.deepEqual(manifest.errors, [])
} finally {
  await browser.close()
  writeFileSync(join(output, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n')
}
