import { createHash, randomInt, randomUUID } from 'node:crypto'
import { execFile } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { promisify } from 'node:util'
import { CuaDriverTransport } from './cua-driver-transport.mjs'
import { CuaLocalTransport } from './cua-driver-local.mjs'

const BACKENDS = new Set(['cua', 'cua-vm', 'orca'])
const SNAPSHOT_TTL = 120_000
const MAX_SNAPSHOTS = 32
const execFileAsync = promisify(execFile)
// Match the credential-app exclusions in Orca's macOS provider.
const BLOCKED_APP_IDS = new Set([
  'com.1password.1password', 'com.1password.safari', 'com.bitwarden.desktop',
  'com.dashlane.dashlanephonefinal', 'com.lastpass.lastpass', 'com.nordsec.nordpass',
  'me.proton.pass.electron', 'me.proton.pass.catalyst'
])
const ACTION_TOOLS = {
  click: 'click',
  performSecondaryAction: 'click',
  scroll: 'scroll',
  drag: 'drag',
  typeText: 'type_text',
  pressKey: 'press_key',
  hotkey: 'press_key',
  pasteText: 'paste_text',
  setValue: 'set_value'
}
const MODIFIERS = {
  command: 'cmd', cmd: 'cmd', meta: 'cmd', super: 'cmd', win: 'cmd',
  ctrl: 'ctrl', control: 'ctrl', alt: 'alt', option: 'alt', shift: 'shift', fn: 'fn'
}

function failure(code, message) {
  return Object.assign(new Error(message), { code })
}

function transportFailure(error, backend) {
  const destination = backend === 'cua-vm' ? 'Guest Cua Driver via SSH' : 'Local Cua Driver'
  if (error.code === 'cua_driver_connection_changed') return failure('window_stale', error.message)
  return failure(error.code === 'cua_driver_timeout' ? 'action_timeout' : 'provider_incompatible',
    `${destination}: ${error.message}`)
}

export function readComputerUseTarget(env = process.env, override) {
  const path = env.ORCA_WAILS_COMPUTER_USE_CONFIG_PATH
  if (!path) {
    if (override && override !== 'orca') throw failure('unsupported_capability', 'Cua requires an Orca Wails Core')
    return { backend: 'orca' }
  }
  let config
  try {
    config = JSON.parse(readFileSync(path, 'utf8'))
    if (!config || typeof config !== 'object' || Array.isArray(config)) throw new Error('expected a JSON object')
  } catch (error) {
    throw failure('invalid_argument', `Cannot read Wails Computer Use settings: ${error.message}`)
  }
  const backend = override ?? config.computer_use_backend ?? 'cua'
  if (!BACKENDS.has(backend)) throw failure('invalid_argument', 'Unknown Wails Computer Use backend')
  if (backend !== 'cua-vm') return { backend }
  const ssh = config.computer_use_vm_ssh
  if (typeof ssh !== 'string' || !/^[A-Za-z0-9_][A-Za-z0-9_.-]{0,252}$/.test(ssh)) {
    throw failure('invalid_argument', 'Set a valid guest SSH host alias in Settings > Computer Use')
  }
  return { backend, ssh }
}

export function cuaDriverPath(env = process.env) {
  const path = env.ORCA_WAILS_CUA_DRIVER_PATH
  if (!path || !existsSync(path)) {
    throw failure('unsupported_capability', 'Cua Driver is missing from this Core. Update Core or choose Orca in Settings > Computer Use.')
  }
  return path
}

export function createCuaTransport(target, env = process.env) {
  if (target.backend === 'cua-vm') {
    return new CuaDriverTransport('/usr/bin/ssh', [
      '-T', '-S', 'none', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes',
      '-o', 'ForwardAgent=no', '-o', 'ClearAllForwardings=yes',
      '-o', 'ConnectTimeout=8', '-o', 'ServerAliveInterval=10',
      '-o', 'ServerAliveCountMax=2', '--', target.ssh, 'cua-driver mcp'
    ], { initializeTimeoutMs: 10_000, timeoutMs: 25_000 })
  }
  return new CuaLocalTransport(cuaDriverPath(env), { env })
}

