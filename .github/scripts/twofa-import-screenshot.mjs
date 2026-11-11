// Reuses oauth-initial-ws-screenshot.mjs / issue733-browser.mjs conventions:
// original complete frontend trees, real component/CSS, isolated synthetic APIs.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

assert.equal(process.env.GITHUB_ACTIONS, 'true', 'Dynamic execution is remote GitHub Actions only')
const { SOURCE_BASE, EXPECTED_HEAD, BASELINE_ROOT, PLAYWRIGHT_ROOT, SCREENSHOT_DIR } = process.env
assert(SOURCE_BASE && EXPECTED_HEAD && BASELINE_ROOT && PLAYWRIGHT_ROOT && SCREENSHOT_DIR)
const git = (...args) => execFileSync('git', args, { encoding: 'utf8' }).trim()
assert.equal(git('rev-parse', 'HEAD'), EXPECTED_HEAD)
assert.equal(git('diff', '--exit-code'), '')
mkdirSync(SCREENSHOT_DIR, { recursive: true })
const scratch = mkdtempSync(join(tmpdir(), 'twofa-render-'))
const hash = bytes => createHash('sha256').update(bytes).digest('hex')
const component = 'src/components/account/OpenAITwoFAImport.vue'
const manifest = {
  baseline: SOURCE_BASE, head: EXPECTED_HEAD,
  command: 'node .github/scripts/twofa-import-screenshot.mjs',
  boundary: 'Remote CI only; complete original baseline/head frontend trees, actual OpenAITwoFAImport, CredentialEncryptionSetup, locales and project CSS. Fictional accounts, synthetic in-browser Axios adapter and import/enrollment callback; no backend, real login/OTP/provider/paid/production calls. Parent callback behavior is covered separately by mounted tests. Not full navigation or production acceptance.',
  sources: [], results: [], screenshots: [], errors: [],
}
const { chromium } = await import(pathToFileURL(join(PLAYWRIGHT_ROOT, 'index.mjs')).href)
let browser
try {
  browser = await chromium.launch({ headless: true })
  for (const mode of ['before', 'after']) {
    const root = resolve(mode === 'before' ? BASELINE_ROOT : 'frontend')
    const require = createRequire(join(root, 'package.json'))
    const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
    const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
    const tailwindcss = require('tailwindcss'), autoprefixer = require('autoprefixer')
    const theme = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
    const original = readFileSync(join(root, component))
    const expectedBlob = git('rev-parse', `${mode === 'before' ? SOURCE_BASE : EXPECTED_HEAD}:frontend/${component}`)
    assert.equal(execFileSync('git', ['hash-object', '--stdin'], { input: original, encoding: 'utf8' }).trim(), expectedBlob)
    manifest.sources.push({ mode, commit: mode === 'before' ? SOURCE_BASE : EXPECTED_HEAD, component_blob: expectedBlob,
      component_sha256: hash(original), css_sha256: hash(readFileSync(join(root, 'src/style.css'))),
      tailwind_sha256: hash(readFileSync(join(root, 'tailwind.config.js'))),
    })
    const suffix = `.ci-twofa-${process.pid}`
    const html = join(root, suffix + '.html'), entry = join(root, suffix + '.js')
    const buildDir = join(scratch, mode)
    let server, context
    try {
      writeFileSync(html, `<!doctype html><html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="./${suffix}.js"></script></html>`, { flag: 'wx' })
      writeFileSync(entry, `
        import {createApp,h} from 'vue';import {createI18n} from 'vue-i18n';
        import Component from '/src/components/account/OpenAITwoFAImport.vue';
        import {apiClient} from '/src/api/client.ts';
        import zh from '/src/i18n/locales/zh/index.ts';import en from '/src/i18n/locales/en/index.ts';import '/src/style.css';
        const lang=new URLSearchParams(location.search).get('lang')||'zh';
        const state={starts:[],requests:[],imports:[],enrollments:[],aborted:[],unexpected:[],busy:false};window.__twofa=state;
        const pending=new Map(),failures=new Map();
        apiClient.defaults.adapter=async request=>{
          const path=request.url;const method=request.method;state.requests.push({path,method});let data;
          const payload=typeof request.data==='string'?JSON.parse(request.data):request.data;
          if(path==='/admin/account-ops/token-guard-v2/encryption'&&method==='get')data={configured:true,source:'server_config'};
          else if(path==='/admin/account-ops/token-guard/two-fa-login'&&method==='post'){
            state.starts.push(payload.email);data={id:payload.email,status:'running'};
          }else if(path.startsWith('/admin/account-ops/token-guard/two-fa-login/')&&method==='delete')data={};
          else if(path.startsWith('/admin/account-ops/token-guard/two-fa-login/')&&method==='get'){
            const email=path.split('/').at(-1);
            data=await new Promise((resolve,reject)=>{
              const fail=error=>{request.signal?.removeEventListener('abort',abort);pending.delete(email);failures.delete(email);reject(error)};
              const abort=()=>{state.aborted.push(email);fail(Error('synthetic abort'))};
              failures.set(email,fail);
              pending.set(email,()=>{request.signal?.removeEventListener('abort',abort);pending.delete(email);failures.delete(email);resolve({id:email,status:'succeeded',credential:{synthetic_account:email}})});
              request.signal?.addEventListener('abort',abort,{once:true});if(request.signal?.aborted)abort();
            });
          }else {state.unexpected.push({path,method});throw Error('Unexpected fixture endpoint')}
          return{data:{code:0,message:'success',data},status:200,statusText:'OK',headers:{},config:request};
        };
        state.complete=email=>{if(!pending.has(email))throw Error('No pending synthetic login '+email);pending.get(email)()};
        state.failPending=()=>{for(const fail of [...failures.values()])fail(Error('synthetic polling failure'))};
        const importCredential=async(credential,email,login,signal)=>{
          signal?.throwIfAborted();if(credential.synthetic_account!==email||login.email!==email)throw Error('Synthetic identity mismatch');
          state.imports.push(email);await Promise.resolve();signal?.throwIfAborted();state.enrollments.push(email);return 'created';
        };
        const app=createApp({render:()=>h('main',{class:'mx-auto max-w-3xl p-6 bg-white dark:bg-dark-900 text-gray-900 dark:text-gray-100'},[
          h('h1',{class:'text-xl font-semibold mb-4'},lang==='zh'?'2FA 登录导入':'2FA login and import'),
          h(Component,{importCredential,onBusy:value=>state.busy=value})
        ])});
        app.use(createI18n({legacy:false,locale:lang,messages:{zh,en}})).mount('#app');
      `, { flag: 'wx' })
      const content = [html, entry, join(root, 'src/**/*.{vue,js,ts,jsx,tsx}')]
      await build({ root, configFile: false, cacheDir: join(scratch, mode + '-cache'), plugins: [vue()],
        resolve: { alias: { '@': join(root, 'src') } },
        define: { __INTLIFY_JIT_COMPILATION__: true },
        css: { postcss: { plugins: [tailwindcss({ ...theme, content }), autoprefixer()] } },
        build: { outDir: buildDir, emptyOutDir: true, minify: false, target: 'esnext', rollupOptions: { input: html } },
      })
      manifest.sources.at(-1).tailwind_content = content
      server = await preview({ root, configFile: false, build: { outDir: buildDir }, preview: { host: '127.0.0.1', port: 4196, strictPort: true } })
      context = await browser.newContext({ viewport: { width: 1100, height: 900 }, locale: 'zh-CN', timezoneId: 'UTC' })
      const page = await context.newPage()
      page.on('pageerror', error => manifest.errors.push(`${mode}: ${error.message}`))
      page.on('console', message => { if (message.type() === 'error') manifest.errors.push(`${mode}: ${message.text()}`) })
      await page.route('**/*', route => {
        const url = new URL(route.request().url())
        if (url.origin === 'http://127.0.0.1:4196' || url.protocol === 'data:') return route.continue()
        manifest.errors.push(`Blocked external origin ${url.origin}`)
        return route.abort()
      })
      const emails = ['a', 'b', 'c', 'd', 'e'].map(name => `${name}@example.test`)
      const input = emails.map(email => `${email}----synthetic-password----synthetic-secret`).join('\n')
      const snapshot = () => page.evaluate(() => {
        const { starts, imports, enrollments, aborted, requests, unexpected, busy } = window.__twofa
        return { starts, imports, enrollments, aborted, requests, unexpected, busy }
      })
      const capture = async name => {
        await page.evaluate(async () => { await document.fonts.ready; await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))) })
        const filename = `${mode}-${name}.png`
        await page.locator('main').screenshot({ path: join(SCREENSHOT_DIR, filename), animations: 'disabled' })
        manifest.screenshots.push({ file: filename, sha256: hash(readFileSync(join(SCREENSHOT_DIR, filename))) })
        manifest.results.push({ mode, scenario: name, ...await snapshot() })
      }
      const begin = async lang => {
        await page.goto(`http://127.0.0.1:4196/${suffix}.html?lang=${lang}`)
        await page.getByTestId('two-fa-start').waitFor()
        await page.waitForFunction(() => !document.querySelector('[data-testid="two-fa-start"]').disabled)
        const inputStyle = await page.locator('textarea').evaluate(el => ({ radius: getComputedStyle(el).borderRadius }))
        const buttonStyle = await page.getByTestId('two-fa-start').evaluate(el => ({ radius: getComputedStyle(el).borderRadius, padding: getComputedStyle(el).paddingTop }))
        assert.equal(inputStyle.radius, '12px');assert.equal(buttonStyle.radius, '12px');assert.equal(buttonStyle.padding, '10px')
        await page.locator('textarea').fill(input)
        await page.getByTestId('two-fa-start').click()
        const count = mode === 'before' ? 1 : 3
        await page.waitForFunction(count => window.__twofa.starts.length === count && window.__twofa.requests.filter(r=>r.method==='get'&&r.path.includes('two-fa-login/')).length === count, count)
        assert.deepEqual((await snapshot()).starts, emails.slice(0, count))
        assert.deepEqual(await page.locator('li > span:first-child').allTextContents(), emails.map((email, i) => `${i + 1}. ${email}`))
        const styles = await page.locator('li').first().evaluate(el => ({ display: getComputedStyle(el).display, gap: getComputedStyle(el).columnGap, font: getComputedStyle(el).fontSize }))
        assert.deepEqual(styles, { display: 'flex', gap: '16px', font: '14px' })
        manifest.results.push({ mode, scenario: `${lang}-computed-css`, styles, inputStyle, buttonStyle })
      }
      await begin('zh')
      await capture('zh-desktop-active')
      if (mode === 'after') {
        await page.getByTestId('two-fa-stop').click()
        await page.waitForFunction(() => !window.__twofa.busy)
        assert.equal((await snapshot()).starts.length, 3)
        assert.deepEqual((await snapshot()).imports, [])
        assert.equal((await snapshot()).aborted.length, 3)
        await capture('zh-desktop-stopped')
        await begin('zh')
        await page.evaluate(() => window.__twofa.failPending())
        await page.getByTestId('two-fa-owned-jobs').waitFor()
        assert.equal((await snapshot()).busy, true, 'Parent Close remains fenced while the attempt is paused')
        assert.equal(await page.getByTestId('two-fa-start').isEnabled(), true)
        assert.equal(await page.getByTestId('two-fa-stop').isEnabled(), true)
        assert.equal((await snapshot()).starts.length, 3)
        await capture('zh-desktop-unresolved-tasks')
        await page.getByTestId('two-fa-stop').click()
        await page.waitForFunction(() => !window.__twofa.busy)
        await begin('zh')
        for (const email of [emails[1], emails[0], emails[2], emails[4], emails[3]]) {
          await page.waitForFunction(email => window.__twofa.starts.includes(email), email)
          await page.evaluate(email => window.__twofa.complete(email), email)
          await page.waitForFunction(email => window.__twofa.enrollments.includes(email), email)
        }
        await page.waitForFunction(() => !window.__twofa.busy)
        assert.deepEqual((await snapshot()).imports, (await snapshot()).enrollments)
        assert.equal((await snapshot()).enrollments.length, 5)
        assert.deepEqual(await page.locator('li > span:first-child').allTextContents(), emails.map((email, i) => `${i + 1}. ${email}`))
        await capture('zh-desktop-completed')
      }
      await page.setViewportSize({ width: 390, height: 844 })
      await begin('en')
      await page.evaluate(() => document.documentElement.classList.add('dark'))
      await capture('en-mobile-dark-active')
      assert.deepEqual((await snapshot()).unexpected, [])
    } finally {
      await context?.close()
      await server?.close()
      rmSync(html, { force: true });rmSync(entry, { force: true })
    }
  }
  assert.deepEqual(manifest.errors, [])
  console.log('Actual 2FA component/CSS before-after screenshots and computed styles passed')
} finally {
  await browser?.close()
  rmSync(scratch, { recursive: true, force: true })
  writeFileSync(join(SCREENSHOT_DIR, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n')
  writeFileSync(join(SCREENSHOT_DIR, 'SHA256SUMS'), manifest.screenshots.map(shot => `${shot.sha256}  ${shot.file}`).join('\n') + '\n')
}
