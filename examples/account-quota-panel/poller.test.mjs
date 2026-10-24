import test from 'node:test'
import assert from 'node:assert/strict'
import { setImmediate as immediate } from 'node:timers/promises'
import { createQuotaPoller } from './poller.mjs'

const summary = { generated_at: '2026-10-24T00:00:00Z', eligible: 0, pending: 0, groups: [] }
const ok = () => new Response(JSON.stringify(summary), { status: 200 })
const base = { baseUrl: 'https://keeper.example/keeper/', apiKey: 'host-only-key', onData() {} }
const flush = async () => { await immediate(); await immediate() }

function setup(t, fetchImpl) {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const data = [], statuses = [], requests = []
  const poller = createQuotaPoller({ ...base, fetchImpl: (...args) => { requests.push(args); return fetchImpl(...args) }, onData: value => data.push(value), onStatus: status => statuses.push(status) })
  t.after(() => poller.stop())
  return { poller, data, statuses, requests }
}

test('host requests the base-path endpoint with a header key and polls every 60 seconds', async t => {
  const { poller, data, statuses, requests } = setup(t, async () => ok())
  await poller.start()
  assert.equal(requests[0][0], 'https://keeper.example/keeper/api/v1/external/quota/summary')
  const options = requests[0][1]
  assert.equal(options.headers.Authorization, 'Bearer host-only-key')
  assert.equal(options.method, 'GET')
  assert.equal(options.redirect, 'error')
  assert.equal(options.cache, 'no-store')
  assert.equal(requests[0][0].includes('host-only-key'), false)
  assert.deepEqual(data, [summary])
  assert.deepEqual(statuses.map(status => status.state), ['loading', 'ok'])
  t.mock.timers.tick(59_999)
  await flush()
  assert.equal(requests.length, 1)
  t.mock.timers.tick(1)
  await flush()
  assert.equal(requests.length, 2)
  assert.ok(statuses.at(-1).lastSuccessAt)
})

test('retains last success and backs off 60/120/240/300 seconds, then recovers', async t => {
  let fail = false
  const { poller, data, statuses, requests } = setup(t, async () => fail ? new Response('', { status: 503 }) : ok())
  await poller.start()
  fail = true
  t.mock.timers.tick(60_000)
  await flush()
  assert.equal(statuses.at(-1).state, 'error')
  assert.equal(data.length, 1)
  assert.ok(statuses.at(-1).lastSuccessAt)
  for (const delay of [60_000, 120_000, 240_000, 300_000, 300_000]) {
    const before = requests.length
    t.mock.timers.tick(delay - 1)
    await flush()
    assert.equal(requests.length, before)
    t.mock.timers.tick(1)
    await flush()
    assert.equal(requests.length, before + 1)
  }
  fail = false
  t.mock.timers.tick(300_000)
  await flush()
  assert.equal(statuses.at(-1).state, 'ok')
  assert.equal(data.length, 2)
  const before = requests.length
  t.mock.timers.tick(60_000)
  await flush()
  assert.equal(requests.length, before + 1)
})

test('401 stops polling until explicitly restarted and does not expose a response body', async t => {
  const { poller, data, statuses, requests } = setup(t, async () => new Response('host-only-key', { status: 401 }))
  await poller.start()
  t.mock.timers.tick(900_000)
  await flush()
  assert.equal(requests.length, 1)
  assert.deepEqual(data, [])
  assert.equal(statuses.at(-1).state, 'unauthorized')
  assert.equal(JSON.stringify(statuses).includes('host-only-key'), false)
  await poller.start()
  assert.equal(requests.length, 2)
})

test('10 second timeout aborts without overlapping requests; stopping cancels retries', async t => {
  let aborted = 0
  const { poller, statuses, requests } = setup(t, (_url, options) => new Promise((_resolve, reject) => {
    options.signal.addEventListener('abort', () => { aborted++; reject(new Error('private request headers')) }, { once: true })
  }))
  const pending = poller.start()
  poller.start() // idempotent while active
  t.mock.timers.tick(9_999)
  await flush()
  assert.equal(aborted, 0)
  assert.equal(requests.length, 1)
  t.mock.timers.tick(1)
  await pending
  assert.equal(aborted, 1)
  assert.equal(statuses.at(-1).state, 'error')
  assert.equal(JSON.stringify(statuses).includes('private'), false)
  poller.stop()
  t.mock.timers.tick(900_000)
  await flush()
  assert.equal(requests.length, 1)
  assert.equal(statuses.at(-1).state, 'stopped')
})

test('a stopped or superseded in-flight response cannot overwrite the panel', async t => {
  let resolve
  const { poller, data, statuses } = setup(t, () => new Promise(done => { resolve = done }))
  const pending = poller.start()
  poller.stop()
  resolve(ok()) // simulate a host transport that ignores AbortSignal
  await pending
  assert.deepEqual(data, [])
  assert.equal(statuses.at(-1).state, 'stopped')
})

test('network failures and malformed responses retain data without forwarding raw errors', async t => {
  let mode = 0
  const { poller, statuses, data } = setup(t, async () => {
    if (mode === 0) throw new Error('private https://host/?key=host-only-key')
    if (mode === 1) return new Response('<html>proxy login</html>', { status: 200 })
    return new Response('{"wrong":true}', { status: 200 })
  })
  await poller.start()
  for (mode = 1; mode < 3; mode++) {
    t.mock.timers.tick(mode === 1 ? 60_000 : 120_000)
    await flush()
    assert.equal(statuses.at(-1).state, 'error')
  }
  assert.deepEqual(data, [])
  assert.equal(JSON.stringify(statuses).includes('host-only-key'), false)
})

test('invalid host configuration never reflects accidental URL credentials', () => {
  for (const baseUrl of ['https://user:secret@keeper.example', 'https://keeper.example?key=secret', 'file:///secret', 'https://keeper.example/#secret']) {
    assert.throws(() => createQuotaPoller({ ...base, baseUrl }), err => !err.message.includes('secret'))
  }
  assert.throws(() => createQuotaPoller({ ...base, apiKey: '' }))
})