function appInfo(app) {
  return { name: app.name ?? app.app_name, bundleId: app.bundle_id ?? app.desktop_id ?? null, pid: app.pid }
}

function normalizeWindow(w, index) {
  const bounds = w.bounds ?? w.geometry ?? w
  return {
    id: w.window_id, index, title: w.title ?? '', x: bounds.x ?? null, y: bounds.y ?? null,
    width: bounds.width ?? bounds.w ?? 0, height: bounds.height ?? bounds.h ?? 0,
    isMinimized: w.minimized ?? w.is_minimized ?? null,
    isOffscreen: typeof w.is_on_screen === 'boolean' ? !w.is_on_screen : null,
    isMain: w.is_main ?? null
  }
}

function rankedWindow(a, b) {
  return Number(a.is_on_screen !== false) - Number(b.is_on_screen !== false) ||
    (a.z_index ?? -1) - (b.z_index ?? -1)
}

function quoteTreeValue(value) {
  return JSON.stringify(String(value).slice(0, 1000))
}

function elementLine(element, index) {
  const indent = '\t'.repeat(Math.max(0, Math.min(25, Number(element.depth) || 0)))
  const role = String(element.role || 'Element').replace(/\s+/g, '')
  const label = element.label ?? element.title ?? element.name
  const value = element.value ?? element.value_description
  const actions = [...(element.actions ?? []), ...(element.custom_actions ?? []).map(action => action.name)]
  return [
    `${indent}${index} ${role}`,
    ...(label ? [quoteTreeValue(label)] : []),
    ...(value !== undefined && value !== label ? [`Value: ${quoteTreeValue(value)}`] : []),
    ...(element.enabled === false ? ['(disabled)'] : []),
    ...(element.selected === true ? ['(selected)'] : []),
    ...(actions.length ? [`Actions: ${actions.join(', ')}`] : [])
  ].join(' ')
}

export class CuaComputerProvider {
  constructor(transport, { backend = 'cua', validateAction } = {}) {
    this.transport = transport
    this.backend = backend
    this.validateAction = validateAction
    this.snapshots = new Map()
    this.sessionPrefix = randomUUID()
    // Indexes represent one observation, even across provider restarts and switches.
    this.nextIndex = randomInt(10_000, 2 ** 40)
  }

  close() {
    this.snapshots.clear()
    this.transport.close()
  }

  async schema() {
    const connection = this.transport.connectionId
    if (!this.tools || ('connectionId' in this.transport && (!connection || this.schemaConnection !== connection))) {
      this.snapshots.clear()
      try {
        const tools = await this.transport.listTools()
        this.tools = new Map(tools.map(tool => [tool.name, tool.inputSchema]))
        this.schemaConnection = this.transport.connectionId
      } catch (error) {
        this.snapshots.clear()
        throw transportFailure(error, this.backend)
      }
    }
    return this.tools
  }

  async call(name, params = {}, expectedConnection) {
    const tools = await this.schema()
    if (!tools.has(name)) throw failure('unsupported_capability', `Cua Driver does not support ${name}`)
    try {
      return await this.transport.call(name, params, expectedConnection)
    } catch (error) {
      const code = String(error.code ?? '').toLowerCase()
      if (code.startsWith('cua_driver_')) {
        this.tools = undefined
        this.snapshots.clear()
        throw transportFailure(error, this.backend)
      }
      const mapped =
        /permission|not_trusted|consent_required/.test(code) ? 'permission_denied' :
        /screen_locked/.test(code) ? 'screen_locked' :
        /element_token|snapshot|element_not_found/.test(code) ? 'element_not_found' :
        /window.*not_found|window_owner|window_stale/.test(code) ? 'window_stale' :
        /background|would_raise|focus_unconfirmed/.test(code) ? 'background_unavailable' :
        /app_not_found|app_not_installed/.test(code) ? 'app_not_found' :
        /invalid|argument/.test(code) ? 'invalid_argument' : 'accessibility_error'
      throw failure(mapped, error.message)
    }
  }

  async platform() {
    const tools = await this.schema()
    return this.backend === 'cua' || tools.get('get_window_state')?.properties?.screenshot_format
      ? 'darwin' : 'linux'
  }

