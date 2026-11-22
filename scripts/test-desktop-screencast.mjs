import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { runInNewContext } from 'node:vm'
import { test } from 'node:test'

const source = readFileSync(new URL('../assets/desktop-init.js', import.meta.url), 'utf8')

test('the desktop delivers screenshot bytes in the same form as the streaming client', async () => {
  const window = {}
  let polls = 0
  const responses = []
  const frames = []
  const errors = []
  const requests = []
  let finish
  const closed = new Promise((resolve) => { finish = resolve })
  runInNewContext(`${source}({token: 'fixture', platform: 'darwin'})`, {
    window,
    AbortController,
    Uint8Array,
    atob,
    crypto: globalThis.crypto,
    fetch: async (url, options) => {
      assert.equal(url, '/desktop/rpc')
      assert.equal(options.headers['X-Orca-Desktop'], 'fixture')
      const body = JSON.parse(options.body)
      requests.push(body)
      if (body.action !== 'poll') {
        return { status: 204 }
      }
      if (polls++ > 0) {
        return { status: 410 }
      }
      return {
        status: 200,
        ok: true,
        json: async () => [
          { ok: true, result: { type: 'ready' } },
          { ok: true, _wailsBinary: 'AQID/w==' }
        ]
      }
    }
  })

  await window.__orcaWailsDesktop.subscribe('browser.screencast', { page: 'fixture-page' }, {
    onResponse: (response) => responses.push(response.result.type),
    onBinary: (bytes) => frames.push(bytes),
    onError: (error) => errors.push(error),
    onClose: finish
  })
  await closed
  assert.deepEqual(responses, ['ready'])
  assert.deepEqual(frames, [Uint8Array.of(1, 2, 3, 255)])
  assert.deepEqual(errors, [])
  assert.deepEqual(requests.map((request) => request.action), ['subscribe', 'poll', 'poll', 'cancel'])
  assert.ok(requests.every((request) => request.id === requests[0].id))
})

test('opens desktop settings when a cold renderer registers, then handles later requests immediately', () => {
  const window = {}
  runInNewContext(`${source}({token: 'fixture', platform: 'darwin', shellSettings: true})`, {
    window,
    crypto: globalThis.crypto
  })

  const bridge = window.__orcaWailsDesktop
  let opened = 0
  assert.equal(bridge.shellSettings, true)
  bridge.openSettings()
  bridge.openSettings()
  bridge.registerSettings(() => { opened++ })
  assert.equal(opened, 1)
  bridge.openSettings()
  assert.equal(opened, 2)
})
