import assert from 'node:assert/strict'
import { join } from 'node:path'
import { bindDetailResponse } from './pelican-detail-identity.mjs'
import { nextDifferentNominal, questionAt, bankVersion } from './pelican-nominal-round.mjs'

const unwrap = value => value?.data ?? value
const pause = () => new Promise(resolve => setTimeout(resolve, 250))

// Metadata only: no routing, browser evaluation, timers, bodies or headers.
// Scope is the observed auth request plus this fixture's document routes.
export function createBrowserLifecycleMetadata(page, readContext, { now = () => performance.now() } = {}) {
  const paths = new Set(['/login', '/admin/accounts', '/admin/pelican-tests', '/api/v1/auth/me'])
  const stages = new Set(['browser-startup', 'normal-authentication', 'manual-same-round-a-b-two-samples',
    'manual-next-round-identity', 'manual-localstorage-reload-history-dom', 'disabled-group-plan-real-ui-api-pg',
    'account-scheduled-ui-create-a-b', 'account-scheduled-nominal-time-and-real-claim',
    'account-scheduled-pg-provider-reconciliation', 'account-scheduled-history-detail-dom',
    'group-scheduled-ui-enable', 'group-scheduled-due-and-real-claim',
    'group-scheduled-pg-provider-reconciliation', 'group-scheduled-list-detail-dom',
    'complete-scheduled-rounds', 'post-bootstrap-browser-and-provider-error-lists'])
  const gates = new Set(['second-nominal-account', 'second-nominal-group', 'deterministic-refusal-account',
    'deterministic-refusal-group', 'after-refusal-valid-round-account', 'after-refusal-valid-round-group',
    'ui-edit-account-custom', 'ui-edit-account-random', 'ui-edit-group-custom', 'ui-edit-group-random', 'edited-old-snapshots'])
  const safePath = raw => {
    try {
      const url = new URL(raw)
      return url.origin === 'http://app:8080' && paths.has(url.pathname) ? url.pathname : null
    } catch { return null }
  }
  const requests = new WeakMap()
  const events = []
  const maxEvents = 8192
  const started = now()
  const time = () => Math.round((now() - started) * 1000) / 1000
  let sequence = 0, requestID = 0, navigationID = 0, documentID = 0, nextDocumentID = 0, dropped = 0
  let documentRequest
  const scope = () => {
    const { stage, active_gate } = readContext()
    return { stage: stages.has(stage) ? stage : 'unlisted', active_gate: gates.has(active_gate) ? active_gate : null }
  }
  const record = (event, fields, at = time()) => {
    if (events.length === maxEvents) { events.shift(); dropped++ }
    events.push({ sequence: ++sequence, at_ms: at, event, ...scope(), ...fields })
  }
  page.on('request', request => {
    const path = safePath(request.url())
    if (!path) return
    const method = request.method()
    let mainFrame = null
    // Playwright may not have attached a frame yet for an early document
    // request. Unknown metadata must not interrupt the original scenario.
    try { mainFrame = request.frame() === page.mainFrame() } catch { /* keep unknown */ }
    const documentNavigation = mainFrame === true && request.isNavigationRequest()
    const context = scope()
    const data = { request_id: ++requestID, method: ['GET', 'POST'].includes(method) ? method : 'unlisted', path,
      main_frame: mainFrame, is_navigation_request: documentNavigation, navigation_id: navigationID,
      document_id: documentID, requested_document_id: documentNavigation ? ++nextDocumentID : null,
      start_ms: time(), start_stage: context.stage, start_gate: context.active_gate }
    requests.set(request, data)
    if (documentNavigation) documentRequest = data
    record('request-start', data, data.start_ms)
  })
  const complete = (request, event) => {
    const data = requests.get(request)
    if (!data || data.end_ms !== undefined) return
    Object.assign(data, { end_ms: time(), end_event: event, end_document_id: documentID, end_navigation_id: navigationID })
    record(event, { ...data }, data.end_ms)
    if (event === 'request-failed' && documentRequest === data) documentRequest = undefined
  }
  page.on('requestfinished', request => complete(request, 'request-finished'))
  page.on('requestfailed', request => complete(request, 'request-failed'))
  page.on('framenavigated', frame => {
    if (frame !== page.mainFrame()) return
    const path = safePath(frame.url())
    if (!path) return
    // Same-document router navigation also emits framenavigated. Only a
    // matching main-document request establishes a new document identity.
    const committed = documentRequest?.path === path ? documentRequest : undefined
    if (committed) { documentID = committed.requested_document_id; documentRequest = undefined }
    record(committed ? 'document-commit' : 'frame-navigation', { path, document_id: documentID,
      navigation_id: navigationID, request_id: committed?.request_id ?? null })
  })
  return {
    documentIntervalsRetired(count) {
      assert.ok(Number.isSafeInteger(count) && count >= 0)
      record('document-intervals-retired', { document_id: documentID, navigation_id: navigationID, intervals: count })
    },
    navigationIntent(action, url) {
      const path = safePath(url)
      if (!['goto', 'reload'].includes(action) || !path) return
      record('navigation-intent', { action, path, navigation_id: ++navigationID, document_id: documentID })
    },
    requestMetadata(request) { const data = requests.get(request); return data ? { ...data } : null },
    snapshot() { return { version: 1, clock: 'harness monotonic milliseconds since collector start',
      max_events: maxEvents, dropped_events: dropped, paths: [...paths], events: events.map(event => ({ ...event })) } },
  }
}

