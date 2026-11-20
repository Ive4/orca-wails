// Route a native tray selection through the packaged Orca Web renderer's own
// reveal callbacks. This shim calls no Tauri API and runs only in the local view.
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
    if (chat) {
      // The paired renderer projects host chat IDs into its own tab IDs.
      // First reveal its workspace, then activate the actual mounted tab.
      const scope = `[data-tab-group-strip-id][data-worktree-id="${CSS.escape(worktreeId)}"]`;
      const escaped = CSS.escape(tabId);
      const candidates = document.querySelectorAll(
        `${scope} [data-tab-id="${escaped}"], ${scope} [data-tab-id^="${escaped}:history-"]`
      );
      if (candidates.length === 1) {
        editorListener({worktreeId, tabId:candidates[0].dataset.tabId, userInitiated:true});
        pending = undefined;
        return;
      }
      if (!pending.announced) {
        const sessionId = tabId.slice("structured-agent-session-".length);
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
})();
