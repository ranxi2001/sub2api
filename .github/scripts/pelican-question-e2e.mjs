// Remote CI only. The sole provider substitute is the isolated HTTPS server.
// No application API fulfillment, auth-store replacement, injected questions,
// results, CSS changes, forced clicks, HAR, cookies or token exports.
import assert from 'node:assert/strict'
import { createServer } from 'node:https'
import { readFileSync, writeFileSync, appendFileSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { createRequire } from 'node:module'

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
      res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' })
      const event = data => res.write(`event: ${data.type}\ndata: ${JSON.stringify(data)}\n\n`)
      event({ type: 'message_start', message: {
        id: `msg_fixture_${sequence}`, type: 'message', role: 'assistant', model,
        content: [], stop_reason: null, usage: { input_tokens: 40, output_tokens: 0 },
      } })
      if (!refused) {
        const html = `<!doctype html><html><head><meta charset="utf-8"><title>${marker}</title></head><body><p>${marker}</p><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 300"><circle cx="120" cy="200" r="40" fill="teal"/><circle cx="280" cy="200" r="40" fill="orange"/></svg></body></html>`
        event({ type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } })
        event({ type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: html } })
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
    let prepareCount = 0
    page.on('request', request => {
      const path = new URL(request.url()).pathname
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

    stage = 'post-bootstrap-browser-and-provider-error-lists'
    const finalCaptures = await (await fetch(`${upstreamOrigin}/__fixture/captures`)).json()
    assert.deepEqual(finalCaptures.rejected, [])
    assert.equal(state.blocked.length, 0, 'browser may contact only app origin')
    assert.equal(state.pageErrors.length, 0, 'no uncaught browser errors')
    assert.equal(state.consoleErrors.length, 0, 'no browser console errors')
    assert.equal(state.requestFailures.length, 0, 'no failed browser requests')
    state.scenarios.push({ name: stage, status: 'passed' })
    console.log('Implemented initial E2E scenarios passed; full acceptance remains incomplete.')
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
