import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'

// Mirror only the selection oracle from service/pelican_questions.go. Never
// inject a selected question or change the application's selection algorithm.
export const bankVersion = 'svg-motion-v1'
export const questionIDs = ['pelican-bicycle', 'penguin-bicycle', 'rabbit-scooter', 'otter-scooter']
export function questionAt(nominal) {
  const hash = createHash('sha256').update(`${bankVersion}:scheduled:${nominal}`).digest()
  return questionIDs[Number(hash.readBigUInt64BE(0) % BigInt(questionIDs.length))]
}

export function nextDifferentNominal(clockSecond, previous) {
  assert.match(clockSecond, /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/)
  assert.ok(previous.length > 0)
  for (let microsecond = 234001; microsecond < 235000; microsecond++) {
    // Go RFC3339Nano trims zeroes. Keep six significant fractional digits so
    // PG's US projection and the exact source round string are identical.
    if (microsecond % 10 === 0) continue
    const candidate = clockSecond.replace('Z', `.${microsecond}Z`)
    if (previous.every(row => Date.parse(candidate) > Date.parse(row.nominal_time)
      && questionAt(candidate) !== row.question_id)) return candidate
  }
  throw new Error('No later, different deterministic nominal time in fixture bound')
}
