import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const script = readFileSync(new URL('../assets/bot-avatars.js', import.meta.url), 'utf8');
const pack = [{
  en: 'Jujutsu Kaisen',
  zh: '咒术回战',
  avatars: [
    { file: '01-gojo-satoru.webp', slot: 'A1', en: 'Satoru Gojo', zh: '五条悟', src: 'data:image/webp;base64,R09KTw==' },
    { file: '09-fushiguro-toji.webp', en: 'Toji Fushiguro', zh: '伏黑甚尔', src: 'data:image/webp;base64,VE9KSQ==' },
  ],
}];

function element(tag, attributes = {}) {
  return {
    nodeType: 1, tagName: tag.toUpperCase(), children: [], style: {},
    getAttribute(name) { return attributes[name] ?? null; },
    setAttribute(name, value) { attributes[name] = value; },
    append(...nodes) { this.children.push(...nodes); },
    after(node) { this.parentElement.append(node); },
    querySelector(selector) { return this.querySelectorAll(selector)[0] ?? null; },
    querySelectorAll(selector) {
      return this.children.flatMap(child => [
        ...(child.matches(selector) ? [child] : []),
        ...child.querySelectorAll(selector),
      ]);
    },
    matches(selector) {
      return (tag === 'img' && selector.includes('img[src]') && !!attributes.src) ||
        (tag === 'input' && selector.includes('input[type="file"]')) ||
        (tag === 'select' && selector === 'select[data-orca-avatar-library]' && !!this.dataset.orcaAvatarLibrary) ||
        (attributes['aria-roledescription'] === 'carousel' && selector === ':scope > [aria-roledescription="carousel"]');
    },
    addEventListener(name, handler) { this[name] = handler; },
    dataset: {},
  };
}

function install(nodes = [], href = 'http://127.0.0.1:16768/web-index.html') {
  const root = element('html');
  root.lang = 'zh-CN';
  root.append(...nodes);
  const document = {
    documentElement: root,
    querySelectorAll: selector => root.querySelectorAll(selector),
    createElement: element,
  };
  const window = { location: new URL(href) };
  window.top = window.self = window;
  let mutations = () => {};
  vm.runInNewContext(`${script}\n(pack)`, {
    pack, document, window, URL, navigator: { language: 'zh-CN' }, atob, File, Event,
    MutationObserver: class {
      constructor(callback) { mutations = callback; }
      observe() {}
    },
    DataTransfer: class {
      files = [];
      items = { add: file => this.files.push(file) };
    },
  });
  return { mutations };
}

test('replaces bundled and recolored mascot images, including images rendered later', () => {
  const base = element('img', { src: '/assets/A1-C49URgBU.svg' });
  const bitmap = element('img', { src: '/assets/A1-DvApH1kv.webp' });
  const uploaded = element('img', { src: 'data:image/png;base64,AA==' });
  const foreign = element('img', { src: 'https://example.com/assets/A1-C49URgBU.svg' });
  const { mutations } = install([base, bitmap, uploaded, foreign]);
  assert.equal(base.getAttribute('src'), 'data:image/webp;base64,R09KTw==');
  assert.equal(bitmap.getAttribute('src'), 'data:image/webp;base64,R09KTw==');
  assert.equal(uploaded.getAttribute('src'), 'data:image/png;base64,AA==');
  assert.equal(foreign.getAttribute('src'), 'https://example.com/assets/A1-C49URgBU.svg');

  const svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 128 128"><defs><filter id="A1b"';
  base.setAttribute('src', 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg));
  mutations([{ type: 'attributes', target: base }]);
  assert.equal(base.getAttribute('src'), 'data:image/webp;base64,R09KTw==');
  const holder = element('div');
  const later = element('img', { src: '/assets/A1-cut-CvGk81fy.webp' });
  holder.append(later);
  mutations([{ type: 'childList', addedNodes: [holder] }]);
  assert.equal(later.getAttribute('src'), 'data:image/webp;base64,R09KTw==');
});

test('adds one grouped library per avatar picker and selects through the existing file input', async () => {
  const picker = element('div');
  const input = element('input', { accept: 'image/png,image/webp' });
  input.parentElement = picker;
  const carousel = element('div', { 'aria-roledescription': 'carousel' });
  picker.append(carousel, input);
  const unrelated = element('div');
  const unrelatedInput = element('input', { accept: 'image/png,image/webp' });
  unrelatedInput.parentElement = unrelated;
  unrelated.append(unrelatedInput);
  const { mutations } = install([picker, unrelated]);
  mutations([{ type: 'childList', addedNodes: [picker] }]);

  const select = picker.querySelector('select[data-orca-avatar-library]');
  assert.ok(select);
  assert.equal(picker.querySelectorAll('select[data-orca-avatar-library]').length, 1);
  assert.equal(unrelated.querySelector('select[data-orca-avatar-library]'), null);
  assert.equal(select.children[1].label, '咒术回战');
  assert.equal(select.children[1].children[1].textContent, '伏黑甚尔');
  let selected;
  input.dispatchEvent = event => {
    assert.equal(event.type, 'change');
    assert.equal(event.bubbles, true);
    selected = input.files[0];
  };
  select.value = select.children[1].children[1].value;
  select.change();
  assert.equal(selected.name, '09-fushiguro-toji.webp');
  assert.equal(selected.type, 'image/webp');
  assert.equal(await selected.text(), 'TOJI');
});

test('leaves other pages on the same host alone', () => {
  const icon = element('img', { src: '/assets/A1-C49URgBU.svg' });
  install([icon], 'http://127.0.0.1:16768/other.html');
  assert.equal(icon.getAttribute('src'), '/assets/A1-C49URgBU.svg');
});