// Serialized by Playwright into each main document before application scripts.
// Stop future recurring work only when the harness is about to discard that
// document. Native callbacks, existing fetch/XHR, timeouts and their errors are
// untouched. A fresh document gets a fresh registry with normal scheduling.
function installDocumentIntervals() {
  if (globalThis.top && globalThis.top !== globalThis) return
  if (Object.hasOwn(globalThis, '__pelicanDocumentIntervals')) return
  const intervals = new Set()
  const nativeSet = globalThis.setInterval
  const nativeClear = globalThis.clearInterval
  const nativeClearTimeout = globalThis.clearTimeout
  let retired = false
  globalThis.setInterval = function (...args) {
    const id = Reflect.apply(nativeSet, globalThis, args)
    if (retired) Reflect.apply(nativeClear, globalThis, [id])
    else intervals.add(id)
    return id
  }
  globalThis.clearInterval = function (id) {
    intervals.delete(id)
    return Reflect.apply(nativeClear, globalThis, [id])
  }
  // Browser timer IDs share a pool: clearTimeout can cancel an interval too.
  globalThis.clearTimeout = function (id) {
    intervals.delete(id)
    return Reflect.apply(nativeClearTimeout, globalThis, [id])
  }
  Object.defineProperty(globalThis, '__pelicanDocumentIntervals', { value: Object.freeze({
    retire() {
      if (retired) throw new Error('cannot reuse a retired document for navigation')
      retired = true
      const count = intervals.size
      for (const id of intervals) Reflect.apply(nativeClear, globalThis, [id])
      intervals.clear()
      return count
    },
  }) })
}

function retireDocumentIntervals() {
  if (!Object.hasOwn(globalThis, '__pelicanDocumentIntervals')) throw new Error('document interval boundary was not installed')
  return globalThis.__pelicanDocumentIntervals.retire()
}

