// Real shared UsageTable + real styles/locales, with synthetic data only.
import assert from 'node:assert/strict'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
const exec = promisify(execFile)
import { createRequire } from 'node:module'
import { createHash } from 'node:crypto'
import fs from 'node:fs/promises'
import path from 'node:path'
import { pathToFileURL } from 'node:url'

const root = process.cwd()
const frontend = path.join(root, 'frontend')
const output = process.env.FREE_FAST_SCREENSHOT_DIR
assert.ok(output, 'FREE_FAST_SCREENSHOT_DIR must point outside the source tree')
const base = '0e8e97aa23afd8e88a61ea64a84a8fb84f1bfdc6'
const head = (await exec('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' })).stdout.trim()
const req = createRequire(path.join(frontend, 'package.json'))
const { createServer } = req('vite')
const vue = req('@vitejs/plugin-vue').default
const browserReq = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))
const { chromium } = browserReq('playwright')
await fs.mkdir(output, { recursive: true })
const before = path.join(output, 'before-source')
await fs.mkdir(before, { recursive: true })
// Export the complete baseline source, including transitive non-frontend assets.
const archive = path.join(output, 'baseline-source.tar')
await exec('git', ['archive', '--output=' + archive, base])
await exec('tar', ['-xf', archive, '-C', before])
await fs.symlink(path.join(frontend, 'node_modules'), path.join(before, 'frontend/node_modules')).catch(e => { if (e.code !== 'EEXIST') throw e })
const fixtureFile = 'src/components/admin/usage/__tests__/fixtures/freeFastCustomerCost.ts'
const fixture = await fs.readFile(path.join(frontend, fixtureFile), 'utf8')
const manifest = { complete: false, base, head, fixtureSha256: createHash('sha256').update(fixture).digest('hex'), harnessSha256: createHash('sha256').update(await fs.readFile(new URL(import.meta.url))).digest('hex'), synthetic: true, source: 'real Vue shared UsageTable, real DataTable, popover/dialog, locales and style.css', screenshots: [], errors: [], requests: [] }
const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'] })
try {
  for (const revision of ['before', 'after']) {
    const source = revision === 'before' ? path.join(before, 'frontend') : frontend
    const component = await fs.readFile(path.join(source, 'src/components/admin/usage/UsageTable.vue'))
    const componentHash = createHash('sha256').update(component).digest('hex')
    const html = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><div id="app"></div><script type="module">
import {createApp,h} from 'vue';import {createPinia} from 'pinia';
import UsageTable from '/src/components/admin/usage/UsageTable.vue';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {freeFastCachePriceFixture,freeFastMixedCachePriceFixture} from 'fixture:cost';
import '/src/style.css';
const params=new URLSearchParams(location.search),lang=params.get('lang'),admin=params.get('role')==='admin';
await loadLocaleMessages(lang);i18n.global.locale.value=lang;
const fixture=params.get('fixture')==='mixed'?freeFastMixedCachePriceFixture:freeFastCachePriceFixture;
const data=[{...fixture,created_at:'2026-01-01T00:00:00Z',duration_ms:1000,first_token_ms:100,request_type:'stream',cache_ttl_overridden:false,long_context_billing_applied:false}];
const columns=[{key:'model',label:i18n.global.t('usage.model')},{key:'cost',label:i18n.global.t('usage.costDetails')}];
createApp({render:()=>h('main',{class:'p-4 mx-auto max-w-4xl'},[h(UsageTable,{data,columns,showAccountBilling:admin,cacheRateAudience:admin?'admin':'user',enableTimingDetails:false})])}).use(createPinia()).use(i18n).mount('#app');
</script></body></html>`
    const tailwindConfig = (await import(pathToFileURL(path.join(source, 'tailwind.config.js')).href)).default
    process.chdir(source)
    const server = await createServer({ root: source, configFile: false, envFile: false,
      cacheDir: path.join(output, 'vite-cache-' + revision),
      define: { __INTLIFY_JIT_COMPILATION__: true },
      css: { postcss: { plugins: [
        req('tailwindcss')({ ...tailwindConfig, content: [path.join(source, 'index.html'), path.join(source, 'src/**/*.{vue,js,ts,jsx,tsx}')] }),
        req('autoprefixer')(),
      ] } },
      plugins: [vue(), { name: 'synthetic-cost-page',
        resolveId(id) { if (id === 'fixture:cost') return '\0fixture:cost' },
        load(id) { if (id === '\0fixture:cost') return fixture },
        configureServer(vite) {
        vite.middlewares.use((request, response, next) => {
          if (request.url?.split('?')[0] !== '/') return next()
          response.setHeader('Content-Type', 'text/html')
          void vite.transformIndexHtml('/', html).then(text => response.end(text))
        })
      } }],
      resolve: { alias: { '@': path.join(source, 'src'), 'vue-i18n': 'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js' } },
      server: { host: '127.0.0.1', port: 4189, strictPort: true, fs: { allow: [source, frontend] } },
      optimizeDeps: { entries: [], include: ['vue', 'pinia', 'vue-i18n', 'axios'] },
    })
    await server.listen()
    try {
      for (const fixtureName of ['exact', 'mixed']) for (const locale of ['en', 'zh']) for (const role of ['user', 'admin']) for (const size of ['desktop', 'mobile']) {
        const viewport = size === 'mobile' ? { width: 390, height: 844 } : { width: 1280, height: 900 }
        const context = await browser.newContext({ viewport, colorScheme: 'light', timezoneId: 'UTC', locale: locale === 'zh' ? 'zh-CN' : 'en-US' })
        await context.route('**/*', async route => {
          const url = new URL(route.request().url())
          if (url.origin !== 'http://127.0.0.1:4189') throw new Error('Unexpected external request: ' + url.origin)
          if (url.pathname.startsWith('/api/')) {
            manifest.requests.push(url.pathname)
            return route.fulfill({ json: { code: 0, data: {} } })
          }
          return route.continue()
        })
        const page = await context.newPage()
        page.on('pageerror', error => manifest.errors.push(error.message))
        await page.goto(`http://127.0.0.1:4189/?lang=${locale}&role=${role}&fixture=${fixtureName}`, { waitUntil: 'networkidle' })
        const button = page.getByRole('button', { name: locale === 'zh' ? '费用明细' : 'Cost Breakdown', exact: true })
        // Use the actual accessible label even when a locale changes its translation.
        const costButton = await button.count() ? button : page.locator('button[aria-expanded]').filter({ has: page.locator('.text-green-600') })
        await costButton.click()
        const panel = page.locator(size === 'mobile' ? '[role="dialog"]' : '[role="tooltip"]')
        await panel.waitFor({ state: 'visible' })
        // Capture the settled dialog, not a partially transparent enter frame.
        await panel.evaluate(async element => {
          await Promise.all(element.getAnimations({ subtree: true })
            .filter(animation => animation.effect?.getComputedTiming().iterations !== Infinity)
            .map(animation => animation.finished))
        })
        const text = await panel.innerText()
        assert.ok(text.includes(revision === 'after' ? '$10.0000' : '$20.0000'), text)
        assert.ok(text.includes(revision === 'after' ? '$50.0000' : '$100.0000'), text)
        const amounts = fixtureName === 'exact'
          ? (revision === 'after' ? ['0.08345000', '0.00680000', '0.01496250', '0.03746900', '0.14268150'] : ['0.16690000', '0.01360000', '0.01496250', '0.07493800', '0.27040050'])
          : (revision === 'after' ? ['0.08345000', '0.00680000', '0.00046250', '0.00001700', '0.09072950'] : ['0.16690000', '0.01360000', '0.00046250', '0.00003400', '0.18099650'])
        for (const value of amounts) assert.ok(text.includes('$' + value), text)
        assert.ok(text.includes(fixtureName === 'exact' ? '$0.07134075' : '$0.04536475'), text)
        if (revision === 'after') {
          assert.ok((await panel.getByTestId('cache-unit-read').innerText()).includes('$1.0000'))
          assert.ok((await panel.getByTestId('cache-unit-write_30m').innerText()).includes('$12.5000'))
          if (fixtureName === 'mixed') assert.ok((await panel.getByTestId('cache-unit-write_12h').innerText()).includes('$25.0000'))
          assert.ok(text.includes(locale === 'zh' ? '缓存读取单价' : 'Cache read price'), text)
          assert.ok(text.includes(locale === 'zh' ? '缓存写入单价（30m）' : 'Cache write price (30m)'), text)
        }
        assert.ok(text.includes('Fast'), text)
        assert.ok(!/Free Fast|Fast 加速免费|按 Standard 计费/.test(text), text)
        const bounds = await panel.boundingBox()
        assert.ok(bounds && bounds.x >= 0 && bounds.x + bounds.width <= viewport.width + 1 && bounds.y >= 0 && bounds.y + bounds.height <= viewport.height + 1, JSON.stringify(bounds))
        const contentBounds = await panel.locator('.bg-gray-900').boundingBox()
        assert.ok(contentBounds && contentBounds.x >= 0 && contentBounds.y >= 0 && contentBounds.x + contentBounds.width <= viewport.width + 1 && contentBounds.y + contentBounds.height <= viewport.height + 1, JSON.stringify(contentBounds))
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
        const computedStyle = await panel.locator('.bg-gray-900').evaluate(element => {
          const css = getComputedStyle(element)
          return { background: css.backgroundColor, fontSize: css.fontSize, padding: css.paddingLeft, border: css.borderTopWidth }
        })
        assert.equal(computedStyle.background, 'rgb(17, 24, 39)', JSON.stringify(computedStyle))
        assert.equal(computedStyle.fontSize, '12px')
        assert.equal(computedStyle.padding, '12px')
        assert.equal(computedStyle.border, '1px')
        const name = `${fixtureName}-${revision}-${locale}-${role}-${size}.png`
        await page.screenshot({ path: path.join(output, name), fullPage: true, animations: 'disabled' })
        const digest = createHash('sha256').update(await fs.readFile(path.join(output, name))).digest('hex')
        manifest.screenshots.push({ file: name, sha256: digest, componentSha256: componentHash, fixtureName, revision, locale, role, viewport, bounds, contentBounds, computedStyle, text })
        await context.close()
      }
    } finally { await server.close(); process.chdir(root) }
  }
  assert.deepEqual(manifest.errors, [])
  assert.equal(manifest.screenshots.length, 32)
  manifest.complete = true
} finally {
  await browser.close()
  await fs.writeFile(path.join(output, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n')
}
