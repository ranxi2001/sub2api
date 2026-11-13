// Actual user error component and project styles, with synthetic DTO fixtures.
// Images and browser logs are evidence artifacts, never source files.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const [repoArg, outputArg] = process.argv.slice(2)
const repo = resolve(repoArg), output = resolve(outputArg), root = join(repo, 'frontend')
const req = createRequire(join(root, 'package.json'))
const { createServer } = await import(pathToFileURL(join(dirname(req.resolve('vite/package.json')), 'dist/node/index.js')).href)
const vue = (await import(pathToFileURL(req.resolve('@vitejs/plugin-vue')).href)).default
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const git = (...args) => execFileSync('git', args, { cwd: repo, encoding: 'utf8' }).trimEnd()
const sha = git('rev-parse', 'HEAD'), baseline = 'ccf997c73868083d96c0bec56e71c1a34d26c98a'
mkdirSync(output, { recursive: true })
const scratch = mkdtempSync(join(output, 'vite-')), entryName = `.http2-preview-${process.pid}.js`, entry = join(root, entryName)
let before = true, server, browser
const errors = [], captures = []
const origin = 'http://127.0.0.1:4299'
try {
  writeFileSync(entry, `
import {mount} from '@vue/test-utils';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import View from '/src/components/user/UserErrorDetailModal.vue';
import '/src/style.css';
const params=new URL(location.href).searchParams;
const locale=params.get('locale')||'en',before=params.get('before')==='1';
await loadLocaleMessages(locale);i18n.global.locale.value=locale;
window.fixture={id:833,created_at:'2026-11-13T10:00:00Z',model:'example-model',inbound_endpoint:'/v1/responses',status_code:before?502:200,category:before?'upstream':'transport_error',platform:'openai',message:before?'Upstream HTTP/2 stream failed':'Upstream connection reset by peer (HTTP/2 INTERNAL_ERROR). Content may be incomplete. Do not automatically retry the same request.',reset_reason:before?undefined:'INTERNAL_ERROR',upstream_status_code:before?502:undefined,key_name:'Demo key',key_deleted:false,error_body:JSON.stringify({error:{type:'upstream_error',code:'upstream_http2_stream_error',...(before?{}:{category:'transport_error',protocol:'HTTP/2',reset_reason:'INTERNAL_ERROR'})}},null,2)};
window.preview=mount(View,{attachTo:'#app',props:{show:false,errorId:833,'onUpdate:show':show=>window.preview.setProps({show})},global:{plugins:[i18n]}});
await window.preview.setProps({show:true});
`, { flag: 'wx' })
  process.chdir(root)
  server = await createServer({ root, configFile: false, cacheDir: scratch,
    resolve: { alias: { '@': join(root, 'src') } },
    plugins: [{ name: 'http2-preview', enforce: 'pre', load(id) {
      const clean = id.split('?')[0]
      if (clean === join(root, 'src/api/usage.ts')) return 'export const getMyErrorDetail=async()=>window.fixture'
      if (before && !id.includes('?') && clean === join(root, 'src/components/user/UserErrorDetailModal.vue')) return git('show', baseline + ':frontend/src/components/user/UserErrorDetailModal.vue')
    }, configureServer(vite) {
      vite.middlewares.use((request, response, next) => {
        if (request.url === '/' || request.url?.startsWith('/?')) {
          response.setHeader('Content-Type', 'text/html')
          response.end('<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/' + entryName + '"></script>')
        } else if (request.url === '/favicon.ico') { response.statusCode = 204; response.end() } else next()
      })
    } }, vue()],
    server: { host: '127.0.0.1', port: 4299, strictPort: true },
    optimizeDeps: { entries: [], include: ['vue', 'vue-i18n', '@vue/test-utils'] }
  })
  await server.listen()
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1180, height: 900 }, timezoneId: 'UTC' })
  page.on('pageerror', error => errors.push(error.message))
  await page.route('**/*', route => {
    if (new URL(route.request().url()).origin === origin) return route.continue()
    errors.push('unexpected external request'); return route.abort()
  })
  for (const state of ['before', 'after']) {
    before = state === 'before'; server.moduleGraph.invalidateAll()
    for (const locale of ['en', 'zh']) {
      await page.goto(origin + '/?locale=' + locale + '&before=' + (before ? '1' : '0'), { waitUntil: 'networkidle' })
      await page.getByText('example-model', { exact: true }).waitFor()
      const dialog = page.getByRole('dialog'), text = await dialog.innerText()
      if (!before) {
        for (const expected of [locale === 'en' ? 'Do not automatically retry' : '请勿自动重试', locale === 'en' ? 'Actual client HTTP status' : '客户端实际 HTTP 状态码', locale === 'en' ? 'HTTP 200 does not mean generation completed' : 'HTTP 200 不表示生成已完成', locale === 'en' ? 'Gateway-generated error details' : '网关生成的错误详情', 'HTTP/2 INTERNAL_ERROR', '200']) if (!text.includes(expected)) throw Error('Missing ' + expected)
        for (const forbidden of ['502', 'stream ID', 'usage.errors.', '上游响应内容']) if (text.includes(forbidden)) throw Error('Unexpected ' + forbidden)
      }
      const name = `${state}-${locale}.png`
      await dialog.screenshot({ path: join(output, name) })
      captures.push({ name, sha: before ? baseline : sha, fixture: state, text })
      await page.getByLabel('Close modal').click()
      await page.getByRole('dialog').waitFor({ state: 'detached' })
    }
  }
  writeFileSync(join(output, 'manifest.json'), JSON.stringify({ sha, baseline, captures, errors, boundary: 'Real UserErrorDetailModal, BaseDialog, EN/ZH translations and styles. Synthetic API DTOs; before uses baseline component. No production traffic.' }, null, 2))
  if (errors.length) throw Error(errors.join('\n'))
  console.log('Rendered and exercised before/after error details in EN/ZH at ' + sha)
} finally {
  await browser?.close(); await server?.close()
  rmSync(entry, { force: true }); rmSync(scratch, { recursive: true, force: true })
}
