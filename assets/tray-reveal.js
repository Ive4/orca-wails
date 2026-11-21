// Route tray selections through the packaged Orca Web renderer's UI.
// This shim calls no Tauri API and runs only in the local view.
(() => {
  if (window.top !== window || location.protocol !== "http:" ||
      location.hostname !== "127.0.0.1" || location.pathname !== "/web-index.html") return;

  let terminalListener, editorListener, pending, timer;
  const requestId = "orca-tray";
  function subscribe(set) {
    return callback => {
      set(callback);
      if (pending) {
        clearTimeout(timer);
        timer = setTimeout(reveal, 0);
      }
      return () => set(undefined);
    };
  }
  function adapt(api) {
    const ui = api?.ui;
    if (typeof ui?.onRevealTerminalPane !== "function") return;
    ui.onRevealTerminalPane = subscribe(callback => { terminalListener = callback; });
    ui.onFocusEditorTab = subscribe(callback => { editorListener = callback; });
    const reply = ui.replyTerminalPaneReveal;
    ui.replyTerminalPaneReveal = result => {
      if (result.requestId !== requestId) return reply.call(ui, result);
      // The bridge can subscribe before it has the workspace catalog.
      if (pending && !result.revealed && pending.retries < 3) {
        pending.retries += 1;
        pending.announced = false;
        pending.retryAt = performance.now() + 400 * pending.retries;
      }
    };
  }
  if (window.api) {
    adapt(window.api);
  } else {
    Object.defineProperty(window, "api", {
      configurable: true,
      set(value) {
        adapt(value);
        Object.defineProperty(window, "api", {value, writable:true, configurable:true});
      }
    });
  }

  function reveal() {
    if (!pending) return;
    const {worktreeId, tabId, leafId} = pending.target;
    const chat = tabId.startsWith("structured-agent-session-");
    if (!(chat ? editorListener : terminalListener)) return;
    const now = performance.now();
    pending.deadline ??= now + 15000;
    if (now >= pending.deadline) {
      pending = undefined;
      return;
    }
    const sessionId = chat ? tabId.slice("structured-agent-session-".length) : "";
    if (chat && pending.target.focusComposer && pending.announced) {
      const scope = CSS.escape("agent-session:" + sessionId);
      const root = `[data-native-chat-composer-scope="${scope}"] [data-composer-field]`;
      const composer = document.querySelector(`${root} [contenteditable="true"], ${root} textarea:not(:disabled)`);
      if (composer?.getClientRects().length) {
        composer.focus({preventScroll: true});
        if (document.activeElement === composer) {
          pending = undefined;
          return;
        }
      }
    }
    if (chat) {
      // The paired renderer projects host chat IDs into its own tab IDs.
      // First reveal its workspace, then activate the actual mounted tab.
      const scope = `[data-tab-group-strip-id][data-worktree-id="${CSS.escape(worktreeId)}"]`;
      const escaped = CSS.escape(tabId);
      const candidates = document.querySelectorAll(
        `${scope} [data-tab-id="${escaped}"], ${scope} [data-tab-id^="${escaped}:history-"]`
      );
      if (candidates.length === 1 && !pending.selectedTab) {
        const selected = candidates[0].dataset.tabId;
        editorListener({worktreeId, tabId:selected, userInitiated:true});
        if (!pending.target.focusComposer) {
          pending = undefined;
          return;
        }
        pending.announced = true;
        pending.selectedTab = selected;
      }
      if (!pending.announced) {
        editorListener({worktreeId, tabId:"agent-session:" + sessionId, userInitiated:true});
        pending.announced = true;
      }
    } else {
      const mirror = "web-terminal-" + encodeURIComponent(tabId);
      const mounted = [mirror, tabId].some(id => {
        const root = `[data-terminal-tab-id="${CSS.escape(id)}"]`;
        return document.querySelector(leafId ? `${root} [data-leaf-id="${CSS.escape(leafId)}"]` : root);
      });
      if (now >= pending.retryAt && (mounted || !pending.announced)) {
        pending.announced = true;
        terminalListener({worktreeId, tabId, leafId, requestId, executionHostId:null});
      }
      if (mounted && pending.announced) {
        pending = undefined;
        return;
      }
    }
    timer = setTimeout(reveal, 200);
  }

  window.__orcaTrayReveal = target => {
    clearTimeout(timer);
    // Start the mount deadline after pairing and the renderer subscription.
    pending = {target, retries:0, retryAt:0};
    reveal();
  };
  function openSidebarControl(selector) {
    clearTimeout(timer);
    pending = undefined;
    const deadline = performance.now() + 3000;
    let expanded = false;
    function open() {
      const button = document.querySelector(selector);
      if (button && !button.disabled && button.getClientRects().length) {
        button.click();
        return;
      }
      if (!expanded && !document.querySelector("[data-sidebar-resize-handle]")) {
        const toggle = document.querySelector('button[class="sidebar-toggle"]');
        if (toggle) {
          expanded = true;
          toggle.click();
        }
      }
      if (performance.now() < deadline) timer = setTimeout(open, 200);
    }
    open();
  }
  window.__orcaTrayNewWorkspace = () => openSidebarControl('button[data-contextual-tour-target="workspace-create-control"]');
  // Orca 1.4.268's sidebar search button uses this class in every locale.
  window.__orcaTrayWorkspaceSearch = () => openSidebarControl('button[class~="bg-worktree-sidebar-foreground/5"][aria-label]');
  window.__orcaTrayOpenBots = () => {
    clearTimeout(timer);
    pending = undefined;
    const deadline = performance.now() + 15000;
    let toggledSidebar = false;
    let expandedBots = false;
    let switchedFromActivity = false;

    function showBots() {
      if (performance.now() >= deadline) return;
      if (!document.querySelector("[data-sidebar-resize-handle]")) {
        const toggle = document.querySelector('button[class="sidebar-toggle"]');
        if (toggle && !toggledSidebar) {
          toggledSidebar = true;
          toggle.click();
        }
      } else {
        const fold = document.querySelector('button[data-sidebar-section-fold="bots"]');
        if (fold?.getAttribute("aria-expanded") === "true") {
          fold.scrollIntoView({block: "nearest"});
          fold.focus({preventScroll: true});
          return;
        }
        if (fold && !expandedBots) {
          expandedBots = true;
          fold.click();
        } else if (!fold && !switchedFromActivity && !document.querySelector("[data-sidebar-section-header]")) {
          // The activity view replaces the workspace sidebar and its section headers.
          switchedFromActivity = true;
          window.dispatchEvent(new CustomEvent("orca-scroll-to-current-workspace-reveal-request", {
            detail: {target: {type: "active-workspace"}, highlight: false},
          }));
        }
      }
      timer = setTimeout(showBots, 200);
    }
    showBots();
  };
})();
