// Use the native window's startup offer to recognize its Core even after a reload.
((nativeFragment) => {
  "use strict";
  try {
    if (window.top !== window.self) return;
    const page = new URL(window.location.href);
    if (
      page.protocol !== "http:" ||
      page.hostname !== "127.0.0.1" ||
      !page.port ||
      page.username ||
      page.password ||
      page.pathname !== "/web-index.html" ||
      page.search ||
      nativeFragment.length >= 65536
    ) return;

    const fragment = new URLSearchParams(nativeFragment);
    if ([...fragment].length !== 1 || fragment.getAll("pairing").length !== 1) return;
    const pairing = new URL(fragment.get("pairing"));
    if (
      pairing.protocol !== "orca:" ||
      pairing.hostname !== "pair" ||
      pairing.username ||
      pairing.password ||
      (pairing.pathname !== "" && pairing.pathname !== "/") ||
      pairing.hash ||
      [...pairing.searchParams].length !== 1 ||
      pairing.searchParams.getAll("code").length !== 1
    ) return;
    const code = pairing.searchParams.get("code");
    if (!code || code.length > 65536 || !/^[A-Za-z0-9_-]+={0,2}$/.test(code)) return;
    const base64 = code.replace(/-/g, "+").replace(/_/g, "/");
    const bytes = Uint8Array.from(atob(base64), (character) => character.charCodeAt(0));
    const offer = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
    const text = (value) => typeof value === "string" && value.length > 0 && value.length <= 65536;
    const publicKey = (value) =>
      typeof value === "string" && /^[A-Za-z0-9+/]{43}=$/.test(value) && atob(value).length === 32;
    if (
      !offer || typeof offer !== "object" || Array.isArray(offer) ||
      offer.v !== 2 || offer.scope !== "runtime" ||
      !text(offer.endpoint) || !text(offer.deviceToken) || !publicKey(offer.publicKeyB64)
    ) return;

    const localEndpoint = (value) => {
      const expected = "ws://" + page.host;
      return value === expected || value === expected + "/" ? expected : null;
    };
    const offeredEndpoint = localEndpoint(offer.endpoint);
    if (!offeredEndpoint) return;

    // Mirrors runtime-host-pairing-identity.ts: host key anchors identity;
    // installationId distinguishes a copied/reinstalled profile retaining that key.
    // machineBinding is a move hint, never host identity by itself.
    const invalid = Symbol("invalid descriptor");
    const descriptor = (value) => {
      if (value === undefined) return null;
      if (
        !value || typeof value !== "object" || Array.isArray(value) ||
        typeof value.installationId !== "string" ||
        !/^[\da-f]{8}-[\da-f]{4}-4[\da-f]{3}-[89ab][\da-f]{3}-[\da-f]{12}$/.test(value.installationId) ||
        (value.machineBinding !== undefined &&
          (typeof value.machineBinding !== "string" || !/^[A-Za-z0-9_-]{43}$/.test(value.machineBinding)))
      ) return invalid;
      return value;
    };
    const offeredHost = descriptor(offer.hostDescriptor);
    if (offeredHost === invalid) return;
    const offeredKey = offer.publicKeyB64;
    const selectedEndpoint = (environment) =>
      environment.endpoints.find((endpoint) => endpoint?.id === environment.preferredEndpointId)
        ?? environment.endpoints[0];
    const ownsRuntime = (environment) => {
      if (!environment || !text(environment.id) || !Array.isArray(environment.endpoints)) return false;
      const endpoint = selectedEndpoint(environment);
      if (localEndpoint(endpoint?.endpoint) !== offeredEndpoint || endpoint.publicKeyB64 !== offeredKey) return false;
      const previousHost = descriptor(environment.hostDescriptor);
      return previousHost !== invalid &&
        (!previousHost || !offeredHost || previousHost.installationId === offeredHost.installationId);
    };
    Object.defineProperty(window, "__orcaWailsOwnsRuntime", { value: ownsRuntime });

    // Only suppress this startup offer, and only when the saved credential can resume it.
    if (page.hash.slice(1) !== nativeFragment) return;
    const raw = window.localStorage.getItem("orca.web.runtimeEnvironment.v1");
    if (!raw || raw.length > 262144) return;
    const stored = JSON.parse(raw);
    if (!ownsRuntime(stored) || !text(stored.name) || stored.sessionOnly === true) return;
    const token = selectedEndpoint(stored).deviceToken;
    if (!text(token) || token === "migrated:") return;

    // A native window's existence is not evidence that pairing was saved.
    // Only an existing valid environment for this exact host suppresses reimport.
    // Upstream then takes its normal "use-stored-environment" path and can resume
    // a persisted Ed25519 key, rather than replacing it with the old bearer offer.
    page.hash = "";
    window.history.replaceState(window.history.state, "", page.href);
  } catch {
    // Malformed/unsupported offers or inaccessible storage are left to upstream.
    // Do not log an exception: location and pairing inputs contain credentials.
  }
})
