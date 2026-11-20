// Fill gaps in Orca's lazy-loaded Simplified Chinese catalog before i18next reads it.
((fallback) => {
  "use strict";
  if (window.top !== window.self) return;
  const page = new URL(window.location.href);
  if (
    page.protocol !== "http:" || !["127.0.0.1", "localhost"].includes(page.hostname) ||
    !page.port || page.pathname !== "/web-index.html"
  ) return;

  // Orca uses nested resources, and i18next also accepts dotted keys.
  function hasOfficialKey(resource, key) {
    if (!resource || typeof resource !== "object") return false;
    if (Object.hasOwn(resource, key)) return true;
    for (let dot = key.indexOf("."); dot !== -1; dot = key.indexOf(".", dot + 1)) {
      const prefix = key.slice(0, dot);
      if (Object.hasOwn(resource, prefix) && hasOfficialKey(resource[prefix], key.slice(dot + 1))) {
        return true;
      }
    }
    return false;
  }

  const fetch = window.fetch;
  window.fetch = async function (input, ...options) {
    const response = await fetch.call(window, input, ...options);
    if (!response.ok || response.redirected) return response;
    const url = new URL(response.url || input.url || input, page);
    if (url.origin !== page.origin || !/^\/assets\/zh(?:-[\w-]{8})?\.json$/.test(url.pathname)) {
      return response;
    }
    const json = response.json.bind(response);
    response.json = async () => {
      const catalog = await json();
      if (!catalog || typeof catalog !== "object" || Array.isArray(catalog)) return catalog;
      const missing = Object.entries(fallback).filter(([key]) => {
        if (hasOfficialKey(catalog, key)) return false;
        // Chinese has only the "other" plural category. A new official base
        // translation must also take priority over our plural fallback.
        return !key.endsWith("_other") || !hasOfficialKey(catalog, key.slice(0, -6));
      });
      return Object.assign(catalog, Object.fromEntries(missing));
    };
    return response;
  };
})
