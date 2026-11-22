import { spawn } from 'node:child_process';

const protocolVersion = '2025-06-18';
const maxResponseLength = 128 * 1024 * 1024;

function driverError(code, message) {
  return Object.assign(new Error(message), { code });
}

function protocolError(message = 'Cua Driver returned an invalid MCP response.') {
  return driverError('cua_driver_protocol_error', message);
}

function unavailable() {
  return driverError('cua_driver_unavailable', 'Cua Driver process is unavailable.');
}

function errorCode(...codes) {
  return codes.find((code) => typeof code === 'string' && /^[a-z0-9_.-]{1,128}$/i.test(code));
}

function errorMessage(message, args, images = []) {
  const inputs = [args?.text, args?.value];
  for (const input of inputs) {
    if (typeof input !== 'string' || !input) continue;
    message = message.replaceAll(input, '[redacted]');
    message = message.replaceAll(JSON.stringify(input).slice(1, -1), '[redacted]');
  }
  for (const image of images) {
    if (image.data) message = message.replaceAll(image.data, '[redacted]');
  }
  return message
    .replace(/data:image\/[\w.+-]+;base64,[\w+/=-]+/gi, '[redacted]')
    .replace(/[a-z0-9+/_=-]{128,}/gi, '[redacted]')
    .slice(0, 4096);
}

export class CuaDriverTransport {
  #command;
  #args;
  #env;
  #timeoutMs;
  #initializeTimeoutMs;
  #session;
  #closed = false;
  #nextId = 0;

  constructor(command, args, { env, timeoutMs = 120_000, initializeTimeoutMs = timeoutMs } = {}) {
    this.#command = command;
    this.#args = args;
    this.#env = env;
    this.#timeoutMs = timeoutMs;
    this.#initializeTimeoutMs = initializeTimeoutMs;
  }

  get serverInfo() {
    return this.#session?.serverInfo;
  }

  get connectionId() {
    return this.#session?.identity;
  }

  async listTools() {
    const session = this.#getSession();
    await session.ready;
    const result = await this.#request(session, 'tools/list', {});
    if (!Array.isArray(result?.tools)) throw protocolError();
    return result.tools;
  }

  async call(toolName, params = {}, expectedConnection) {
    if (expectedConnection !== undefined && this.connectionId !== expectedConnection) {
      throw driverError('cua_driver_connection_changed', 'Cua Driver disconnected. Observe the window again before sending input.');
    }
    const session = this.#getSession();
    await session.ready;
    const result = await this.#request(session, 'tools/call', { name: toolName, arguments: params });
    if (!Array.isArray(result?.content)) throw protocolError();
    const images = result.content.filter((block) => block?.type === 'image'
      && typeof block.data === 'string' && typeof block.mimeType === 'string');
    const text = result.content.filter((block) => block?.type === 'text'
      && typeof block.text === 'string').map((block) => block.text);
    const data = result.structuredContent;
    if (result.isError === true) {
      const code = errorCode(data?.refusal?.code, data?.error?.code, data?.code, data?.error) ?? 'cua_tool_error';
      const message = [data?.refusal?.message, data?.error?.message, data?.message, text[0]]
        .find((value) => typeof value === 'string' && value) ?? 'Cua Driver rejected the tool call.';
      throw driverError(code, errorMessage(message, params, images));
    }
    return { data: data === undefined ? (text.length ? text.join('\n') : null) : data, images, connectionId: session.identity };
  }

  close() {
    this.#closed = true;
    if (this.#session) this.#endSession(this.#session, this.#closedError());
  }

  #closedError() {
    return driverError('cua_driver_closed', 'Cua Driver connection was closed.');
  }

