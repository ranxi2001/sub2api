import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import test from 'node:test'
import { createContext, runInContext } from 'node:vm'
import { createPageNavigation, createBrowserLifecycleMetadata } from '../../scripts/pelican-complete-rounds.mjs'

class Page extends EventEmitter {
  calls = []
  initScripts = []
  constructor() { super(); this.newDocument() }
  newDocument() {
    this.intervals = new Map()
    this.timeouts = new Map()
    let nextID = 0
    this.realm = createContext({
      setInterval: (callback, delay, ...args) => {
        const id = ++nextID
        this.intervals.set(id, { callback, delay, args })
        return id
      },
      setTimeout: (callback, delay, ...args) => {
        const id = ++nextID
        this.timeouts.set(id, { callback, delay, args })
        return id
      },
      clearInterval: id => { this.intervals.delete(id); this.timeouts.delete(id) },
      clearTimeout: id => { this.intervals.delete(id); this.timeouts.delete(id) },
    })
    for (const script of this.initScripts) runInContext(`(${script})()`, this.realm)
  }
  async addInitScript(fn) { this.initScripts.push(fn.toString()) }
  async evaluate(fn) { return runInContext(`(${fn.toString()})()`, this.realm) }
  tickIntervals() {
    for (const timer of [...this.intervals.values()]) timer.callback(...timer.args)
  }
  // A document lifecycle state alone is not an in-flight request barrier.
  async waitForLoadState() {}
  async goto(url, options) { this.calls.push({ url, options }); this.newDocument() }
  async reload(options) { this.calls.push({ reload: true, options }); this.newDocument() }
}
const nextTask = () => new Promise(resolve => setImmediate(resolve))

function metadataFixture() {
  const page = new Page()
  let currentURL = 'http://app:8080/admin/accounts'
  const frame = { url: () => currentURL }
  page.mainFrame = () => frame
  page.url = () => currentURL
  const request = (url = 'http://app:8080/api/v1/auth/me', navigation = false, owner = frame) => ({
    url: () => url, method: () => 'GET', frame: () => owner, isNavigationRequest: () => navigation,
    headers: () => { throw new Error('must not read headers') },
    postData: () => { throw new Error('must not read request bodies') },
    response: () => { throw new Error('must not read responses') },
    failure: () => { throw new Error('must not read arbitrary failure text') },
  })
  return { page, frame, request, setURL: url => { currentURL = url } }
}

test('lifecycle metadata strips queries, excludes other routes/origins and allowlists context', () => {
  const { page, frame, request } = metadataFixture()
  let elapsed = 100
  const lifecycle = createBrowserLifecycleMetadata(page, () => ({ stage: 'private-stage-token', active_gate: 'private-gate-token' }), { now: () => elapsed })
  const auth = request('http://app:8080/api/v1/auth/me?secret=query-token#private-fragment')
  page.emit('request', auth)
  elapsed = 105
  page.emit('requestfinished', auth)
  for (const url of ['https://outside.invalid/api/v1/auth/me?secret=external-token',
    'http://app:8080/api/v1/auth/private-token', 'http://app:8080/api/v1/auth/me/private-token']) {
    page.emit('request', request(url))
  }
  lifecycle.navigationIntent('goto', 'http://app:8080/admin/accounts?secret=navigation-token')
  lifecycle.navigationIntent('private-action-token', 'http://app:8080/admin/accounts')
  page.emit('framenavigated', { url: () => 'http://app:8080/admin/accounts?secret=subframe-token' })
  page.emit('framenavigated', frame)
  const snapshot = lifecycle.snapshot()
  assert.equal(snapshot.events.length, 4)
  assert.deepEqual(snapshot.events.map(event => event.event), ['request-start', 'request-finished', 'navigation-intent', 'frame-navigation'])
  assert.equal(snapshot.events[0].at_ms, 0)
  assert.equal(snapshot.events[1].at_ms, 5)
  assert.ok(snapshot.events.every(event => event.stage === 'unlisted' && event.active_gate === null))
  assert.ok(!JSON.stringify(snapshot).includes('token'))
  assert.ok(!JSON.stringify(snapshot).includes('?'))
  assert.deepEqual(lifecycle.requestMetadata(auth), { request_id: 1, method: 'GET', path: '/api/v1/auth/me',
    main_frame: true, is_navigation_request: false, navigation_id: 0, document_id: 0, requested_document_id: null,
    start_ms: 0, start_stage: 'unlisted', start_gate: null, end_ms: 5, end_event: 'request-finished', end_document_id: 0, end_navigation_id: 0 })
})