  async dispatch(method, params) {
    if (method === 'capabilities') return this.capabilities()
    if (method === 'permissionsStatus') return this.permissionsStatus()
    if (method === 'permissions') return this.permissions(params)
    if (method === 'listApps') return this.listApps()
    if (method === 'launchApp') return this.launchApp(params)
    if (method === 'listWindows') return this.listWindows(params)
    if (method === 'getAppState') return this.observe(params, await this.target(params))
    if (!Object.hasOwn(ACTION_TOOLS, method)) throw failure('invalid_argument', `Unknown computer method '${method}'`)
    await this.validateAction?.(method, params, await this.platform())
    return this.act(method, params)
  }

  async permissionsStatus() {
    const platform = await this.platform()
    const helperAppPath = null
    const tools = await this.schema()
    if (!tools.has('check_permissions')) {
      return { platform, helperAppPath, permissions: [], helperUnavailableReason: null }
    }
    const properties = tools.get('check_permissions')?.properties
    const { data } = await this.call('check_permissions', properties?.prompt ? { prompt: false } : {})
    if (platform === 'linux') {
      const desktopAvailable = data.x11 === true || (data.wayland === true && data.wayland_enabled === true)
      return {
        platform, helperAppPath,
        helperUnavailableReason: desktopAvailable && data.atspi === true ? null
          : 'Run cua-driver doctor in the guest graphical session. Check display access and the AT-SPI session bus for the SSH user.',
        permissions: [
          { id: 'accessibility', status: data.atspi === true ? 'granted' : 'not-granted' },
          { id: 'screenshots', status: 'unsupported' }
        ]
      }
    }
    return {
      platform, helperAppPath, helperUnavailableReason: null,
      permissions: [
        { id: 'accessibility', status: data.accessibility ? 'granted' : 'not-granted' },
        { id: 'screenshots', status: data.screen_recording ? 'granted' : 'not-granted' }
      ]
    }
  }

  async permissions(params) {
    const status = await this.permissionsStatus()
    const missing = status.permissions.some(permission =>
      permission.status === 'not-granted' && (!params.id || permission.id === params.id))
    const nextStep = status.platform === 'linux' ? status.helperUnavailableReason
      : missing ? (this.backend === 'cua-vm'
      ? 'Run cua-driver permissions grant in the guest desktop.'
      : 'Grant Accessibility and Screen Recording to Orca Wails, then reopen it.') : null
    let openedSettings = false
    if (this.backend === 'cua' && (missing || (params.id && !params.skipGranted))) {
      const pane = (params.id ?? status.permissions.find(permission => permission.status === 'not-granted')?.id) === 'screenshots'
        ? 'Privacy_ScreenCapture' : 'Privacy_Accessibility'
      try {
        await execFileAsync('/usr/bin/open', [`x-apple.systempreferences:com.apple.preference.security?${pane}`], { timeout: 3_000 })
        openedSettings = true
      } catch {
        // The response still tells the user which grants to change manually.
      }
    }
    return { ...status, permissionId: params.id, openedSettings, launchedHelper: false, nextStep }
  }

  async capabilities() {
    const tools = await this.schema()
    const has = name => tools.has(name)
    const platform = await this.platform()
    return {
      platform,
      provider: 'cua-driver',
      backend: this.backend,
      providerVersion: this.transport.serverInfo?.version ?? 'unknown',
      protocolVersion: 1,
      supports: {
        apps: { list: has('list_apps'), bundleIds: platform === 'darwin', pids: true, launch: has('launch_app') },
        windows: { list: has('list_windows'), targetById: true, targetByIndex: true, focus: false, moveResize: false },
        observation: { screenshot: has('get_window_state'), annotatedScreenshot: false, elementFrames: false, ocr: false, treeDiff: false, disabledState: true },
        actions: {
          click: has('click'), typeText: has('type_text'),
          typeTextElement: Boolean(tools.get('type_text')?.properties?.element_token),
          pressKey: has('press_key'), hotkey: has('press_key'), scroll: has('scroll'),
          drag: has('drag'), pasteText: has('paste_text'), setValue: has('set_value'),
          performAction: Boolean(tools.get('click')?.properties?.action)
        },
        surfaces: { menus: platform === 'darwin', dialogs: true, dock: false, menubar: false },
        input: { background: platform === 'darwin' }
      }
    }
  }

