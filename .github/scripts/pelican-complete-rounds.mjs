import assert from 'node:assert/strict'
import { join } from 'node:path'
import { bindDetailResponse } from './pelican-detail-identity.mjs'
import { nextDifferentNominal, questionAt, bankVersion } from './pelican-nominal-round.mjs'

const unwrap = value => value?.data ?? value
const pause = () => new Promise(resolve => setTimeout(resolve, 250))

// Called only after the original real application scenarios have succeeded.
// All writes except next_run_at go through existing authenticated UI controls.
export async function completeScheduledRounds(c) {
  const { db, page, origin, accounts, groups, plans, groupPlanId, groupId, model,
    dialog, openAccount, checkSnapshot, readPlans, readResults, readGroupPlan, readGroupResults,
    fixtureControl, capturesNow, save, evidence, state } = c
  const initial = { account: await readResults(), group: await readGroupResults() }
  const readers = { account: readResults, group: readGroupResults }
  const planReaders = { account: readPlans, group: async () => [await readGroupPlan()] }
  const ids = { account: plans.map(p => p.id), group: [groupPlanId] }
  const tables = { account: 'scheduled_test_plans', group: 'pelican_group_test_plans' }

  async function accountHistory(label, expected) {
    await page.goto(`${origin}/admin/accounts`)
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
    const listed = page.waitForResponse(response => new URL(response.url()).pathname === '/api/v1/admin/pelican-group-test-results'
      && response.request().method() === 'GET' && response.status() === 200)
    await page.goto(`${origin}/admin/pelican-tests`)
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
        assert.ok(Date.parse(p.last_run_at) > Date.parse(beforePlans.find(old => old.id === p.id).last_run_at))
        assert.ok(Date.parse(p.next_run_at) > Date.parse(nominal))
      }
      assert.deepEqual(results.filter(row => before.some(old => old.id === row.id)), before, 'all saved snapshots remain byte-for-byte equivalent as JSON')
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
        assert.equal(cfg.prompt_mode, 'random')
        assert.equal(cfg.parallel_count, beforePlans.find(p => p.id === row.plan_id).pelican_config.parallel_count)
        assert.notEqual(cfg.pelican_question.id, previous.at(-1).question_id)
        const capture = sent.find(item => item.marker === row.marker)
        assert.ok(capture, 'persisted marker binds one actual provider request')
        assert.equal(capture.alias, accounts[0].id === row.account_id ? 'a' : 'b')
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
  // Subsequent increments add UI editing and failure/recovery to this same
  // runner; partial scope stays explicit in acceptance.json until implemented.
  assert.deepEqual((await readResults()).filter(row => initial.account.some(old => old.id === row.id)), initial.account)
  assert.deepEqual((await readGroupResults()).filter(row => initial.group.some(old => old.id === row.id)), initial.group)
}