test('lifecycle binds an old-document auth failure across navigation intent and commit without suppressing it', () => {
  const { page, frame, request, setURL } = metadataFixture()
  let elapsed = 0
  let context = { stage: 'normal-authentication', active_gate: null }
  const lifecycle = createBrowserLifecycleMetadata(page, () => context, { now: () => elapsed })
  lifecycle.navigationIntent('goto', page.url())
  const document = request(page.url(), true)
  page.emit('request', document)
  page.emit('framenavigated', frame)
  page.emit('requestfinished', document)
  // A same-document route event does not create a new document identity.
  page.emit('framenavigated', frame)
  const auth = request()
  elapsed = 10
  context = { stage: 'complete-scheduled-rounds', active_gate: 'second-nominal-account' }
  page.emit('request', auth)
  elapsed = 11
  lifecycle.navigationIntent('reload', page.url())
  const nextDocument = request(page.url(), true)
  page.emit('request', nextDocument)
  elapsed = 12
  setURL('http://app:8080/admin/accounts')
  page.emit('framenavigated', frame)
  const failures = []
  page.on('requestfailed', failed => failures.push(failed))
  elapsed = 13
  context = { stage: 'complete-scheduled-rounds', active_gate: 'second-nominal-group' }
  page.emit('requestfailed', auth)
  const metadata = lifecycle.requestMetadata(auth)
  assert.equal(metadata.request_id, 2)
  assert.equal(metadata.document_id, 1)
  assert.equal(metadata.navigation_id, 1)
  assert.equal(metadata.start_ms, 10)
  assert.equal(metadata.end_ms, 13)
  assert.equal(metadata.end_document_id, 2)
  assert.equal(metadata.end_navigation_id, 2)
  assert.equal(metadata.start_gate, 'second-nominal-account')
  assert.deepEqual(failures, [auth])
  assert.throws(() => assert.equal(failures.length, 0, 'no failed browser requests'))
  const commits = lifecycle.snapshot().events.filter(event => event.event === 'document-commit')
  assert.deepEqual(commits.map(event => [event.document_id, event.navigation_id, event.request_id]), [[1, 1, 1], [2, 2, 3]])
  assert.equal(lifecycle.snapshot().events.at(-1).active_gate, 'second-nominal-group')
})

test('lifecycle metadata is bounded with explicit truncation and request object identity', () => {
  const { page, request } = metadataFixture()
  const lifecycle = createBrowserLifecycleMetadata(page, () => ({ stage: 'complete-scheduled-rounds' }))
  const first = request(), second = request()
  page.emit('request', first)
  page.emit('request', second)
  assert.notEqual(lifecycle.requestMetadata(first).request_id, lifecycle.requestMetadata(second).request_id)
  page.emit('requestfinished', second)
  page.emit('requestfailed', first)
  for (let i = 0; i < 8200; i++) lifecycle.navigationIntent('goto', 'http://app:8080/admin/accounts')
  const snapshot = lifecycle.snapshot()
  assert.equal(snapshot.events.length, snapshot.max_events)
  assert.equal(snapshot.dropped_events, 12)
  assert.equal(snapshot.events[0].sequence, 13)
  assert.equal(snapshot.events.at(-1).sequence, 8204)
  assert.equal(lifecycle.requestMetadata(first).end_event, 'request-failed')
})

test('an early request with no attached frame records unknown without breaking the scenario', () => {
  const { page, request } = metadataFixture()
  const lifecycle = createBrowserLifecycleMetadata(page, () => ({ stage: 'normal-authentication' }))
  const early = request()
  early.frame = () => { throw new Error('private browser error text') }
  page.emit('request', early)
  page.emit('requestfailed', early)
  assert.equal(lifecycle.requestMetadata(early).main_frame, null)
  assert.ok(!JSON.stringify(lifecycle.snapshot()).includes('private browser error text'))
})

