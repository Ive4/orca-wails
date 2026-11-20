import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const page = 'http://127.0.0.1:6768/web-index.html';
const catalogUrl = '/assets/zh-UAxbJ6Df.json';

function install({ location = page, frame = false, status = 200 } = {}) {
  const official = {
    bots: {
      section: { title: '官方机器人', collapse: '官方收起', showMore: '官方显示更多' },
      activity: { title: '官方活动' },
      'new.title': '官方新建机器人',
    },
    'bots.section.expand': '官方展开',
    'bots.chats': { title: '官方聊天' },
    settings: { appearance: { title: '官方外观' } },
  };
  const fetch = async () => new Response(JSON.stringify(official), { status });
  const window = { location: new URL(location), fetch };
  window.self = window;
  window.top = frame ? {} : window;
  const script = readFileSync(new URL('../assets/bots-i18n.js', import.meta.url), 'utf8');
  const catalog = readFileSync(new URL('../assets/bots-i18n.zh.json', import.meta.url), 'utf8');
  vm.runInNewContext(`${script}(\n${catalog}\n);`, { window, URL });
  return { window, official, fetch };
}

test('fills individual missing Bots keys while preserving nested and flat official translations', async () => {
  const { window } = install();
  const request = new Request(new URL(catalogUrl, page));
  const response = await window.fetch(request, { cache: 'no-cache' });
  assert.ok(response instanceof Response);
  const catalog = await response.json();
  assert.equal(catalog.bots.section.title, '官方机器人');
  assert.equal(catalog.bots.section.collapse, '官方收起');
  assert.equal(catalog['bots.section.expand'], '官方展开');
  assert.equal(catalog.bots.section.expand, undefined);
  assert.equal(catalog.bots.activity.title, '官方活动');
  assert.equal(catalog.bots['new.title'], '官方新建机器人');
  assert.equal(catalog['bots.new.title'], undefined);
  assert.equal(catalog['bots.section.showMore_other'], undefined);
  assert.equal(catalog['bots.chats.title'], undefined);
  assert.equal(catalog['bots.new.createBot'], '创建机器人');
  assert.equal(catalog.settings.appearance.title, '官方外观');
});

test('only fills the default Chinese catalog on successful same-origin requests', async () => {
  const { window, official } = install();
  for (const url of [
    '/assets/zh-Hant-yO9GbLbt.json',
    '/assets/ja-D_8CR8Zo.json',
    '/assets/zh-UAxbJ6Df.js',
    '/api/zh-UAxbJ6Df.json',
    'http://127.0.0.1:6769' + catalogUrl,
  ]) {
    assert.deepEqual(await (await window.fetch(url)).json(), official);
  }
  const failed = install({ status: 404 });
  assert.deepEqual(await (await failed.window.fetch(catalogUrl)).json(), official);
});

test('does not install in iframes or outside the local Orca web client', () => {
  for (const options of [
    { frame: true },
    { location: 'https://example.com/web-index.html' },
    { location: 'http://127.0.0.1:6768/index.html' },
  ]) {
    const { window, fetch } = install(options);
    assert.equal(window.fetch, fetch);
  }
});

test('fills the catalog on the separate paired-server origin', async () => {
  const { window } = install({ location: 'http://localhost:16768/web-index.html' });
  const catalog = await (await window.fetch(catalogUrl)).json();
  assert.equal(catalog['bots.new.createBot'], '创建机器人');
});
