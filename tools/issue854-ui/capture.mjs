// Offline browser evidence: real components/CSS from immutable Git archives.
// Usage: ISSUE854_PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs node tools/issue854-ui/capture.mjs REPO OUTPUT BASELINE CANDIDATE
import { createRequire } from 'node:module'
import { execFileSync } from 'node:child_process'
import * as fs from 'node:fs'
import path from 'node:path'
import { createHash } from 'node:crypto'
import assert from 'node:assert/strict'
const [repo, output, baseline, candidate] = process.argv.slice(2)
assert(repo && output && baseline && candidate)
fs.mkdirSync(output, { recursive: true, mode: 0o700 })
const require = createRequire(path.join(repo, 'frontend/package.json'))
const { createServer } = await import(path.join(path.dirname(require.resolve('vite/package.json')), 'dist/node/index.js'))
const { default: vue } = await import(require.resolve('@vitejs/plugin-vue'))
const { chromium } = await import(process.env.ISSUE854_PLAYWRIGHT_MODULE || 'playwright')
const hash = data => createHash('sha256').update(data).digest('hex')
const git = (...args) => execFileSync('git', ['-C', repo, ...args], { maxBuffer: 128 * 1024 * 1024 })
const base = { created_at: '2026-11-15T00:00:00Z', platform: 'openai', model: 'fixture-model', severity: 'P1', resolved: false, upstream_status_code: null, user_email: '', account_name: '', group_name: '', client_ip: '', inbound_endpoint: '/v1/responses', upstream_endpoint: '/v1/responses', upstream_errors: '[]', is_business_limited: false }
const rows = [
  { ...base, id: 85401, request_id: 'fixture-http-transport', status_code: 200, phase: 'network', type: 'transport_error', error_owner: 'provider', error_source: 'upstream_transport', severity: 'error', request_type: 2, message: 'Synthetic upstream connection reset', error_body: JSON.stringify({ error: { code: 'upstream_http2_stream_error', message: 'Synthetic upstream connection reset' } }) },
  { ...base, id: 85402, request_id: 'fixture-ws-owner-turn2', status_code: 409, severity: 'P3', phase: 'internal', type: 'invalid_request_error', error_owner: 'platform', error_source: 'gateway', request_type: 3, message: "The proven encrypted agent owner is currently unavailable. Preserve this task and context. Ask the administrator to check the original account's access and availability; ordinary account switching cannot establish compatibility.", error_body: JSON.stringify({ error: { code: 'encrypted_agent_recovery_owner_unavailable', message: "The proven encrypted agent owner is currently unavailable. Preserve this task and context. Ask the administrator to check the original account's access and availability; ordinary account switching cannot establish compatibility.", type: 'invalid_request_error', protocol: 'websocket', turn: 2, phase: 'before_upstream_write', outcome: 'failed', wire_status: 101, status: 409, request_written: false } }) }
]
fs.writeFileSync(path.join(output, 'fixtures.json'), JSON.stringify(rows, null, 2))
const app = `<script setup>
import { ref } from 'vue'
import OpsErrorLogTable from '@/views/admin/ops/components/OpsErrorLogTable.vue'
import OpsErrorDetailModal from '@/views/admin/ops/components/OpsErrorDetailModal.vue'
const rows = ${JSON.stringify(rows)}
const selected = ref(null)
const show = ref(false)
function open(id) { selected.value = id; show.value = true }
</script>
<template>
<main class="p-4 h-screen bg-gray-50"><p class="mb-4 text-sm text-gray-600">Issue854 · Synthetic fixture · {{ new URLSearchParams(location.search).get('label') }}</p><section style="min-height: 420px"><OpsErrorLogTable :rows="rows" :total="2" :loading="false" :page="1" :page-size="20" :visible-column-keys="['category','status','message','actions']" @open-error-detail="open" /></section><OpsErrorDetailModal v-model:show="show" :error-id="selected" error-type="request" /></main>
</template>`
// No substituted components, styles, translation messages, or API client.
const fixedApp = app.replace("{{ new URLSearchParams(location.search).get('label') }}", '{{ label }}').replace('const rows =', "const label = new URLSearchParams(location.search).get('label')\nconst rows =")
const main = `import { createApp } from 'vue'; import { createPinia } from 'pinia'; import i18n, { initI18n } from './src/i18n'; import './src/style.css'; import App from './Evidence.vue'; await initI18n(); createApp(App).use(createPinia()).use(i18n).mount('#app');`
const manifest = { baseline, candidate, fixtureSHA256: hash(JSON.stringify(rows, null, 2)), harnessSHA256: hash(fs.readFileSync(new URL(import.meta.url))), captureOrder: ['before','after'], privacy: 'Only synthetic fixture; no production data, credentials, emails, addresses or opaque history. Browser network restricted to loopback.', versions: { node: process.version }, snapshots: [], captures: [], assertions: [], errors: [] }
const browser = await chromium.launch({ executablePath: process.env.ISSUE854_CHROME || '/usr/bin/google-chrome', headless: true, args: ['--no-sandbox','--disable-dev-shm-usage'] })
manifest.versions.browser = browser.version()
try {
for (const [label, sha] of [['before', baseline], ['after', candidate]]) {
  const root = path.join(output, label)
  fs.mkdirSync(root, { recursive: true })
  execFileSync('tar', ['-x', '-C', root], { input: git('archive', sha, 'frontend') })
  const front = path.join(root, 'frontend')
  fs.symlinkSync(path.join(repo, 'frontend/node_modules'), path.join(front, 'node_modules'), 'dir')
  fs.writeFileSync(path.join(front, 'Evidence.vue'), fixedApp)
  fs.writeFileSync(path.join(front, 'evidence.ts'), main)
  fs.writeFileSync(path.join(front, 'index.html'), '<!doctype html><html><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Issue854 synthetic evidence</title></head><body><div id="app"></div><script type="module" src="/evidence.ts"></script></body></html>')
  const blobFiles = ['frontend/src/views/admin/ops/components/OpsErrorLogTable.vue','frontend/src/views/admin/ops/components/OpsErrorDetailModal.vue','frontend/src/utils/errorCategory.ts','frontend/src/style.css','frontend/tailwind.config.js','frontend/pnpm-lock.yaml']
  manifest.snapshots.push({label, sha, tree: git('rev-parse', sha+'^{tree}').toString().trim(), blobs: Object.fromEntries(blobFiles.map(p=>[p,git('rev-parse',sha+':'+p).toString().trim()])) })
  const server = await createServer({ configFile: false, root: front, plugins: [vue()], resolve: { alias: { '@': path.join(front,'src'), 'vue-i18n': require.resolve('vue-i18n/dist/vue-i18n.runtime.esm-bundler.js') } }, define: { __INTLIFY_JIT_COMPILATION__: true }, css: { postcss: { plugins: [require('tailwindcss')({ ...((await import(path.join(front,'tailwind.config.js'))).default), content: [path.join(front,'src/**/*.{vue,js,ts,jsx,tsx}'), path.join(front,'Evidence.vue')] }),require('autoprefixer')()] } }, server: { host: '127.0.0.1', port: 0, fs: { allow: [front, path.join(repo,'frontend')] } } })
  await server.listen()
  const url = server.resolvedUrls.local[0]
  try {
    for (const locale of ['en','zh']) for (const [device, viewport] of [['desktop',{width:1440,height:1000}],['mobile',{width:390,height:844}]]) {
      const context = await browser.newContext({ viewport, deviceScaleFactor: 1, locale: locale === 'zh' ? 'zh-CN' : 'en-US', timezoneId: 'UTC', colorScheme: 'light' })
      await context.addInitScript(locale => { localStorage.setItem('sub2api_locale',locale) },locale)
      const page = await context.newPage()
      page.on('pageerror', e => manifest.errors.push({label,locale,device,message:e.message}))
      await page.route('**/*', async route => {
        const u = new URL(route.request().url())
        if (u.hostname !== '127.0.0.1') throw new Error('Unexpected external request: '+u.origin)
        if (u.pathname.startsWith('/api/')) {
          const item = rows.find(r=>u.pathname.endsWith('/'+r.id))
          assert(item || u.pathname.endsWith('/upstream-errors'), 'Unexpected API path '+u.pathname)
          return route.fulfill({json:{code:0,message:'ok',data:item || {items:[],total:0}}})
        }
        return route.continue()
      })
      await page.goto(url+'?label='+label, {waitUntil:'networkidle'})
      const renderedRows = page.locator(device === 'desktop' ? 'tbody tr' : 'section .cursor-pointer').filter({has: page.locator(device === 'desktop' ? 'td' : '[data-field]')})
      await renderedRows.first().waitFor()
      const tableText = await page.locator('section').innerText()
      assert(!tableText.includes('usage.errors.'), 'untranslated keys')
      const httpRow = renderedRows.nth(0), wsRow = renderedRows.nth(1)
      assert((await httpRow.innerText()).includes('200'))
      assert((await wsRow.innerText()).includes('409'))
      assert(!(await wsRow.innerText()).includes('101'))
      const hintText = await httpRow.locator('p').allTextContents()
      assert.equal(hintText.length, label==='after' ? 1 : 0, 'HTTP200 failure hint delta')
      assert((await wsRow.innerText()).includes(locale==='en'?'Platform error':'平台错误'))
      const css = await page.locator('section').evaluate(e => getComputedStyle(e.querySelector('.card')).borderRadius)
      assert.notEqual(css, '0px', 'real project CSS must load')
      manifest.assertions.push({label,locale,device,check:'table HTTP200 failure hint and WS logical409',pass:true,tableText})
      const shot = async (view) => {
        await page.evaluate(()=>document.fonts.ready)
        const name = `${label}-${locale}-${device}-${view}.png`
        const buf = await page.screenshot({path:path.join(output,name), animations:'disabled'})
        manifest.captures.push({name,label,sha,locale,viewport,view,utc:new Date().toISOString(),sha256:hash(buf),bytes:buf.length})
      }
      await shot('table')
      for (const [n,kind] of [[0,'http'],[1,'ws']]) {
        await renderedRows.nth(n).click()
        const dialog=page.getByRole('dialog')
        await dialog.getByText(rows[n].request_id,{exact:true}).waitFor()
        const text=await dialog.innerText()
        assert(text.includes(String(rows[n].status_code)))
        if(kind==='ws') { assert(text.includes('"wire_status": 101'));assert(text.includes('"turn": 2'));assert(text.includes('"request_written": false'));assert(text.includes('"outcome": "failed"')) }
        if(kind==='http') assert.equal((await dialog.locator('p.text-amber-700').count()),label==='after'?1:0)
        manifest.assertions.push({label,locale,device,check:kind+' actual detail shape',pass:true})
        // Scroll the actual modal: preserve project CSS and viewport. No resized full-panel composite.
        // Select the actual numeric status badge, not a positional metadata
        // card. A preceding summary field must not move status off-screen.
        const statusLabel=dialog.locator('.grid > div').filter({
          has: page.locator('span').filter({hasText: new RegExp(`^${rows[n].status_code}$`)})
        })
        assert.equal(await statusLabel.count(), 1, 'one actual status card')
        await statusLabel.evaluate(e=>e.scrollIntoView({block:'center',behavior:'instant'}))
        const box=await statusLabel.boundingBox()
        assert(box && box.y >= 50 && box.y+box.height <= viewport.height-20, 'entire status card must be inside viewport')
        if(kind==='http' && label==='after') {
          const warning=statusLabel.locator('p.text-amber-700')
          assert.equal(await warning.count(),1)
          const warningBox=await warning.boundingBox()
          assert(warningBox && warningBox.y >= 50 && warningBox.y+warningBox.height <= viewport.height-20, 'failure warning must be inside viewport')
        }
        manifest.assertions.push({label,locale,device,check:kind+' status card viewport bounds',pass:true,box})
        await shot(kind+'-detail-status')
        if(kind==='ws') { await dialog.locator('pre').first().scrollIntoViewIfNeeded(); await shot('ws-detail-diagnostics') }
        await dialog.getByRole('button',{name:'Close modal',exact:true}).click()
      }
      await context.close()
    }
  } finally { await server.close() }
}
assert.equal(manifest.errors.length,0,JSON.stringify(manifest.errors))
} finally { await browser.close(); fs.writeFileSync(path.join(output,'manifest.json'),JSON.stringify(manifest,null,2)) }
console.log(JSON.stringify({captures:manifest.captures.length,assertions:manifest.assertions.length,errors:manifest.errors}))
