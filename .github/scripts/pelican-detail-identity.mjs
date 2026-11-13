import assert from 'node:assert/strict'

// Pure evidence validation: never sends, replaces or mutates an application API.
// Select the PG row by the request URL, never by the response body's identity.
export function bindDetailResponse(requestURL, body, expectedRows, kind = 'account') {
  assert.ok(kind === 'account' || kind === 'group')
  const path = new URL(requestURL).pathname
  const match = kind === 'account'
    ? path.match(/^\/api\/v1\/admin\/scheduled-test-plans\/(\d+)\/results\/(\d+)$/)
    : path.match(/^\/api\/v1\/admin\/pelican-group-test-results\/(\d+)$/)
  assert.ok(match, 'expected real detail request path')
  const requestResultID = kind === 'account' ? match[2] : match[1]
  const matchingRows = expectedRows.filter(row => String(row.id) === requestResultID)
  assert.equal(matchingRows.length, 1, 'request result ID must identify exactly one PG row')
  const row = matchingRows[0]
  const requestPlanID = kind === 'account' ? match[1] : null
  if (kind === 'account') assert.equal(requestPlanID, String(row.plan_id), 'request plan ID must equal PG plan ID')
  assert.equal(String(body.id), requestResultID, 'response result ID must equal request URL result ID')
  assert.equal(String(body.plan_id), String(row.plan_id), 'response plan ID must equal PG plan ID')
  assert.equal(body.status, row.status)
  assert.deepEqual(body.pelican_config, row.pelican_config)
  const marker = body.response_text?.match(/fixture795-\d+/)?.[0]
  assert.equal(marker, row.marker, 'request-bound response marker must equal its PG marker')
  return { request_path: path, request_plan_id: requestPlanID, request_result_id: requestResultID,
    pg_plan_id: String(row.plan_id), pg_result_id: String(row.id), marker }
}
