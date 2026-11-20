import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

const script = readFileSync(new URL('../assets/window-init.js', import.meta.url), 'utf8');

function load({ systemDark = false, settings, delayedRoot = false } = {}) {
  const classes = new Set();
  const root = {
    classList: { toggle: (name, on) => on ? classes.add(name) : classes.delete(name) },
    append: (style) => assert.match(style.textContent, /#titlebar-tabs\.invisible/),
  };
  const observers = new Map();
  const document = {
    documentElement: delayedRoot ? null : root,
    createElement: () => ({}),
  };
  const window = { location: { pathname: '/web-index.html' } };
  window.top = window.self = window;
  runInNewContext(script, {
    document, window, matchMedia: () => ({ matches: systemDark }),
    localStorage: { getItem: (name) => name === 'orca.web.settings.v1' ? settings : null },
    MutationObserver: class {
      constructor(callback) { this.callback = callback; }
      observe(target) { observers.set(target, this.callback); }
      disconnect() { observers.delete(document); }
    },
  });
  if (delayedRoot) {
    document.documentElement = root;
    observers.get(document)?.();
  }
  return [...classes];
}

assert.deepEqual(load({ systemDark: true, delayedRoot: true }), ['dark']);
assert.deepEqual(load(), ['light']);
assert.deepEqual(load({ settings: '{"theme":"dark"}' }), ['dark']);
assert.deepEqual(load({ systemDark: true, settings: '{"theme":"light"}' }), ['light']);
assert.deepEqual(load({ systemDark: true, settings: '{malformed' }), ['dark']);
console.log('Window startup theme checks passed');
