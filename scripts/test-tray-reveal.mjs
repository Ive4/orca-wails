import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const script = readFileSync(new URL("../assets/tray-reveal.js", import.meta.url), "utf8");

function renderer(url = "http://127.0.0.1:9090/web-index.html", subscribeImmediately = true) {
  const clock = { now: 0, next: null };
  const observed = { terminal: [], chat: [], terminalMounted: false, workspaceReady: true, chats: [], controls: new Map() };
  const document = {
    activeElement: null,
    querySelector: selector => observed.controls.get(selector) ?? (observed.terminalMounted ? {} : null),
    querySelectorAll: () => observed.chats.map(tabId => ({ dataset: { tabId } })),
  };
  const window = {};
  window.top = window;
  vm.runInNewContext(script, {
    window,
    location: new URL(url),
    CustomEvent: class {
      constructor(type, { detail }) { Object.assign(this, { type, detail }); }
    },
    performance: { now: () => clock.now },
    CSS: { escape: value => value },
    document,
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
    document,
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
  assert.equal(window.__orcaTrayOpenBots, undefined);
  assert.equal(window.__orcaTrayNewWorkspace, undefined);
  assert.equal(window.__orcaTrayWorkspaceSearch, undefined);
});

test("workspace menu actions wait for the sidebar and activate the renderer controls", () => {
  const { window, observed, tick } = renderer();
  let expanded = 0;
  let created = 0;
  let searches = 0;
  const visibleButton = click => ({ click, getClientRects: () => [1] });
  observed.controls.set('button[class="sidebar-toggle"]', { click: () => expanded++ });
  window.__orcaTrayNewWorkspace();
  tick();
  assert.equal(expanded, 1);
  observed.controls.set('[data-sidebar-resize-handle]', {});
  observed.controls.set('button[data-contextual-tour-target="workspace-create-control"]', visibleButton(() => created++));
  tick();
  tick();
  assert.equal(created, 1);
  observed.controls.set('button[class~="bg-worktree-sidebar-foreground/5"][aria-label]', visibleButton(() => searches++));
  window.__orcaTrayWorkspaceSearch();
  tick();
  assert.equal(searches, 1);
});

test("focuses only the new Bot conversation input after its tab mounts", () => {
  const { window, observed, tick, document } = renderer();
  const tabId = "structured-agent-session-bot-chat";
  const root = '[data-native-chat-composer-scope="agent-session:bot-chat"] [data-composer-field]';
  const selector = `${root} [contenteditable="true"], ${root} textarea:not(:disabled)`;
  let focused = 0;
  window.__orcaTrayReveal({ worktreeId: "bots", tabId, focusComposer: true });
  observed.chats.push(tabId);
  tick();
  assert.equal(observed.chat.at(-1).tabId, tabId);
  assert.equal(focused, 0);
  const input = {
    getClientRects: () => [1],
    focus() { focused++; document.activeElement = input; },
  };
  observed.controls.set(selector, input);
  tick();
  tick();
  assert.equal(focused, 1);
  assert.equal(document.activeElement, input);
});

test("opens the Bots section after the renderer mounts and leaves it expanded on repeat", () => {
  const { window, observed, tick } = renderer();
  const { controls } = observed;
  let sidebarClicks = 0;
  let foldClicks = 0;
  let focused = 0;
  const fold = {
    getAttribute: () => foldClicks ? "true" : "false",
    click: () => { foldClicks++; },
    scrollIntoView: () => {},
    focus: () => { focused++; },
  };

  window.__orcaTrayOpenBots();
  tick();
  controls.set('button[class="sidebar-toggle"]', { click: () => { sidebarClicks++; } });
  tick();
  tick();
  assert.equal(sidebarClicks, 1);
  controls.set("[data-sidebar-resize-handle]", {});
  controls.set('button[data-sidebar-section-fold="bots"]', fold);
  tick();
  tick();
  assert.equal(foldClicks, 1);
  assert.equal(focused, 1);

  window.__orcaTrayOpenBots();
  assert.equal(sidebarClicks, 1);
  assert.equal(foldClicks, 1);
  assert.equal(focused, 2);
});

test("returns from the activity sidebar to the Bots section", () => {
  const { window, observed, tick } = renderer();
  const { controls } = observed;
  let focused = false;
  controls.set("[data-sidebar-resize-handle]", {});
  window.dispatchEvent = event => {
    assert.equal(event.type, "orca-scroll-to-current-workspace-reveal-request");
    controls.set("[data-sidebar-section-header]", {});
    controls.set('button[data-sidebar-section-fold="bots"]', {
      getAttribute: () => "true",
      scrollIntoView: () => {},
      focus: () => { focused = true; },
    });
  };
  window.__orcaTrayOpenBots();
  tick();
  assert.equal(focused, true);
});