  async readApps() {
    const { data } = await this.call('list_apps')
    if (!Array.isArray(data?.apps)) throw failure('provider_incompatible', 'Cua Driver returned an invalid app list')
    return data.apps.filter(app => Number.isSafeInteger(app.pid) && typeof app.name === 'string')
  }

  async listApps() {
    return { apps: (await this.readApps()).map(app => ({
      ...appInfo(app), isRunning: app.running ?? app.pid > 0, lastUsedAt: app.last_used ?? null, useCount: null
    })) }
  }

  async findApp(query, running) {
    const q = query.trim().toLowerCase()
    if (BLOCKED_APP_IDS.has(q)) throw failure('app_blocked', `App '${query}' is blocked for safety.`)
    const matches = (await this.readApps()).filter(app => (!running || (app.running ?? app.pid > 0)) && (
      [app.name, app.bundle_id, app.desktop_id, app.launch_path].some(name => name?.toLowerCase() === q) ||
      (app.pid > 0 && (String(app.pid) === q || `pid:${app.pid}` === q))
    ))
    if (matches.length > 1) throw failure('invalid_argument', `App '${query}' is ambiguous; use its PID or bundle ID`)
    const app = matches[0]
    if (!app) throw failure('app_not_found', `App '${query}' was not found on the Cua desktop. Check list-apps.`)
    const info = appInfo(app)
    if (BLOCKED_APP_IDS.has(info.bundleId?.toLowerCase())) throw failure('app_blocked', `App '${query}' is blocked for safety.`)
    return { ...info, isRunning: app.running ?? app.pid > 0, launchPath: app.launch_path }
  }

  async launchApp(params) {
    if (params.activate) throw failure('unsupported_capability', 'Cua launch-app supports background launch; choose Orca for --activate')
    const existing = await this.findApp(params.app, false)
    const app = { name: existing.name, bundleId: existing.bundleId, pid: existing.pid }
    if (existing.isRunning) return { app, launched: false, delivery: 'background' }
    const properties = (await this.schema()).get('launch_app')?.properties
    const platform = await this.platform()
    let input
    if (platform === 'linux') {
      if (!properties?.launch_path || typeof existing.launchPath !== 'string' || !existing.launchPath) {
        throw failure('app_launch_failed', 'The guest did not report an installed launcher for this app.')
      }
      input = { launch_path: existing.launchPath }
    } else {
      input = app.bundleId && properties?.bundle_id ? { bundle_id: app.bundleId } : { name: existing.name }
    }
    const { data } = await this.call('launch_app', input)
    if (!Number.isSafeInteger(data?.pid) || data.pid <= 0) {
      throw failure('app_launch_failed', 'Cua sent the launch request, but no running process was confirmed. Check list-apps.')
    }
    return {
      app: { name: data.name ?? app.name, bundleId: data.bundle_id ?? app.bundleId, pid: data.pid },
      launched: true, delivery: data.self_activation_suppressed === false ? 'foreground' : 'background'
    }
  }

  async listWindows(params) {
    const app = await this.findApp(params.app, true)
    const info = { name: app.name, bundleId: app.bundleId, pid: app.pid }
    const windows = await this.windowsForApp(app.pid)
    return { app: info, windows: windows
      .filter(w => !w.layer || w.layer === 0)
      .sort((a, b) => rankedWindow(b, a))
      .map((w, index) => ({ ...normalizeWindow(w, index), app: info })) }
  }

  async windowsForApp(pid) {
    const properties = (await this.schema()).get('list_windows')?.properties
    const { data } = await this.call('list_windows', !properties || properties.pid ? { pid } : {})
    if (!Array.isArray(data?.windows)) throw failure('provider_incompatible', 'Cua Driver returned an invalid window list')
    return data.windows.filter(w => w.pid === pid && Number.isSafeInteger(w.window_id))
  }

  namespace(params) {
    return JSON.stringify([params.session ?? null, params.worktree ?? null])
  }

  sessionArg(params, properties) {
    if (!properties?.session) return {}
    const hash = createHash('sha256').update(this.sessionPrefix).update(this.namespace(params)).digest('hex')
    return { session: `wails-${hash.slice(0, 20)}` }
  }

