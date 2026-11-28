// Remote CI only: real SettingsView/layout/CSS from complete, immutable revisions.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { createRequire } from 'node:module'
import { mkdirSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve, sep } from 'node:path'
import { pathToFileURL } from 'node:url'

assert.equal(process.env.CI, 'true', 'Build/browser execution belongs in remote CI')
const git = (...args) => execFileSync('git', args, { encoding: 'utf8' }).trim()
const sha256 = value => createHash('sha256').update(value).digest('hex')
const baseline = '76a2e0f308219781833f560e5b642368cd106ad8'
const candidate = process.env.CANDIDATE_SHA
assert.match(candidate || '', /^[0-9a-f]{40}$/)
assert.equal(git('rev-parse', 'HEAD'), candidate)
assert.equal(git('status', '--porcelain'), '')
const temporary = resolve(process.env.RUNNER_TEMP)
const output = resolve(process.env.SCREENSHOT_DIR)
assert(output.startsWith(temporary + sep), 'Artifacts must be outside source')
assert(process.env.PLAYWRIGHT_ROOT)
mkdirSync(output, { recursive: true })
const source = readFileSync('frontend/src/views/admin/__tests__/SettingsView.spec.ts', 'utf8')
const start = source.indexOf('const baseSettingsResponse = {')
const end = source.indexOf('\n};', start) + 3
assert(start >= 0 && end > start, 'Missing fictional settings fixture')
const fixture = source.slice(start, end).replaceAll('sub3.nebula-spaces.com', 'fixture.example.invalid')
const entry = `
import {createApp} from 'vue';import {createPinia} from 'pinia';
import {createRouter,createWebHistory,RouterView} from 'vue-router';
import {i18n,initI18n} from '/src/i18n/index.ts';
import {apiClient} from '/src/api/client.ts';import {useAuthStore} from '/src/stores/auth.ts';
import {useAppStore} from '/src/stores/app.ts';import {NEW_FEATURE_NOTICES} from '/src/constants/newFeatureNotices.ts';
import {useNewFeatureNoticeStore} from '/src/stores/newFeatureNotices.ts';
import View from '/src/views/admin/SettingsView.vue';import '/src/style.css';
${fixture}
const mode=new URL(location.href).searchParams.get('mode');
const storageKey='ci-resend-public-settings:'+mode;
const initial={...baseSettingsResponse,email_verify_enabled:true,email_provider:'smtp',resend_api_key_configured:false,
 smtp_host:'smtp.example.invalid',smtp_from_email:'mail@example.invalid',smtp_from_name:'Fixture mail',
 subscription_enabled:false,site_logo:'/logo.svg'};
const state={settings:JSON.parse(localStorage.getItem(storageKey)||'null')||initial,reads:[],writes:[],testmail:[],unexpected:[],requests:[],
 oldNotice:NEW_FEATURE_NOTICES.protocolFeatureSwitches,newNotice:NEW_FEATURE_NOTICES.resendEmailProvider};
window.__resendEvidence=state;
const auxiliary={
 '/admin/settings/overload-cooldown':{enabled:true,cooldown_minutes:10},
 '/admin/settings/rate-limit-429-cooldown':{enabled:true,cooldown_seconds:5},
 '/admin/settings/panel-rate-limit':{enabled:true,user_rpm:240,heavy_rpm:60,exempt_admin:true,public_ip_rpm:300},
 '/admin/settings/stream-timeout':{enabled:true,action:'temp_unsched',temp_unsched_minutes:5,threshold_count:3,threshold_window_minutes:10},
 '/admin/settings/openai-response-header-timeout':{enabled:false,seconds:20},
 '/admin/settings/account-performance':{window_minutes:60},
 '/admin/settings/rectifier':{enabled:true,thinking_signature_enabled:true,thinking_budget_enabled:true,apikey_signature_enabled:false,apikey_signature_patterns:[]},
 '/admin/settings/beta-policy':{rules:[]},
 '/admin/settings/web-search-emulation':{enabled:false,providers:[]},
 '/admin/settings/admin-api-key':{exists:false,masked_key:''},
 '/admin/settings/admin-api-keys':{items:[],permissions:[]},
 '/admin/settings/email-templates':{events:[],locales:[],placeholders:[]},
 '/admin/accounts/upstream-billing-probe/settings':{enabled:false,interval_minutes:30},
 '/admin/accounts/ollama-cloud-usage/settings':{enabled:false,interval_minutes:60,debounce_minutes:1},
 '/admin/accounts/opencode-go-usage/settings':{enabled:false,interval_minutes:60,debounce_minutes:1},
 '/admin/payment/config':{enabled:false},'/admin/payment/providers':[],
 '/admin/groups/all':[],'/admin/proxies':{items:[],total:0,pages:1},'/keys':{items:[],total:0,pages:1},
 '/announcements':[],'/subscriptions/active':[],
 '/admin/system/check-updates':{current_version:'fixture',latest_version:'fixture',has_update:false,build_type:'source'},
 '/admin/backups/s3-config':{endpoint:'',region:'',bucket:'',access_key_id:'',prefix:'',force_path_style:false},
 '/admin/backups/image-storage':{config:{enabled:false,reuse_backup_s3:true,bucket:'',prefix:'',public_base_url:'',presign_expiry_hours:24,max_download_bytes:1048576,endpoint:'',region:'',access_key_id:'',force_path_style:false},secret_configured:false},
 '/admin/backups/schedule':{enabled:false,cron_expr:'0 3 * * *',retain_days:7,retain_count:7,monthly_archive:{enabled:false,days:[],include_month_end:false,retain_count:0}},
 '/admin/backups':{items:[]},
 '/admin/serverless':{config:{enabled:false,pods:[],regions:[]},pods:[],stats:{}}
};
apiClient.defaults.adapter=async request=>{
 const path=request.url.split('?')[0],method=request.method||'get';
 state.requests.push({path,method});let data;
 const payload=()=>typeof request.data==='string'?JSON.parse(request.data):structuredClone(request.data);
 if(path==='/admin/settings'&&(method==='get'||method==='put')){
  if(method==='put'){
   const body=payload(),hasKey=Object.hasOwn(body,'resend_api_key');
   if(body.email_provider!=='resend'||(hasKey&&body.resend_api_key!=='re_fictional_first'))throw Error('Incorrect provider/key save');
   state.writes.push({provider:body.email_provider,key_supplied:hasKey,key_matches_fixture:!hasKey||body.resend_api_key==='re_fictional_first'});
   const configured=state.settings.resend_api_key_configured||hasKey;
   delete body.resend_api_key;delete body.smtp_password;
   state.settings={...state.settings,...body,resend_api_key_configured:configured};
   localStorage.setItem(storageKey,JSON.stringify(state.settings));
  }
  data=structuredClone(state.settings);state.reads.push(structuredClone(data));
 }else if(path==='/settings/public'&&method==='get')data=structuredClone(initial);
 else if(path==='/admin/settings/send-test-email'&&method==='post'){
  const body=payload(),hasKey=Object.hasOwn(body,'resend_api_key');
  if(body.email_provider!=='resend'||body.email!=='recipient@example.invalid'||(hasKey&&body.resend_api_key!=='re_fictional_replacement'))throw Error('Incorrect test mail payload');
  state.testmail.push({provider:body.email_provider,key_source:hasKey?'replacement':'saved',recipient:body.email});
  data={message:'Synthetic mail accepted; no delivery performed'};
 }else if(path==='/admin/settings/web-search-emulation'&&method==='put')data=auxiliary[path];
 else if(method==='get'&&Object.hasOwn(auxiliary,path))data=structuredClone(auxiliary[path]);
 else{state.unexpected.push({path,method});throw Error('Unexpected fixture endpoint '+method+' '+path)}
 if(Object.hasOwn(data||{},'resend_api_key'))throw Error('Fixture response echoed a key');
 return{data:{code:0,message:'success',data},status:200,statusText:'OK',headers:{},config:request};
};
const pinia=createPinia(),router=createRouter({history:createWebHistory(),routes:[{path:'/:pathMatch(.*)*',component:View}]});
const app=createApp(RouterView);app.use(pinia);
window.__resendNoticeState=()=>{
 const store=useNewFeatureNoticeStore(pinia);
 return{old:store.isDismissed(41,NEW_FEATURE_NOTICES.protocolFeatureSwitches),
  current:store.isDismissed(41,NEW_FEATURE_NOTICES.resendEmailProvider),future:store.isDismissed(41,'fixture-future-mail-provider-C')};
};
useAuthStore(pinia).user={id:41,username:'fixture-admin',role:'admin',status:'active',email:'admin@example.invalid',balance:0,frozen_balance:0};
await initI18n();await useAppStore(pinia).fetchPublicSettings();app.use(router).use(i18n);
await router.isReady();app.mount('#app');state.ready=true;
`
const manifest = {
  baseline_sha: baseline, candidate_sha: candidate, sources: {}, scenarios: [], screenshots: [], errors: [], passed: false,
  fixture_sha256: sha256(fixture), entry_sha256: sha256(entry), harness_sha256: sha256(readFileSync('.github/scripts/resend-settings-evidence.mjs')),
  github: { run_id: process.env.GITHUB_RUN_ID, run_attempt: process.env.GITHUB_RUN_ATTEMPT, job: process.env.GITHUB_JOB },
  boundary: 'Complete archived baseline/candidate trees; real SettingsView, AppLayout, Sidebar, Header, controls, locales and full CSS/Tailwind. Synthetic admin and strict Axios fixtures with public-only localStorage persistence. No component/layout/style stubs, force clicks, provider calls, real delivery, backend persistence or production login. Screenshots require independent parent inspection and native PR attachments.',
}
const origin = 'http://127.0.0.1:4198'
const paths = ['package.json', 'pnpm-lock.yaml', 'tailwind.config.js', 'postcss.config.js', 'src/style.css',
  'src/views/admin/SettingsView.vue', 'src/components/layout/AppLayout.vue', 'src/components/layout/AppSidebar.vue',
  'src/components/layout/AppHeader.vue', 'src/components/common/NewFeatureBadge.vue', 'src/stores/newFeatureNotices.ts',
  'src/constants/newFeatureNotices.ts', 'src/api/admin/settings.ts', 'src/i18n/locales/zh/admin/settings.ts', 'src/i18n/locales/en/admin/settings.ts']
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
let browser, activePage
const archives = []
try {
  browser = await chromium.launch({ headless: true })
  manifest.browser_version = browser.version()
  for (const mode of ['before', 'after']) {
    const revision = mode === 'before' ? baseline : candidate
    const tree = join(temporary, `resend-${mode}-${candidate}`)
    archives.push(tree)
    mkdirSync(tree)
    const archive = join(tree, 'source.tar')
    execFileSync('git', ['archive', '--format=tar', `--output=${archive}`, revision])
    execFileSync('tar', ['-xf', archive, '-C', tree])
    const root = join(tree, 'frontend')
    for (const path of ['package.json', 'pnpm-lock.yaml']) assert(readFileSync(join(root, path)).equals(readFileSync(join('frontend', path))), 'Dependency identity: ' + path)
    symlinkSync(resolve('frontend/node_modules'), join(root, 'node_modules'), 'dir')
    const identity = manifest.sources[mode] = { revision, tree: git('rev-parse', `${revision}^{tree}`), files: {}, css: [] }
    for (const path of paths) {
      const file = join(root, path), blob = git('rev-parse', `${revision}:frontend/${path}`)
      assert.equal(git('hash-object', file), blob, mode + ' source identity: ' + path)
      identity.files[path] = { blob, sha256: sha256(readFileSync(file)) }
    }
    const require = createRequire(join(root, 'package.json'))
    const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
    const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
    const theme = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
    const html = join(root, '.ci-resend.html'), module = join(root, '.ci-resend.mjs'), buildDir = join(tree, 'rendered')
    writeFileSync(html, '<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"></head><body><div id="app"></div><script type="module" src="./.ci-resend.mjs"></script></body></html>', { flag: 'wx' })
    writeFileSync(module, entry, { flag: 'wx' })
    await build({ root, configFile: false, cacheDir: join(tree, 'cache'), resolve: { alias: { '@': join(root, 'src') } }, plugins: [vue()],
      css: { postcss: { plugins: [require('tailwindcss')({ ...theme, content: [html, module, join(root, 'src/**/*.{vue,js,ts}')] }), require('autoprefixer')()] } },
      build: { target: 'esnext', outDir: buildDir, emptyOutDir: true, minify: false, rollupOptions: { input: html } },
    })
    mkdirSync(join(output, 'css', mode), { recursive: true })
    for (const file of readdirSync(join(buildDir, 'assets')).filter(file => file.endsWith('.css'))) {
      const css = readFileSync(join(buildDir, 'assets', file))
      writeFileSync(join(output, 'css', mode, file), css)
      identity.css.push({ file, sha256: sha256(css), artifact_path: `css/${mode}/${file}` })
    }
    assert(identity.css.length > 0, 'Missing compiled repository CSS')
    const server = await preview({ root, configFile: false, build: { outDir: buildDir }, preview: { host: '127.0.0.1', port: 4198, strictPort: true } })
    try {
      for (const locale of ['zh', 'en']) {
        const context = await browser.newContext({ viewport: { width: 1440, height: 1080 }, locale: locale === 'zh' ? 'zh-CN' : 'en-US', timezoneId: 'UTC', serviceWorkers: 'block' })
        await context.addInitScript(lang => {
          localStorage.setItem('sub2api_locale', lang)
          localStorage.setItem('admin_guide_41_admin_v4_interactive', 'true')
          const key = 'sub2api:new-feature-notices:v1:41'
          if (!localStorage.getItem(key)) localStorage.setItem(key, JSON.stringify(['protocol-feature-switches-initial-announcement']))
        }, locale)
        await context.route('**/*', route => {
          const url = new URL(route.request().url())
          if ((url.origin === origin && route.request().method() === 'GET' && !url.pathname.startsWith('/api/')) || url.protocol === 'data:') return route.continue()
          manifest.errors.push('Blocked browser request ' + route.request().method() + ' ' + url.origin + url.pathname)
          return route.abort()
        })
        const page = await context.newPage()
        activePage = page
        page.setDefaultTimeout(20000)
        page.on('pageerror', error => manifest.errors.push(`${mode}/${locale}: ${error.message}`))
        page.on('console', message => { if (message.type() === 'error') manifest.errors.push(`${mode}/${locale}: ${message.text()}`) })
        page.on('response', response => { if (response.status() >= 400) manifest.errors.push(`${mode}/${locale}: HTTP ${response.status()} ${response.url()}`) })
        const card = () => page.locator('.card').filter({ has: page.locator('#settings-section-smtp') })
        const testCard = () => page.locator('.card').filter({ has: page.locator('#settings-section-test-email') })
        const wait = async () => {
          await page.waitForFunction(() => window.__resendEvidence?.ready && window.__resendEvidence.reads.length > 0)
          await page.locator('#settings-tab-email').waitFor({ state: 'visible' })
        }
        const snapshot = async scenario => {
          const state = await page.evaluate(() => {
            const { settings, reads, ...state } = window.__resendEvidence
            return { ...state, public_response_has_key: reads.some(read => Object.hasOwn(read, 'resend_api_key')), configured: settings.resend_api_key_configured }
          })
          assert.deepEqual(state.unexpected, [])
          assert.equal(state.public_response_has_key, false)
          const notices = await page.evaluate(() => window.__resendNoticeState())
          manifest.scenarios.push({ mode, locale, scenario, assertions_passed: true, state, notices })
        }
        const capture = async (scenario, focus) => {
          if (focus) await focus.scrollIntoViewIfNeeded()
          await page.evaluate(async () => { await document.fonts.ready; await new Promise(done => requestAnimationFrame(() => requestAnimationFrame(done))) })
          const file = `${mode}-${locale}-${scenario}.png`
          await page.screenshot({ path: join(output, file), animations: 'disabled' })
          manifest.screenshots.push({ file, scenario, mode, locale, revision, sha256: sha256(readFileSync(join(output, file))) })
          await snapshot(scenario)
        }
        await page.goto(`${origin}/.ci-resend.html?mode=${mode}&tab=general`)
        await wait()
        assert.equal(await card().isVisible(), false, 'Email panel starts closed on site tab')
        await capture('closed')
        await page.locator('#settings-tab-email').click()
        await card().waitFor({ state: 'visible' })
        await capture('smtp-open', card())
        if (mode === 'before') {
          assert.equal(await page.getByTestId('email-provider-select').count(), 0)
          assert.equal(await page.getByTestId('resend-api-key').count(), 0)
        } else {
          assert.deepEqual(await page.evaluate(() => window.__resendNoticeState()), { old: true, current: false, future: false })
          const provider = page.getByTestId('email-provider-select'), key = page.getByTestId('resend-api-key')
          assert.equal(await provider.inputValue(), 'smtp')
          await provider.selectOption('resend')
          await key.waitFor({ state: 'visible' })
          assert.equal(await key.getAttribute('type'), 'password')
          const layout = await provider.evaluate(el => ({ width: el.getBoundingClientRect().width, padding: getComputedStyle(el).paddingLeft }))
          assert(layout.width > 100 && parseFloat(layout.padding) > 0, 'Full input CSS is rendered')
          await capture('resend-selected', card())
          await key.fill('re_fictional_first')
          await page.locator('.settings-save-dock button[type="submit"]').click()
          await page.waitForFunction(() => window.__resendEvidence.writes.length === 1 && document.querySelector('[data-testid="resend-api-key"]')?.value === '')
          assert.equal(await page.evaluate(() => window.__resendEvidence.settings.resend_api_key_configured), true)
          await capture('saved-key-cleared', card())
          await page.reload(); await wait(); await key.waitFor({ state: 'visible' })
          assert.equal(await provider.inputValue(), 'resend')
          assert.equal(await key.inputValue(), '')
          assert.equal(await key.getAttribute('placeholder'), '********')
          await capture('reloaded-configured-only', card())
          await page.locator('.settings-save-dock button[type="submit"]').click()
          await page.waitForFunction(() => window.__resendEvidence.writes.length === 1)
          assert.equal(await page.evaluate(() => window.__resendEvidence.writes[0].key_supplied), false)
          await testCard().locator('input[type="email"]').fill('recipient@example.invalid')
          await testCard().getByRole('button').click()
          await page.waitForFunction(() => window.__resendEvidence.testmail.length === 1)
          await key.fill('re_fictional_replacement')
          await testCard().getByRole('button').click()
          await page.waitForFunction(() => window.__resendEvidence.testmail.length === 2)
          assert.deepEqual(await page.evaluate(() => window.__resendEvidence.testmail.map(mail => mail.key_source)), ['saved', 'replacement'])
          await key.fill('')
          await capture('saved-and-replacement-testmail', testCard())
          const tabBadge = page.locator('#settings-tab-email + [data-testid="new-feature-badge"]')
          assert.equal(await tabBadge.count(), 1)
          assert.equal(await card().getByTestId('new-feature-badge').count(), 1)
          await (locale === 'zh' ? card().getByTestId('new-feature-badge') : tabBadge).click()
          assert.equal(await tabBadge.count(), 0)
          assert.equal(await card().getByTestId('new-feature-badge').count(), 0)
          assert.deepEqual(await page.evaluate(() => window.__resendNoticeState()), { old: true, current: true, future: false })
          await page.reload(); await wait(); await key.waitFor({ state: 'visible' })
          assert.equal(await tabBadge.count(), 0)
          assert.equal(await card().getByTestId('new-feature-badge').count(), 0)
          await capture('badge-dismissal-reloaded', card())
          await page.locator('#settings-tab-general').click()
          assert.equal(await card().isVisible(), false)
          await page.locator('#settings-tab-email').click()
          await card().waitFor({ state: 'visible' })
        }
        await context.close(); activePage = null
      }
    } finally {
      await new Promise((done, reject) => server.httpServer.close(error => error ? reject(error) : done()))
    }
  }
  assert.deepEqual(manifest.errors, [])
  manifest.passed = true
} catch (error) {
  manifest.errors.push(String(error.stack || error))
  if (activePage) {
    writeFileSync(join(output, 'failure.txt'), await activePage.locator('body').innerText().catch(() => 'Unavailable'))
    await activePage.screenshot({ path: join(output, 'failure.png'), animations: 'disabled' }).catch(() => {})
  }
  throw error
} finally {
  writeFileSync(join(output, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n')
  if (browser) await browser.close()
  for (const tree of archives) rmSync(tree, { recursive: true, force: true })
}
