import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { runInNewContext } from 'node:vm'

const source = readFileSync(new URL('../assets/desktop-storage-migration.js', import.meta.url), 'utf8')
const endpoint = {
  endpoint: 'ws://127.0.0.1:16768/',
  publicKeyB64: 'local-public-key',
  deviceToken: 'legacy-token'
}
const settingsKey = 'orca.web.settings.v1'
const uiKey = 'orca.web.ui.v1'
const sessionKey = 'orca.web.workspaceSession.v1'

function migrate(environment, identity) {
  const data = new Map([
    ['orca.web.runtimeEnvironment.v1', JSON.stringify(environment)],
    [settingsKey, JSON.stringify({ theme: 'dark', activeRuntimeEnvironmentId: 'old-id' })],
    [uiKey, JSON.stringify({
      workspaceHostScope: 'runtime:old-id',
      workspaceHostOrder: ['runtime:old-id', 'runtime:older-id', 'runtime:external']
    })],
    [`${sessionKey}.runtime:older-id`, JSON.stringify({ activeWorktreeId: 'local-worktree' })]
  ])
  return runInNewContext(`${source}(identity)`, {
    identity, URL, location: { host: '127.0.0.1:16768' },
    localStorage: { getItem: key => data.get(key) ?? null }
  })
}

test('moves verified local preferences and workspace references without copying credentials', () => {
  const result = migrate({
    id: 'old-id', compatibleEnvironmentIds: ['older-id'], endpoints: [endpoint]
  }, { publicKeyB64: endpoint.publicKeyB64 })
  assert.doesNotMatch(result, /legacy-token|runtimeEnvironment\.v1/)
  const snapshot = JSON.parse(result)
  assert.deepEqual(JSON.parse(snapshot[settingsKey]), { theme: 'dark', activeRuntimeEnvironmentId: null })
  assert.deepEqual(JSON.parse(snapshot[uiKey]), {
    workspaceHostScope: 'local', workspaceHostOrder: ['local', 'runtime:external']
  })
  assert.deepEqual(JSON.parse(snapshot[sessionKey]), { activeWorktreeId: 'local-worktree' })
})

test('rejects stale identities and endpoints away from the original loopback origin', () => {
  assert.equal(migrate({ id: 'old-id', endpoints: [endpoint] }, { publicKeyB64: 'different-key' }), null)
  assert.equal(migrate({
    id: 'old-id', hostDescriptor: { installationId: 'local-installation' },
    endpoints: [{ ...endpoint, endpoint: 'ws://example.com:16768' }]
  }, { installationId: 'local-installation', publicKeyB64: endpoint.publicKeyB64 }), null)
})
