((packs) => {
  "use strict";
  const page = window.location;
  if (window.top !== window.self || page.protocol !== "http:" ||
      !["127.0.0.1", "localhost"].includes(page.hostname) || !page.port ||
      page.pathname !== "/web-index.html") return;

  const portraits = packs.flatMap(pack => pack.avatars);
  const preset = new Map(portraits.filter(avatar => avatar.slot).map(avatar => [avatar.slot, avatar.src]));
  const fileInput = 'input[type="file"][accept*="image/png"][accept*="image/webp"]';
  const controls = `img[src], ${fileInput}`;
  const recoloredSVG = "data:image/svg+xml;charset=utf-8,";
  const presetFaces = Array.from(preset.keys(), id => `[data-face="${id}"]`).join(",");

  function replaceMascot(image) {
    const src = image.getAttribute("src");
    if (!src) return;
    let id;
    try {
      if (src.startsWith(recoloredSVG)) {
        const header = decodeURIComponent(src.slice(recoloredSVG.length).split("%3E", 3).join("%3E"));
        id = /^<svg [^>]*viewBox="0 0 128 128"><defs><filter id="([A-Z]\d)b"/.exec(header)?.[1];
      } else if (!src.startsWith("data:")) {
        const url = new URL(src, page.href);
        if (url.origin === page.origin) {
          id = /^\/assets\/([A-Z]\d)(?:-(?:rim|cut))?-[\w-]{8}\.(?:svg|webp)$/.exec(url.pathname)?.[1];
        }
      }
    } catch {
      return;
    }
    const replacement = preset.get(id);
    if (replacement && replacement !== src) image.setAttribute("src", replacement);
  }

  function mountLibrary(input) {
    const picker = input.parentElement;
    if (!picker?.querySelector(':scope > [aria-roledescription="carousel"]') ||
        picker.querySelector("select[data-orca-avatar-library]")) return;

    const language = (document.documentElement?.lang || navigator.language).startsWith("zh") ? "zh" : "en";
    const label = language === "zh" ? "从头像库选择…" : "Choose from avatar library…";
    const select = document.createElement("select");
    select.dataset.orcaAvatarLibrary = "true";
    select.setAttribute("aria-label", label);
    select.style.cssText = "display:block;max-width:100%;margin:6px auto 0;padding:3px 6px;" +
      "border:1px solid var(--border);border-radius:6px;background:var(--popover);" +
      "color:var(--popover-foreground);font:inherit;font-size:12px;";

    const placeholder = document.createElement("option");
    placeholder.value = "";
    placeholder.textContent = label;
    select.append(placeholder);

    let index = 0;
    for (const pack of packs) {
      const group = document.createElement("optgroup");
      group.label = pack[language];
      for (const avatar of pack.avatars) {
        const option = document.createElement("option");
        option.value = String(index++);
        option.textContent = avatar[language];
        group.append(option);
      }
      select.append(group);
    }
    select.addEventListener("change", () => {
      if (select.value === "") return;
      const avatar = portraits[Number(select.value)];
      select.value = "";
      if (!avatar) return;
      const base64 = avatar.src.split(",", 2)[1];
      const bytes = Uint8Array.from(atob(base64), letter => letter.charCodeAt(0));
      const transfer = new DataTransfer();
      transfer.items.add(new File([bytes], avatar.file.split("/").at(-1), { type: "image/webp" }));
      input.files = transfer.files;
      input.dispatchEvent(new Event("change", { bubbles: true }));
    });
    // These portraits contain their own backgrounds, so the mascot color swatches do not apply.
    picker.setAttribute("data-orca-avatar-picker", "");
    const style = document.createElement("style");
    style.textContent = `[data-orca-avatar-picker]:has(> [aria-roledescription="carousel"] > [data-position="current"]:is(${presetFaces}))
      > div [role="group"]:has(> button[aria-pressed][style*="background-color"]) { display: none; }`;
    input.after(select, style);
  }

  function update(node) {
    if (node.tagName === "IMG") replaceMascot(node);
    else mountLibrary(node);
  }

  new MutationObserver(records => {
    for (const record of records) {
      if (record.type === "attributes") {
        if (record.target.tagName === "IMG") replaceMascot(record.target);
        continue;
      }
      for (const node of record.addedNodes) {
        if (node.nodeType !== 1) continue;
        if (node.matches(controls)) update(node);
        node.querySelectorAll(controls).forEach(update);
      }
    }
  }).observe(document, { subtree: true, childList: true, attributes: true, attributeFilter: ["src"] });
  document.querySelectorAll(controls).forEach(update);
})
