// CI-only preview of the actual new component, project CSS and localStorage notices.
// This does not exercise full Settings navigation or a real admin API.
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
const root = resolve('frontend')
const output = process.env.SCREENSHOT_DIR
if (!output || !process.env.PLAYWRIGHT_ROOT) throw Error('Missing isolated screenshot paths')
mkdirSync(output, { recursive: true })
const require = createRequire(join(root, 'package.json'))
const { build, preview } = await import(pathToFileURL(join(root, 'node_modules/vite/dist/node/index.js')).href)
const vue = (await import(pathToFileURL(require.resolve('@vitejs/plugin-vue')).href)).default
const tailwindcss = require('tailwindcss'), autoprefixer = require('autoprefixer')
const tailwindConfig = (await import(pathToFileURL(join(root, 'tailwind.config.js')).href)).default
const { chromium } = await import(pathToFileURL(join(process.env.PLAYWRIGHT_ROOT, 'index.mjs')).href)
const suffix = `.ci-forecast-${process.pid}`
const entry = join(root, suffix + '.js'), html = join(root, suffix + '.html')
const previewDir = join(output, 'preview-build'), errors = [], screenshots = []
let browser, server
try {
  writeFileSync(entry, `
    import { createApp, h, ref } from 'vue';
    import { createPinia } from 'pinia';
    import { createI18n } from 'vue-i18n';
    import zh from '/src/i18n/locales/zh/index.ts';
    import en from '/src/i18n/locales/en/index.ts';
    import Panel from '/src/components/settings/BalanceForecastSettings.vue';
    import Badge from '/src/components/common/NewFeatureBadge.vue';
    import { NEW_FEATURE_NOTICES } from '/src/constants/newFeatureNotices.ts';
    import '/src/style.css';
    const locale=new URLSearchParams(location.search).get('lang') || 'zh';
    createApp({setup(){
      const enabled=ref(false),hours=ref(12),cap=ref(3);
      return()=>h('main',{class:'min-h-screen bg-gray-50 p-4 text-gray-900 dark:bg-dark-900 dark:text-gray-100'},[
        h('div',{class:'mx-auto max-w-3xl space-y-4'},[
          h('h1',{class:'text-xl font-semibold'},'Settings / 邮件'),
          h('p','CI component preview · fictional data · no production/API delivery'),
          h('nav',[h('span','邮件 / Email '),h(Badge,{noticeId:NEW_FEATURE_NOTICES.balanceForecast,userId:41})]),
          h(Panel,{enabled:enabled.value,thresholdHours:hours.value,maxNotifications:cap.value,userId:41,
            'onUpdate:enabled':v=>enabled.value=v,'onUpdate:thresholdHours':v=>hours.value=v,'onUpdate:maxNotifications':v=>cap.value=v}),
        ])
      ]);
    }}).use(createPinia()).use(createI18n({legacy:false,locale,messages:{zh,en}})).mount('#app');
  `, { flag: 'wx' })
  writeFileSync(html, `<html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module" src="./${suffix}.js"></script></html>`, { flag: 'wx' })
  await build({root,configFile:false,resolve:{alias:{'@':join(root,'src')}},plugins:[vue()],
    css:{postcss:{plugins:[tailwindcss({...tailwindConfig,content:[html,join(root,'src/**/*.{vue,js,ts}')]}),autoprefixer()]}},
    build:{outDir:previewDir,emptyOutDir:true,minify:false,rollupOptions:{input:html}}})
  server=await preview({root,configFile:false,build:{outDir:previewDir},preview:{host:'127.0.0.1',port:4192,strictPort:true}})
  browser=await chromium.launch({headless:true})
  const page=await browser.newPage({viewport:{width:1280,height:1000}})
  page.on('pageerror',err=>errors.push(err.message))
  await page.route('**/*',route=>{
    const url=new URL(route.request().url())
    if(url.origin==='http://127.0.0.1:4192' || url.protocol==='data:')return route.continue()
    errors.push('Blocked external origin: '+url.origin);return route.abort()
  })
  async function capture(name){
    const overflow=await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth)
    if(overflow)errors.push(name+': horizontal overflow')
    await page.screenshot({path:join(output,name+'.png'),fullPage:true,animations:'disabled'});screenshots.push(name+'.png')
  }
  for(const lang of ['zh','en']){
    await page.goto(`http://127.0.0.1:4192/${suffix}.html?lang=${lang}`)
    await page.locator('[data-testid="forecast-enabled"]').waitFor()
    await capture(lang+'-default')
    await page.locator('[data-testid="forecast-enabled"]').check()
    await page.locator('[data-testid="forecast-hours"]').fill('6.5')
    await page.locator('[data-testid="forecast-cap"]').fill('2')
    if(await page.locator('[data-testid="forecast-hours"]').inputValue()!=='6.5')throw Error('input binding failed')
    await capture(lang+'-configured')
    await page.evaluate(()=>document.documentElement.classList.add('dark'))
    await capture(lang+'-dark')
    await page.setViewportSize({width:390,height:844});await capture(lang+'-mobile')
    await page.setViewportSize({width:1280,height:1000})
    await page.evaluate(()=>document.documentElement.classList.remove('dark'))
  }
  await page.locator('[data-testid="new-feature-badge"]').last().click()
  if(await page.locator('[data-testid="new-feature-badge"]').count()!==0)throw Error('shared notice did not dismiss')
  await page.reload()
  await page.locator('[data-testid="forecast-enabled"]').waitFor()
  if(await page.locator('[data-testid="new-feature-badge"]').count()!==0)throw Error('notice dismissal did not persist')
  writeFileSync(join(output,'manifest.json'),JSON.stringify({source_commit:execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim(),mode:'actual new component and CSS, local fictional preview; no full-page/API/SMTP validation',screenshots,errors},null,2)+'\n')
  if(errors.length)throw Error(errors.join('\n'))
} catch (error) {
  writeFileSync(join(output,'failure.json'),JSON.stringify({message:String(error),errors},null,2)+'\n')
  throw error
} finally {
  await browser?.close()
  if (server) await new Promise((resolve, reject) => server.httpServer.close(error => error ? reject(error) : resolve()))
  rmSync(entry,{force:true});rmSync(html,{force:true});rmSync(previewDir,{recursive:true,force:true})
}
