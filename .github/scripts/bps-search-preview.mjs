// Render the actual Vue component in CI with a fictional administrator.
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
const entry = '.bps-search-preview.js', cacheDir = mkdtempSync(join(tmpdir(), 'bps-search-preview-'))
let server, browser
mkdirSync(output, { recursive: true })
try {
  writeFileSync(join(root, entry), `
import {mount} from '@vue/test-utils';
import {createPinia,setActivePinia} from 'pinia';
import {i18n,loadLocaleMessages} from '/src/i18n/index.ts';
import {useAuthStore} from '/src/stores/auth.ts';
import View from '/src/components/account/ExcelBPSSearchOptions.vue';
import '/src/style.css';
await loadLocaleMessages('zh');i18n.global.locale.value='zh';
const pinia=createPinia();setActivePinia(pinia);useAuthStore().user={id:42,role:'admin',username:'demo'};
document.querySelector('#app').innerHTML='<p class="mb-4 text-sm text-gray-500">账号编辑 → BPS 搜索 · 实际 Vue 组件 / 虚构管理员</p><div id="preview"></div>';
window.preview=mount(View,{attachTo:'#preview',props:{enabled:false,automatic:false,'onUpdate:enabled':value=>window.preview.setProps({enabled:value}),'onUpdate:automatic':value=>window.preview.setProps({automatic:value})},global:{plugins:[pinia,i18n]}});
`)
  process.chdir(root)
  server = await createServer({ root, configFile: false, cacheDir,
    resolve: { alias: { '@': join(root, 'src') } },
    plugins: [{ name: 'bps-search-fixture', configureServer(vite) { vite.middlewares.use((request, response, next) => {
      if (request.url === '/') { response.setHeader('Content-Type','text/html'); response.end('<!doctype html><html lang="zh"><meta charset="utf-8"><div id="app" style="padding:24px;max-width:760px;margin:auto"></div><script type="module" src="/' + entry + '"></script></html>') }
      else if (request.url === '/favicon.ico') { response.statusCode = 204; response.end() }
      else next()
    }) } }, vue()], server: { host: '127.0.0.1', port: 4299, strictPort: true },
    optimizeDeps: { entries: [], include: ['vue','pinia','vue-i18n','axios','@vue/test-utils'] } })
  await server.listen(); browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 900, height: 620 } }), errors = []
  page.on('pageerror', error => errors.push(error.message))
  await page.route('**/*', route => new URL(route.request().url()).origin === 'http://127.0.0.1:4299' ? route.continue() : route.abort())
  await page.goto('http://127.0.0.1:4299', { waitUntil: 'networkidle' })
  await page.getByTestId('bps-search-options').waitFor()
  await page.screenshot({ path: join(output,'default-off.png'), fullPage: true })
  await page.getByTestId('excel-bps-web-search').check()
  await page.getByTestId('excel-bps-web-search-auto').check()
  await page.screenshot({ path: join(output,'search-and-auto.png'), fullPage: true })
  await page.evaluate(() => document.documentElement.classList.add('dark'))
  await page.screenshot({ path: join(output,'dark.png'), fullPage: true })
  await page.setViewportSize({ width: 390, height: 700 })
  await page.screenshot({ path: join(output,'mobile.png'), fullPage: true })
  await page.getByTestId('excel-bps-web-search').uncheck()
  if (await page.getByTestId('excel-bps-web-search-auto').isChecked()) throw Error('Automatic search remained enabled')
  if (errors.length) throw Error(JSON.stringify(errors))
  writeFileSync(join(output,'evidence.json'),JSON.stringify({ source:execFileSync('git',['rev-parse','HEAD'],{cwd:repo,encoding:'utf8'}).trim(),fixture:true,errors,states:['default-off','search-and-auto','dark','mobile'],production:false },null,2))
} finally {
  await browser?.close(); await server?.close()
  rmSync(join(root,entry),{force:true}); rmSync(cacheDir,{recursive:true,force:true})
}
