// Run from frontend/. The Vite dev server serves the real modal, locales and Tailwind styles.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { readFileSync, writeFileSync, rmSync, mkdirSync } from 'node:fs'
import { createRequire } from 'node:module'
import { resolve, join } from 'node:path'
import { pathToFileURL } from 'node:url'

const [mode, out] = process.argv.slice(2)
assert.ok(['before', 'after'].includes(mode) && out)
mkdirSync(out, { recursive: true })
const require = createRequire(resolve('package.json'))
const { createServer } = await import(pathToFileURL(require.resolve('vite').replace(/index\.cjs$/, 'dist/node/index.js')))
// Resolve the browser from an isolated installation, outside the application.
const { chromium } = createRequire(resolve(process.env.PLAYWRIGHT_ROOT, 'package.json'))('playwright')
const vue = require('@vitejs/plugin-vue')
const html = 'batch-whitelist-evidence.html'
const entry = 'batch-whitelist-evidence.js'
execFileSync('git', ['diff', '--quiet', 'HEAD', '--', 'src'])
const sha = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim()
const manifest = { sha, mode, screenshots: [], fixture: 'Real BulkEditAccountModal and CSS. Synthetic admin, selected IDs and Axios adapter.' }
let browser, server
try {
  writeFileSync(html, '<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><div id="app"></div><script type="module" src="./'+entry+'"></script></html>', { flag: 'wx' })
  writeFileSync(entry, `
    import {createApp, h} from 'vue'
    import {createPinia} from 'pinia'
    import Modal from './src/components/account/BulkEditAccountModal.vue'
    import {i18n, loadLocaleMessages} from './src/i18n'
    import {useAuthStore} from './src/stores/auth'
    import {apiClient} from './src/api/client'
    import './src/style.css'
    await loadLocaleMessages('zh')
    i18n.global.locale.value='zh'
    window.calls=[]
    window.requests=[]
    apiClient.defaults.adapter=async config=>{
      const url=config.url
      window.requests.push({method:config.method,url})
      if(config.method !== 'get' && !url.endsWith('/models/sync-upstream')) throw Error('Unexpected fixture write '+url)
      let data={}
      if(url.endsWith('/models/sync-upstream')){
        window.calls.push(url)
        data=url.includes('/42/') && window.calls.length===2 ? {models:[],skipped:'empty'} : {models:['synthetic'],applied:true,changed:true}
      }
      return {data:{code:0,data},status:200,statusText:'OK',headers:{},config}
    }
    const pinia=createPinia()
    useAuthStore(pinia).user={id:9,role:'admin'}
    createApp({render:()=>h(Modal,{show:true,accountIds:[41,42],selectedPlatforms:['anthropic'],selectedTypes:['apikey'],groups:[],proxies:[]})}).use(pinia).use(i18n).mount('#app')
  `, { flag: 'wx' })
  server = await createServer({ configFile: false, plugins: [vue()], cacheDir: join(out, 'vite-cache'),
    resolve: { alias: { '@': resolve('src') } }, server: { host: '127.0.0.1', port: 0 } })
  await server.listen()
  browser = await chromium.launch()
  for (const [name, viewport] of [['desktop',{width:1366,height:900}],['mobile',{width:390,height:844}]]) {
    const page = await browser.newPage({viewport})
    const errors = []
    page.on('pageerror', e => errors.push(e.message))
    await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1' ? route.continue() : route.abort())
    await page.goto(server.resolvedUrls.local[0] + html)
    await page.locator('#bulk-edit-model-restriction-enabled').check()
    const shared = page.getByRole('button', {name:'同步最新支持模型',exact:true})
    const batch = page.getByRole('button', {name:'批量同步上游模型',exact:true})
    assert.equal(await batch.count(), mode === 'after' ? 1 : 0)
    const focus = mode === 'before' ? shared : batch
    await focus.evaluate(el => el.scrollIntoView({block:'center',behavior:'instant'}))
    await page.evaluate(async () => {
      await document.fonts.ready
      await Promise.all(document.getAnimations().filter(a=>a.effect.getTiming().iterations!==Infinity).map(a=>a.finished.catch(()=>{})))
    })
    if (mode === 'after') {
      await batch.click()
      await page.getByRole('button',{name:'仅重试失败账号'}).waitFor()
    }
    const box = await focus.boundingBox()
    assert.ok(box && box.x >= 0 && box.x + box.width <= viewport.width && box.y > 0 && box.y + box.height < viewport.height)
    assert.ok(await focus.evaluate(el => {
      const b=el.getBoundingClientRect()
      return el.contains(document.elementFromPoint(b.x+b.width/2,b.y+b.height/2))
    }))
    assert.deepEqual(errors, [])
    const path = join(out, mode+'-'+name+'.png')
    await page.screenshot({path})
    if(mode === 'after') {
      await page.getByRole('button',{name:'仅重试失败账号'}).click()
      await page.waitForFunction(()=>window.calls.length===3)
      assert.deepEqual(await page.evaluate(()=>window.calls),[41,42,42].map(id=>'/admin/accounts/'+id+'/models/sync-upstream'))
    }
    manifest.screenshots.push({name,viewport,box,sha256:createHash('sha256').update(readFileSync(path)).digest('hex'), requests:await page.evaluate(()=>window.requests)})
    await page.close()
  }
  writeFileSync(join(out, mode+'-manifest.json'), JSON.stringify(manifest,null,2)+'\n')
} finally {
  await browser?.close()
  await server?.close()
  rmSync(entry,{force:true})
  rmSync(html,{force:true})
}
