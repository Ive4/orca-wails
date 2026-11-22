import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { CuaDriverTransport } from './cua-driver-transport.mjs';

const fixture = String.raw`
const assert = require('node:assert/strict');
const readline = require('node:readline');
let initialized = false;
let connected = false;
const send = (id, result) => process.stdout.write(JSON.stringify({jsonrpc: '2.0', id, result}) + '\n');
const error = (structuredContent, message, extra = []) => ({
  isError: true, structuredContent, content: [...extra, {type: 'text', text: message}],
});
readline.createInterface({input: process.stdin}).on('line', (line) => {
  const request = JSON.parse(line);
  const {id, method, params} = request;
  assert.equal(request.jsonrpc, '2.0');
  if (method === 'initialize') {
    assert.equal(initialized, false);
    assert.equal(params.protocolVersion, '2025-06-18');
    assert.equal(params.clientInfo.name, 'orca-wails');
    assert.deepEqual(params.capabilities, {});
    initialized = true;
    if (process.env.CUA_TRANSPORT_TEST_HOLD_INITIALIZE) return;
    return send(id, {protocolVersion: process.env.CUA_TRANSPORT_TEST_PROTOCOL ?? '2025-06-18',
      capabilities: {tools: {}}, serverInfo: {name: 'cua-driver', version: '0.56.4'}});
  }
  if (method === 'notifications/initialized') {
    assert.equal(initialized, true);
    assert.equal(id, undefined);
    connected = true;
    return;
  }
  assert.equal(connected, true);
  if (method === 'tools/list') {
    return send(id, {tools: [{name: 'get_window_state', inputSchema: {type: 'object'}}]});
  }
  assert.equal(method, 'tools/call');
  const text = params.arguments.text;
  switch (params.name) {
    case 'get_window_state': {
      assert.deepEqual(params.arguments, {pid: 42, window_id: 11});
      const response = Buffer.from(JSON.stringify({jsonrpc: '2.0', id, result: {
        structuredContent: {snapshot_id: 'cua:猫'},
        content: [{type: 'image', mimeType: 'image/png', data: 'cG5n'}, {type: 'text', text: 'summary'}],
      }}) + '\r\n');
      const split = response.indexOf(Buffer.from('猫')) + 1;
      process.stdout.write(response.subarray(0, split));
      return setTimeout(() => process.stdout.write(response.subarray(split)), 10);
    }
    case 'plain_text':
      assert.deepEqual(params.arguments, {});
      return send(id, {content: [{type: 'text', text: 'first'}, {type: 'text', text: 'second'}]});
    case 'click':
      return send(id, error({status: 'refused', refusal: {code: 'stale_element_token', message: 'Take a fresh snapshot.'}}, 'summary'));
    case 'launch_app':
      return send(id, error({error: 'APP_NOT_INSTALLED'}, 'The app could not be found.'));
    case 'type_text':
      return send(id, error({code: 'type_text_failed'}, 'Input ' + JSON.stringify(text) + ' failed; c2NyZWVuc2hvdA==',
        [{type: 'image', mimeType: 'image/png', data: 'c2NyZWVuc2hvdA=='}]));
    case 'rpc_error':
      return process.stdout.write(JSON.stringify({jsonrpc: '2.0', id,
        error: {code: -32602, message: 'Invalid params.', data: 'private-rpc-data'}}) + '\n');
    case 'malformed':
      process.stderr.write('private-stderr\n');
      return process.stdout.write('{\"data\":\"private-stdout\"\n');
    case 'crash':
      process.stderr.write('private-stderr\n');
      return process.exit(9);
    case 'counted_hold':
      require('node:fs').appendFileSync(process.env.CUA_TRANSPORT_TEST_CALLS, 'call\n');
      return;
    case 'hold':
      return;
    default:
      throw new Error('Unexpected fixture call');
  }
});
`;

function transport(t, options) {
  const connection = new CuaDriverTransport(process.execPath, ['-e', fixture], options);
  t.after(() => connection.close());
  return connection;
}

