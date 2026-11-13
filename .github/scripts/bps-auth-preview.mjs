// CI-only component rendering; all API responses and callback values are synthetic.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const [repoArg, outputArg] = process.argv.slice(2)
const repo = resolve(repoArg), output = resolve(outputArg), root = join(repo, 'frontend')
const require = createRequire(join(root, 'package.json'))
const { createServer } = await import(pathToFileURL(join(dirname(require.resolve('vite/package.json')), 'dist/node/index.js')).href)
const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const entry = '.bps-auth-preview.js', cacheDir = mkdtempSync(join(tmpdir(), 'bps-auth-preview-'))
const baseline = '0b9cbfbabb2d0ff03adc7f3f5f65b9b4edaa029b'
let before = true, server, browser
mkdirSync(output, { recursive: true })
try {
  writeFileSync(join(root, entry), `
import {mount} from '@vue/test-utils';
import {createPinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {apiClient} from '/src/api/client.ts';
import View from '/src/components/account/OpenAIAuthConnections.vue';
import '/src/style.css';
await loadLocaleMessages('zh'); i18n.global.locale.value='zh';
window.statusResult='unconfirmed'; window.callbackCalls=0;
const connection={id:'fixture',account_id:42,kind:'bps',label:'示例 BPS 连接',legacy:false,status:'active',client_id:'fixture-client',identity_verified:true,scopes:[],capabilities:['responses'],preferred:true,revision:2};
apiClient.defaults.adapter=async config=>{
let data;
if(config.url.endsWith('/callback')){window.callbackCalls++;throw Error('Synthetic lost response')}
else if(config.url.endsWith('/authorize'))data={session_id:'1234567890abcdef1234567890abcdef',auth_url:'https://auth.example.invalid/authorize'};
else if(config.url.endsWith('/authorization-status'))data={status:window.statusResult};
else if(config.method==='get')data=[connection];
else throw Error('Unexpected fixture request');
return {config,status:200,statusText:'OK',headers:{},data:{code:0,data}};
};
document.querySelector('#app').innerHTML='<p class="mb-4 text-gray-500">账号编辑 → 授权连接 · 实际 Vue 组件 / 虚构数据</p><div id="preview"></div>';
window.preview=mount(View,{attachTo:'#preview',props:{accountId:42,accountType:'oauth',active:true},global:{plugins:[createPinia(),i18n]}});
`)
  process.chdir(root)
  server = await createServer({ root, configFile: false, cacheDir,
    resolve: { alias: { '@': join(root, 'src') } },
    plugins: [{ name: 'bps-auth-fixture', enforce: 'pre', load(id) {
      if (before && id === join(root, 'src/components/account/OpenAIAuthConnections.vue')) return execFileSync('git', ['show', baseline + ':frontend/src/components/account/OpenAIAuthConnections.vue'], { cwd: repo, encoding: 'utf8' })
    }, configureServer(vite) { vite.middlewares.use((request, response, next) => {
      if (request.url === '/') { response.setHeader('Content-Type','text/html'); response.end('<!doctype html><html lang="zh"><meta charset="utf-8"><div id="app" style="padding:24px;max-width:920px;margin:auto"></div><script type="module" src="/' + entry + '"></script></html>') }
      else if (request.url === '/favicon.ico') { response.statusCode = 204; response.end() }
      else next()
    }) } }, vue()], server: { host: '127.0.0.1', port: 4298, strictPort: true },
    optimizeDeps: { entries: [], include: ['vue', 'pinia', 'vue-i18n', 'axios', '@vue/test-utils'] } })
  await server.listen(); browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1100, height: 980 } })
  const errors = []
  page.on('pageerror', e => errors.push(e.message))
  await page.route('**/*', route => new URL(route.request().url()).origin === 'http://127.0.0.1:4298' ? route.continue() : route.abort())
  const begin = async () => { await page.getByRole('button', { name: '授权 / 重新登录', exact: true }).click(); await page.locator('#connection-callback-42').waitFor() }
  await page.goto('http://127.0.0.1:4298', { waitUntil: 'networkidle' }); await begin()
  await page.screenshot({ path: join(output, 'before.png'), fullPage: true })
  before = false; server.moduleGraph.invalidateAll()
  await page.reload({ waitUntil: 'networkidle' }); await begin()
  await page.locator('#connection-callback-42').fill('https://example.invalid/callback?code=fixture&state=fixture')
  await page.getByRole('button', { name: '完成授权', exact: true }).click()
  await page.getByTestId('authorization-result').waitFor()
  await page.waitForFunction(() => !document.querySelector('[data-testid="check-authorization"]').disabled)
  await page.screenshot({ path: join(output, 'unconfirmed.png'), fullPage: true })
  await page.evaluate(() => { window.statusResult = 'saved' })
  await page.getByTestId('check-authorization').click()
  await page.getByText('授权凭据已保存。模型是否可用请另行查看模型列表。', { exact: true }).waitFor()
  await page.screenshot({ path: join(output, 'saved.png'), fullPage: true })
  const calls = await page.evaluate(() => window.callbackCalls)
  if (calls !== 1 || errors.length) throw Error(JSON.stringify({calls,errors}))
  writeFileSync(join(output, 'evidence.json'), JSON.stringify({ source: execFileSync('git',['rev-parse','HEAD'],{cwd:repo,encoding:'utf8'}).trim(), baseline, callback_calls: calls, errors, fixture: true }, null, 2))
} finally {
  await browser?.close(); await server?.close()
  rmSync(join(root, entry), { force: true }); rmSync(cacheDir, { recursive: true, force: true })
}