// Track real request completion across repeated document navigations. A
// previously reached lifecycle state alone cannot prevent a later auth/settings
// request from being aborted by the harness. Failures remain in the caller's
// strict requestfailed inventory; this barrier never filters or retries them.
export function createPageNavigation(page, { waitForBootstrap = () => Promise.resolve(), lifecycle } = {}) {
  const pending = new Set()
  const waiters = new Set()
  let activity = 0
  page.on('request', request => { activity++; pending.add(request) })
  const completed = request => {
    activity++
    pending.delete(request)
    if (!pending.size) for (const resolve of [...waiters]) resolve()
  }
  page.on('requestfinished', completed)
  page.on('requestfailed', completed)
  let installed
  const install = () => installed ??= (async () => {
    await page.addInitScript(installDocumentIntervals)
    // The initial about:blank document predates addInitScript.
    await page.evaluate(installDocumentIntervals)
  })()
  const settle = async (retire = false) => {
    const deadline = Date.now() + 20_000
    const remaining = () => {
      const ms = deadline - Date.now()
      if (ms <= 0) throw new Error('browser requests did not finish before navigation')
      return ms
    }
    const bounded = async operation => {
      const ms = remaining()
      let timer
      try {
        return await Promise.race([operation(), new Promise((_, reject) => {
          timer = setTimeout(() => reject(new Error('browser requests did not finish before navigation')), ms)
        })])
      } finally { clearTimeout(timer) }
    }
    if (retire) await bounded(install)
    // A completed background fetch followed by a srcdoc preview lifecycle can
    // remove networkidle without restarting its timer (Playwright 1.56.1).
    // Document load + our real completion inventory does not depend on that
    // stale frame flag. Keep one 20s deadline, including document readiness.
    await page.waitForLoadState('load', { timeout: remaining() })
    if (retire) {
      // Atomic in the old document: an interval either ran before this fence
      // (its requests must drain below), or cannot fire in the intent/commit gap.
      const count = await bounded(() => page.evaluate(retireDocumentIntervals))
      lifecycle?.documentIntervalsRetired?.(count)
    }
    for (;;) {
      while (pending.size) await new Promise((resolve, reject) => {
        const done = () => { clearTimeout(timer); waiters.delete(done); resolve() }
        const timer = setTimeout(() => {
          waiters.delete(done)
          reject(new Error('browser requests did not finish before navigation'))
        }, Math.max(0, deadline - Date.now()))
        waiters.add(done)
      })
      // load can fire before Vue schedules its auth refresh. Observe a quiet
      // interval after draining, so that deferred requests cannot be aborted
      // by the next document navigation. Keep the original total deadline.
      const ms = remaining()
      const observed = activity
      await new Promise(resolve => setTimeout(resolve, Math.min(250, ms)))
      if (!pending.size && activity === observed) return
    }
  }
  return {
    settle: () => settle(),
    async goto(url, beforeNavigate = () => {}) {
      await settle(true)
      beforeNavigate()
      lifecycle?.navigationIntent('goto', url)
      await Promise.all([waitForBootstrap(url), page.goto(url, { waitUntil: 'load' })])
      await settle()
    },
    async reload(beforeNavigate = () => {}) {
      await settle(true)
      beforeNavigate()
      lifecycle?.navigationIntent('reload', page.url?.())
      await Promise.all([waitForBootstrap(page.url?.()), page.reload({ waitUntil: 'load' })])
      await settle()
    },
  }
}

