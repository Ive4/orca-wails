import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const script = readFileSync(new URL("../assets/tray-reveal.js", import.meta.url), "utf8");

function renderer(url = "http://127.0.0.1:9090/web-index.html", subscribeImmediately = true) {
  const clock = { now: 0, next: null };
  const observed = { terminal: [], chat: [], terminalMounted: false, workspaceReady: true, chats: [] };
  const window = {};
  window.top = window;
  vm.runInNewContext(script, {
    window,
    location: new URL(url),
    performance: { now: () => clock.now },
    CSS: { escape: value => value },
    document: {
      querySelector: () => observed.terminalMounted,
      querySelectorAll: () => observed.chats.map(tabId => ({ dataset: { tabId } })),
    },
    setTimeout: callback => { clock.next = callback; },
    clearTimeout: () => { clock.next = null; },
  });
  window.api = { ui: { onRevealTerminalPane: () => {}, onFocusEditorTab: () => {}, replyTerminalPaneReveal: () => {} } };
  const subscribe = () => {
    window.api.ui.onRevealTerminalPane(target => {
      observed.terminal.push(target);
      window.api.ui.replyTerminalPaneReveal({ requestId: target.requestId, revealed: observed.workspaceReady });
    });
    window.api.ui.onFocusEditorTab(target => observed.chat.push(target));
  };
  if (subscribeImmediately) subscribe();
  return {
    window,
    observed,
    subscribe,
    tick: () => {
      clock.now += 200;
      const next = clock.next;
      clock.next = null;
      next?.();
    },
  };
}

test("keeps a tray selection until the cold renderer subscribes", () => {
  const { window, observed, subscribe, tick } = renderer(undefined, false);
  window.__orcaTrayReveal({ worktreeId: "workspace", tabId: "tab", leafId: "leaf" });
  for (let i = 0; i < 200; i++) tick();
  subscribe();
  tick();
  assert.equal(observed.terminal.length, 1);
  assert.equal(observed.terminal[0].leafId, "leaf");
});

test("retries when the renderer has subscribed but the workspace catalog is still loading", () => {
  const { window, observed, tick } = renderer();
  observed.workspaceReady = false;
  window.__orcaTrayReveal({ worktreeId: "workspace", tabId: "tab", leafId: "leaf" });
  observed.workspaceReady = true;
  tick();
  tick();
  assert.equal(observed.terminal.length, 2);
  assert.equal(observed.terminal[1].worktreeId, "workspace");
});

test("stops retrying a workspace that stays unavailable", () => {
  const { window, observed, tick } = renderer();
  observed.workspaceReady = false;
  window.__orcaTrayReveal({ worktreeId: "workspace", tabId: "tab", leafId: "leaf" });
  for (let i = 0; i < 100; i++) tick();
  assert.equal(observed.terminal.length, 4);
});

test("selects a terminal after its workspace starts mounting without repeating focus", () => {
  const { window, observed, tick } = renderer();
  window.__orcaTrayReveal({ worktreeId: "workspace", tabId: "tab", leafId: "leaf" });
  for (let i = 0; i < 5; i++) tick();
  assert.equal(observed.terminal.length, 1);
  observed.terminalMounted = true;
  tick();
  tick();
  assert.equal(observed.terminal.length, 2);
  assert.equal(observed.terminal[1].leafId, "leaf");
  assert.equal(observed.terminal[1].worktreeId, "workspace");
});

test("reveals a chat through its mounted renderer tab, including history suffixes", () => {
  const { window, observed, tick } = renderer();
  window.__orcaTrayReveal({ worktreeId: "workspace", tabId: "structured-agent-session-session" });
  tick();
  assert.equal(observed.chat.length, 1);
  assert.equal(observed.chat[0].tabId, "agent-session:session");
  observed.chats = ["structured-agent-session-session:history-1"];
  tick();
  tick();
  assert.equal(observed.chat.length, 2);
  assert.equal(observed.chat[1].tabId, "structured-agent-session-session:history-1");
});

test("paired server pages cannot install the local navigation shim", () => {
  const { window } = renderer("http://localhost:9090/web-index.html");
  assert.equal(window.__orcaTrayReveal, undefined);
});
