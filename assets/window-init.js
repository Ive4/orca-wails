(() => {
  'use strict';
  if (window.top !== window.self || window.location.pathname !== '/web-index.html') return;

  function prepare(root) {
    let theme;
    try {
      // Read the upstream web preference before its asynchronous settings load.
      theme = JSON.parse(localStorage.getItem('orca.web.settings.v1'))?.theme;
    } catch {
      // Private or unavailable storage still lets us paint the system appearance.
    }
    const dark = theme === 'dark' ||
      (theme !== 'light' && matchMedia('(prefers-color-scheme: dark)').matches);
    root.classList.toggle('dark', dark);
    root.classList.toggle('light', !dark);

    const style = document.createElement('style');
    style.textContent = `
      :root.light { color-scheme: light; background-color: var(--background, #fff); }
      :root.dark { color-scheme: dark; background-color: var(--background, #0a0a0a); }
      /* Settings and Space can mount a Web title row with no visible controls. */
      .titlebar:has(> #titlebar-tabs.invisible):not(:has(button, .titlebar-app-name)) {
        display: none;
      }
      /* Orca renders this indicator separately from the scrollable tab strip. */
      [data-testid="tab-strip-scroll-indicator"] {
        display: none;
      }
    `;
    root.append(style);
  }

  if (document.documentElement) {
    prepare(document.documentElement);
  } else {
    const observer = new MutationObserver(() => {
      if (!document.documentElement) return;
      observer.disconnect();
      prepare(document.documentElement);
    });
    observer.observe(document, { childList: true });
  }
})();
