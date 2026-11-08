// Remote CI only: render real AI operations components with synthetic modules.
import { createRequire } from 'node:module'
import path from 'node:path'
import fs from 'node:fs/promises'
import { execFileSync } from 'node:child_process'

const root = process.cwd(), frontend = path.join(root, 'frontend')
const requireFrontend = createRequire(path.join(frontend, 'package.json'))
const { createServer } = requireFrontend('vite')
const vueModule = requireFrontend('@vitejs/plugin-vue'), vue = vueModule.default || vueModule
const tailwind = requireFrontend('tailwindcss'), autoprefixer = requireFrontend('autoprefixer')
const requireBrowser = createRequire(path.join(process.env.PLAYWRIGHT_ROOT, 'package.json'))
const { chromium } = requireBrowser('playwright')
const output = process.env.AI_OPS_SCREENSHOT_DIR
await fs.mkdir(output, { recursive: true })

const fixture = `
const copy = value => structuredClone(value);
const now = new Date().toISOString(), until = new Date(Date.now() + 120000).toISOString();
const scenario = new URLSearchParams(location.search).get('scenario') || 'ready';
const disabled = scenario === 'disabled', cooldown = scenario === 'cooldown';
const controlEnabled = !disabled && scenario !== 'observe';
const runStartedMs = Date.now() - (cooldown ? 0 : 600000);
const runStartedAt = new Date(runStartedMs).toISOString();
const runFinishedAt = new Date(runStartedMs + (cooldown ? 0 : 25000)).toISOString();
const scope = {model:'synthetic-model',effort:'high',protocol:'native_http'};
const policy = {control_enabled:controlEnabled,revision:5,mode:controlEnabled?'approve_each':'disabled',halted:!controlEnabled,group_id:2,accounts:[{account_id:3,capacity_domain:'provider-a'},{account_id:4,capacity_domain:'provider-b'}],min_healthy_accounts:1,min_total_concurrency:5,min_concurrency:1,max_concurrency:100,max_step_percent:10,daily_action_limit:10,cooldown_seconds:300,observation_seconds:300,evidence_max_age_seconds:120,min_samples:20,error_rate_threshold:0.2,rollback_error_rate_delta:0.05,auto_actions:['set_concurrency'],max_pause_seconds:900};
const run = {id:'synthetic-run-a',status:'completed',created_at:runStartedAt,finished_at:runFinishedAt,group_id:2,window:{start:new Date(runStartedMs-900000).toISOString(),end:runStartedAt},evidence:[{id:'capacity-1',source:'account_capacity',status:'ok',collected_at:runStartedAt,metrics:{samples:80,error_rate:0.2,concurrency:10,inflight:3}}],result:{summary:'合成诊断：一个容量域出现上游错误，建议小幅降低并发。',findings:[{severity:'warning',category:'capacity',description:'合成账号 #3 在 80 个样本中观察到错误率升高。',recommendation:'对照其他容量域，预览 10 → 9 的并发变更。',evidence_ids:['capacity-1'],account_ids:[3]}],proposals:[{action:'set_concurrency',account_id:3,proposed_value:9,reason:'合成证据建议减少单域压力，保留独立容量域作为余量。',evidence_ids:['capacity-1'],executable:false}],limitations:['仅用于离线 CI 页面验收；不含生产数据。']}};
const proposal = {id:'synthetic-proposal-a',run_id:run.id,proposal_index:0,action:'set_concurrency',account_id:3,proposed_value:9,before_value:10,policy_revision:5,target_revision:8,digest:'d'.repeat(64),expires_at:until,created_at:now,status:'ready',blocked_reasons:[],evidence:{observed_at:now,window_start:run.window.start,window_end:now,samples:80,error_rate:0.2,inflight:-1,eligible_accounts:2,total_concurrency:30,model_coverage_complete:true,probe_passed:false,...scope},impact_group_ids:[2]};
const records = [];
globalThis.__aiOpsFixture={calls:[],mutations:[],paidCalls:[],scenario};
function record(name,args){globalThis.__aiOpsFixture.calls.push({name,args});}
export const aiOpsAPI = {
 async status(){record('status',[]);return {enabled:!disabled,configured:!disabled,mode:'observe',running:false,group_id:disabled?null:2,window_minutes:15,min_interval_seconds:300,automatic_interval_seconds:0,daily_run_limit:24,...(cooldown?{last_run_at:now}:{})};},
 async list(){record('runs',[]);return {items:disabled?[]:[copy(run)],total:disabled?0:1};},
 async get(id){record('run',[id]);return copy(run);},
 async create(){globalThis.__aiOpsFixture.paidCalls.push('diagnostic');throw new Error('unexpected_diagnostic_creation');}
};
export const aiOpsControlAPI = {
 async policy(){record('policy',[]);return copy(policy);},
 async actions(){record('actions',[]);return {items:copy(records),total:records.length};},
 async probes(){record('probes',[]);return {items:[],total:0};},
 async preview(id,index){record('preview',[id,index]);if(id!==run.id||index!==0)throw new Error('wrong_candidate');return copy(proposal);},
 async cancel(id,digest){if(id!==proposal.id||digest!==proposal.digest)throw new Error('wrong_digest');proposal.status='cancelled';globalThis.__aiOpsFixture.mutations.push({kind:'cancel',id,digest});return copy(proposal);},
 async apply(id,digest){if(proposal.status!=='ready'||id!==proposal.id||digest!==proposal.digest)throw new Error('wrong_digest');globalThis.__aiOpsFixture.mutations.push({kind:'apply',id,digest});const action={id:'synthetic-action-a',proposal_id:id,run_id:run.id,action:'set_concurrency',account_id:3,before_value:10,after_value:9,status:'propagation_pending',created_at:now,updated_at:now,observe_until:until,owner_revision:9,target_revision:8,reason_code:'',auto:false};records.push(action);return copy(action);},
 async savePolicy(){globalThis.__aiOpsFixture.mutations.push({kind:'save'});throw new Error('unexpected_policy_write');},
 async halt(){globalThis.__aiOpsFixture.mutations.push({kind:'halt'});return {...copy(policy),halted:true,revision:6};},
 async rollback(){globalThis.__aiOpsFixture.mutations.push({kind:'rollback'});throw new Error('unexpected_rollback');},
 async probe(){globalThis.__aiOpsFixture.paidCalls.push('probe');throw new Error('unexpected_probe');}
};
`
const modules = {
  fixture,
  auth: `export const useAuthStore=()=>({isAdmin:true,isObserver:false,user:{id:41,role:'admin'}})`,
  settings: `export const useAdminSettingsStore=()=>({requestCaptureEnabled:false})`,
  layout: `import {h} from 'vue';export default {setup(_,ctx){return ()=>h('main',{class:'mx-auto max-w-7xl p-4 text-gray-900'},ctx.slots.default?.())}}`,
}
const aliases = [
  ['@/api/admin/aiOpsControl', 'fixture'], ['@/api/admin/aiOps', 'fixture'],
  ['@/stores/auth', 'auth'], ['@/stores/adminSettings', 'settings'],
  ['@/components/layout/AppLayout.vue', 'layout'],
].map(([find, name]) => ({ find, replacement: `virtual:ai-ops-${name}` }))
const html = `<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><div id="app"></div><script type="module">
import {createApp} from 'vue';import {createPinia} from 'pinia';import {createRouter,createWebHistory,RouterView} from 'vue-router';import {createI18n} from 'vue-i18n';
import Page from '/src/views/admin/AIOpsView.vue';import zh from '/src/i18n/locales/zh/index.ts';import '/src/style.css';
const router=createRouter({history:createWebHistory(),routes:[{path:'/:pathMatch(.*)*',component:Page}]});
createApp(RouterView).use(createPinia()).use(router).use(createI18n({legacy:false,locale:'zh',messages:{zh}})).mount('#app');</script></html>`
process.chdir(frontend)
const server = await createServer({ root: frontend, configFile: false, plugins: [vue(), {
  name: 'ai-ops-offline',
  resolveId(id) { if (id.startsWith('virtual:ai-ops-')) return '\0' + id },
  load(id) { const name = id.replace('\0virtual:ai-ops-', ''); return modules[name] },
  configureServer(vite) { vite.middlewares.use((req, res, next) => {
    if (req.url?.split('?')[0] !== '/admin/ai-ops') return next()
    res.setHeader('Content-Type', 'text/html')
    void vite.transformIndexHtml('/admin/ai-ops', html).then(value => res.end(value))
  }) },
}], resolve: { alias: [...aliases, { find: '@', replacement: path.join(frontend, 'src') }] },
css: { postcss: { plugins: [tailwind({ config: path.join(frontend, 'tailwind.config.js') }), autoprefixer()] } },
server: { host: '127.0.0.1', port: 4185, strictPort: true },
optimizeDeps: { noDiscovery: true, include: ['vue', 'vue-i18n', 'pinia', 'vue-router'] } })
await server.listen()
const browser = await chromium.launch({ headless: true })
const errors = [], consoleErrors = [], blocked = [], widths = []
async function requireClosed(page, testId) {
  const details = page.getByTestId(testId)
  await details.waitFor()
  if (!(await details.evaluate(element => element instanceof HTMLDetailsElement && !element.open))) {
    throw new Error(`expected_collapsed_details:${testId}`)
  }
}
async function recordMobileWidth(page) {
  widths.push(await page.evaluate(() => ({ body: document.body.scrollWidth, viewport: innerWidth })))
  if (widths.some(width => width.body > width.viewport + 2)) throw new Error('mobile_horizontal_overflow')
}
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1100 } })
  page.on('pageerror', error => errors.push(error.message))
  page.on('console', message => { if (message.type() === 'error') consoleErrors.push(message.text()) })
  await page.route('**/*', route => {
    const url = new URL(route.request().url())
    if (url.hostname !== '127.0.0.1' || url.pathname.startsWith('/api/')) { blocked.push(url.href); return route.abort() }
    return route.continue()
  })
  await page.goto('http://127.0.0.1:4185/admin/ai-ops?scenario=disabled', { waitUntil: 'networkidle' })
  await page.getByTestId('ai-ops-disabled').waitFor()
  await page.getByTestId('ai-control-disabled').waitFor()
  await page.screenshot({ path: path.join(output, 'disabled-desktop-synthetic.png'), fullPage: true })
  const disabledCalls = await page.evaluate(() => globalThis.__aiOpsFixture)
  if (disabledCalls.mutations.length || disabledCalls.paidCalls.length) throw new Error('disabled_page_created_side_effect')

  await page.goto('http://127.0.0.1:4185/admin/ai-ops?scenario=observe', { waitUntil: 'networkidle' })
  await page.getByTestId('ai-ops-summary').waitFor()
  await page.getByTestId('ai-control-disabled').waitFor()
  if (await page.getByTestId('ai-ops-cooldown').count()) throw new Error('observe_fixture_unexpected_cooldown')
  await requireClosed(page, 'ai-ops-history')
  await requireClosed(page, 'ai-control-advanced')
  await requireClosed(page, 'ai-ops-report-details')
  if (await page.getByTestId('ai-control-halt').count()) throw new Error('disabled_execution_shows_halt_action')
  if (await page.getByTestId('ai-control-halted').count()) throw new Error('disabled_execution_shows_halt_warning')
  await page.screenshot({ path: path.join(output, 'observe-desktop-synthetic.png'), fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.screenshot({ path: path.join(output, 'observe-mobile-synthetic.png'), fullPage: true })
  await recordMobileWidth(page)
  const observeCalls = await page.evaluate(() => globalThis.__aiOpsFixture)
  if (observeCalls.mutations.length || observeCalls.paidCalls.length) throw new Error('observe_page_created_side_effect')

  await page.setViewportSize({ width: 1440, height: 1100 })
  await page.goto('http://127.0.0.1:4185/admin/ai-ops?scenario=cooldown', { waitUntil: 'networkidle' })
  await page.getByTestId('ai-ops-summary').waitFor()
  await page.getByTestId('ai-ops-cooldown').waitFor()
  await page.getByTestId('ai-ops-cost-consent').check()
  if (!(await page.getByTestId('ai-ops-start').isDisabled())) throw new Error('cooldown_allows_paid_submission')
  await page.screenshot({ path: path.join(output, 'cooldown-desktop-synthetic.png'), fullPage: true })
  const cooldownCalls = await page.evaluate(() => globalThis.__aiOpsFixture)
  if (cooldownCalls.mutations.length || cooldownCalls.paidCalls.length) throw new Error('cooldown_page_created_side_effect')

  await page.goto('http://127.0.0.1:4185/admin/ai-ops?scenario=ready', { waitUntil: 'networkidle' })
  await page.getByTestId('ai-ops-summary').waitFor()
  if (await page.getByTestId('ai-ops-cooldown').count()) throw new Error('ready_fixture_unexpected_cooldown')
  await requireClosed(page, 'ai-ops-history')
  await requireClosed(page, 'ai-control-advanced')
  await requireClosed(page, 'ai-ops-report-details')
  if (!(await page.getByTestId('ai-control-halt').isVisible())) throw new Error('active_execution_hides_emergency_halt')
  if (await page.evaluate(() => globalThis.__aiOpsFixture.mutations.length + globalThis.__aiOpsFixture.paidCalls.length)) throw new Error('page_load_created_side_effect')
  await page.screenshot({ path: path.join(output, 'ready-desktop-synthetic.png'), fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.screenshot({ path: path.join(output, 'ready-mobile-synthetic.png'), fullPage: true })
  await recordMobileWidth(page)
  await page.setViewportSize({ width: 1440, height: 1100 })
  await page.getByTestId('ai-control-advanced').locator(':scope > summary').click()
  await page.getByTestId('ai-control-preview-0').waitFor()
  await page.getByTestId('ai-control-preview-0').click()
  await page.getByTestId('ai-control-frozen').waitFor()
  if (!(await page.getByTestId('ai-control-apply').isDisabled())) throw new Error('apply_without_consent')
  if (await page.evaluate(() => globalThis.__aiOpsFixture.mutations.length)) throw new Error('preview_wrote_production_state')
  await page.getByTestId('ai-control-advanced').locator(':scope > summary').click()
  if (await page.getByTestId('ai-control-frozen').count() !== 1) throw new Error('collapse_discarded_frozen_preview')
  await page.getByTestId('ai-control-advanced').locator(':scope > summary').click()
  await page.getByTestId('ai-control-frozen').waitFor()
  if (await page.evaluate(() => globalThis.__aiOpsFixture.calls.filter(call => call.name === 'preview').length) !== 1) throw new Error('collapse_recreated_frozen_preview')
  await page.screenshot({ path: path.join(output, 'frozen-desktop-synthetic.png'), fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.screenshot({ path: path.join(output, 'frozen-mobile-synthetic.png'), fullPage: true })
  await recordMobileWidth(page)
  await page.getByTestId('ai-control-apply-consent').check()
  await page.getByTestId('ai-control-apply').click()
  await page.getByTestId('ai-control-action').waitFor()
  const verification = await page.evaluate(() => globalThis.__aiOpsFixture)
  if (verification.mutations.length !== 1 || verification.mutations[0].kind !== 'apply' || verification.mutations[0].digest !== 'd'.repeat(64)) throw new Error('invalid_explicit_approval')
  if (verification.paidCalls.length || blocked.length || errors.length) throw new Error('unexpected_paid_or_network_calls_or_browser_error')
  await page.goto('http://127.0.0.1:4185/admin/ai-ops?scenario=cancel', { waitUntil: 'networkidle' })
  await page.getByTestId('ai-ops-summary').waitFor()
  await page.getByTestId('ai-control-advanced').locator(':scope > summary').click()
  await page.getByTestId('ai-control-preview-0').click()
  await page.getByTestId('ai-control-frozen').waitFor()
  await page.getByTestId('ai-control-cancel').click()
  await page.getByTestId('ai-control-cancel').waitFor({ state: 'hidden' })
  if (!(await page.getByTestId('ai-control-apply').isDisabled())) throw new Error('cancelled_proposal_can_apply')
  await page.screenshot({ path: path.join(output, 'cancelled-mobile-synthetic.png'), fullPage: true })
  await recordMobileWidth(page)
  await page.setViewportSize({ width: 1440, height: 1100 })
  await page.screenshot({ path: path.join(output, 'cancelled-desktop-synthetic.png'), fullPage: true })
  await page.getByTestId('ai-control-preview-0').click()
  await page.getByTestId('ai-control-frozen').waitFor()
  if (await page.getByTestId('ai-control-cancel').count() || !(await page.getByTestId('ai-control-apply').isDisabled())) throw new Error('repreview_revived_cancelled_proposal')
  const cancellation = await page.evaluate(() => globalThis.__aiOpsFixture)
  if (cancellation.mutations.length !== 1 || cancellation.mutations[0].kind !== 'cancel' || cancellation.mutations[0].digest !== 'd'.repeat(64) || cancellation.paidCalls.length) throw new Error('invalid_proposal_cancellation')
  if (blocked.length || errors.length) throw new Error('unexpected_cancellation_network_or_browser_error')
  await fs.writeFile(path.join(output, 'verification.json'), JSON.stringify({
    commit: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim(), synthetic: true,
    actualComponents: ['AIOpsView.vue', 'AIOpsReport.vue', 'AIOpsControls.vue', 'AIOpsRecoveryProbes.vue', 'SmartOpsNav.vue'],
    defaultDetailsCollapsed: ['ai-ops-history', 'ai-control-advanced', 'ai-ops-report-details'],
    activeEmergencyHaltVisible: true, disabledExecutionHaltHidden: true, cooldownBlocksConsentedSubmission: true,
    frozenPreviewPreservedAcrossCollapse: true, cancelledProposalCannotApplyOrRevive: true, cancellationMutations: cancellation.mutations,
    disabledSideEffects: 0, observeSideEffects: 0, cooldownSideEffects: 0, unapprovedWrites: 0, confirmedSyntheticMutations: verification.mutations,
    paidRequests: 0, productionApiCalls: 0, browserErrors: errors, blockedRequests: blocked, mobileWidths: widths,
  }, null, 2))
} catch (error) {
  for (const page of browser.contexts().flatMap(context => context.pages())) {
    await page.screenshot({ path: path.join(output, 'failure-synthetic.png'), fullPage: true })
    await fs.writeFile(path.join(output, 'failure.json'), JSON.stringify({ error: String(error), browserErrors: errors, consoleErrors, blockedRequests: blocked, body: await page.locator('body').innerText() }, null, 2))
  }
  throw error
} finally { await browser.close(); await server.close() }