  key(params) {
    return JSON.stringify([
      this.namespace(params), params.app.trim().toLowerCase(),
      params.windowId ?? null, params.windowIndex ?? null
    ])
  }

  findSnapshot(params) {
    const exact = this.snapshots.get(this.key(params))
    if (exact || params.windowId === undefined) return exact
    const q = params.app.trim().toLowerCase()
    return [...this.snapshots.values()].find(state => state.namespace === this.namespace(params) &&
      state.window.id === params.windowId &&
      [state.app.name, state.app.bundleId, String(state.app.pid), `pid:${state.app.pid}`].some(name => name?.toLowerCase() === q))
  }

  forgetWindow(app, window, namespace) {
    for (const [key, state] of this.snapshots) {
      if (state.app.pid === app.pid && state.window.id === window.id &&
          (namespace === undefined || namespace === state.namespace)) this.snapshots.delete(key)
    }
  }

  async target(params) {
    const { app, windows } = await this.listWindows(params)
    const window = params.windowId !== undefined ? windows.find(item => item.id === params.windowId)
      : windows[params.windowIndex ?? 0]
    if (!window) throw failure('window_not_found', `No matching Cua window for '${params.app}'`)
    return { app, window }
  }

  async observe(params, target, expectedConnection) {
    const { app } = target
    let { window } = target
    const key = this.key(params)
    if (params.rootElementIndex !== undefined) {
      throw failure('unsupported_capability', 'Cua reads the whole window. Omit --root-element-index for a fresh observation.')
    }
    const namespace = this.namespace(params)
    this.forgetWindow(app, window, namespace)
    const properties = (await this.schema()).get('get_window_state')?.properties
    const maxEdge = [
      params.screenshotMaxEdge,
      params.screenshotScale && Math.round(Math.max(window.width, window.height) * params.screenshotScale)
    ].filter(value => Number.isSafeInteger(value) && value > 0)
    const { data, images, connectionId } = await this.call('get_window_state', {
      pid: app.pid, window_id: window.id, include_screenshot: !params.noScreenshot,
      tree_format: 'elements',
      ...this.sessionArg(params, properties),
      ...(properties?.screenshot_format && params.screenshotFormat ? { screenshot_format: params.screenshotFormat } : {}),
      ...(maxEdge.length ? { max_dimension: Math.min(...maxEdge) } : {})
    }, expectedConnection)
    if (data?.pid !== app.pid || data?.window_id !== window.id) {
      throw failure('window_stale', 'Cua could no longer observe the selected app window')
    }
    if (data.window_bounds?.width > 0 && data.window_bounds?.height > 0) {
      window = { ...window, ...data.window_bounds }
    }
    if ((data.element_count ?? 0) === 0 && /kAXErrorAPIDisabled/.test(data.degraded_reason ?? '') && !images.length) {
      const location = this.backend === 'cua-vm' ? 'the guest Cua Driver' : 'Orca Wails'
      throw failure('permission_denied', `Grant Accessibility to ${location}, then observe the window again.`)
    }
    const elements = new Map()
    const lines = [`${app.name} — ${window.title}`, '']
    const nativeIndexes = new Map()
    const disabledElementIndexes = []
    for (const element of data.elements ?? []) {
      if (typeof element.element_token !== 'string') continue
      const index = this.nextIndex++
      elements.set(index, element)
      nativeIndexes.set(element.element_index, index)
      if (element.enabled === false) disabledElementIndexes.push(index)
      lines.push(elementLine(element, index))
    }
    for (const observation of data.observations ?? []) {
      lines.push(elementLine(observation, '•'))
    }
    if (!elements.size && data.tree_markdown) lines.push(String(data.tree_markdown).slice(0, 24_000))
    if (data.degraded_reason) lines.push('', String(data.degraded_reason))
    const image = images.find(part => ['image/png', 'image/jpeg'].includes(part.mimeType))
    const width = data.screenshot_width
    const height = data.screenshot_height
    const screenshot = !params.noScreenshot && data.screenshot_frame_valid !== false && image &&
      width > 0 && height > 0 && window.width > 0 && window.height > 0 ? {
        data: image.data, width, height, scale: width / window.width,
        format: image.mimeType === 'image/jpeg' ? 'jpeg' : 'png'
      } : null
    const snapshot = {
      id: data.snapshot_id ?? randomUUID(), app, window, backend: this.backend,
      coordinateSpace: 'window', treeText: lines.join('\n'),
      elementCount: data.element_count ?? elements.size,
      focusedElementId: nativeIndexes.get(data.focused_element_index) ?? null,
      disabledElementIndexes,
      ...(data.truncated ? { truncation: { truncated: true } } : {})
    }
    const screenshotStatus = screenshot ? { state: 'captured', metadata: { windowId: window.id } }
      : params.noScreenshot ? { state: 'skipped', reason: 'no_screenshot_flag' }
      : { state: 'failed', code: 'screenshot_failed', message: 'Cua returned no window screenshot. Check Screen Recording permission on the selected desktop.' }
    if (this.snapshots.size >= MAX_SNAPSHOTS) this.snapshots.delete(this.snapshots.keys().next().value)
    this.snapshots.set(key, {
      app, window, elements, namespace, captureId: data.capture_id, connectionId,
      expiresAt: Date.now() + SNAPSHOT_TTL,
      scaleX: screenshot?.scale, scaleY: screenshot ? screenshot.height / window.height : undefined
    })
    return { snapshot, screenshot, screenshotStatus }
  }

