// Remote CI only. The sole provider substitute is the isolated HTTPS server.
// No application API fulfillment, auth-store replacement, injected questions,
// results, CSS changes, forced clicks, HAR, cookies or token exports.
import assert from 'node:assert/strict'
import { createServer } from 'node:https'
import { readFileSync, writeFileSync, appendFileSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { createRequire } from 'node:module'
import { bindDetailResponse } from './pelican-detail-identity.mjs'
import { completeScheduledRounds } from './pelican-complete-rounds.mjs'

const evidence = process.env.EVIDENCE_DIR || '/evidence'
const upstreamOrigin = 'https://fake-upstream:4199'
const model = 'claude-opus-5-5'
const mode = process.argv[2]
const unwrap = value => value?.data ?? value
const save = (name, value) => writeFileSync(join(evidence, name), JSON.stringify(value, null, 2) + '\n')

function topology() {
  assert.equal(JSON.parse(readFileSync(join(evidence, 'network-internal.json'))), true)
  const destinations = {
    postgres: ['/var/lib/postgresql'],
    redis: ['/data'],
    app: ['/app/data', '/fixture/config.yml'],
    'fake-upstream': ['/evidence'],
    browser: ['/evidence', '/fixture/acceptance.json', '/tmp'],
  }
  for (const [service, allowed] of Object.entries(destinations)) {
    const actual = JSON.parse(readFileSync(join(evidence, `network-${service}.json`)))
    assert.equal(actual.service, service)
    assert.equal(actual.privileged, false)
    if (['browser', 'fake-upstream'].includes(service)) {
      assert.equal(actual.user, `${process.env.FIXTURE_UID}:${process.env.FIXTURE_GID}`)
    }
    assert.equal(Object.keys(actual.ports || {}).length, 0, 'no published ports')
    assert.deepEqual(Object.keys(actual.networks), [`${process.env.COMPOSE_PROJECT_NAME}_isolated`])
    assert.equal(actual.networkMode, `${process.env.COMPOSE_PROJECT_NAME}_isolated`)
    for (const mount of actual.mounts) {
      assert.ok(allowed.includes(mount.Destination), `unexpected mount destination in ${service}`)
      assert.ok(!(mount.Source || '').includes('docker.sock'), 'no Docker socket')
      if (mount.Destination.startsWith('/fixture/')) assert.equal(mount.RW, false)
    }
  }
  save('topology-result.json', { status: 'passed', services: Object.keys(destinations), internal: true })
}

function upstream() {
  mkdirSync(evidence, { recursive: true })
  const captures = []
  const rejected = []
  let refuseNext = false
  let holdScheduled = false
  const heldReplies = new Set()
  const json = (res, status, value) => {
    res.writeHead(status, { 'content-type': 'application/json' })
    res.end(JSON.stringify(value))
  }
  createServer({
    key: readFileSync('/opt/fixture/tls/key.pem'),
    cert: readFileSync('/opt/fixture/tls/cert.pem'),
  }, async (req, res) => {
    // Deliberately never retain request headers, complete bodies or URL queries.
    const path = new URL(req.url, upstreamOrigin).pathname
    if (req.method === 'GET' && path === '/health') return json(res, 200, { status: 'fixture-only' })
    if (req.method === 'GET' && path === '/__fixture/captures') return json(res, 200, { captures, rejected })
    if (req.method === 'POST' && path === '/__fixture/refuse-next') {
      refuseNext = true
      return json(res, 200, { armed: true })
    }
    // Hold only provider replies, never the application's handlers or results.
    // This lets CI observe real Claim leases before the fast fixture completes.
    if (req.method === 'POST' && path === '/__fixture/hold-scheduled') {
      if (holdScheduled || heldReplies.size) return json(res, 409, { error: 'already holding' })
      holdScheduled = true
      return json(res, 200, { holding: true })
    }
    if (req.method === 'POST' && path === '/__fixture/release-scheduled') {
      holdScheduled = false
      const released = heldReplies.size
      for (const release of heldReplies) release()
      return json(res, 200, { released })
    }
    // Explicitly reject the app's optional pricing refresh; this is not a fake
    // pricing response. The packaged application fallback remains in use.
    if (req.method === 'GET' && ['/disabled-pricing', '/disabled-pricing-hash'].includes(path)) {
      return json(res, 503, { error: 'fixture pricing refresh disabled' })
    }
    if (!((req.method === 'GET' && path === '/v1/models') || (req.method === 'POST' && path === '/v1/messages'))) {
      rejected.push({ method: req.method, path })
      save('upstream-rejected.json', rejected)
      return json(res, 404, { error: 'unexpected fixture path' })
    }
    const alias = ({ 'fixture-only-key-a': 'a', 'fixture-only-key-b': 'b' })[req.headers['x-api-key']]
    if (!alias) {
      rejected.push({ method: req.method, path, reason: 'fixture credentials required' })
      save('upstream-rejected.json', rejected)
      return json(res, 401, { error: 'fixture credentials required' })
    }
    if (req.method === 'GET' && path === '/v1/models') {
      return json(res, 200, {
        data: [{ id: model, type: 'model', display_name: model, created_at: '2026-01-01T00:00:00Z' }],
        has_more: false, first_id: model, last_id: model,
      })
    }
    try {
      const chunks = []
      let size = 0
      for await (const chunk of req) {
        size += chunk.length
        assert.ok(size <= 256 * 1024, 'fixture input bound')
        chunks.push(chunk)
      }
      const input = JSON.parse(Buffer.concat(chunks))
      assert.equal(input.model, model)
      assert.equal(input.stream, true)
      assert.equal(input.messages.length, 1)
      assert.equal(input.messages[0].role, 'user')
      const content = input.messages[0].content
      const prompt = typeof content === 'string' ? content : content.filter(x => x.type === 'text').map(x => x.text).join('')
      assert.ok(prompt.length > 0)
      const sequence = captures.length + 1
      const marker = `fixture795-${sequence}`
      const refused = refuseNext
      refuseNext = false
      const capture = { sequence, marker, alias, model: input.model, prompt, refused }
      captures.push(capture)
      appendFileSync(join(evidence, 'provider-captures.jsonl'), JSON.stringify(capture) + '\n')
      if (holdScheduled) {
        await new Promise((resolve, reject) => {
          const release = () => { clearTimeout(timer); heldReplies.delete(release); resolve() }
          const timer = setTimeout(() => {
            heldReplies.delete(release)
            reject(new Error('fixture scheduled reply hold expired'))
          }, 30_000)
          heldReplies.add(release)
        })
      }
      res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' })
      const event = data => res.write(`event: ${data.type}\ndata: ${JSON.stringify(data)}\n\n`)
      event({ type: 'message_start', message: {
        id: `msg_fixture_${sequence}`, type: 'message', role: 'assistant', model,
        content: [], stop_reason: null, usage: { input_tokens: 40, output_tokens: 0 },
      } })
      {
        const html = `<!doctype html><html><head><meta charset="utf-8"><title>${marker}</title></head><body><p>${marker}</p><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 300"><circle cx="120" cy="200" r="40" fill="teal"/><circle cx="280" cy="200" r="40" fill="orange"/></svg></body></html>`
        event({ type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } })
        // A refusal may contain partial text. Its unique marker binds the real
        // failed persisted output to this provider request, without treating it
        // as a successful HTML answer or relying on arrival ordering.
        event({ type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: refused ? `${marker}: fixture refusal` : html } })
        event({ type: 'content_block_stop', index: 0 })
      }
      event({ type: 'message_delta', delta: refused
        ? { stop_reason: 'refusal', stop_details: { type: 'refusal', category: 'fixture-deterministic' } }
        : { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: refused ? 1 : 100 } })
      event({ type: 'message_stop' })
      res.end()
    } catch {
      rejected.push({ method: req.method, path, reason: 'fixture input contract rejected' })
      save('upstream-rejected.json', rejected)
      if (!res.headersSent) json(res, 400, { error: 'fixture input contract rejected' })
      else res.end()
    }
  }).listen(4199, '0.0.0.0')
}