for (const action of ['goto', 'reload']) {
  test(`${action}: metadata intent is synchronous and follows the existing drain and observer`, async () => {
    const page = new Page()
    page.url = () => 'http://app:8080/admin/accounts'
    const order = []
    const lifecycle = { navigationIntent: (kind, url) => { order.push('intent'); assert.equal(kind, action); assert.equal(url, page.url()) } }
    const navigation = createPageNavigation(page, { lifecycle })
    const oldRequest = {}
    page.emit('request', oldRequest)
    page[action] = async () => { order.push('navigate') }
    const arm = () => order.push('observer')
    const done = action === 'goto' ? navigation.goto(page.url(), arm) : navigation.reload(arm)
    await nextTask()
    assert.deepEqual(order, [])
    page.emit('requestfinished', oldRequest)
    await done
    assert.deepEqual(order, ['observer', 'intent', 'navigate'])
  })

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

// Model the observed event ordering, not a wall-clock/quiet-window guess:
// old interval becomes due after navigation-intent, before main-frame commit.
for (const action of ['goto', 'reload']) {
  test(`${action}: old-document interval due at navigation intent cannot start an aborted auth request`, async () => {
    const page = new Page()
    const requests = [], pending = new Set(), failures = []
    let race = false, document = 0
    const auth = () => {
      const request = { path: '/api/v1/auth/me', document }
      requests.push(request)
      pending.add(request)
      page.emit('request', request)
      if (!race) { pending.delete(request); page.emit('requestfinished', request) }
    }
    const boot = () => {
      document++
      // Actual auth store uses immediate refresh plus a 60s interval.
      page.realm.setInterval(auth, 60_000)
      auth()
    }
    for (const kind of ['goto', 'reload']) {
      const navigate = page[kind].bind(page)
      page[kind] = async (...args) => {
        for (const request of pending) {
          page.emit('requestfailed', request)
        }
        pending.clear()
        await navigate(...args)
        race = false
        boot()
      }
    }
    page.on('requestfailed', request => failures.push(request))
    const navigation = createPageNavigation(page, {
      lifecycle: { navigationIntent() { if (race) page.tickIntervals() } },
      waitForBootstrap: () => new Promise(resolve => page.once('requestfinished', request => {
        assert.equal(request.path, '/api/v1/auth/me')
        resolve()
      })),
    })
    await navigation.goto('http://app:8080/admin/accounts')
    assert.equal(requests.length, 1, 'normal initial document auth ran')
    page.tickIntervals()
    assert.equal(requests.length, 2, 'old-document periodic auth runs during normal scenario activity')
    race = true
    if (action === 'goto') await navigation.goto('http://app:8080/admin/pelican-tests')
    else await navigation.reload()
    assert.deepEqual(failures, [], 'strict inventory: no old auth may be aborted at commit')
    assert.equal(requests.length, 3, 'new document must still perform its normal auth bootstrap')
    page.tickIntervals()
    assert.equal(requests.length, 4, 'new document periodic auth remains enabled')
    assert.equal(requests.at(-1).document, 2)
  })
}

for (const action of ['goto', 'reload']) {
  test(`${action}: an interval already dispatching at retirement must finish its real request`, async () => {
    const page = new Page()
    const navigation = createPageNavigation(page)
    await navigation.goto('http://app:8080/admin/accounts')
    const request = { path: '/api/v1/auth/me' }
    const evaluate = page.evaluate.bind(page)
    let dispatched = false
    page.realm.setInterval(() => { dispatched = true; page.emit('request', request) }, 60_000)
    page.evaluate = async fn => {
      if (fn.name === 'retireDocumentIntervals') page.tickIntervals()
      return evaluate(fn)
    }
    let observed = false
    const beforeNavigate = () => { observed = true }
    const done = action === 'goto' ? navigation.goto('http://app:8080/admin/pelican-tests', beforeNavigate) : navigation.reload(beforeNavigate)
    await nextTask()
    assert.equal(dispatched, true)
    assert.equal(observed, false)
    assert.equal(page.calls.length, 1, 'retirement must not drop the already started request from pending')
    assert.equal(page.intervals.size, 0, 'future periodic work stopped before drain')
    page.emit('requestfinished', request)
    await done
    assert.equal(observed, true)
    assert.equal(page.calls.length, 2)
  })
}

for (const path of ['/api/v1/auth/me', '/api/v1/settings/public']) {
  for (const phase of ['old-document', 'new-document']) {
    test(`abort-${path.includes('auth') ? 'auth' : 'settings'}: ${phase} failure remains in the strict inventory`, async () => {
      const page = new Page(), failures = []
      const navigation = createPageNavigation(page)
      page.on('requestfailed', request => failures.push(request))
      const request = { path, reason: 'net::ERR_ABORTED' }
      if (phase === 'old-document') {
        page.emit('request', request)
        const done = navigation.goto('http://app:8080/admin/accounts')
        await nextTask()
        assert.equal(page.calls.length, 0)
        page.emit('requestfailed', request)
        await done
      } else {
        const goto = page.goto.bind(page)
        page.goto = async (...args) => {
          await goto(...args)
          page.emit('request', request)
          page.emit('requestfailed', request)
        }
        await navigation.goto('http://app:8080/admin/accounts')
      }
      assert.deepEqual(failures, [request])
      assert.throws(() => assert.equal(failures.length, 0, 'no failed browser requests'))
    })
  }
}

test('document retirement leaves one-shot callbacks, pending request timeouts and native timer arguments intact', async () => {
  const page = new Page(), values = [], counts = []
  const navigation = createPageNavigation(page, { lifecycle: {
    documentIntervalsRetired: count => counts.push(count), navigationIntent() {},
  } })
  await navigation.goto('http://app:8080/admin/accounts')
  page.realm.setInterval((a, b) => values.push([a, b]), 60_000, 'native', 17)
  const cleared = page.realm.setInterval(() => assert.fail('cleared interval fired'), 60_000)
  page.realm.clearTimeout(cleared)
  page.tickIntervals()
  assert.deepEqual(values, [['native', 17]])
  assert.equal([...page.intervals.values()][0].delay, 60_000)
  const request = { path: '/api/v1/settings/public' }
  page.emit('request', request)
  const timeoutID = page.realm.setTimeout(() => page.emit('requestfinished', request), 15_000)
  const oldRealm = page.realm
  const done = navigation.reload()
  await nextTask()
  assert.deepEqual(counts, [0, 1], 'native clearTimeout removed its interval from the registry')
  assert.equal(page.timeouts.has(timeoutID), true, 'retirement must not clear request timeouts')
  assert.equal(page.intervals.size, 0)
  oldRealm.setInterval(() => assert.fail('late recurring work on retired document fired'), 1)
  assert.equal(page.intervals.size, 0, 'retired document cannot re-arm its polling during drain')
  page.timeouts.get(timeoutID).callback()
  await done
  page.realm.setInterval(() => values.push('new-document'), 60_000)
  page.tickIntervals()
  assert.deepEqual(values, [['native', 17], 'new-document'])
})

test('interval retirement and initialization share the original twenty-second preparation deadline', async t => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now: 0 })
  const page = new Page()
  const add = page.addInitScript.bind(page)
  page.addInitScript = async fn => { await new Promise(resolve => setTimeout(resolve, 15_000)); await add(fn) }
  const evaluate = page.evaluate.bind(page)
  page.evaluate = fn => fn.name === 'retireDocumentIntervals' ? new Promise(() => {}) : evaluate(fn)
  const navigation = createPageNavigation(page)
  let failed = false
  const done = assert.rejects(navigation.goto('http://app:8080/admin/accounts'), /browser requests did not finish before navigation/)
    .then(() => { failed = true })
  t.mock.timers.tick(15_000)
  await nextTask()
  t.mock.timers.tick(4_999)
  await nextTask()
  assert.equal(failed, false)
  t.mock.timers.tick(1)
  await done
  assert.equal(failed, true)
  assert.equal(page.calls.length, 0, 'no navigation or blind retry after boundary timeout')
})

test('a missing destination interval boundary fails closed instead of navigating without isolation', async () => {
  const page = new Page()
  const navigation = createPageNavigation(page)
  await navigation.goto('http://app:8080/admin/accounts')
  // Model an init script that was not executed for the next document.
  page.initScripts = []
  page.newDocument()
  await assert.rejects(navigation.reload(), /document interval boundary was not installed/)
  assert.equal(page.calls.length, 1)
})
