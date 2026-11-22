((identity) => {
  const read = key => {
    try {
      const value = JSON.parse(localStorage.getItem(key));
      return value && typeof value === "object" && !Array.isArray(value) ? value : null;
    } catch {
      return null;
    }
  };
  const legacy = read("orca.web.runtimeEnvironment.v1");
  const endpoints = legacy?.endpoints;
  if (!Array.isArray(endpoints) || endpoints.length === 0 || !endpoints.every(entry => {
    try {
      const endpoint = new URL(entry.endpoint);
      return endpoint.protocol === "ws:" && endpoint.host === location.host &&
        endpoint.pathname === "/" && !endpoint.username && !endpoint.password &&
        !endpoint.search && !endpoint.hash;
    } catch {
      return false;
    }
  })) return null;

  const installationMatches = identity.installationId &&
    identity.installationId === legacy.hostDescriptor?.installationId;
  const keyMatches = identity.publicKeyB64 && endpoints.every(entry => entry.publicKeyB64 === identity.publicKeyB64);
  if (!installationMatches && !keyMatches) return null;
  const aliases = [legacy.id, ...(Array.isArray(legacy.compatibleEnvironmentIds)
    ? legacy.compatibleEnvironmentIds : [])].filter(value => typeof value === "string" && value.length > 0);
  const hostAliases = new Set(aliases.map(value => "runtime:" + encodeURIComponent(value)));
  const localHost = value => hostAliases.has(value) ? "local" : value;
  const result = {};
  for (const key of ["settings", "ui", "onboarding", "keybindings", "workspaceSession"]) {
    const storageKey = `orca.web.${key}.v1`;
    let value = read(storageKey);
    if (key === "settings" && value && aliases.includes(value.activeRuntimeEnvironmentId)) {
      value.activeRuntimeEnvironmentId = null;
    }
    if (key === "ui" && value) {
      value.workspaceHostScope = localHost(value.workspaceHostScope);
      for (const field of ["visibleWorkspaceHostIds", "workspaceHostOrder", "agentsVisibleHostIds"]) {
        if (Array.isArray(value[field])) value[field] = [...new Set(value[field].map(localHost))];
      }
      if (value.automationHostFilter?.kind === "host") {
        value.automationHostFilter.hostKey = localHost(value.automationHostFilter.hostKey);
      }
    }
    if (key === "workspaceSession") {
      for (const host of hostAliases) {
        const previousSession = read(`${storageKey}.${host}`);
        if (previousSession) {
          value = previousSession;
          break;
        }
      }
      if (!value?.activeWorktreeId) {
        const ui = read("orca.web.ui.v1");
        if (ui) value = { ...value, activeRepoId: ui.lastActiveRepoId, activeWorktreeId: ui.lastActiveWorktreeId };
      }
    }
    if (value) result[storageKey] = JSON.stringify(value);
  }
  return JSON.stringify(result);
})