async function browser() {
  const require = createRequire('/opt/fixture/package.json')
  const { chromium } = require('playwright')
  const { Client } = require('pg')
  const origin = process.env.APP_ORIGIN
  assert.equal(origin, 'http://app:8080', 'fixture origin only')
  const state = { full_acceptance_complete: false, scenarios: [], blocked: [], requestFailures: [], pageErrors: [], consoleErrors: [] }
  const declared = JSON.parse(readFileSync('/fixture/acceptance.json'))
  save('acceptance.json', declared)
  const db = new Client({ connectionTimeoutMillis: 5000, statement_timeout: 5000 })
  let browser
  let context
  let page
  let stage = 'browser-startup'
  try {
    browser = await chromium.launch({ headless: true })
    context = await browser.newContext({ viewport: { width: 1440, height: 1100 }, locale: 'en-US', serviceWorkers: 'block' })
    stage = 'normal-authentication'
    await db.connect()
    const { rows: accounts } = await db.query("SELECT id::text, name FROM accounts WHERE name IN ('fixture-pelican-a', 'fixture-pelican-b') ORDER BY name")
    assert.equal(accounts.length, 2)
    const { rows: groups } = await db.query("SELECT id::text, name FROM groups WHERE name = 'fixture-pelican-group'")
    assert.equal(groups.length, 1)
    save('fixture-entities.json', { accounts, groups })
    await context.route('**/*', async route => {
      const url = new URL(route.request().url())
      if (url.origin === origin || ['data:', 'blob:', 'about:'].includes(url.protocol)) return route.continue()
      state.blocked.push({ method: route.request().method(), origin: url.origin, path: url.pathname })
      await route.abort('blockedbyclient')
    })
    await context.routeWebSocket('**/*', ws => {
      const url = new URL(ws.url())
      if (url.origin === 'ws://app:8080') ws.connectToServer()
      else {
        state.blocked.push({ method: 'WEBSOCKET', origin: url.origin, path: url.pathname })
        ws.close()
      }
    })
    page = await context.newPage()
    page.setDefaultTimeout(20_000)
    page.on('requestfailed', request => {
      const url = new URL(request.url())
      state.requestFailures.push({ method: request.method(), path: url.pathname, reason: request.failure()?.errorText })
    })
    // Console text and exception messages can contain application data. Retain
    // only their types; scenario assertion failures carry safe fixture context.
    page.on('pageerror', error => state.pageErrors.push({ name: error.name }))
    page.on('console', message => { if (message.type() === 'error') state.consoleErrors.push({ type: 'error' }) })
    const manualRequests = []
    const forbiddenRuns = []
    let prepareCount = 0
    page.on('request', request => {
      const path = new URL(request.url()).pathname
      if (request.method() === 'POST' && /\/(?:scheduled-test-plans|pelican-group-tests)\/\d+\/run$/.test(path)) forbiddenRuns.push(path)
      if (path === '/api/v1/admin/accounts/pelican-question' && request.method() === 'GET') prepareCount++
      const match = path.match(/^\/api\/v1\/admin\/accounts\/(\d+)\/pelican-test$/)
      if (match && request.method() === 'POST') {
        const input = request.postDataJSON()
        manualRequests.push({ account_id: match[1], model_id: input.model_id, prompt: input.prompt,
          prompt_mode: input.prompt_mode, pelican_question: input.pelican_question })
      }
    })
    await page.goto(`${origin}/login?redirect=/admin/accounts`)
    await page.locator('#email').fill('reviewer@example.invalid')
    await page.locator('#password').fill('Fixture-Only-795-Password!')
    const login = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/auth/login' && response.request().method() === 'POST')
    await page.locator('button[type=submit]').click()
    assert.equal((await login).status(), 200)
    await page.waitForURL(url => url.pathname === '/admin/accounts')
    // Complete normal first-login UI on this disposable fixture installation.
    // Do not inject consent/auth state or hide overlays with CSS.
    await page.locator('.driver-popover').waitFor({ state: 'visible' })
    await page.keyboard.press('Escape')
    await page.locator('.driver-popover').waitFor({ state: 'hidden' })
    const compliance = page.getByRole('dialog').filter({ has: page.locator('#admin-compliance-phrase') })
    await compliance.waitFor({ state: 'visible' })
    const phrase = await compliance.locator('label[for="admin-compliance-phrase"] + div').innerText()
    await compliance.locator('#admin-compliance-phrase').fill(phrase)
    const accepted = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/admin/compliance/accept')
    await compliance.getByRole('button', { name: 'Acknowledge and Continue', exact: true }).click()
    assert.equal((await accepted).status(), 200)
    await compliance.waitFor({ state: 'hidden' })
    // Requests made before first-install acknowledgement are gated by the real
    // middleware. Preserve their diagnostics, then reload the real page so its
    // initial account query runs after acknowledgement has persisted.
    await page.waitForLoadState('networkidle')
    state.bootstrap = { blocked: [...state.blocked], requestFailures: [...state.requestFailures],
      pageErrors: [...state.pageErrors], consoleErrors: [...state.consoleErrors] }
    assert.equal(state.blocked.length, 0, 'bootstrap must not request external resources')
    assert.equal(state.requestFailures.length, 0, 'bootstrap transport must complete')
    state.pageErrors = []
    state.consoleErrors = []
    await page.reload()
    await page.getByRole('row').filter({ hasText: accounts[0].name }).waitFor()
    assert.ok(await page.locator('link[rel=stylesheet]').count(), 'real application CSS loaded')
    state.scenarios.push({ name: stage, status: 'passed', entry: '/admin/accounts' })

    const dialog = () => page.getByRole('dialog').filter({ has: page.getByTestId('question-select') })
    async function openAccount(account) {
      const row = page.getByRole('row').filter({ hasText: account.name })
      const more = row.getByRole('button', { name: 'More', exact: true })
      // AccountsView closes its menu on table/window scroll. Let Playwright's
      // scroll-to-button finish before opening the menu. Only menu navigation
      // may be repeated; model submissions below are never retried.
      for (let attempt = 0; attempt < 3; attempt++) {
        await more.scrollIntoViewIfNeeded()
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
        await more.click()
        try {
          await page.locator('.action-menu-content').getByRole('button', { name: 'Intelligence test', exact: true }).click({ timeout: 5000 })
          break
        } catch (error) {
          if (await dialog().isVisible()) break
          if (attempt === 2 || await page.locator('.action-menu-content').isVisible()) throw error
          state.menuReopens = (state.menuReopens || 0) + 1
        }
      }
      await dialog().getByTestId('question-select').getByRole('button').click()
      await page.getByRole('option', { name: 'Pelican riding a bicycle (HTML)', exact: true }).click()
    }
    async function readRecords(account) {
      return page.evaluate(id => JSON.parse(localStorage.getItem(`sub2api-pelican-test:${id}`) || '[]'), account.id)
    }
    async function checkSnapshot(root, config) {
      const snapshot = root.getByTestId('pelican-question-snapshot')
      await snapshot.locator('summary').click()
      await snapshot.locator('pre').waitFor({ state: 'visible' })
      assert.equal(await snapshot.locator('pre').innerText(), config.actual_prompt)
      assert.ok((await snapshot.innerText()).includes(config.pelican_question.round_id))
      assert.ok((await snapshot.locator('summary').innerText()).includes(config.pelican_question.id))
      assert.ok((await snapshot.locator('summary').innerText()).includes(config.pelican_question.version))
    }
    async function runSamples(account, config) {
      if (!(await dialog().getByTestId('manual-model-input').isVisible())) {
        await dialog().getByTestId('model-input-toggle').click()
      }
      await dialog().getByTestId('manual-model-input').fill(model)
      await dialog().locator('input[type=number]').fill('2')
      await dialog().getByRole('button', { name: 'Start test', exact: true }).click()
      await page.waitForFunction(id => {
        const records = JSON.parse(localStorage.getItem(`sub2api-pelican-test:${id}`) || '[]')
        return records[0]?.runs?.length === 2 && records[0].runs.every(run => run.status === 'success')
      }, account.id, { timeout: 60_000 })
      const record = (await readRecords(account))[0]
      assert.deepEqual(record.pelicanQuestion, config.pelican_question)
      assert.equal(record.actualPrompt, config.actual_prompt)
      for (const run of record.runs) assert.match(run.output, /fixture795-\d+/)
      return record
    }

    stage = 'manual-same-round-a-b-two-samples'
    await openAccount(accounts[0])
    const prepare = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/admin/accounts/pelican-question')
    await dialog().getByTestId('pelican-mode-select').selectOption('random')
    const prepared = await prepare
    assert.equal(prepared.status(), 200)
    const config = unwrap(await prepared.json())
    assert.match(config.pelican_question.round_id, /^manual:[a-f0-9]{32}$/)
    await checkSnapshot(dialog(), config)
    const aRecord = await runSamples(accounts[0], config)
    await page.screenshot({ path: join(evidence, 'manual-a-same-round.png'), fullPage: true })
    await dialog().getByRole('button', { name: 'Close modal', exact: true }).click()
    await openAccount(accounts[1])
    await dialog().getByTestId('pelican-mode-select').selectOption('random')
    await checkSnapshot(dialog(), config)
    const bRecord = await runSamples(accounts[1], config)
    assert.equal(prepareCount, 1, 'account B reuses the prepared round')
    assert.equal(manualRequests.length, 4)
    for (const account of accounts) assert.equal(manualRequests.filter(request => request.account_id === account.id).length, 2)
    for (const request of manualRequests) {
      assert.equal(request.prompt_mode, 'random')
      assert.equal(request.model_id, model)
      assert.equal(request.prompt, config.actual_prompt)
      assert.deepEqual(request.pelican_question, config.pelican_question)
    }
    const captured = await (await fetch(`${upstreamOrigin}/__fixture/captures`)).json()
    assert.deepEqual(captured.rejected, [])
    assert.equal(captured.captures.length, 4)
    assert.equal(new Set([aRecord, bRecord].flatMap(record => record.runs.map(run => run.output.match(/fixture795-\d+/)[0]))).size, 4)
    for (const alias of ['a', 'b']) assert.equal(captured.captures.filter(capture => capture.alias === alias).length, 2)
    for (const [index, record] of [aRecord, bRecord].entries()) {
      for (const run of record.runs) {
        const marker = run.output.match(/fixture795-\d+/)[0]
        const sent = captured.captures.find(capture => capture.marker === marker)
        assert.ok(sent, 'each real localStorage output links to one provider capture')
        assert.equal(sent.alias, index === 0 ? 'a' : 'b')
        assert.equal(sent.model, model)
        // This fixture's transport normalization boundary is UTF-8 text after
        // the real service's TrimSpace. No claim about arbitrary input strings.
        assert.equal(sent.prompt, config.actual_prompt.trim())
        assert.equal(sent.refused, false)
      }
    }
    save('manual-api-projection.json', { prepared: config, requests: manualRequests })
    save('manual-history-projection.json', { [accounts[0].id]: aRecord, [accounts[1].id]: bRecord })
    state.scenarios.push({ name: stage, status: 'passed', samples: 4, round_id: config.pelican_question.round_id,
      persistence: 'localStorage only; no manual PostgreSQL result claim' })

    stage = 'manual-next-round-identity'
    const next = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/admin/accounts/pelican-question')
    await dialog().getByTestId('pelican-next-round').click()
    const nextResponse = await next
    assert.equal(nextResponse.status(), 200)
    const nextConfig = unwrap(await nextResponse.json())
    assert.notEqual(nextConfig.pelican_question.round_id, config.pelican_question.round_id)
    // The random bank can legitimately choose the same question again.
    save('manual-next-round.json', nextConfig)
    state.scenarios.push({ name: stage, status: 'passed', question_repetition_permitted: true })

    stage = 'manual-localstorage-reload-history-dom'
    await page.reload()
    assert.deepEqual((await readRecords(accounts[0]))[0], aRecord)
    assert.deepEqual((await readRecords(accounts[1]))[0], bRecord)
    await openAccount(accounts[1])
    await dialog().getByTestId('open-records').click()
    const panel = page.getByTestId('records-panel')
    await panel.getByTestId('pelican-record-card').first().waitFor()
    assert.equal(await panel.getByTestId('pelican-record-card').count(), 2)
    await panel.getByTestId('pelican-record-card').first().getByRole('button').click()
    const detail = page.getByTestId('record-detail')
    await checkSnapshot(detail, config)
    const body = await detail.frameLocator('iframe').locator('body').innerText()
    assert.ok(bRecord.runs.some(run => body.includes(run.output.match(/fixture795-\d+/)[0])))
    await page.screenshot({ path: join(evidence, 'manual-history-reloaded.png'), fullPage: true })
    state.scenarios.push({ name: stage, status: 'passed', cards: 2 })

    stage = 'disabled-group-plan-real-ui-api-pg'
    await detail.getByRole('button', { name: 'Close', exact: true }).click()
    await panel.getByRole('button', { name: 'Close', exact: true }).click()
    await dialog().getByRole('button', { name: 'Close modal', exact: true }).click()
    await page.goto(`${origin}/admin/pelican-tests`)
    await page.getByTestId('pelican-tests-create').click()
    const editor = page.getByTestId('pelican-test-editor')
    await editor.getByRole('button').first().click()
    await page.getByRole('option', { name: /fixture-pelican-group/ }).click()
    await editor.getByTestId('pelican-test-model').fill(model)
    await editor.getByTestId('pelican-mode-select').selectOption('random')
    await editor.locator('#pelican-test-parallel').fill('2')
    const enabled = editor.getByTestId('pelican-test-enabled')
    if (await enabled.getAttribute('aria-checked') === 'true') await enabled.click()
    const creation = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/admin/pelican-group-tests' && response.request().method() === 'POST')
    await page.getByTestId('pelican-test-save').click()
    const createdResponse = await creation
    assert.equal(createdResponse.status(), 200)
    const created = unwrap(await createdResponse.json())
    const { rows } = await db.query('SELECT id::text, group_id::text, model_id, enabled, pelican_config FROM pelican_group_test_plans WHERE id = $1', [created.id])
    assert.equal(rows.length, 1)
    assert.equal(rows[0].group_id, groups[0].id)
    assert.equal(rows[0].model_id, model)
    assert.equal(rows[0].enabled, false)
    assert.equal(rows[0].pelican_config.prompt_mode, 'random')
    assert.equal(rows[0].pelican_config.parallel_count, 2)
    await page.getByTestId(`pelican-plan-${created.id}`).waitFor()
    save('disabled-group-plan-projection.json', { api: { id: created.id, group_id: created.group_id,
      model_id: created.model_id, enabled: created.enabled, pelican_config: created.pelican_config }, pg: rows[0] })
    await page.screenshot({ path: join(evidence, 'group-plan-created-disabled.png'), fullPage: true })
    state.scenarios.push({ name: stage, status: 'passed', plan_id: created.id,
      boundary: 'disabled creation only; no scheduled RunDue, /run or result acceptance claimed' })

    stage = 'account-scheduled-ui-create-a-b'
    await page.goto(`${origin}/admin/accounts`)
    const plans = []
    const planProjection = plan => ({ id: String(plan.id), account_id: String(plan.account_id),
      model_id: plan.model_id, enabled: plan.enabled, cron_expression: plan.cron_expression,
      pelican_config: plan.pelican_config })
    for (const account of accounts) {
      await openAccount(account)
      await dialog().getByRole('button', { name: 'Scheduled tests', exact: true }).click()
      await dialog().getByRole('button', { name: 'Add Plan', exact: true }).click()
      const form = dialog().locator('div.rounded-xl').filter({ has: page.getByPlaceholder('*/30 * * * *', { exact: true }) })
      assert.equal(await form.count(), 1)
      await form.locator('input[type=text]').first().fill(model)
      // A far future real schedule prevents an accidental tick between the two
      // UI creations; only next_run_at is aligned below, not enabled or config.
      await form.getByPlaceholder('*/30 * * * *', { exact: true }).fill('0 0 1 1 *')
      await form.getByTestId('pelican-mode-select').selectOption('random')
      await form.locator('input[type=number]').last().fill('2')
      const creation = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/admin/scheduled-test-plans' && response.request().method() === 'POST')
      await form.getByRole('button', { name: 'Save', exact: true }).click()
      const response = await creation
      assert.equal(response.status(), 200)
      const plan = planProjection(unwrap(await response.json()))
      assert.equal(plan.account_id, account.id)
      assert.equal(plan.enabled, true)
      assert.equal(plan.model_id, model)
      assert.equal(plan.cron_expression, '0 0 1 1 *')
      assert.equal(plan.pelican_config.question_kind, 'pelican')
      assert.equal(plan.pelican_config.prompt_mode, 'random')
      assert.equal(plan.pelican_config.parallel_count, 2)
      assert.ok(!plan.pelican_config.pelican_question, 'plan is a rule, not an injected frozen question')
      plans.push(plan)
      await form.waitFor({ state: 'hidden' })
      await dialog().getByRole('button', { name: 'Close modal', exact: true }).click()
    }
    const planIds = plans.map(plan => plan.id)
    assert.equal(new Set(planIds).size, 2)
    const planSQL = `SELECT id::text, account_id::text, model_id, enabled, cron_expression, pelican_config,
      to_char(next_run_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS next_run_at,
      running_until::text, last_run_at::text, running_until > now() AS lease_active
      FROM scheduled_test_plans WHERE id = ANY($1::bigint[]) ORDER BY id`
    const resultSQL = `SELECT r.id::text, r.plan_id::text, p.account_id::text, r.status, r.error_message, r.pelican_config,
      substring(r.response_text from 'fixture795-[0-9]+') AS marker
      FROM scheduled_test_results r JOIN scheduled_test_plans p ON p.id = r.plan_id
      WHERE r.plan_id = ANY($1::bigint[]) ORDER BY r.id`
    const readPlans = async () => (await db.query(planSQL, [planIds])).rows
    const readResults = async () => (await db.query(resultSQL, [planIds])).rows
    const initialPlans = await readPlans()
    assert.equal(initialPlans.length, 2)
    for (const plan of initialPlans) {
      assert.deepEqual(planProjection(plan), plans.find(item => item.id === plan.id))
      assert.equal(plan.running_until, null)
      assert.equal(plan.last_run_at, null)
      assert.ok(Date.parse(plan.next_run_at) > Date.now(), 'fresh plans must not yet be due')
    }
    assert.deepEqual(await readResults(), [])
    save('account-scheduled-created.json', { api: plans, pg: initialPlans })
    state.scenarios.push({ name: stage, status: 'passed', plans: planIds, samples_per_account: 2 })

    stage = 'account-scheduled-nominal-time-and-real-claim'
    const fixtureControl = async path => {
      const response = await fetch(`${upstreamOrigin}/__fixture/${path}`, { method: 'POST', signal: AbortSignal.timeout(5000) })
      assert.equal(response.status, 200)
      return response.json()
    }
    const capturesNow = async () => {
      const response = await fetch(`${upstreamOrigin}/__fixture/captures`, { signal: AbortSignal.timeout(5000) })
      assert.equal(response.status, 200)
      return response.json()
    }
    const beforeScheduled = await capturesNow()
    assert.equal(beforeScheduled.captures.length, 4, 'only previous manual samples exist')
    assert.deepEqual(beforeScheduled.rejected, [])
    const captureStart = beforeScheduled.captures.length
    await fixtureControl('hold-scheduled')
    let repliesReleased = false
    try {
      // The only SQL mutation after entity seeding. Bind both UI-created plan
      // IDs to their exact fictional accounts, require untouched rules, and
      // change only their nominal due time in one atomic statement.
      const due = await db.query(`WITH nominal AS (
          SELECT date_trunc('second', now()) - interval '1 second' + interval '0.123456 second' AS t
        ) UPDATE scheduled_test_plans p SET next_run_at = nominal.t
        FROM nominal, accounts a
        WHERE p.account_id = a.id AND a.deleted_at IS NULL
          AND ((p.id = $1::bigint AND a.id = $2::bigint AND a.name = 'fixture-pelican-a')
            OR (p.id = $3::bigint AND a.id = $4::bigint AND a.name = 'fixture-pelican-b'))
          AND p.enabled AND p.model_id = $5 AND p.cron_expression = '0 0 1 1 *'
          AND p.pelican_config->>'prompt_mode' = 'random'
          AND p.pelican_config->>'parallel_count' = '2'
          AND p.running_until IS NULL AND p.last_run_at IS NULL AND p.next_run_at > now()
          AND NOT EXISTS (SELECT 1 FROM scheduled_test_results r WHERE r.plan_id = p.id)
        RETURNING p.id::text, to_char(p.next_run_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS nominal_time`,
      [plans[0].id, accounts[0].id, plans[1].id, accounts[1].id, model])
      assert.equal(due.rowCount, 2)
      assert.equal(due.rows[0].nominal_time, due.rows[1].nominal_time)
      const nominalTime = due.rows[0].nominal_time
      save('account-scheduled-due.json', due.rows)

      const deadline = Date.now() + 90_000 // includes one real minute-tick wait
      let claimed
      while (Date.now() < deadline) {
        const currentPlans = await readPlans()
        const currentCaptures = await capturesNow()
        assert.deepEqual(currentCaptures.rejected, [])
        const sent = currentCaptures.captures.slice(captureStart)
        assert.ok(sent.length <= 4, 'no duplicate scheduled submissions')
        save('account-scheduled-wait.json', { nominal_time: nominalTime, plans: currentPlans, received_samples: sent.length })
        if (sent.length === 4 && currentPlans.length === 2 && currentPlans.every(plan => plan.lease_active)) {
          assert.deepEqual(await readResults(), [], 'held upstream replies cannot yet yield result rows')
          claimed = currentPlans
          break
        }
        await new Promise(resolve => setTimeout(resolve, 250))
      }
      assert.ok(claimed, 'real runner must claim both plans and send four samples within 90 seconds')
      for (const plan of claimed) assert.ok(Date.parse(plan.next_run_at) > Date.parse(nominalTime))
      save('account-scheduled-claimed.json', { nominal_time: nominalTime, plans: claimed })
      const release = await fixtureControl('release-scheduled')
      repliesReleased = true
      assert.equal(release.released, 4)

      stage = 'account-scheduled-pg-provider-reconciliation'
      let scheduledResults
      const resultDeadline = Date.now() + 30_000
      while (Date.now() < resultDeadline) {
        const rows = await readResults()
        assert.ok(rows.length <= 4, 'exactly four scheduled results are expected')
        if (rows.length === 4) { scheduledResults = rows; break }
        await new Promise(resolve => setTimeout(resolve, 250))
      }
      assert.ok(scheduledResults, 'real service must persist all four results')
      const provider = await capturesNow()
      assert.deepEqual(provider.rejected, [])
      const sent = provider.captures.slice(captureStart)
      assert.equal(sent.length, 4)
      assert.equal(new Set(scheduledResults.map(row => row.marker)).size, 4)
      const snapshot = scheduledResults[0].pelican_config
      assert.equal(snapshot.prompt_mode, 'random')
      assert.equal(snapshot.parallel_count, 2)
      assert.equal(snapshot.pelican_question.source, 'builtin:svg-motion')
      assert.ok(snapshot.pelican_question.id && snapshot.pelican_question.version && snapshot.actual_prompt)
      assert.equal(snapshot.pelican_question.round_id, `scheduled:${nominalTime}`)
      for (const [index, plan] of plans.entries()) {
        const results = scheduledResults.filter(row => row.plan_id === plan.id)
        assert.equal(results.length, 2)
        for (const result of results) {
          assert.equal(result.account_id, plan.account_id)
          assert.equal(result.status, 'success')
          assert.match(result.marker, /^fixture795-\d+$/)
          assert.deepEqual(result.pelican_config, snapshot, 'all four samples freeze the same rule and question')
          assert.equal(result.pelican_config.trigger_source, 'scheduled')
          const capture = sent.find(item => item.marker === result.marker)
          assert.ok(capture, 'PG marker must identify an actual provider request')
          assert.equal(capture.alias, index === 0 ? 'a' : 'b')
          assert.equal(capture.model, model)
          assert.equal(capture.prompt, result.pelican_config.actual_prompt.trim())
          assert.equal(capture.refused, false)
        }
      }
      save('account-scheduled-results.json', { nominal_time: nominalTime, pg: scheduledResults, provider: sent })
      state.scenarios.push({ name: 'account-scheduled-nominal-time-and-real-claim', status: 'passed', nominal_time: nominalTime, plans: planIds })
      state.scenarios.push({ name: stage, status: 'passed', result_ids: scheduledResults.map(row => row.id) })

      stage = 'account-scheduled-history-detail-dom'
      for (const account of accounts) {
        await openAccount(account)
        const expected = scheduledResults.filter(row => row.account_id === account.id)
        // Read responses caused by the actual page. No independent token reads,
        // API request substitutes, fabricated store state or fulfilled routes.
        const details = new Map()
        const pending = []
        const detailErrors = []
        const observeDetail = response => {
          const path = new URL(response.url()).pathname
          const match = path.match(/^\/api\/v1\/admin\/scheduled-test-plans\/(\d+)\/results\/(\d+)$/)
          if (!match || !expected.some(row => row.plan_id === match[1] && row.id === match[2])) return
          pending.push((async () => {
            assert.equal(response.status(), 200)
            const value = unwrap(await response.json())
            const binding = bindDetailResponse(response.request().url(), value, expected)
            const record = { status: response.status(), value, binding }
            details.set(binding.request_result_id, record)
            return record
          })().catch(error => {
            detailErrors.push({ name: error.name, message: error.message })
            return { read_error: error.name }
          }))
        }
        page.on('response', observeDetail)
        try {
          const historyResponse = page.waitForResponse(response => {
            const url = new URL(response.url())
            return url.pathname === '/api/v1/admin/pelican-test-results' && url.searchParams.get('account_id') === account.id
          })
          await dialog().getByTestId('open-records').click()
          const history = await historyResponse
          assert.equal(history.status(), 200)
          const historyBody = unwrap(await history.json())
          assert.equal(historyBody.next_cursor, 0)
          assert.equal(historyBody.items.length, 2)
          for (const row of expected) {
            const item = historyBody.items.find(item => String(item.id) === row.id)
            assert.ok(item)
            assert.equal(String(item.plan_id), row.plan_id)
            assert.equal(String(item.account_id), account.id)
            assert.equal(item.status, row.status)
            assert.deepEqual(item.pelican_config, row.pelican_config)
          }
          const panel = page.getByTestId('records-panel')
          // Validate URL -> body -> PG for every response BEFORE selecting any
          // marker-based card. Swapped bodies must fail before a screenshot can
          // be mislabeled. The map is keyed only by validated request identity.
          const detailDeadline = Date.now() + 20_000
          while (details.size < expected.length && detailErrors.length === 0 && Date.now() < detailDeadline) {
            await new Promise(resolve => setTimeout(resolve, 50))
          }
          assert.deepEqual(detailErrors, [], 'detail URL/body/PG identity validation must pass before DOM binding')
          assert.equal(details.size, expected.length)
          for (const row of expected) {
            const { binding } = details.get(row.id)
            assert.equal(binding.request_plan_id, row.plan_id)
            assert.equal(binding.request_result_id, row.id)
            assert.equal(binding.marker, row.marker)
            const card = panel.getByTestId('pelican-record-card').filter({ has: page.locator(`iframe[srcdoc*="${binding.marker}"]`) })
            await card.waitFor({ state: 'visible' })
            assert.equal(await card.count(), 1)
            await card.getByRole('button').click()
            const detail = page.getByTestId('record-detail')
            await checkSnapshot(detail, row.pelican_config)
            assert.equal(await detail.frameLocator('iframe').locator('p').innerText(), row.marker)
            await page.screenshot({ path: join(evidence, `account-scheduled-${account.id}-${binding.request_result_id}.png`), fullPage: true })
            await detail.getByRole('button', { name: 'Close', exact: true }).click()
          }
          await panel.getByRole('button', { name: 'Close', exact: true }).click()
          for (const response of await Promise.all(pending)) {
            assert.equal(response.read_error, undefined, 'real detail response must be readable')
            assert.equal(response.status, 200)
            // Includes duplicate/poll responses: each must retain its own URL
            // binding, rather than trusting an unordered set of body IDs.
            assert.deepEqual(response.binding, bindDetailResponse(`http://app:8080${response.binding.request_path}`, response.value, expected))
          }
          assert.deepEqual(detailErrors, [], 'all observed detail responses must keep their URL identity')
          assert.equal(details.size, 2)
          for (const row of expected) {
            const api = details.get(row.id)?.value
            assert.ok(api)
            assert.equal(String(api.plan_id), row.plan_id)
            assert.equal(api.status, row.status)
            assert.deepEqual(api.pelican_config, row.pelican_config)
            assert.equal(api.response_text.match(/fixture795-\d+/)?.[0], row.marker)
          }
          save(`account-scheduled-history-${account.id}.json`, {
            history: historyBody.items.map(item => ({ id: item.id, plan_id: item.plan_id, account_id: item.account_id,
              status: item.status, pelican_config: item.pelican_config })),
            detail: [...details.values()].map(({ value: item, binding }) => ({ id: item.id, plan_id: item.plan_id,
              status: item.status, pelican_config: item.pelican_config, marker: binding.marker, request_binding: binding })),
            expanded_dom: expected.map(row => ({ result_id: row.id, marker: row.marker, snapshot: row.pelican_config,
              request_binding: details.get(row.id).binding })),
          })
        } finally {
          page.off('response', observeDetail)
        }
        await dialog().getByRole('button', { name: 'Close modal', exact: true }).click()
      }
      assert.deepEqual(await readResults(), scheduledResults, 'reading history must not change saved results')
      assert.deepEqual(forbiddenRuns, [], 'no manual /run may stand in for scheduled execution')
      assert.equal(manualRequests.length, 4, 'no new manual submission may stand in for scheduled execution')
      state.scenarios.push({ name: stage, status: 'passed', result_ids: scheduledResults.map(row => row.id) })
    } finally {
      // On assertion failure unblock the provider, but retain the original error
      // and leave all database rows to the application / disposable CI cleanup.
      if (!repliesReleased) {
        try { await fixtureControl('release-scheduled') }
        catch (error) { save('account-scheduled-release-error.json', { name: error.name }) }
      }
    }

    stage = 'group-scheduled-ui-enable'
    await page.goto(`${origin}/admin/pelican-tests`)
    const groupPlanId = String(created.id)
    const groupId = groups[0].id
    const groupPlanPath = `/api/v1/admin/pelican-group-tests/${groupPlanId}`
    // Keep the already-tested disabled creation intact. Edit only its schedule
    // through the real form before enabling, to avoid an unintended due tick.
    await page.getByTestId(`pelican-plan-edit-${groupPlanId}`).click()
    const groupEditor = page.getByTestId('pelican-test-editor')
    assert.equal(await groupEditor.getByTestId('pelican-test-enabled').getAttribute('aria-checked'), 'false')
    await groupEditor.getByTestId('pelican-test-schedule').getByRole('button').click()
    await page.getByRole('option', { name: 'Custom', exact: true }).click()
    await groupEditor.getByTestId('pelican-test-cron').fill('0 0 1 1 *')
    const groupEditResponse = page.waitForResponse(response => new URL(response.url()).pathname === groupPlanPath && response.request().method() === 'PUT')
    await page.getByTestId('pelican-test-save').click()
    const groupEdited = await groupEditResponse
    assert.equal(groupEdited.status(), 200)
    const groupEditedBody = unwrap(await groupEdited.json())
    assert.equal(groupEditedBody.enabled, false)
    assert.equal(groupEditedBody.cron_expression, '0 0 1 1 *')
    await groupEditor.waitFor({ state: 'hidden' })
    // savePlan closes the editor before its list reload completes. Wait until
    // the actual row carries the new schedule, so toggle cannot resend stale input.
    await page.getByTestId(`pelican-plan-${groupPlanId}`).locator('.plan-meta').getByText('0 0 1 1 *', { exact: true }).waitFor()
    const groupEnableResponse = page.waitForResponse(response => new URL(response.url()).pathname === groupPlanPath && response.request().method() === 'PUT')
    await page.getByTestId(`pelican-plan-toggle-${groupPlanId}`).click()
    const groupEnabled = await groupEnableResponse
    assert.equal(groupEnabled.status(), 200)
    const groupEnabledBody = unwrap(await groupEnabled.json())
    assert.equal(String(groupEnabledBody.id), groupPlanId)
    assert.equal(String(groupEnabledBody.group_id), groupId)
    assert.equal(groupEnabledBody.enabled, true)
    assert.equal(groupEnabledBody.model_id, model)
    assert.equal(groupEnabledBody.cron_expression, '0 0 1 1 *')
    assert.deepEqual(groupEnabledBody.pelican_config, groupEditedBody.pelican_config)
    assert.equal(groupEnabledBody.pelican_config.prompt_mode, 'random')
    assert.equal(groupEnabledBody.pelican_config.parallel_count, 2)
    assert.ok(!groupEnabledBody.pelican_config.pelican_question)
    await page.getByTestId(`pelican-plan-${groupPlanId}`).getByRole('button', { name: 'Pause', exact: true }).waitFor()
    const groupPlanSQL = `SELECT p.id::text, p.group_id::text, p.enabled, p.model_id,
      p.cron_expression, p.pelican_config,
      to_char(p.next_run_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS next_run_at,
      p.running_until::text, p.last_run_at::text, p.running_until > now() AS lease_active
      FROM pelican_group_test_plans p JOIN groups g ON g.id = p.group_id
      WHERE p.id = $1::bigint AND g.id = $2::bigint AND g.name = 'fixture-pelican-group'`
    const groupResultSQL = `SELECT r.id::text, r.plan_id::text, p.group_id::text,
      r.account_id::text, r.account_name, r.status, r.error_message, r.pelican_config,
      jsonb_array_length(r.attempts) AS attempt_count,
      substring(r.response_text from 'fixture795-[0-9]+') AS marker
      FROM pelican_group_test_results r JOIN pelican_group_test_plans p ON p.id = r.plan_id
      WHERE r.plan_id = $1::bigint AND p.group_id = $2::bigint ORDER BY r.id`
    const readGroupPlan = async () => {
      const { rows } = await db.query(groupPlanSQL, [groupPlanId, groupId])
      assert.equal(rows.length, 1)
      return rows[0]
    }
    const readGroupResults = async () => (await db.query(groupResultSQL, [groupPlanId, groupId])).rows
    const enabledGroupPlan = await readGroupPlan()
    assert.equal(enabledGroupPlan.enabled, true)
    assert.equal(enabledGroupPlan.model_id, model)
    assert.equal(enabledGroupPlan.cron_expression, '0 0 1 1 *')
    assert.deepEqual(enabledGroupPlan.pelican_config, groupEnabledBody.pelican_config)
    assert.equal(enabledGroupPlan.last_run_at, null)
    assert.equal(enabledGroupPlan.running_until, null)
    assert.ok(Date.parse(enabledGroupPlan.next_run_at) > Date.now())
    assert.deepEqual(await readGroupResults(), [])
    save('group-scheduled-enabled.json', {
      api: { id: groupEnabledBody.id, group_id: groupEnabledBody.group_id, enabled: groupEnabledBody.enabled,
        model_id: groupEnabledBody.model_id, cron_expression: groupEnabledBody.cron_expression,
        pelican_config: groupEnabledBody.pelican_config }, pg: enabledGroupPlan,
    })
    state.scenarios.push({ name: stage, status: 'passed', plan_id: groupPlanId, group_id: groupId })

    stage = 'group-scheduled-due-and-real-claim'
    const beforeGroup = await capturesNow()
    assert.deepEqual(beforeGroup.rejected, [])
    assert.equal(beforeGroup.captures.length, 8, 'only manual and account-scheduled samples precede this round')
    const groupCaptureStart = beforeGroup.captures.length
    await fixtureControl('hold-scheduled')
    let groupRepliesReleased = false
    try {
      // Only this fixture plan's nominal due field is changed. Enabling and
      // schedule configuration above went through the actual authenticated UI.
      const due = await db.query(`WITH nominal AS (
          SELECT date_trunc('second', now()) - interval '1 second' + interval '0.654321 second' AS t
        ) UPDATE pelican_group_test_plans p SET next_run_at = nominal.t
        FROM nominal, groups g
        WHERE p.id = $1::bigint AND p.group_id = $2::bigint AND g.id = p.group_id
          AND g.name = 'fixture-pelican-group' AND g.deleted_at IS NULL AND g.status = 'active'
          AND p.enabled AND p.model_id = $3 AND p.cron_expression = '0 0 1 1 *'
          AND p.pelican_config->>'prompt_mode' = 'random'
          AND p.pelican_config->>'parallel_count' = '2'
          AND p.running_until IS NULL AND p.last_run_at IS NULL AND p.next_run_at > now()
          AND NOT EXISTS (SELECT 1 FROM pelican_group_test_results r WHERE r.plan_id = p.id)
        RETURNING p.id::text, to_char(p.next_run_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS nominal_time`,
      [groupPlanId, groupId, model])
      assert.equal(due.rowCount, 1)
      const groupNominalTime = due.rows[0].nominal_time
      save('group-scheduled-due.json', due.rows[0])
      let groupClaim
      const claimDeadline = Date.now() + 90_000
      while (Date.now() < claimDeadline) {
        const plan = await readGroupPlan()
        const provider = await capturesNow()
        assert.deepEqual(provider.rejected, [])
        const sent = provider.captures.slice(groupCaptureStart)
        assert.ok(sent.length <= 2, 'group success fixture must not duplicate or retry submissions')
        save('group-scheduled-wait.json', { nominal_time: groupNominalTime, plan, received_samples: sent.length })
        if (sent.length === 2 && plan.lease_active) {
          assert.deepEqual(await readGroupResults(), [], 'held provider replies cannot yet yield group results')
          assert.ok(Date.parse(plan.next_run_at) > Date.parse(groupNominalTime))
          groupClaim = plan
          break
        }
        await new Promise(resolve => setTimeout(resolve, 250))
      }
      assert.ok(groupClaim, 'real group RunDue/Claim must send two samples within 90 seconds')
      save('group-scheduled-claimed.json', { nominal_time: groupNominalTime, plan: groupClaim })
      const release = await fixtureControl('release-scheduled')
      groupRepliesReleased = true
      assert.equal(release.released, 2)
      state.scenarios.push({ name: stage, status: 'passed', plan_id: groupPlanId, nominal_time: groupNominalTime })

      stage = 'group-scheduled-pg-provider-reconciliation'
      let groupResults
      let completedGroupPlan
      const resultsDeadline = Date.now() + 30_000
      while (Date.now() < resultsDeadline) {
        const results = await readGroupResults()
        const plan = await readGroupPlan()
        assert.ok(results.length <= 2)
        if (results.length === 2 && plan.running_until === null && plan.last_run_at !== null) {
          groupResults = results
          completedGroupPlan = plan
          break
        }
        await new Promise(resolve => setTimeout(resolve, 250))
      }
      assert.ok(groupResults, 'two real group results and successful-run lease completion are required')
      assert.ok(Date.parse(completedGroupPlan.next_run_at) > Date.parse(groupNominalTime))
      const groupProvider = await capturesNow()
      assert.deepEqual(groupProvider.rejected, [])
      const groupSent = groupProvider.captures.slice(groupCaptureStart)
      assert.equal(groupSent.length, 2)
      assert.equal(new Set(groupResults.map(row => row.marker)).size, 2)
      const groupSnapshot = groupResults[0].pelican_config
      assert.equal(groupSnapshot.prompt_mode, 'random')
      assert.equal(groupSnapshot.parallel_count, 2)
      assert.equal(groupSnapshot.model_id, model)
      assert.equal(groupSnapshot.pelican_question.round_id, `scheduled:${groupNominalTime}`)
      assert.equal(groupSnapshot.pelican_question.source, 'builtin:svg-motion')
      assert.ok(groupSnapshot.pelican_question.id && groupSnapshot.pelican_question.version && groupSnapshot.actual_prompt)
      for (const row of groupResults) {
        assert.equal(row.plan_id, groupPlanId)
        assert.equal(row.group_id, groupId)
        assert.equal(row.status, 'success')
        assert.equal(row.attempt_count, 0)
        assert.match(row.marker, /^fixture795-\d+$/)
        assert.deepEqual(row.pelican_config, groupSnapshot)
        const accountIndex = accounts.findIndex(account => account.id === row.account_id)
        assert.ok(accountIndex >= 0, 'gateway must route to a fixture account')
        assert.equal(row.account_name, accounts[accountIndex].name)
        const capture = groupSent.find(item => item.marker === row.marker)
        assert.ok(capture)
        assert.equal(capture.alias, accountIndex === 0 ? 'a' : 'b')
        assert.equal(capture.model, model)
        assert.equal(capture.prompt, row.pelican_config.actual_prompt.trim())
        assert.equal(capture.refused, false)
        // Both samples may legitimately choose the same account. No balance or
        // equal-route assertion: record and reconcile the actual chosen account.
      }
      save('group-scheduled-results.json', { nominal_time: groupNominalTime, plan: completedGroupPlan, pg: groupResults, provider: groupSent })
      state.scenarios.push({ name: stage, status: 'passed', plan_id: groupPlanId, result_ids: groupResults.map(row => row.id) })

      stage = 'group-scheduled-list-detail-dom'
      // PelicanTestsView polls its real list API. Wait for a poll containing this
      // run, without injecting a store update or issuing a substitute request.
      const listed = await page.waitForResponse(async response => {
        if (new URL(response.url()).pathname !== '/api/v1/admin/pelican-group-test-results'
          || response.request().method() !== 'GET' || response.status() !== 200) return false
        const body = unwrap(await response.json())
        return Array.isArray(body.items) && groupResults.every(row => body.items.some(item => String(item.id) === row.id))
      }, { timeout: 25_000 })
      const list = unwrap(await listed.json())
      assert.equal(list.total, 2)
      assert.equal(list.items.length, 2)
      const groupAPIProjection = item => ({ id: String(item.id), plan_id: String(item.plan_id),
        group_id: String(item.group_id), account_id: String(item.account_id), account_name: item.account_name,
        status: item.status, error_message: item.error_message, pelican_config: item.pelican_config, attempt_count: item.attempts.length })
      const expectedAPIProjection = row => {
        const { marker, ...projection } = row
        return projection
      }
      const detailProjection = []
      const domProjection = []
      for (const row of groupResults) {
        const item = list.items.find(item => String(item.id) === row.id)
        assert.deepEqual(groupAPIProjection(item), expectedAPIProjection(row))
        assert.equal(item.group_name, groups[0].name)
        const domRow = page.getByTestId(`pelican-result-${row.id}`)
        await domRow.waitFor({ state: 'visible' })
        assert.equal(await domRow.locator('td').nth(1).innerText(), groups[0].name)
        assert.equal(await domRow.locator('td').nth(2).innerText(), model)
        assert.equal(await domRow.locator('td').nth(3).innerText(), row.account_name)
        assert.equal(await domRow.locator('.result-badge').innerText(), 'Success')
        const detailResponse = page.waitForResponse(response => new URL(response.url()).pathname === `/api/v1/admin/pelican-group-test-results/${row.id}` && response.request().method() === 'GET')
        await page.getByTestId(`pelican-result-view-${row.id}`).click()
        const response = await detailResponse
        assert.equal(response.status(), 200)
        const full = unwrap(await response.json())
        const requestBinding = bindDetailResponse(response.request().url(), full, [row], 'group')
        assert.deepEqual(groupAPIProjection(full), expectedAPIProjection(row))
        assert.equal(full.response_text.match(/fixture795-\d+/)?.[0], row.marker)
        const preview = page.getByTestId('pelican-result-preview')
        await preview.getByTestId('pelican-artwork-preview').locator('iframe').waitFor({ state: 'visible' })
        await checkSnapshot(preview, row.pelican_config)
        assert.equal(await preview.frameLocator('iframe').locator('p').innerText(), row.marker)
        await page.screenshot({ path: join(evidence, `group-scheduled-${groupPlanId}-${row.id}.png`), fullPage: true })
        detailProjection.push({ ...groupAPIProjection(full), marker: row.marker, request_binding: requestBinding })
        domProjection.push({ result_id: row.id, plan_id: groupPlanId, group_id: groupId,
          account_id: row.account_id, marker: row.marker, snapshot: row.pelican_config, request_binding: requestBinding })
        await page.getByRole('dialog').filter({ has: preview }).getByRole('button', { name: 'Close', exact: true }).click()
        await preview.waitFor({ state: 'hidden' })
      }
      save('group-scheduled-api-dom.json', { list: list.items.map(groupAPIProjection), detail: detailProjection, expanded_dom: domProjection })
      assert.deepEqual(await readGroupResults(), groupResults, 'viewing group history must not mutate saved results')
      assert.deepEqual(forbiddenRuns, [], 'group RunDue cannot be substituted with manual /run')
      assert.equal(manualRequests.length, 4, 'group results cannot come from extra manual account tests')
      assert.equal((await capturesNow()).captures.length, groupCaptureStart + 2)
      state.scenarios.push({ name: stage, status: 'passed', plan_id: groupPlanId, result_ids: groupResults.map(row => row.id) })
    } finally {
      if (!groupRepliesReleased) {
        try { await fixtureControl('release-scheduled') }
        catch (error) { save('group-scheduled-release-error.json', { name: error.name }) }
      }
    }

    stage = 'complete-scheduled-rounds'
    await completeScheduledRounds({ db, page, origin, accounts, groups, plans, groupPlanId, groupId, model,
      dialog, openAccount, checkSnapshot, readPlans, readResults, readGroupPlan, readGroupResults,
      fixtureControl, capturesNow, save, evidence, state })

    assert.deepEqual(forbiddenRuns, [], 'all successor rounds must use real RunDue')
    assert.equal(manualRequests.length, 4, 'successor rounds cannot be manual submissions')
    stage = 'post-bootstrap-browser-and-provider-error-lists'
    const finalCaptures = await (await fetch(`${upstreamOrigin}/__fixture/captures`)).json()
    assert.deepEqual(finalCaptures.rejected, [])
    assert.equal(state.blocked.length, 0, 'browser may contact only app origin')
    assert.equal(state.pageErrors.length, 0, 'no uncaught browser errors')
    assert.equal(state.consoleErrors.length, 0, 'no browser console errors')
    assert.equal(state.requestFailures.length, 0, 'no failed browser requests')
    state.scenarios.push({ name: stage, status: 'passed' })
    console.log('Implemented manual, account and group scheduled E2E scenarios passed; full acceptance remains incomplete.')
  } catch (error) {
    state.scenarios.push({ name: stage, status: 'failed', error: { name: error.name, message: error.message.slice(0, 2000) } })
    if (page) {
      try { await page.screenshot({ path: join(evidence, 'failure.png'), fullPage: true }) } catch { /* retain original failure */ }
    }
    console.error(`Isolated E2E failed in scenario: ${stage}`)
    process.exitCode = 1
  } finally {
    save('scenario-results.json', state)
    await context?.close()
    await browser?.close()
    await db.end()
  }
}

try {
  if (mode === 'upstream') upstream()
  else if (mode === 'upstream-health') {
    const response = await fetch(`${upstreamOrigin}/health`)
    assert.equal(response.status, 200)
  } else if (mode === 'topology') topology()
  else if (mode === 'browser') await browser()
  else throw new Error('Expected upstream, upstream-health, topology or browser mode')
} catch (error) {
  // All modes operate on fictional inputs only. Never serialize request
  // objects, env, headers, database clients or browser authentication state.
  try { save('fixture-mode-error.json', { mode, name: error.name, message: error.message.slice(0, 2000) }) } catch { /* evidence directory may not exist */ }
  console.error(`Fixture mode failed: ${mode} (${error.name})`)
  process.exitCode = 1
}
