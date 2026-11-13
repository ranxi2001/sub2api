import assert from 'node:assert/strict'
import test from 'node:test'
import { bindDetailResponse } from '../../scripts/pelican-detail-identity.mjs'

// Same plan, status and question; only the sample identities/markers differ.
// These synthetic checks do not replace the real application E2E or its APIs.
const rows = ['101', '102'].map((id, index) => ({ id, plan_id: '7', status: 'success',
  pelican_config: { pelican_question: { round_id: 'scheduled:fixture', id: 'same-question' } },
  marker: `fixture795-${index + 1}` }))
const bodies = rows.map(row => ({ id: Number(row.id), plan_id: Number(row.plan_id),
  status: row.status, pelican_config: row.pelican_config, response_text: `<p>${row.marker}</p>` }))
const url = (kind, id, plan = '7') => kind === 'account'
  ? `http://app:8080/api/v1/admin/scheduled-test-plans/${plan}/results/${id}`
  : `http://app:8080/api/v1/admin/pelican-group-test-results/${id}`

for (const kind of ['account', 'group']) {
  test(`${kind}: legitimate out-of-order details retain their request identities`, () => {
    const bindings = [1, 0].map(index => bindDetailResponse(url(kind, rows[index].id), bodies[index], rows, kind))
    assert.deepEqual(bindings.map(item => item.request_result_id), ['102', '101'])
    assert.deepEqual(bindings.map(item => item.marker), ['fixture795-2', 'fixture795-1'])
  })
  test(`${kind}: whole R1/R2 response swap is rejected in BOTH directions before card binding`, () => {
    for (const index of [0, 1]) {
      assert.throws(() => bindDetailResponse(url(kind, rows[index].id), bodies[1 - index], rows, kind),
        /response result ID must equal request URL result ID/)
    }
  })
  test(`${kind}: correct result ID with wrong body plan is rejected`, () => {
    assert.throws(() => bindDetailResponse(url(kind, '101'), { ...bodies[0], plan_id: 8 }, rows, kind),
      /response plan ID must equal PG plan ID/)
  })
  test(`${kind}: correct IDs with the other sample's content is rejected`, () => {
    assert.throws(() => bindDetailResponse(url(kind, '101'), { ...bodies[0], response_text: bodies[1].response_text }, rows, kind),
      /request-bound response marker must equal its PG marker/)
  })
}
test('account: wrong request plan is rejected even when the body matches a PG row', () => {
  assert.throws(() => bindDetailResponse(url('account', '101', '8'), bodies[0], rows),
    /request plan ID must equal PG plan ID/)
})
