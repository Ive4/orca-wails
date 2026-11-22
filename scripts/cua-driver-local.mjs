import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { CuaDriverTransport } from './cua-driver-transport.mjs';

const startupTimeoutMs = 4_000;

function localError(code, message) {
  return Object.assign(new Error(message), { code });
}

export class CuaLocalTransport {
  #command;
  #hostBundle;
  #env;
  #session;
  #closed = false;

  constructor(command, { env = process.env } = {}) {
    this.#command = command;
    this.#hostBundle = env.ORCA_WAILS_BUNDLE_ID;
    this.#env = Object.fromEntries(Object.entries(env).filter(([key]) => !key.startsWith('CUA_')));
    this.#env.DO_NOT_TRACK = '1';
    this.#env.CUA_DRIVER_RS_UPDATE_CHECK = '0';
  }

  get serverInfo() {
    return this.#session?.transport?.serverInfo;
  }

  get connectionId() {
    return this.#session?.transport?.connectionId;
  }

  listTools() {
    return this.#request(transport => transport.listTools());
  }

  async call(name, args, expectedConnection) {
    if (expectedConnection !== undefined && this.connectionId !== expectedConnection) {
      throw localError('cua_driver_connection_changed', 'Cua Driver disconnected. Observe the window again before sending input.');
    }
    return this.#request(transport => transport.call(name, args, expectedConnection));
  }

  close() {
    this.#closed = true;
    if (this.#session) this.#stop(this.#session);
  }

  async #request(run) {
    const transport = await this.#getTransport();
    try {
      return await run(transport);
    } catch (error) {
      const session = this.#session;
      if (session?.transport === transport && !transport.serverInfo) this.#stop(session);
      throw error;
    }
  }

  #getTransport() {
    if (this.#closed) throw this.#closedError();
    if (!this.#session) {
      const session = {};
      this.#session = session;
      session.ready = this.#start(session);
      return session.ready;
    }
    return this.#session.ready;
  }

  #closedError() {
    return localError('cua_driver_closed', 'Cua Driver connection was closed.');
  }

  async #start(session) {
    const unavailable = () => localError('cua_driver_unavailable', 'The local Cua Driver daemon could not start.');
    try {
      // macOS gives UNIX socket paths a 104-byte limit.
      session.directory = mkdtempSync('/tmp/orca-cua-local-');
      const socket = join(session.directory, 'driver.sock');
      const pidFile = join(session.directory, 'driver.pid');
      const args = ['serve', '--embedded', '--permission-mode', 'standard', '--no-automation-prompts',
        '--socket', socket, '--pid-file', pidFile];
      if (this.#hostBundle) args.push('--host-bundle-id', this.#hostBundle);
      const child = spawn(this.#command, args, {
        env: { ...this.#env, CUA_DRIVER_PARENT_LIVENESS_STDIN: '1' },
        stdio: ['pipe', 'ignore', 'ignore'],
      });
      session.child = child;
      child.stdin.on('error', () => {});
      child.once('error', () => this.#stop(session));
      child.once('close', () => {
        clearTimeout(session.stopTimer);
        this.#stop(session);
        rmSync(session.directory, { recursive: true, force: true });
      });

      // Upstream writes the PID file only after securing the socket.
      const deadline = performance.now() + startupTimeoutMs;
      while (!session.stopped && !statSync(pidFile, { throwIfNoEntry: false })) {
        if (performance.now() >= deadline) {
          throw localError('cua_driver_timeout', 'The local Cua Driver daemon did not start in time.');
        }
        await delay(25);
      }
      if (this.#closed) throw this.#closedError();
      if (session.stopped) throw unavailable();
      session.transport = new CuaDriverTransport(this.#command, ['mcp', '--embedded', '--socket', socket], {
        env: this.#env,
        initializeTimeoutMs: startupTimeoutMs,
      });
      return session.transport;
    } catch (error) {
      this.#stop(session);
      if (error.code?.startsWith('cua_driver_')) throw error;
      throw unavailable();
    }
  }

  #stop(session) {
    if (session.stopped) return;
    session.stopped = true;
    if (this.#session === session) this.#session = undefined;
    session.transport?.close();
    if (!session.child) {
      if (session.directory) rmSync(session.directory, { recursive: true, force: true });
      return;
    }
    const child = session.child;
    // EOF shuts the embedded daemon down even when the Core exits abruptly.
    child.stdin.destroy();
    if (child.pid && child.exitCode === null && child.signalCode === null) {
      session.stopTimer = setTimeout(() => child.kill('SIGKILL'), 1_500);
      session.stopTimer.unref();
    }
  }
}