test('negotiates MCP once and preserves structured results and images', async (t) => {
  const connection = transport(t);
  assert.equal(connection.serverInfo, undefined);
  const [tools, result] = await Promise.all([
    connection.listTools(),
    connection.call('get_window_state', {pid: 42, window_id: 11}),
  ]);
  assert.deepEqual(tools, [{name: 'get_window_state', inputSchema: {type: 'object'}}]);
  assert.deepEqual(connection.serverInfo, {name: 'cua-driver', version: '0.56.4'});
  assert.deepEqual(result, {
    data: {snapshot_id: 'cua:猫'},
    images: [{type: 'image', mimeType: 'image/png', data: 'cG5n'}],
    connectionId: connection.connectionId,
  });
  assert.deepEqual(await connection.call('plain_text'), {data: 'first\nsecond', images: [], connectionId: connection.connectionId});
  await assert.rejects(connection.call('click', {}, Symbol()), {code: 'cua_driver_connection_changed'});
});

test('returns native refusal codes and messages without attaching MCP payloads', async (t) => {
  const connection = transport(t);
  await assert.rejects(connection.call('click'), {code: 'stale_element_token', message: 'Take a fresh snapshot.'});
  await assert.rejects(connection.call('launch_app'), {code: 'APP_NOT_INSTALLED', message: 'The app could not be found.'});
  await assert.rejects(connection.call('type_text', {text: 'private\ntext'}), (error) => {
    assert.equal(error.code, 'type_text_failed');
    assert.doesNotMatch(error.message, /private|c2NyZWVuc2hvdA/);
    return true;
  });
  await assert.rejects(connection.call('rpc_error'), {code: '-32602', message: 'Invalid params.'});
});

test('rejects an unsupported protocol without echoing the received version', async (t) => {
  const connection = transport(t, {env: {...process.env, CUA_TRANSPORT_TEST_PROTOCOL: 'private-protocol'}});
  await assert.rejects(connection.listTools(), (error) => {
    assert.equal(error.code, 'cua_driver_protocol_error');
    assert.doesNotMatch(error.message, /private-protocol/);
    return true;
  });
});

test('rejects a missing executable without exposing the command line', async (t) => {
  const connection = new CuaDriverTransport('/nonexistent/private-command', ['private-argument']);
  t.after(() => connection.close());
  await assert.rejects(connection.listTools(), (error) => {
    assert.equal(error.code, 'cua_driver_unavailable');
    assert.doesNotMatch(error.message, /private/);
    return true;
  });
});

for (const [tool, code] of [['malformed', 'cua_driver_protocol_error'], ['crash', 'cua_driver_unavailable']]) {
  test(`rejects all pending calls when the child returns ${tool}`, async (t) => {
    const connection = transport(t);
    const results = await Promise.allSettled([connection.call('hold'), connection.call(tool)]);
    for (const result of results) {
      assert.equal(result.status, 'rejected');
      assert.equal(result.reason.code, code);
      assert.doesNotMatch(result.reason.message, /private/);
    }
    assert.equal((await connection.listTools())[0].name, 'get_window_state');
  });
}

test('times out without retrying the request', async (t) => {
  const directory = mkdtempSync(join(tmpdir(), 'orca-cua-transport-test-'));
  t.after(() => rmSync(directory, {recursive: true, force: true}));
  const calls = join(directory, 'calls');
  const connection = transport(t, {timeoutMs: 400, env: {...process.env, CUA_TRANSPORT_TEST_CALLS: calls}});
  await connection.listTools();
  await assert.rejects(connection.call('counted_hold'), {code: 'cua_driver_timeout'});
  assert.equal((await connection.listTools())[0].name, 'get_window_state');
  assert.equal(readFileSync(calls, 'utf8'), 'call\n');
});

test('can bound initialization separately from tool calls', {timeout: 2000}, async (t) => {
  const connection = transport(t, {
    timeoutMs: 10_000,
    initializeTimeoutMs: 100,
    env: {...process.env, CUA_TRANSPORT_TEST_HOLD_INITIALIZE: '1'},
  });
  await assert.rejects(connection.listTools(), {code: 'cua_driver_timeout'});
});

test('close rejects pending calls and prevents future calls', async (t) => {
  const connection = transport(t);
  await connection.listTools();
  const pending = connection.call('hold');
  await Promise.resolve();
  connection.close();
  assert.equal(connection.serverInfo, undefined);
  await assert.rejects(pending, {code: 'cua_driver_closed'});
  await assert.rejects(connection.listTools(), {code: 'cua_driver_closed'});
});
