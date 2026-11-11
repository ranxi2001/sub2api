// CI-only actual component/CSS capture; all API data is fictional.
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
const [repoArg, outputArg] = process.argv.slice(2)
const repo = resolve(repoArg), root = join(repo, 'frontend'), output = resolve(outputArg)
const req = createRequire(join(root, 'package.json'))
const { createServer } = await import(pathToFileURL(join(dirname(req.resolve('vite/package.json')), 'dist/node/index.js')).href)
const vue = (await import(pathToFileURL(req.resolve('@vitejs/plugin-vue')).href)).default
const { chromium } = await import(pathToFileURL(process.env.SUB2API_PLAYWRIGHT_MODULE).href)
const git = (...args) => execFileSync('git', args, { cwd: repo, encoding: 'utf8' }).trimEnd()
const baseline = '87d01c39e', head = git('rev-parse', 'HEAD')
const cacheDir = mkdtempSync(join(tmpdir(), 'vip-history-'))
const entryName = `.vip-history-${process.pid}.js`, entry = join(root, entryName)
const origin = 'http://127.0.0.1:4299', errors = [], captures = []
let before = true, server, browser
mkdirSync(output, { recursive: true })
try {
  writeFileSync(entry, `
import { createApp, h, ref, onMounted } from 'vue';
import { createPinia } from 'pinia';
import { createI18n } from 'vue-i18n';
import { apiClient } from '/src/api/client.ts';
import Modal from '/src/components/admin/user/UserBalanceHistoryModal.vue';
import zh from '/src/i18n/locales/zh/index.ts';
import en from '/src/i18n/locales/en/index.ts';
import '/src/style.css';
const before = new URLSearchParams(location.search).has('before');
const credit = (id, status) => ({id,type:'auto_credit',value:1000,code:'VIP-'+id,notes:'自动充值需索取费用',created_at:'2026-11-11T12:00:00Z',auto_credit:{ledger_id:id,amount:'1000.00000000',balance_before:'999.00000000',balance_after:'1999.00000000',status,settled_at:status==='settled'?'2026-11-11T14:00:00Z':null,settled_by:status==='settled'?9:null}});
const items = [{id:1,type:'admin_balance',value:8000,code:'ADMIN-1',notes:'充值 10000，抵扣 2000 垫款',created_at:'2026-11-11T15:00:00Z'},...(!before?[credit(2,'pending'),credit(1,'settled')]:[])];
apiClient.defaults.adapter=async config=>{
if(config.method!=='get'||config.url!=='/admin/users/7/balance-history')throw Error('Unexpected API '+config.url);
window.lastFilter=config.params?.type;
const selected=config.params?.type?items.filter(i=>i.type===config.params.type):items;
return {config,status:200,statusText:'OK',headers:{},data:{code:0,data:{items:selected,total:selected.length,total_recharged:8000}}};
};
const lang=new URLSearchParams(location.search).get('lang')||'zh';
createApp({setup(){const show=ref(false);onMounted(()=>{show.value=true});return ()=>h(Modal,{show:show.value,user:{id:7,email:'vip@example.invalid',username:'VIP 示例',balance:9000,created_at:'2026-11-01T00:00:00Z'},onClose:()=>{show.value=false}})}}).use(createPinia()).use(createI18n({legacy:false,locale:lang,messages:{zh,en}})).mount('#app');
`, { flag: 'wx' })
  process.chdir(root)
  server = await createServer({ root, configFile: false, cacheDir,
    resolve: { alias: { '@': join(root, 'src') } },
    plugins: [{ name: 'vip-history-baseline', enforce: 'pre', load(id) {
      const path = 'frontend/src/components/admin/user/UserBalanceHistoryModal.vue'
      if (before && id === join(repo, path)) return git('show', baseline + ':' + path)
    }, configureServer(vite) { vite.middlewares.use((request, response, next) => {
      if (request.url === '/' || request.url?.startsWith('/?')) {
        response.setHeader('Content-Type', 'text/html')
        response.end('<!doctype html><html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="/' + entryName + '"></script></html>')
      } else if (request.url === '/favicon.ico') { response.statusCode = 204; response.end() } else next()
    }) } }, vue()],
    server: { host: '127.0.0.1', port: 4299, strictPort: true },
    optimizeDeps: { entries: [], include: ['vue', 'pinia', 'vue-i18n', 'vue-router', 'axios'] }
  })
  await server.listen()
  browser = await chromium.launch({ headless: true })
  for (const lang of ['zh', 'en']) {
    const page = await browser.newPage({ viewport: { width: 1280, height: 1000 }, timezoneId: 'UTC' })
    page.on('pageerror', e => errors.push(e.message))
    await page.route('**/*', route => {
      if (new URL(route.request().url()).origin === origin) return route.continue()
      errors.push('Blocked external request'); return route.abort()
    })
    for (const stage of ['before', 'after']) {
      before = stage === 'before'; server.moduleGraph.invalidateAll()
      await page.goto(origin + '/?lang=' + lang + (before ? '&before=1' : ''), { waitUntil: 'networkidle' })
      await page.getByText('+$8000.00', { exact: true }).waitFor()
      if (!before) {
        if (await page.getByText('+$1000.00', { exact: true }).count() !== 2) throw Error('Missing two-decimal credit amounts')
        if (await page.getByText((lang === 'zh' ? '余额：' : 'Balance: ') + '$999.00 → $1999.00', { exact: true }).count() !== 2) throw Error('Missing two-decimal balances')
        await page.getByText('#2 · ' + (lang === 'zh' ? '待收款' : 'Uncollected'), { exact: true }).waitFor()
      }
      const file = lang + '-' + stage + '.png'
      await page.screenshot({ path: join(output, file), fullPage: true })
      captures.push({ file, source: before ? git('rev-parse', baseline) : head, lang })
    }
    await page.getByRole('button', { name: 'Select option', exact: true }).click()
    await page.getByRole('option', { name: lang === 'zh' ? 'VIP 自动垫付' : 'VIP auto credit', exact: true }).click()
    await page.waitForFunction(() => window.lastFilter === 'auto_credit')
    if (await page.getByText('+$8000.00', { exact: true }).count()) throw Error('Filter retained admin recharge')
    await page.screenshot({ path: join(output, lang + '-filtered.png'), fullPage: true })
    await page.close()
  }
  if (errors.length) throw Error(errors.join('\n'))
  writeFileSync(join(output, 'evidence.json'), JSON.stringify({ head, baseline, captures, api: 'synthetic, no production access' }, null, 2))
} finally {
  await browser?.close(); await server?.close()
  rmSync(entry, { force: true }); rmSync(cacheDir, { recursive: true, force: true })
}