  requireElement(state, index) {
    const element = state?.expiresAt > Date.now() && state.elements.get(index)
    if (!element) throw failure('element_not_found', 'Cua element reference is stale. Run get-app-state again on this window.')
    return element
  }

  point(state, x, y) {
    if (!(state?.expiresAt > Date.now() && state.scaleX > 0 && state.scaleY > 0)) {
      throw failure('screenshot_failed', 'Run get-app-state with a screenshot before using Cua coordinates.')
    }
    if (x < 0 || y < 0 || x >= state.window.width || y >= state.window.height) {
      throw failure('invalid_argument', 'Cua coordinates are outside the observed window')
    }
    return { x: Math.floor(x * state.scaleX), y: Math.floor(y * state.scaleY) }
  }

  async act(method, params) {
    const tools = await this.schema()
    const name = method === 'click' && params.elementIndex !== undefined && params.clickCount === 2 &&
      (!params.mouseButton || params.mouseButton === 'left') && tools.has('double_click')
      ? 'double_click' : ACTION_TOOLS[method]
    if (!tools.has(name)) throw failure('unsupported_capability', `Cua Driver does not support ${method}`)
    const properties = tools.get(name)?.properties
    const state = this.findSnapshot(params)
    const element = params.elementIndex !== undefined ? this.requireElement(state, params.elementIndex) : null
    const coordinates = params.x !== undefined || params.fromX !== undefined || params.fromElementIndex !== undefined
    const target = element || coordinates ? state : state?.expiresAt > Date.now() ? state : await this.target(params)
    if (!target) throw failure('screenshot_failed', 'Run get-app-state with a screenshot before using Cua coordinates.')
    const input = { pid: target.app.pid, window_id: target.window.id, ...this.sessionArg(params, properties) }
    if (!properties || properties.delivery_mode) input.delivery_mode = params.restoreWindow ? 'foreground' : 'background'
    else if (params.restoreWindow) throw failure('unsupported_capability', 'This Cua desktop does not support --restore-window')
    if (element) {
      if (properties && !properties.element_token) throw failure('unsupported_capability', `Cua ${name} does not accept element targets`)
      delete input.window_id
      input.element_token = element.element_token
    }
    if (params.x !== undefined) {
      Object.assign(input, this.point(state, params.x, params.y))
      if (state.captureId && properties?.capture_id) input.capture_id = state.captureId
    }
    const platform = await this.platform()
    const modifiers = value => value.split('+').map(part => {
      const key = part.trim().toLowerCase()
      if (key === 'cmdorctrl' || key === 'commandorcontrol') return platform === 'darwin' ? 'cmd' : 'ctrl'
      return (platform === 'darwin' ? MODIFIERS[key] : undefined) ?? key
    })
    switch (method) {
      case 'click':
        if (element && ((params.clickCount > 1 && name !== 'double_click') || params.modifiers)) {
          throw failure('unsupported_capability', 'Use Cua screenshot coordinates for modified or multiple clicks.')
        }
        if (name !== 'double_click') {
          if (params.clickCount !== undefined) input.count = params.clickCount
          if (params.mouseButton) input.button = params.mouseButton
        }
        if (params.modifiers) input.modifier = modifiers(params.modifiers)
        break
      case 'performSecondaryAction':
        input.action = params.action
        break
      case 'drag': {
        const center = index => {
          const frame = this.requireElement(state, index).frame
          if (!frame || state.window.x === null || state.window.y === null) {
            throw failure('element_not_found', 'Cua did not report this element’s position; use screenshot coordinates.')
          }
          return this.point(state, frame.x - state.window.x + frame.w / 2, frame.y - state.window.y + frame.h / 2)
        }
        const from = params.fromElementIndex !== undefined ? center(params.fromElementIndex) : this.point(state, params.fromX, params.fromY)
        const to = params.toElementIndex !== undefined ? center(params.toElementIndex) : this.point(state, params.toX, params.toY)
        Object.assign(input, { from_x: from.x, from_y: from.y, to_x: to.x, to_y: to.y })
        break
      }
      case 'scroll':
        if (params.pages !== undefined && !Number.isInteger(params.pages)) {
          throw failure('unsupported_capability', 'Cua scroll supports a whole number of pages.')
        }
        Object.assign(input, { by: 'page', amount: params.pages ?? 1, direction: params.direction })
        break
      case 'typeText':
      case 'pasteText':
        input.text = params.text
        break
      case 'pressKey':
      case 'hotkey': {
        const keys = method === 'hotkey' ? modifiers(params.key) : [params.key]
        input.key = keys.pop().toLowerCase()
        if (keys.length) input.modifiers = keys
        break
      }
      case 'setValue':
        input.value = params.value
        break
    }
    if (properties) {
      const unsupported = Object.keys(input).find(key => !Object.hasOwn(properties, key))
      if (unsupported) throw failure('unsupported_capability', `Cua ${name} does not support ${unsupported} on this desktop`)
    }
    const expectedConnection = target.connectionId ?? this.transport.connectionId
    if (coordinates && !input.capture_id) {
      const windows = await this.windowsForApp(target.app.pid)
      const current = windows.find(window => window.window_id === target.window.id)
      const bounds = current && normalizeWindow(current)
      if (!bounds || ['x', 'y', 'width', 'height'].some(key => bounds[key] !== target.window[key])) {
        this.forgetWindow(target.app, target.window)
        throw failure('window_stale', 'The Cua window moved or changed size. Capture it again before sending coordinates.')
      }
    }
    // The input may have reached the app even if the transport or follow-up observation fails.
    this.forgetWindow(target.app, target.window)
    const { data } = await this.call(name, input, expectedConnection)
    const path = method === 'pasteText' ? 'clipboard' : /^(ax|accessibility)/.test(data?.path ?? '') || method === 'setValue'
      ? 'accessibility' : 'synthetic'
    const verifiedProperty = method === 'setValue' ? 'value' : method === 'typeText'
      ? (element ? 'elementText' : 'focusedText') : method === 'click' && data?.verified && path === 'accessibility' ? 'selection' : null
    const action = {
      path, targetWindowId: target.window.id,
      ...(input.delivery_mode ? { delivery: input.delivery_mode } : {}),
      verification: data?.verified === true && verifiedProperty
        ? { state: 'verified', property: verifiedProperty }
        : { state: 'unverified', reason: path === 'clipboard' ? 'clipboard_paste' : path === 'accessibility' ? 'accessibility_action_unasserted' : 'synthetic_input' }
    }
    const popup = data?.popup
    const observedTarget = Number.isSafeInteger(popup?.window_id)
      ? { app: target.app, window: { ...target.window, ...normalizeWindow(popup, target.window.index) } }
      : target
    const observationParams = observedTarget === target
      ? params : { ...params, windowId: popup.window_id, windowIndex: undefined }
    try {
      return { ...await this.observe(observationParams, observedTarget, expectedConnection), action }
    } catch (error) {
      throw failure('verification_failed', `Cua dispatched ${name}. The follow-up observation failed: ${error.message}. Observe the window before retrying.`)
    }
  }
}