// Called only after the original real application scenarios have succeeded.
// All writes except next_run_at go through existing authenticated UI controls.
export async function completeScheduledRounds(c) {
  const { db, page, navigation, origin, accounts, groups, plans, groupPlanId, groupId, model,
    dialog, openAccount, checkSnapshot, readPlans, readResults, readGroupPlan, readGroupResults,
    fixtureControl, capturesNow, save, evidence, state } = c
  const initial = { account: await readResults(), group: await readGroupResults() }
  const readers = { account: readResults, group: readGroupResults }
  const planReaders = { account: readPlans, group: async () => [await readGroupPlan()] }
  const ids = { account: plans.map(p => p.id), group: [groupPlanId] }
  const tables = { account: 'scheduled_test_plans', group: 'pelican_group_test_plans' }

  async function accountHistory(label, expected) {
    await navigation.goto(`${origin}/admin/accounts`)
    const projections = []
    for (const account of accounts) {
      await openAccount(account)
      const rows = expected.filter(row => row.account_id === account.id)
      const details = new Map()
      const pending = []
      const errors = []
      const observe = response => {
        const path = new URL(response.url()).pathname
        if (!/^\/api\/v1\/admin\/scheduled-test-plans\/\d+\/results\/\d+$/.test(path)) return
        pending.push((async () => {
          assert.equal(response.status(), 200)
          const body = unwrap(await response.json())
          const binding = bindDetailResponse(response.request().url(), body, rows)
          const row = rows.find(row => row.id === binding.request_result_id)
          assert.equal(body.error_message, row.error_message)
          details.set(row.id, { binding, body })
        })().catch(error => errors.push({ name: error.name, message: error.message })))
      }
      page.on('response', observe)
      try {
        const historyResponse = page.waitForResponse(response => {
          const url = new URL(response.url())
          return url.pathname === '/api/v1/admin/pelican-test-results' && url.searchParams.get('account_id') === account.id
        })
        await dialog().getByTestId('open-records').click()
        const response = await historyResponse
        assert.equal(response.status(), 200)
        const history = unwrap(await response.json())
        assert.equal(history.next_cursor, 0)
        assert.equal(history.items.length, rows.length)
        for (const row of rows) {
          const item = history.items.find(item => String(item.id) === row.id)
          assert.ok(item)
          assert.equal(String(item.plan_id), row.plan_id)
          assert.equal(String(item.account_id), account.id)
          assert.equal(item.status, row.status)
          assert.equal(item.error_message, row.error_message)
          assert.deepEqual(item.pelican_config, row.pelican_config)
        }
        const deadline = Date.now() + 20_000
        while (details.size < rows.length && !errors.length && Date.now() < deadline) await pause()
        assert.deepEqual(errors, [])
        assert.equal(details.size, rows.length, 'every old and new PG identity needs a real detail request')
        const panel = page.getByTestId('records-panel')
        for (const row of rows) {
          const { binding } = details.get(row.id)
          const card = panel.getByTestId('pelican-record-card').filter({ has: row.status === 'success'
            ? page.locator(`iframe[srcdoc*="${binding.marker}"]`)
            : page.locator('pre').filter({ hasText: binding.marker }) })
          await card.waitFor({ state: 'visible' })
          assert.equal(await card.count(), 1)
          assert.equal(await card.getByTestId('record-status').innerText(), row.status === 'success' ? 'Complete' : 'Failed')
          await card.getByRole('button').click()
          const detail = page.getByTestId('record-detail')
          await checkSnapshot(detail, row.pelican_config)
          if (row.status === 'success') assert.equal(await detail.frameLocator('iframe').locator('p').innerText(), row.marker)
          else assert.ok((await detail.innerText()).includes(row.error_message))
          await page.screenshot({ path: join(evidence, `${label}-account-${account.id}-${row.id}.png`), fullPage: true })
          projections.push({ result_id: row.id, status: row.status, snapshot: row.pelican_config, request_binding: binding })
          await detail.getByRole('button', { name: 'Close', exact: true }).click()
        }
        await panel.getByRole('button', { name: 'Close', exact: true }).click()
        await Promise.all(pending)
        assert.deepEqual(errors, [], 'duplicate detail responses also retain URL identity')
      } finally { page.off('response', observe) }
      await dialog().getByRole('button', { name: 'Close modal', exact: true }).click()
    }
    save(`${label}-account-api-dom.json`, projections)
  }

  async function groupHistory(label, expected) {
    // Arm after old-document polling has drained. Its response body disappears
    // on navigation and must never stand in for the destination's history.
    let listed
    await navigation.goto(`${origin}/admin/pelican-tests`, () => {
      listed = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/admin/pelican-group-test-results'
        && response.request().method() === 'GET' && response.status() === 200)
    })
    const response = await listed
    const list = unwrap(await response.json())
    assert.equal(list.total, expected.length)
    assert.equal(list.items.length, expected.length)
    const projections = []
    for (const row of expected) {
      const item = list.items.find(item => String(item.id) === row.id)
      assert.ok(item)
      assert.equal(String(item.plan_id), row.plan_id)
      assert.equal(String(item.group_id), groupId)
      assert.equal(String(item.account_id), row.account_id)
      assert.equal(item.account_name, row.account_name)
      assert.equal(item.status, row.status)
      assert.equal(item.error_message, row.error_message)
      assert.deepEqual(item.pelican_config, row.pelican_config)
      assert.equal(item.attempts.length, row.attempt_count)
      const domRow = page.getByTestId(`pelican-result-${row.id}`)
      await domRow.waitFor({ state: 'visible' })
      assert.equal(await domRow.locator('td').nth(1).innerText(), groups[0].name)
      assert.equal(await domRow.locator('td').nth(2).innerText(), model)
      assert.equal(await domRow.locator('td').nth(3).innerText(), row.account_name)
      assert.equal(await domRow.locator('.result-badge').innerText(), row.status === 'success' ? 'Success' : 'Failed')
      if (row.status === 'failed') {
        assert.equal(await domRow.locator('.error-text').innerText(), row.error_message)
        assert.equal(await page.getByTestId(`pelican-result-view-${row.id}`).count(), 0)
        // Existing failed rows expose list/error only, no preview action. Do
        // not invent a browser detail request or a failed snapshot DOM claim.
        projections.push({ result_id: row.id, list_request_path: new URL(response.request().url()).pathname,
          status: row.status, error_message: row.error_message, api_snapshot: item.pelican_config, preview: 'not offered by existing UI' })
        await page.screenshot({ path: join(evidence, `${label}-group-failed-${row.id}.png`), fullPage: true })
        continue
      }
      const detailResponse = page.waitForResponse(response => new URL(response.url()).pathname === `/api/v1/admin/pelican-group-test-results/${row.id}`)
      await page.getByTestId(`pelican-result-view-${row.id}`).click()
      const detail = await detailResponse
      assert.equal(detail.status(), 200)
      const body = unwrap(await detail.json())
      const binding = bindDetailResponse(detail.request().url(), body, expected, 'group')
      assert.equal(body.error_message, row.error_message)
      const preview = page.getByTestId('pelican-result-preview')
      await checkSnapshot(preview, row.pelican_config)
      assert.equal(await preview.frameLocator('iframe').locator('p').innerText(), row.marker)
      await page.screenshot({ path: join(evidence, `${label}-group-${row.id}.png`), fullPage: true })
      projections.push({ result_id: row.id, snapshot: row.pelican_config, request_binding: binding })
      await page.getByRole('dialog').filter({ has: preview }).getByRole('button', { name: 'Close', exact: true }).click()
      await preview.waitFor({ state: 'hidden' })
    }
    save(`${label}-group-api-dom.json`, projections)
  }

  async function round(kind, label, refuse = false) {
    state.active_gate = `${label}-${kind}`
    const before = await readers[kind]()
    const beforePlans = await planReaders[kind]()
    assert.ok(beforePlans.every(p => p.running_until === null && p.last_run_at !== null))
    const previous = before.map(row => ({ nominal_time: row.pelican_config.pelican_question.round_id.slice('scheduled:'.length),
      question_id: row.pelican_config.pelican_question.id }))
    const { rows: clock } = await db.query(`SELECT to_char(date_trunc('second', now()) - interval '1 second', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS t`)
    const nominal = nextDifferentNominal(clock[0].t, previous.slice(-2))
    const captureStart = (await capturesNow()).captures.length
    const sampleCount = beforePlans.reduce((sum, p) => sum + p.pelican_config.parallel_count, 0)
    await fixtureControl('hold-scheduled')
    let released = false
    try {
      if (refuse) await fixtureControl('refuse-next')
      // Table names come only from the closed map above. Bind exact UI-created
      // IDs and fictional owners; CAS the previously read scheduling state.
      for (const plan of beforePlans) {
        const owner = kind === 'account' ? 'account_id' : 'group_id'
        const entity = kind === 'account' ? 'accounts' : 'groups'
        const name = kind === 'account' ? accounts.find(a => a.id === plan.account_id).name : groups[0].name
        const due = await db.query(`UPDATE ${tables[kind]} p SET next_run_at = $1::timestamptz
          FROM ${entity} e WHERE p.id = $2::bigint AND p.${owner} = $3::bigint
          AND e.id = p.${owner} AND e.name = $4 AND e.deleted_at IS NULL
          AND p.enabled AND p.model_id = $5 AND p.cron_expression = '0 0 1 1 *'
          AND p.running_until IS NULL AND p.last_run_at = $6::timestamptz
          AND p.next_run_at = $7::timestamptz AND p.next_run_at > now()
          AND p.pelican_config = $8::jsonb AND $1::timestamptz < now()
          RETURNING p.id::text`, [nominal, plan.id, plan[owner], name, model, plan.last_run_at, plan.next_run_at, JSON.stringify(plan.pelican_config)])
        assert.equal(due.rowCount, 1)
      }
      save(`${label}-${kind}-due.json`, { nominal_time: nominal, question_id_oracle: questionAt(nominal), previous_plans: beforePlans })
      const deadline = Date.now() + 90_000
      let claimed
      while (Date.now() < deadline) {
        const current = await planReaders[kind]()
        const sent = (await capturesNow()).captures.slice(captureStart)
        assert.ok(sent.length <= sampleCount)
        if (sent.length === sampleCount && current.every(p => p.lease_active)) {
          assert.deepEqual(await readers[kind](), before, 'held replies cannot change saved results')
          for (const p of current) {
            assert.ok(Date.parse(p.next_run_at) > Date.parse(nominal))
            assert.equal(p.last_run_at, beforePlans.find(old => old.id === p.id).last_run_at, 'Claim preserves last_run_at until Finish')
          }
          claimed = current
          break
        }
        await pause()
      }
      assert.ok(claimed, `${label}/${kind}: real RunDue and leases required`)
      save(`${label}-${kind}-claimed.json`, claimed)
      assert.equal((await fixtureControl('release-scheduled')).released, sampleCount)
      released = true
      let results, completed
      const finishDeadline = Date.now() + 30_000
      while (Date.now() < finishDeadline) {
        const rows = await readers[kind]()
        const current = await planReaders[kind]()
        assert.ok(rows.length <= before.length + sampleCount)
        if (rows.length === before.length + sampleCount && current.every(p => p.running_until === null)) {
          results = rows; completed = current; break
        }
        await pause()
      }
      assert.ok(results, 'new persisted results AND released leases required')
      for (const p of completed) {
        const old = beforePlans.find(old => old.id === p.id)
        assert.ok(Date.parse(p.last_run_at) > Date.parse(old.last_run_at))
        assert.ok(Date.parse(p.next_run_at) > Date.parse(nominal))
        const rule = ({ next_run_at, running_until, last_run_at, lease_active, ...fields }) => fields
        assert.deepEqual(rule(p), rule(old), 'execution preserves the saved plan rule and ownership')
      }
      assert.deepEqual(results.filter(row => before.some(old => old.id === row.id)), before, 'all previously saved JSON values remain unchanged')
      const fresh = results.filter(row => !before.some(old => old.id === row.id))
      const provider = await capturesNow()
      assert.deepEqual(provider.rejected, [])
      const sent = provider.captures.slice(captureStart)
      assert.equal(sent.length, sampleCount)
      assert.equal(new Set(fresh.map(row => row.marker)).size, sampleCount)
      assert.equal(fresh.filter(row => row.status === 'failed').length, refuse ? 1 : 0)
      for (const row of fresh) {
        assert.ok(ids[kind].includes(row.plan_id))
        const cfg = row.pelican_config
        assert.equal(cfg.pelican_question.round_id, `scheduled:${nominal}`)
        assert.equal(cfg.pelican_question.id, questionAt(nominal))
        assert.equal(cfg.pelican_question.version, bankVersion)
        assert.equal(cfg.pelican_question.source, 'builtin:svg-motion')
        assert.deepEqual(cfg.pelican_question, fresh[0].pelican_config.pelican_question, 'all samples in the same nominal round share the exact question')
        assert.equal(cfg.actual_prompt, fresh[0].pelican_config.actual_prompt)
        assert.equal(cfg.prompt_mode, 'random')
        assert.equal(cfg.parallel_count, beforePlans.find(p => p.id === row.plan_id).pelican_config.parallel_count)
        assert.notEqual(cfg.pelican_question.id, previous.at(-1).question_id)
        const capture = sent.find(item => item.marker === row.marker)
        assert.ok(capture, 'persisted marker binds one actual provider request')
        const accountIndex = accounts.findIndex(account => account.id === row.account_id)
        assert.ok(accountIndex >= 0, 'actual route must identify a fixture account')
        assert.equal(capture.alias, accountIndex === 0 ? 'a' : 'b')
        if (kind === 'account') {
          assert.equal(row.account_id, plans.find(p => p.id === row.plan_id).account_id)
          assert.equal(cfg.trigger_source, 'scheduled')
        } else {
          assert.equal(row.group_id, groupId)
          assert.equal(row.account_name, accounts[accountIndex].name)
        }
        assert.equal(capture.model, model)
        assert.equal(capture.prompt, cfg.actual_prompt.trim())
        assert.equal(capture.refused, row.status === 'failed')
        assert.equal(row.error_message, row.status === 'failed' ? 'Model refused the request (category: fixture-deterministic)' : '')
        if (kind === 'group') assert.equal(row.attempt_count, 0, 'model refusal must not fail over')
      }
      save(`${label}-${kind}-results.json`, { nominal_time: nominal, pg: results, provider: sent, completed_plans: completed })
      if (kind === 'account') await accountHistory(label, results)
      else await groupHistory(label, results)
      assert.deepEqual(await readers[kind](), results)
      state.scenarios.push({ name: `${label}-${kind}-pg-provider-api-dom`, status: 'passed', nominal_time: nominal,
        result_ids: fresh.map(row => row.id), coexisting_result_ids: results.map(row => row.id), failed: refuse ? 1 : 0 })
    } finally {
      if (!released) await fixtureControl('release-scheduled')
    }
  }

  await round('account', 'second-nominal')
  await round('group', 'second-nominal')

  async function editAccount(mode) {
    state.active_gate = `ui-edit-account-${mode}`
    await navigation.goto(`${origin}/admin/accounts`)
    await openAccount(accounts[0])
    await dialog().getByRole('button', { name: 'Scheduled tests', exact: true }).click()
    await dialog().getByRole('button', { name: 'Edit Plan', exact: true }).click()
    const form = dialog().locator('div.border-t.border-blue-100').filter({ has: page.getByPlaceholder('*/30 * * * *', { exact: true }) })
    assert.equal(await form.count(), 1)
    await form.getByTestId('pelican-mode-select').selectOption(mode)
    const prompt = 'fixture795 edited account rule: draw a standalone SVG with a violet square.'
    if (mode === 'custom') await form.locator('textarea').fill(prompt)
    await form.locator('input[type=number]').last().fill('1')
    const saved = page.waitForResponse(response => new URL(response.url()).pathname === `/api/v1/admin/scheduled-test-plans/${plans[0].id}`
      && response.request().method() === 'PUT')
    await form.getByRole('button', { name: 'Save', exact: true }).click()
    const response = await saved
    assert.equal(response.status(), 200)
    const body = unwrap(await response.json())
    assert.equal(String(body.id), plans[0].id)
    assert.equal(String(body.account_id), accounts[0].id)
    assert.equal(body.pelican_config.prompt_mode, mode)
    assert.equal(body.pelican_config.parallel_count, 1)
    if (mode === 'custom') assert.equal(body.pelican_config.prompt, prompt)
    assert.ok(!body.pelican_config.pelican_question)
    const stored = (await readPlans()).find(p => p.id === plans[0].id)
    assert.deepEqual(stored.pelican_config, body.pelican_config)
    await form.waitFor({ state: 'hidden' })
    await dialog().getByRole('button', { name: 'Close modal', exact: true }).click()
    await navigation.reload()
    await openAccount(accounts[0])
    const listRead = page.waitForResponse(response => new URL(response.url()).pathname === `/api/v1/admin/accounts/${accounts[0].id}/scheduled-test-plans`
      && response.request().method() === 'GET')
    await dialog().getByRole('button', { name: 'Scheduled tests', exact: true }).click()
    const listResponse = await listRead
    assert.equal(listResponse.status(), 200)
    const list = unwrap(await listResponse.json())
    const persisted = list.find(p => String(p.id) === plans[0].id)
    assert.ok(persisted)
    assert.deepEqual(persisted.pelican_config, body.pelican_config)
    await dialog().getByRole('button', { name: 'Edit Plan', exact: true }).click()
    assert.equal(await form.getByTestId('pelican-mode-select').inputValue(), mode)
    assert.equal(await form.locator('input[type=number]').last().inputValue(), '1')
    if (mode === 'custom') assert.equal(await form.locator('textarea').inputValue(), prompt)
    await page.screenshot({ path: join(evidence, `edited-account-${mode}-persisted.png`), fullPage: true })
    save(`edited-account-${mode}.json`, { update_request_path: new URL(response.request().url()).pathname,
      list_request_path: new URL(listResponse.request().url()).pathname,
      api_config: body.pelican_config, pg_plan: stored, reloaded_editor: { prompt_mode: mode, parallel_count: 1 } })
    await form.getByRole('button', { name: 'Cancel', exact: true }).click()
    await dialog().getByRole('button', { name: 'Close modal', exact: true }).click()
  }

  async function editGroup(mode) {
    state.active_gate = `ui-edit-group-${mode}`
    await navigation.goto(`${origin}/admin/pelican-tests`)
    await page.getByTestId(`pelican-plan-edit-${groupPlanId}`).click()
    const form = page.getByTestId('pelican-test-editor')
    await form.getByTestId('pelican-mode-select').selectOption(mode)
    const prompt = 'fixture795 edited group rule: draw a standalone SVG with a violet triangle.'
    if (mode === 'custom') await form.getByTestId('pelican-test-prompt').fill(prompt)
    await form.locator('#pelican-test-parallel').fill('1')
    const saved = page.waitForResponse(response => new URL(response.url()).pathname === `/api/v1/admin/pelican-group-tests/${groupPlanId}`
      && response.request().method() === 'PUT')
    await page.getByTestId('pelican-test-save').click()
    const response = await saved
    assert.equal(response.status(), 200)
    const body = unwrap(await response.json())
    assert.equal(String(body.id), groupPlanId)
    assert.equal(String(body.group_id), groupId)
    assert.equal(body.pelican_config.prompt_mode, mode)
    assert.equal(body.pelican_config.parallel_count, 1)
    if (mode === 'custom') assert.equal(body.pelican_config.prompt, prompt)
    assert.ok(!body.pelican_config.pelican_question)
    const stored = await readGroupPlan()
    assert.deepEqual(stored.pelican_config, body.pelican_config)
    await form.waitFor({ state: 'hidden' })
    let listRead
    await navigation.reload(() => {
      listRead = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/admin/pelican-group-tests'
        && response.request().method() === 'GET')
    })
    const listResponse = await listRead
    assert.equal(listResponse.status(), 200)
    const list = unwrap(await listResponse.json())
    const persisted = list.find(p => String(p.id) === groupPlanId)
    assert.ok(persisted)
    assert.deepEqual(persisted.pelican_config, body.pelican_config)
    await page.getByTestId(`pelican-plan-edit-${groupPlanId}`).click()
    assert.equal(await form.getByTestId('pelican-mode-select').inputValue(), mode)
    assert.equal(await form.locator('#pelican-test-parallel').inputValue(), '1')
    if (mode === 'custom') assert.equal(await form.getByTestId('pelican-test-prompt').inputValue(), prompt)
    await page.screenshot({ path: join(evidence, `edited-group-${mode}-persisted.png`), fullPage: true })
    save(`edited-group-${mode}.json`, { update_request_path: new URL(response.request().url()).pathname,
      list_request_path: new URL(listResponse.request().url()).pathname,
      api_config: body.pelican_config, pg_plan: stored, reloaded_editor: { prompt_mode: mode, parallel_count: 1 } })
    await page.getByRole('dialog').filter({ has: form }).getByRole('button', { name: 'Cancel', exact: true }).click()
  }

  const savedBeforeEdit = { account: await readResults(), group: await readGroupResults() }
  const plansBeforeEdit = { account: await readPlans(), group: await readGroupPlan() }
  await editAccount('custom')
  await editGroup('custom')
  state.active_gate = 'edited-old-snapshots'
  assert.deepEqual(await readResults(), savedBeforeEdit.account)
  assert.deepEqual(await readGroupResults(), savedBeforeEdit.group)
  assert.deepEqual((await readPlans()).find(p => p.id === plans[1].id), plansBeforeEdit.account.find(p => p.id === plans[1].id),
    'editing A must preserve B plan and ownership')
  for (const p of await readPlans()) assert.equal(p.last_run_at, plansBeforeEdit.account.find(old => old.id === p.id).last_run_at)
  assert.equal((await readGroupPlan()).last_run_at, plansBeforeEdit.group.last_run_at)
  await accountHistory('edited-old-snapshots', savedBeforeEdit.account)
  await groupHistory('edited-old-snapshots', savedBeforeEdit.group)
  save('edited-old-pg-snapshots.json', savedBeforeEdit)
  state.scenarios.push({ name: 'ui-edited-account-group-rules-old-pg-api-dom-snapshots-immutable', status: 'passed' })

  // Restore random mode through the real forms, retaining the changed sample
  // count. The failed and recovery records must freeze this new rule, while
  // all earlier results continue to describe the old two-sample rule.
  await editAccount('random')
  await editGroup('random')
  await round('account', 'deterministic-refusal', true)
  await round('account', 'after-refusal-valid-round')
  await round('group', 'deterministic-refusal', true)
  await round('group', 'after-refusal-valid-round')
  assert.deepEqual((await readResults()).filter(row => initial.account.some(old => old.id === row.id)), initial.account)
  assert.deepEqual((await readGroupResults()).filter(row => initial.group.some(old => old.id === row.id)), initial.group)
  state.active_gate = null
}
