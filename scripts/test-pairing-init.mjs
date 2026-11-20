// Behavioral regression checks for the credential-preserving initialization script.
// All keys/tokens below are synthetic fixtures; no browser or Orca profile is read.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const source = readFileSync(new URL('../assets/pairing-init.js', import.meta.url), 'utf8');
const key = Buffer.alloc(32, 1).toString('base64');
const secondKey = Buffer.alloc(32, 2).toString('base64');
const host = { installationId: '11111111-1111-4111-8111-111111111111', machineBinding: 'A'.repeat(43) };
const offer = {
  v: 2, endpoint: 'ws://127.0.0.1:6768', scope: 'runtime',
  publicKeyB64: key, deviceToken: 'synthetic-startup-token',
  pairedDeviceId: 'synthetic-offer-device', hostDescriptor: host,
};
function environment(overrides = {}) {
  return {
    id: 'web-fixture', name: 'Orca Server', createdAt: 100, pairingRevision: 101,
    pairedDeviceId: 'synthetic-device', hostDescriptor: host,
    preferredEndpointId: 'ws-web-fixture',
    endpoints: [{ id: 'ws-web-fixture', kind: 'websocket', endpoint: offer.endpoint,
      publicKeyB64: key, deviceToken: 'migrated:synthetic-device' }],
    ...overrides,
  };
}
function pairingUrl(value = offer) {
  const code = Buffer.from(JSON.stringify(value)).toString('base64url');
  return `http://127.0.0.1:6768/web-index.html#pairing=${encodeURIComponent(`orca://pair?code=${code}`)}`;
}
function run({ value = offer, saved = environment(), url = pairingUrl(value), storageError = false, frame = false } = {}) {
  const raw = typeof saved === 'string' ? saved : saved === null ? null : JSON.stringify(saved);
  const updates = [];
  const location = new URL(url);
  const storage = {
    getItem(name) {
      assert.equal(name, 'orca.web.runtimeEnvironment.v1');
      if (storageError) throw new Error('storage unavailable');
      return raw;
    },
    setItem() { assert.fail('The guard must not write credentials.'); },
    removeItem() { assert.fail('The guard must not remove credentials.'); },
  };
  const window = { location, localStorage: storage, history: {
    state: { fixture: true },
    replaceState(state, title, next) {
      assert.deepEqual(state, { fixture: true }); assert.equal(title, '');
      updates.push(next);
    },
  } };
  window.self = window;
  window.top = frame ? {} : window;
  const context = vm.createContext({ window, URL, URLSearchParams, TextDecoder, Uint8Array,
    atob, console: { log() { assert.fail('No credential logging.'); }, warn() { assert.fail('No credential logging.'); }, error() { assert.fail('No credential logging.'); } } });
  vm.runInContext(source, context, { timeout: 1000 });
  return updates;
}
const clean = ['http://127.0.0.1:6768/web-index.html'];

