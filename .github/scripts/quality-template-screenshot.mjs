// CI-only visual evidence: real view, synthetic modules, no application backend.
import { createRequire } from 'node:module'
import path from 'node:path'
import fs from 'node:fs/promises'
const root = process.cwd(), frontend = path.join(root, 'frontend')
const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vueModule = requireFrontend('@vitejs/plugin-vue')
const requireBrowser = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))
const { chromium } = requireBrowser('playwright')
const output = process.env.QUALITY_SCREENSHOT_DIR
await fs.mkdir(output, { recursive: true })
const modules = {
  auth: `import {reactive} from 'vue'; const auth=reactive({user:{id:1,role:'admin'}}); export const useAuthStore=()=>auth;`,
  layout: `import {h} from 'vue'; export default {setup(_, {slots}){return ()=>h('main',{},slots.default?.())}};`,
  nav: `import {h} from 'vue'; export default {render(){return h('nav','质量运维 · 虚构数据')}};`,
  quality: `const cfg={question_kind:'candy',test_channel:'account',prompt:'Synthetic configured question',reasoning_effort:'xhigh',parallel_count:3,model_ids:['model-a','model-b'],quality:{action:'isolate_models',remove_group_ids:[21],auto_restore:true,expected_answer:'42',trigger_on_upstream_5xx:true,judge:{kind:'jev',group_id:0,model_id:'',prompt:'Compare values'}}};
const plan={id:7,account_id:1,account_name:'虚构模板源账号',model_id:'model-a',cron_expression:'7 */2 * * *',enabled:false,max_results:145,auto_recover:false,last_run_at:null,next_run_at:null,created_at:'',updated_at:'',pelican_config:cfg};let presets=[];window.templateEvidence={presetWrites:0,planWrites:0,runCalls:0};
export const listQualityPlans=async()=>[structuredClone(plan)];export const listQualityOperations=async()=>({items:[],next_cursor:0});export const listQualityTemplates=async()=>[];export const listQualityPresets=async()=>structuredClone(presets);
export const createQualityPreset=async(body)=>{const saved={...JSON.parse(JSON.stringify(body)),id:17,created_at:'',updated_at:''};presets=[saved];window.templateEvidence.presetWrites++;return saved};export const updateQualityPreset=async(id,body)=>{presets=[{...JSON.parse(JSON.stringify(body)),id,created_at:'',updated_at:''}];window.templateEvidence.presetWrites++;return structuredClone(presets[0])};
export const runQualityPlan=async()=>{window.templateEvidence.runCalls++};export const createQualityTemplate=async()=>{throw Error('unexpected_dispatch')};export const updateQualityTemplate=async()=>{throw Error('unexpected_dispatch')};export const deleteQualityTemplate=async()=>{};
export const previewQualityOwnership=async()=>({entries:[],models:[],group_ids:[],action:'',ownership_retained:false,relinquishable_count:0});export const reconcileQualityOwnership=async()=>{};export const restoreQualityOwnership=async()=>{};export const removeQualityOwnedModel=async()=>{};`,
  accounts: `export const list=async()=>({items:[{id:1,name:'虚构账号',platform:'openai',type:'oauth',status:'active'}],total:1}); export const getAntigravityDefaultModelMapping=async()=>({}); export const accountsAPI={};`,
  groups: `export const getModelAllowlistCandidates=async()=>['synthetic-judge']; export const getAllIncludingInactive=async()=>[{id:21,name:'虚构普通判题分组',status:'active',platform:'openai'}];`,
  scheduled: `export default {create:async()=>{window.templateEvidence.planWrites++;return {id:8}},update:async()=>{window.templateEvidence.planWrites++;return {id:7}},delete:async()=>{},listResults:async()=>[],getResult:async()=>({})};`,
}
// Real JS fixtures let Vite's dependency scanner classify imports that replace
// .vue files; a virtual ID there is incorrectly treated as an HTML entry.
const fixtures = path.join(frontend, 'node_modules', '.quality-template-fixtures')
await fs.mkdir(fixtures, { recursive: true })
for (const [name, source] of Object.entries(modules)) await fs.writeFile(path.join(fixtures, `${name}.mjs`), source)
const aliases = [
  ['@/stores/auth', 'auth'], ['@/components/layout/AppLayout.vue', 'layout'],
  ['@/components/admin/operations/SmartOpsNav.vue', 'nav'],
  ['@/api/admin/accountQuality', 'quality'], ['@/api/admin/accounts', 'accounts'],
  ['@/api/admin/groups', 'groups'], ['@/api/admin/scheduledTests', 'scheduled'],
].map(([find, name]) => ({ find, replacement: path.join(fixtures, `${name}.mjs`) }))
const html = `<!doctype html><meta charset="utf-8"><div id="app"></div><script type="module">
import {createApp} from 'vue'; import {createPinia} from 'pinia'; import {createI18n} from 'vue-i18n'; import {createRouter,createMemoryHistory} from 'vue-router';
import View from '/src/views/admin/AccountQualityView.vue'; import zh from '/src/i18n/locales/zh/qualityOps.ts'; import en from '/src/i18n/locales/en/qualityOps.ts'; import '/src/style.css';
const i18n=createI18n({legacy:false,locale:'zh',messages:{zh:{qualityOps:zh},en:{qualityOps:en}},missingWarn:false,fallbackWarn:false});window.qualityI18n=i18n;
const router=createRouter({history:createMemoryHistory(),routes:[{path:'/',component:View}]});
createApp(View).use(createPinia()).use(i18n).use(router).mount('#app');</script>`
process.chdir(frontend)
const server = await createServer({
  root: frontend, configFile: false,
  plugins: [(vueModule.default || vueModule)(), {
    name: 'quality-template-offline',
    resolveId(id, importer) {
      if (id === './auth' && importer?.endsWith('/stores/accountQuality.ts')) return path.join(fixtures, 'auth.mjs')
    },
    configureServer(vite) { vite.middlewares.use((req, res, next) => {
      if (req.url === '/') { res.setHeader('Content-Type', 'text/html'); void vite.transformIndexHtml('/', html).then(value => res.end(value)); return }
      next()
    }) },
  }],
  resolve: { alias: [...aliases, { find: '@', replacement: path.join(frontend, 'src') }] },
  css: { postcss: { plugins: [requireFrontend('tailwindcss')({ config: path.join(frontend, 'tailwind.config.js') }), requireFrontend('autoprefixer')()] } },
  server: { host: '127.0.0.1', port: 4183, strictPort: true },
  optimizeDeps: { include: ['vue', 'vue-i18n', 'vue-router', 'pinia'] },
})
await server.listen()
const browser = await chromium.launch({ headless: true })
const failures = [], blocked = []
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 1100 } })
  page.on('pageerror', error => failures.push(error.message))
  // Any accidental backend or external request fails the evidence job.
  await page.route('**/*', route => {
    const url = new URL(route.request().url())
    if (url.origin !== 'http://127.0.0.1:4183' || url.pathname.startsWith('/api/')) { blocked.push(url.pathname); return route.abort() }
    return route.continue()
  })
  await page.goto('http://127.0.0.1:4183/', { waitUntil: 'networkidle' })
  await page.getByTestId('quality-rule-save-preset').click()
  await page.getByTestId('quality-preset-name').fill('完整策略模板 · 虚构数据')
  await page.getByTestId('quality-action-isolate-models').waitFor()
  if (!await page.getByTestId('quality-action-isolate-models').isChecked()) throw Error('policy_not_copied')
  if (!await page.getByTestId('quality-target-group-21').isChecked()) throw Error('groups_not_copied')
  if (!await page.getByTestId('quality-auto-restore').isChecked()) throw Error('restore_not_copied')
  await page.waitForTimeout(350)
  await page.locator('#quality-rule-form').screenshot({path:path.join(output,'complete-save-template-fields.png')})
  const policy=page.getByTestId('quality-action-isolate-models').locator('..').locator('..')
  await policy.scrollIntoViewIfNeeded()
  await policy.screenshot({path:path.join(output,'complete-save-template-action-groups.png')})
  await page.getByTestId('quality-auto-restore').locator('..').screenshot({path:path.join(output,'complete-save-template-restoration.png')})
  await page.locator('.editor-footer button[form="quality-rule-form"]').click()
  await page.getByTestId('quality-create-rule').click()
  await page.getByTestId('quality-preset-select').selectOption('17')
  await page.getByTestId('quality-edit-preset').click()
  await page.getByTestId('quality-action-remove-groups').check()
  await page.getByTestId('quality-copy-max-results').fill('147')
  await page.waitForTimeout(350)
  await page.getByTestId('quality-action-remove-groups').locator('..').locator('..').screenshot({path:path.join(output,'complete-edit-template-policy.png')})
  await page.locator('.editor-footer button[form="quality-rule-form"]').click()
  await page.getByTestId('quality-create-rule').click()
  await page.getByTestId('quality-preset-select').selectOption('17')
  await page.getByTestId('quality-apply-preset').click()
  if (!await page.getByTestId('quality-action-remove-groups').isChecked()) throw Error('edited_policy_not_applied')
  if (!await page.getByTestId('quality-target-group-21').isChecked()) throw Error('edited_groups_not_applied')
  if (await page.getByTestId('quality-copy-max-results').inputValue()!=='147') throw Error('edited_retention_not_applied')
  await page.getByTestId('quality-reuse-preview').screenshot({path:path.join(output,'complete-read-apply-draft-preview.png')})
  let operations=await page.evaluate(()=>window.templateEvidence)
  if (operations.presetWrites!==2 || operations.planWrites!==0 || operations.runCalls!==0) throw Error('passive_template_wrote_business_state')
  // Bulk import keeps field selection explicit; the preview now offers a
  // complete path from an empty selection to reviewed, saveable changes.
  await page.locator('.editor-footer .btn-secondary').click()
  await page.getByRole('button', { name: '放弃修改', exact: true }).click()
  await page.locator('[data-plan-id="7"] .rule-checkbox input').check()
  await page.getByTestId('quality-bulk-edit').click()
  await page.getByTestId('quality-preset-select').selectOption('17')
  await page.getByTestId('quality-apply-preset').click()
  const bulkSubmit = page.locator('.editor-footer button[form="quality-rule-form"]')
  if (!await bulkSubmit.isDisabled()) throw Error('bulk_empty_fields_save_enabled')
  await page.getByTestId('quality-reuse-preview').screenshot({path:path.join(output,'bulk-import-empty-fields.png')})
  await page.locator('.modal-footer').screenshot({path:path.join(output,'bulk-import-save-hint.png')})
  if (!process.env.QUALITY_BULK_BEFORE) {
    if (!await page.getByTestId('quality-reuse-empty-fields').isVisible()) throw Error('bulk_empty_hint_missing')
    await page.getByTestId('quality-reuse-select-all').click()
    if (!await bulkSubmit.isDisabled()) throw Error('bulk_unconfirmed_save_enabled')
    await page.getByTestId('quality-reuse-confirm').check()
    if (await bulkSubmit.isDisabled()) throw Error('bulk_confirmed_save_disabled')
    await page.locator('[data-reuse-target="7"] summary').click()
    await page.getByTestId('quality-reuse-preview').screenshot({path:path.join(output,'bulk-import-confirmed-fields.png')})
    await page.getByTestId('quality-reuse-field-enabled').uncheck()
    if (!await bulkSubmit.isDisabled()) throw Error('bulk_changed_fields_kept_confirmation')
    await page.getByTestId('quality-reuse-confirm').check()
    await bulkSubmit.click()
    if ((await page.evaluate(()=>window.templateEvidence.planWrites)) !== 1) throw Error('bulk_did_not_save_target')
  }
  operations=await page.evaluate(()=>window.templateEvidence)
  if (failures.length || blocked.length) throw new Error('offline_render_failed')
  await fs.writeFile(path.join(output, 'verification.json'), JSON.stringify({ commit: process.env.QUALITY_HEAD_SHA, component: 'AccountQualityView.vue', operations, synthetic: true, paidRequests: 0, applicationApiCalls: 0, failures, blocked }, null, 2))
} catch (err) {
  await fs.writeFile(path.join(output, 'failure.json'), JSON.stringify({ message: err.message, failures, blocked }, null, 2))
  throw err
} finally { await browser.close(); await server.close() }
