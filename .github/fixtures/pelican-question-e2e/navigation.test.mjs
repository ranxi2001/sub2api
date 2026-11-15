import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import test from 'node:test'
import { createPageNavigation } from '../../scripts/pelican-complete-rounds.mjs'

class Page extends EventEmitter {
  calls = []
  // A document lifecycle state alone is not an in-flight request barrier.
  async waitForLoadState() {}
  async goto(url, options) { this.calls.push({ url, options }) }
  async reload(options) { this.calls.push({ reload: true, options }) }
}
const nextTask = () => new Promise(resolve => setImmediate(resolve))

for (const action of ['goto', 'reload']) {
  test(`${action}: drains auth/settings and subsequent requests before unloading`, async () => {
    const page = new Page()
    const navigation = createPageNavigation(page)
    const auth = {}, settings = {}, followup = {}
    page.emit('request', auth)
    page.emit('request', settings)
    const done = action === 'goto' ? navigation.goto('http://app:8080/admin/accounts') : navigation.reload()
    await nextTask()
    assert.equal(page.calls.length, 0, 'navigation must wait for outstanding bootstrap requests')
    page.emit('requestfinished', auth)
    page.emit('request', followup)
    page.emit('requestfinished', settings)
    await nextTask()
    assert.equal(page.calls.length, 0, 'a request added during drain must also complete')
    page.emit('requestfinished', followup)
    await done
    assert.equal(page.calls.length, 1)
    assert.equal(page.calls[0].options.waitUntil, 'networkidle')
  })
}

test('failed requests still reach the strict error guard without filtering', async () => {
  const page = new Page()
  const failures = []
  page.on('requestfailed', request => failures.push(request))
  const navigation = createPageNavigation(page)
  const failed = { reason: 'net::ERR_ABORTED' }
  page.emit('request', failed)
  const done = navigation.settle()
  page.emit('requestfailed', failed)
  await done
  assert.deepEqual(failures, [failed])
  assert.throws(() => assert.equal(failures.length, 0, 'no failed browser requests'))
})

test('navigation waits for destination requests before returning', async () => {
  const page = new Page()
  const navigation = createPageNavigation(page)
  const pending = {}
  page.goto = async (url, options) => { page.calls.push({ url, options }); page.emit('request', pending) }
  let returned = false
  const done = navigation.goto('http://app:8080/admin/accounts').then(() => { returned = true })
  await nextTask()
  assert.equal(returned, false)
  page.emit('requestfinished', pending)
  await done
  assert.equal(returned, true)
})