test('first window closed before upstream saves pairing keeps its offer on reopen', () => {
  assert.deepEqual(run({ saved: null }), []);
  assert.deepEqual(run({ saved: null }), []);
  assert.deepEqual(run(), clean);
});
test('preserves a migrated device credential instead of importing the old bearer', () => {
  assert.deepEqual(run(), clean);
});
test('preserves a still-usable saved legacy credential', () => {
  const saved = environment(); saved.endpoints[0].deviceToken = 'synthetic-legacy-token';
  assert.deepEqual(run({ saved }), clean);
});
test('cleared profile retains the offer even if earlier windows succeeded', () => {
  assert.deepEqual(run(), clean);
  assert.deepEqual(run({ saved: null }), []);
});
test('same port with a different host key never suppresses pairing', () => {
  assert.deepEqual(run({ value: { ...offer, publicKeyB64: secondKey } }), []);
});
test('same key with a different installation is a different host', () => {
  assert.deepEqual(run({ value: { ...offer, hostDescriptor: { ...host, installationId: '22222222-2222-4222-8222-222222222222' } } }), []);
});
test('machine binding movement alone does not replace installation identity', () => {
  assert.deepEqual(run({ value: { ...offer, hostDescriptor: { ...host, machineBinding: 'B'.repeat(43) } } }), clean);
});
test('legacy descriptor absence follows the upstream host-key matching rule', () => {
  const oldOffer = { ...offer }; delete oldOffer.hostDescriptor;
  assert.deepEqual(run({ value: oldOffer }), clean);
  const saved = environment(); delete saved.hostDescriptor;
  assert.deepEqual(run({ saved }), clean);
});
test('malformed descriptor is left for upstream', () => {
  assert.deepEqual(run({ value: { ...offer, hostDescriptor: { installationId: 'bad' } } }), []);
  assert.deepEqual(run({ saved: environment({ hostDescriptor: null }) }), []);
});
test('missing or malformed saved credentials cannot masquerade as completed pairing', () => {
  for (const saved of [null, '{bad json', 'null', '[]', environment({ id: '' }), environment({ name: '' }), environment({ endpoints: [] })]) {
    assert.deepEqual(run({ saved }), []);
  }
  const saved = environment(); delete saved.endpoints[0].deviceToken;
  assert.deepEqual(run({ saved }), []);
  saved.endpoints[0].deviceToken = 'migrated:';
  assert.deepEqual(run({ saved }), []);
});
test('a session-only environment is not treated as persisted identity', () => {
  assert.deepEqual(run({ saved: environment({ sessionOnly: true }) }), []);
});
test('unavailable localStorage never prevents the native startup offer', () => {
  assert.deepEqual(run({ storageError: true }), []);
});
test('unsupported offer versions, scope and key encoding are untouched', () => {
  for (const value of [{ ...offer, v: 3 }, { ...offer, scope: 'mobile' }, { ...offer, publicKeyB64: 'invalid' }, { ...offer, deviceToken: '' }]) {
    assert.deepEqual(run({ value }), []);
  }
});
test('only the native loopback HTTP page is eligible', () => {
  for (const url of [pairingUrl().replace('127.0.0.1', 'example.com'), pairingUrl().replace('http:', 'https:'),
    pairingUrl().replace('/web-index.html', '/other.html'), pairingUrl().replace('127.0.0.1', 'localhost')]) {
    assert.deepEqual(run({ url }), []);
  }
});
test('an offer for another address or port does not affect this profile', () => {
  for (const endpoint of ['ws://127.0.0.1:6769', 'ws://example.com:6768', 'wss://127.0.0.1:6768', 'ws://user@127.0.0.1:6768', 'ws://127.0.0.1:6768/other']) {
    assert.deepEqual(run({ value: { ...offer, endpoint } }), []);
  }
});
test('matching one endpoint does not suppress a different selected endpoint', () => {
  const saved = environment();
  saved.endpoints.push({ ...saved.endpoints[0], id: 'selected-elsewhere', endpoint: 'ws://127.0.0.1:6769' });
  saved.preferredEndpointId = 'selected-elsewhere';
  assert.deepEqual(run({ saved }), []);
});
test('iframes cannot act on the parent pairing state', () => {
  assert.deepEqual(run({ frame: true }), []);
});
test('query overrides, duplicate fragments, bad encoding and oversized offers are untouched', () => {
  const urls = [
    pairingUrl().replace('/web-index.html#', '/web-index.html?pairing=other#'),
    `${pairingUrl()}&pairing=bad`,
    'http://127.0.0.1:6768/web-index.html#pairing=not-a-url',
    'http://127.0.0.1:6768/web-index.html#pairing=orca%3A%2F%2Fpair%3Fcode%3D%25%25',
    `http://127.0.0.1:6768/web-index.html#pairing=${'x'.repeat(65537)}`,
  ];
  for (const url of urls) assert.deepEqual(run({ url }), []);
});
