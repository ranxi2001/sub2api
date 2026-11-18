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
    assert.equal(page.calls[0].options.waitUntil, 'load')
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
  const deadline = Date.now() + 2_000
  while (!page.calls.length && Date.now() < deadline) await new Promise(resolve => setTimeout(resolve, 10))
  assert.equal(page.calls.length, 1, 'destination navigation has started')
  assert.equal(returned, false)
  page.emit('requestfinished', pending)
  await done
  assert.equal(returned, true)
})

test('srcdoc preview detachment cannot strand navigation on a stale networkidle lifecycle', async () => {
  const page = new Page()
  page.waitForLoadState = async state => {
    assert.equal(state, 'load', 'use document readiness plus tracked request completion, not the stranded networkidle flag')
  }
  const navigation = createPageNavigation(page)
  await navigation.goto('http://app:8080/admin/pelican-tests')
  assert.equal(page.calls.length, 1)
  assert.equal(page.calls[0].options.waitUntil, 'load')
})

test('an unfinished request still fails at the unchanged twenty-second deadline', async t => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 0 })
  const page = new Page()
  const navigation = createPageNavigation(page)
  page.emit('request', {})
  let failed = false
  const done = assert.rejects(navigation.settle(), /browser requests did not finish before navigation/)
    .then(() => { failed = true })
  await Promise.resolve()
  t.mock.timers.tick(19_999)
  await Promise.resolve()
  assert.equal(failed, false)
  t.mock.timers.tick(1)
  await done
  assert.equal(failed, true)
  assert.equal(page.calls.length, 0)
})

test('document readiness consumes the same deadline rather than granting another twenty seconds', async t => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 0 })
  const page = new Page()
  page.waitForLoadState = () => new Promise(resolve => setTimeout(resolve, 15_000))
  const navigation = createPageNavigation(page)
  page.emit('request', {})
  let failed = false
  const done = assert.rejects(navigation.settle(), /browser requests did not finish before navigation/)
    .then(() => { failed = true })
  t.mock.timers.tick(15_000)
  await Promise.resolve()
  t.mock.timers.tick(4_999)
  await Promise.resolve()
  assert.equal(failed, false)
  t.mock.timers.tick(1)
  await done
  assert.equal(failed, true)
})

for (const action of ['goto', 'reload']) {
  test(`${action}: deferred auth after document load must finish before unloading`, async () => {
    const page = new Page()
    const navigation = createPageNavigation(page)
    const auth = {}
    const done = action === 'goto' ? navigation.goto('http://app:8080/admin/accounts') : navigation.reload()
    await nextTask()
    // A completed document can schedule its first API request in a later task.
    page.emit('request', auth)
    await new Promise(resolve => setTimeout(resolve, 300))
    assert.equal(page.calls.length, 0, 'deferred auth must not be aborted by navigation')
    page.emit('requestfinished', auth)
    await done
    assert.equal(page.calls.length, 1)
  })
}

for (const action of ['goto', 'reload']) {
  test(`${action}: destination response observer excludes old-document polling during drain`, async () => {
    const page = new Page()
    const navigation = createPageNavigation(page)
    const oldRequest = {}
    page.emit('request', oldRequest)
    const captured = []
    const arm = () => page.once('response', response => captured.push(response))
    const navigate = page[action].bind(page)
    page[action] = async (...args) => {
      await navigate(...args)
      page.emit('response', { document: 'destination' })
    }
    const done = action === 'goto' ? navigation.goto('http://app:8080/admin/accounts', arm) : navigation.reload(arm)
    await nextTask()
    page.emit('response', { document: 'old' })
    page.emit('requestfinished', oldRequest)
    await done
    assert.deepEqual(captured, [{ document: 'destination' }])
  })
}
