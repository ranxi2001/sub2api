import assert from 'node:assert/strict'
import test from 'node:test'
import { nextDifferentNominal, questionAt } from '../../scripts/pelican-nominal-round.mjs'

test('selection oracle agrees with two prior real Go scheduler result artifacts', () => {
  // PR832 run43405643066 attempt1, PG result projections; no model-quality claim.
  assert.equal(questionAt('2026-11-13T10:06:24.123456Z'), 'penguin-bicycle')
  assert.equal(questionAt('2026-11-13T10:07:16.654321Z'), 'rabbit-scooter')
})

test('later nominal time changes the question without changing the source algorithm', () => {
  const previous = [{ nominal_time: '2026-11-13T10:06:24.123456Z', question_id: 'penguin-bicycle' }]
  const next = nextDifferentNominal('2026-11-13T10:08:00Z', previous)
  assert.match(next, /^2026-11-13T10:08:00\.\d{5}[1-9]Z$/)
  assert.notEqual(questionAt(next), previous[0].question_id)
  assert.ok(Date.parse(next) > Date.parse(previous[0].nominal_time))
  assert.throws(() => nextDifferentNominal('2026-11-13T10:00:00Z', previous), /No later/)
})