  #getSession() {
    if (this.#closed) throw this.#closedError();
    if (this.#session) return this.#session;

    let child;
    try {
      child = spawn(this.#command, this.#args, { env: this.#env, stdio: ['pipe', 'pipe', 'pipe'] });
    } catch {
      throw unavailable();
    }
    const session = { child, identity: Symbol(), pending: new Map(), chunks: [], length: 0 };
    this.#session = session;
    const fail = () => this.#endSession(session, unavailable());
    child.on('error', fail);
    child.on('close', fail);
    child.stdin.on('error', fail);
    child.stdout.on('error', fail);
    child.stdout.on('end', () => this.#endSession(session, session.length ? protocolError() : unavailable()));
    child.stdout.setEncoding('utf8');
    child.stdout.on('data', (chunk) => this.#read(session, chunk));
    child.stderr.on('error', () => {});
    child.stderr.resume();
    session.ready = this.#initialize(session);
    return session;
  }

  async #initialize(session) {
    try {
      const result = await this.#request(session, 'initialize', {
        protocolVersion,
        capabilities: {},
        clientInfo: { name: 'orca-wails', version: '1' },
      }, this.#initializeTimeoutMs);
      if (result?.protocolVersion !== protocolVersion) {
        throw protocolError('Cua Driver returned an unsupported MCP protocol version.');
      }
      if (session.error) throw session.error;
      const { name, version } = result.serverInfo ?? {};
      if (typeof name === 'string' && typeof version === 'string') {
        session.serverInfo = { name, version };
      }
      this.#write(session, '{"jsonrpc":"2.0","method":"notifications/initialized"}\n');
    } catch (error) {
      this.#endSession(session, error);
      throw error;
    }
  }

  #request(session, method, params, timeoutMs = this.#timeoutMs) {
    if (session.error) throw session.error;
    const id = ++this.#nextId;
    let line;
    try {
      line = JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n';
    } catch {
      throw driverError('cua_driver_invalid_request', 'Cua Driver request could not be serialized.');
    }
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => this.#endSession(session,
        driverError('cua_driver_timeout', 'Cua Driver request timed out.')), timeoutMs);
      session.pending.set(id, { resolve, reject, timer, args: params.arguments });
      this.#write(session, line);
    });
  }

  #write(session, line) {
    try {
      session.child.stdin.write(line);
    } catch {
      this.#endSession(session, unavailable());
    }
  }

  #append(session, chunk) {
    session.length += chunk.length;
    if (session.length > maxResponseLength) {
      this.#endSession(session, protocolError('Cua Driver returned an oversized MCP response.'));
      return false;
    }
    if (chunk) session.chunks.push(chunk);
    return true;
  }

  #read(session, chunk) {
    if (session.error) return;
    const parts = chunk.split('\n');
    const tail = parts.pop();
    for (const part of parts) {
      if (!this.#append(session, part)) return;
      const line = session.chunks.join('');
      session.chunks.length = 0;
      session.length = 0;
      if (line.trim()) this.#handleLine(session, line);
      if (session.error) return;
    }
    this.#append(session, tail);
  }

  #handleLine(session, line) {
    let message;
    try {
      message = JSON.parse(line);
    } catch {
      this.#endSession(session, protocolError());
      return;
    }
    if (message?.jsonrpc === '2.0' && typeof message.method === 'string' && !Object.hasOwn(message, 'id')) return;
    if (message?.jsonrpc !== '2.0' || !Number.isSafeInteger(message.id)
      || Object.hasOwn(message, 'result') === Object.hasOwn(message, 'error')) {
      this.#endSession(session, protocolError());
      return;
    }
    const request = session.pending.get(message.id);
    if (!request) return;
    session.pending.delete(message.id);
    clearTimeout(request.timer);
    if (Object.hasOwn(message, 'result')) {
      request.resolve(message.result);
      return;
    }
    const error = message.error;
    const code = Number.isSafeInteger(error?.code) ? String(error.code) : errorCode(error?.code);
    request.reject(code && typeof error.message === 'string'
      ? driverError(code, errorMessage(error.message, request.args)) : protocolError());
  }

  #endSession(session, error) {
    if (session.error) return;
    session.error = error;
    this.#session = undefined;
    session.chunks.length = 0;
    session.length = 0;
    for (const request of session.pending.values()) {
      clearTimeout(request.timer);
      request.reject(error);
    }
    session.pending.clear();
    session.child.stdin.destroy();
    session.child.kill();
  }
}
