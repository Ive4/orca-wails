((options) => {
  "use strict";
  const documentId = Array.from(crypto.getRandomValues(new Uint8Array(12)), byte => byte.toString(16).padStart(2, "0")).join("");
  let nextStream = 0;
  let settingsHandler, settingsRequested = false;
  const request = async (payload, signal) => {
    const response = await fetch("/desktop/rpc", {
      method: "POST",
      headers: { "X-Orca-Desktop": options.token, "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      redirect: "error",
      signal,
    });
    if (response.status === 204 || response.status === 410) return response.status;
    if (!response.ok) throw new Error(await response.text());
    return response.json();
  };
  Object.defineProperty(window, "__orcaWailsDesktop", {
    value: Object.freeze({
      protocol: 1,
      platform: options.platform,
      shellSettings: options.shellSettings === true,
      openSettings: () => {
        if (settingsHandler) settingsHandler();
        else settingsRequested = true;
      },
      registerSettings: (handler) => {
        settingsHandler = handler;
        if (settingsRequested) {
          settingsRequested = false;
          handler();
        }
      },
      call: (method, params, signal) => request({ method, params }, signal),
      subscribe: async (method, params, events, signal) => {
        const id = `${documentId}-${++nextStream}`;
        try {
          await request({ action: "subscribe", id, method, params }, signal);
        } catch (error) {
          void request({ action: "cancel", id }).catch(() => {});
          throw error;
        }
        const controller = new AbortController();
        const cancel = () => {
          if (controller.signal.aborted) return;
          controller.abort();
          signal?.removeEventListener("abort", cancel);
          void request({ action: "cancel", id }).catch(() => {});
        };
        signal?.addEventListener("abort", cancel, { once: true });
        if (signal?.aborted) cancel();
        void (async () => {
          try {
            while (!controller.signal.aborted) {
              const frames = await request({ action: "poll", id }, controller.signal);
              if (frames === 410) break;
              if (Array.isArray(frames)) {
                for (const frame of frames) {
                  if (controller.signal.aborted) break;
                  if (typeof frame._wailsBinary === "string") {
                    if (events.onBinary) {
                      events.onBinary(Uint8Array.from(atob(frame._wailsBinary), char => char.charCodeAt(0)));
                    }
                  } else {
                    events.onResponse(frame);
                  }
                }
              }
            }
          } catch (error) {
            if (!controller.signal.aborted) {
              events.onError?.({ code: "local_connection_interrupted", message: String(error) });
            }
          } finally {
            const ended = !controller.signal.aborted;
            cancel();
            if (ended) events.onClose?.();
          }
        })();
        return cancel;
      },
    }),
  });
})
