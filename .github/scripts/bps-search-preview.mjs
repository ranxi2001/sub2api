// Render the actual Vue component in CI with a fictional administrator.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
const [repoArg, outputArg, baseline = ''] = process.argv.slice(2)
const repo = resolve(repoArg), output = resolve(outputArg), root = join(repo, 'frontend')
const require = createRequire(join(root, 'package.json'))
const { createServer } = await import(pathToFileURL(join(dirname(require.resolve('vite/package.json')), 'dist/node/index.js')).href)
const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const entry = '.bps-search-preview.js', cacheDir = mkdtempSync(join(tmpdir(), 'bps-search-preview-'))
let server, browser
mkdirSync(output, { recursive: true })
try {
  writeFileSync(join(root, entry), `
import {mount} from '@vue/test-utils';
import {createPinia,setActivePinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {useAuthStore} from '/src/stores/auth.ts';
import View from '/src/components/admin/operations/BPSDefaultsCard.vue';
import {defaultExcelBPSDefaults} from '/src/utils/excelBPSDefaults.ts';
import '/src/style.css';
await loadLocaleMessages('zh');i18n.global.locale.value='zh';
const pinia=createPinia();setActivePinia(pinia);useAuthStore().user={id:42,role:'admin',username:'demo'};
document.querySelector('#app').innerHTML='<p class="mb-4 text-sm text-gray-500">OAuth 自动配置 → BPS 默认模板 · 实际组件 / 虚构管理员</p><div id="preview"></div>';
window.preview=mount(View,{attachTo:'#preview',props:{groups:[],modelValue:{...defaultExcelBPSDefaults(),all_models:true},'onUpdate:modelValue':value=>window.preview.setProps({modelValue:value})},global:{plugins:[pinia,i18n]}});
`)
  process.chdir(root)
  server = await createServer({ root, configFile: false, cacheDir,
    resolve: { alias: { '@': join(root, 'src') } },
    plugins: [{ name: 'bps-search-fixture', enforce: 'pre', load(id) {
      if (baseline && id === join(root,'src/components/admin/operations/BPSDefaultsCard.vue')) return execFileSync('git',['show',baseline+':frontend/src/components/admin/operations/BPSDefaultsCard.vue'],{cwd:repo,encoding:'utf8'})
    }, configureServer(vite) { vite.middlewares.use((request, response, next) => {
      if (request.url === '/') { response.setHeader('Content-Type','text/html'); response.end('<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app" style="padding:24px;max-width:760px;margin:auto"></div><script type="module" src="/' + entry + '"></script></html>') }
      else if (request.url === '/favicon.ico') { response.statusCode = 204; response.end() }
      else next()
    }) } }, vue()], server: { host: '127.0.0.1', port: 4299, strictPort: true },
    optimizeDeps: { entries: [], include: ['vue','pinia','vue-i18n','axios','@vue/test-utils'] } })
  await server.listen(); browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1100, height: 1000 } }), errors = []
  page.on('pageerror', error => errors.push(error.message))
  await page.route('**/*', route => new URL(route.request().url()).origin === 'http://127.0.0.1:4299' ? route.continue() : route.abort())
  await page.goto('http://127.0.0.1:4299', { waitUntil: 'networkidle' })
  await page.getByTestId('bps-all-models').waitFor()
  if (baseline) {
    if (await page.getByTestId('bps-search-options').count()) throw Error('Baseline unexpectedly includes search options')
    await page.screenshot({path:join(output,'before.png'),fullPage:true})
  } else {
    await page.getByTestId('bps-search-options').waitFor()
    await page.screenshot({ path: join(output,'default-off.png'), fullPage: true })
    await page.getByTestId('excel-bps-web-search').check()
    await page.getByTestId('excel-bps-web-search-auto').check()
    await page.waitForFunction(() => window.preview.props('modelValue').web_search && window.preview.props('modelValue').web_search_auto)
    await page.screenshot({ path: join(output,'search-and-auto.png'), fullPage: true })
    await page.evaluate(() => document.documentElement.classList.add('dark'))
    await page.screenshot({ path: join(output,'dark.png'), fullPage: true })
    await page.setViewportSize({ width: 390, height: 700 })
    await page.screenshot({ path: join(output,'mobile.png'), fullPage: true })
    await page.getByTestId('excel-bps-web-search').uncheck()
    await page.waitForFunction(() => !window.preview.props('modelValue').web_search && !window.preview.props('modelValue').web_search_auto)
    if (await page.getByTestId('excel-bps-web-search-auto').isChecked()) throw Error('Automatic search remained enabled')
    const saved = await page.evaluate(() => window.preview.props('modelValue'))
    if (saved.web_search || saved.web_search_auto) throw Error('Template model retained disabled search options')
  }
  if (errors.length) throw Error(JSON.stringify(errors))
  writeFileSync(join(output,'evidence.json'),JSON.stringify({ source:execFileSync('git',['rev-parse','HEAD'],{cwd:repo,encoding:'utf8'}).trim(),baseline:baseline||null,fixture:true,errors,states:baseline?['before']:['default-off','search-and-auto','dark','mobile'],production:false },null,2))
} finally {
  await browser?.close(); await server?.close()
  rmSync(join(root,entry),{force:true}); rmSync(cacheDir,{recursive:true,force:true})
}
