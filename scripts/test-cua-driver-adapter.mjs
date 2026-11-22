import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { CuaComputerProvider, readComputerUseTarget } from './cua-driver-adapter.mjs'

test('reads the saved backend on each request and validates a VM alias', () => {
  const root = mkdtempSync(join(tmpdir(), 'orca-cua-config-'))
  try {
    const path = join(root, 'config.json')
    const env = { ORCA_WAILS_COMPUTER_USE_CONFIG_PATH: path }
    writeFileSync(path, '{}')
    assert.deepEqual(readComputerUseTarget(env), { backend: 'cua' })
    writeFileSync(path, JSON.stringify({ computer_use_backend: 'orca' }))
    assert.deepEqual(readComputerUseTarget(env), { backend: 'orca' })
    assert.throws(() => readComputerUseTarget(env, 'cua-vm'), { code: 'invalid_argument' })
    writeFileSync(path, JSON.stringify({ computer_use_backend: 'orca', computer_use_vm_ssh: 'orca-lume' }))
    assert.deepEqual(readComputerUseTarget(env, 'cua-vm'), { backend: 'cua-vm', ssh: 'orca-lume' })
    assert.deepEqual(readComputerUseTarget(env), { backend: 'orca' })
    writeFileSync(path, JSON.stringify({ computer_use_backend: 'cua-vm', computer_use_vm_ssh: 'orca-lume' }))
    assert.deepEqual(readComputerUseTarget(env), { backend: 'cua-vm', ssh: 'orca-lume' })
    writeFileSync(path, JSON.stringify({ computer_use_backend: 'cua-vm', computer_use_vm_ssh: '-oProxyCommand=evil' }))
    assert.throws(() => readComputerUseTarget(env), { code: 'invalid_argument' })
  } finally {
    rmSync(root, { recursive: true })
  }
})

test('converts window-local points and refuses a stale element before sending input', async () => {
  const requests = []
  let snapshot = 0
  let originX = 50
  const tools = {
    list_apps: { apps: [{ pid: 17, name: 'Fixture', bundle_id: 'dev.fixture', running: true }] },
    click: { path: 'cgevent', verified: false },
  }
  const provider = new CuaComputerProvider({
    listTools: async () => ['list_apps', 'list_windows', 'get_window_state', 'click'].map(name => ({ name })),
    async call(name, params) {
      requests.push([name, params])
      if (name === 'list_windows') {
        return { data: { windows: [{ pid: 17, window_id: 9, title: 'Fixture Window',
          bounds: { x: originX, y: 100, width: 400, height: 300 }, is_on_screen: true }] }, images: [] }
      }
      if (name === 'get_window_state') {
        const id = `s${++snapshot}`
        return { data: { snapshot_id: id, pid: 17, window_id: 9, element_count: 1,
          elements: [{ element_index: 0, element_token: `${id}:0`, role: 'AXButton', label: 'Run', depth: 0 }],
          screenshot_width: 800, screenshot_height: 600 }, images: [{ mimeType: 'image/png', data: 'fixture' }] }
      }
      return { data: tools[name], images: [] }
    },
  })
  const first = await provider.dispatch('getAppState', { app: 'dev.fixture' })
  const index = Number(first.snapshot.treeText.match(/^(\d+) AXButton/m)[1])
  assert.equal(first.screenshot.scale, 2)
  assert.equal(first.snapshot.window.width, 400)
  await provider.dispatch('click', { app: 'Fixture', windowId: 9, x: 12, y: 14, noScreenshot: true })
  const [, click] = requests.find(([name]) => name === 'click')
  assert.equal(click.x, 24)
  assert.equal(click.y, 28)
  assert.equal(click.window_id, 9)
  assert.equal(click.delivery_mode, 'background')
  await assert.rejects(provider.dispatch('click', { app: 'dev.fixture', elementIndex: index }), { code: 'element_not_found' })
  await provider.dispatch('getAppState', { app: 'dev.fixture' })
  originX += 20
  await assert.rejects(provider.dispatch('click', { app: 'dev.fixture', x: 12, y: 14 }), { code: 'window_stale' })
  assert.equal(requests.filter(([name]) => name === 'click').length, 1)
})

test('Linux guest checks its desktop and launches only a listed desktop app', async () => {
  const requests = []
  const schemas = {
    get_window_state: { properties: { pid: {}, window_id: {} } },
    check_permissions: { properties: {} },
    list_apps: { properties: {} },
    launch_app: { properties: { launch_path: {}, bundle_id: {} } },
  }
  const results = {
    check_permissions: { x11: true, atspi: true },
    list_apps: { apps: [{ pid: 0, running: false, name: 'Fixture', bundle_id: 'dev.fixture', launch_path: '/usr/bin/fixture' }] },
    launch_app: { pid: 7 },
  }
  const provider = new CuaComputerProvider({
    async listTools() { return Object.entries(schemas).map(([name, inputSchema]) => ({ name, inputSchema })) },
    async call(name, params) {
      requests.push([name, params])
      return { data: results[name], images: [] }
    },
  }, { backend: 'cua-vm' })
  const permissions = await provider.dispatch('permissions', {})
  assert.equal(permissions.platform, 'linux')
  assert.equal(permissions.nextStep, null)
  assert.deepEqual(permissions.permissions, [
    { id: 'accessibility', status: 'granted' },
    { id: 'screenshots', status: 'unsupported' },
  ])
  assert.deepEqual((await provider.dispatch('launchApp', { app: 'dev.fixture' })).app, {
    name: 'Fixture', bundleId: 'dev.fixture', pid: 7,
  })
  await assert.rejects(provider.dispatch('launchApp', { app: '/usr/bin/other' }), { code: 'app_not_found' })
  assert.deepEqual(requests.filter(([name]) => name === 'launch_app'), [
    ['launch_app', { launch_path: '/usr/bin/fixture' }],
  ])
})
