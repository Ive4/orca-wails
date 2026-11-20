"use strict";

const get = id => document.getElementById(id);
const panelToken = location.hash.slice(1);
async function invoke(command, args) {
  const state = command === "tray_state";
  const endpoint = state ? `api?page=${encodeURIComponent(args.page)}` : "api";
  const response = await fetch(endpoint, {
    method: state ? "GET" : "POST",
    headers: { "X-Orca-Panel": panelToken, ...(state ? {} : { "Content-Type": "application/json" }) },
    body: state ? undefined : JSON.stringify(args.action),
    cache: "no-store",
  });
  if (!response.ok) throw (await response.text()).trim();
  return response.json();
}
const panel = get("panel");
const list = get("list");
document.body.toggleAttribute("data-native-material", window.__orcaTrayNativeMaterial === true);
const states = { waiting: "等待输入", blocked: "受阻", monitoring: "监测中", unknown: "待确认", done: "已结束" };
const providers = { codex: "Codex", claude: "Claude Code", cursor: "Cursor", opencode: "OpenCode", gemini: "Gemini", pi: "Pi" };
let page = "tasks";
let tab = "active";
let snapshot;
let rendered = "";
let request = 0;
let pending = false;
let confirmation;
let error = "";
let poll;
let appliedHeight;
let wantedHeight;
let resizing = false;
let ready = false;

function node(tag, className, value) {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (value) element.textContent = value;
  return element;
}

function showNotice(id, text) {
  get(id).hidden = !text;
  get(id).textContent = text || "";
}

async function action(kind, args = {}) {
  if (pending) return;
  pending = true;
  error = "";
  try {
    await invoke("tray_action", { action: { kind, ...args } });
  } catch (failure) {
    error = typeof failure === "string" ? failure : "操作失败，请重试";
  } finally {
    pending = false;
    refresh();
  }
}

function age(value, running) {
  if (!value || value > Date.now()) return "";
  const minutes = Math.floor((Date.now() - value) / 60000);
  if (!minutes) return running ? "刚开始" : "刚刚";
  const amount = minutes < 60 ? `${minutes} 分钟` : `${Math.floor(minutes / 60)} 小时`;
  return running ? `运行 ${amount}` : `${amount}前`;
}

function row(task) {
  const button = node("button", "task");
  button.dataset.focus = task.id;
  button.disabled = !snapshot.canOpen;
  button.onclick = () => action("open", { id: task.id });
  const identity = node("span", "ellipsis");
  identity.append(
    node("span", "task-title", task.title),
    node("span", "meta", [task.workspace, providers[task.provider] || task.provider].filter(Boolean).join(" · ")),
  );
  const right = node("span", "task-right");
  if (states[task.status]) right.append(node("span", "state", states[task.status]));
  const running = task.status === "working";
  const time = age(task.startedAt || task.updatedAt, running);
  if (time) right.append(node("span", "time", time));
  button.append(identity, right);
  return button;
}

function group(title, className, tasks) {
  const section = node("section", `group ${className}`);
  if (title) {
    section.setAttribute("aria-label", title);
    section.append(node("h3", "", title));
  }
  section.append(...tasks.map(row));
  return section;
}

function renderTasks() {
  const summary = snapshot.summary;
  const all = summary?.rows || [];
  const attention = all.filter(task => ["waiting", "blocked"].includes(task.status));
  const working = all.filter(task => ["working", "monitoring"].includes(task.status));
  const unknown = all.filter(task => task.status === "unknown");
  const today = Date.now() - 24 * 60 * 60 * 1000;
  const recent = all.filter(task => task.status === "done" && (task.startedAt || task.updatedAt || 0) >= today);
  get("count").textContent = (attention.length + working.length) || "";
  get("open").disabled = !snapshot.canOpen;
  get("active").setAttribute("aria-current", String(tab === "active"));
  get("recent").setAttribute("aria-current", String(tab === "recent"));
  const children = [];
  if (snapshot.taskState !== "ready") {
    const text = { loading: "正在读取任务…", unavailable: "暂时读不到任务状态", offline: "暂时读不到任务状态", stopped: "暂无运行中的任务" }[snapshot.taskState];
    const empty = node("div", "empty", text);
    if (snapshot.taskState === "unavailable") {
      const retry = node("button", "control retry", "重试");
      retry.onclick = () => action("retry_tasks");
      empty.append(retry);
    }
    children.push(empty);
  } else if (tab === "recent") {
    if (recent.length) children.push(group("", "history", recent));
    else children.push(node("div", "empty", "过去 24 小时暂无已结束的任务"));
  } else {
    if (attention.length) children.push(group("待处理", "needs", attention));
    if (working.length) children.push(group("正在执行", `running${attention.length ? " after-attention" : ""}`, working));
    if (unknown.length) children.push(group("", "unknown", unknown));
    if (!children.length) children.push(node("div", "empty", "没有正在进行的任务"));
  }
  list.replaceChildren(...children);
  showNotice("task-message", error || (summary?.partial ? "仅显示可读取的工作区" : ""));
}

function go(next, focus) {
  page = next;
  confirmation = undefined;
  snapshot = undefined;
  rendered = "";
  error = "";
  get("tasks").hidden = next !== "tasks";
  get("preferences").hidden = next === "tasks";
  get("page-title").textContent = { settings: "设置", about: "关于与更新", advanced: "高级" }[next] || "设置";
  if (next !== "tasks") {
    get("settings-content").replaceChildren(node("p", "", "正在读取…"));
    showNotice("settings-message", "");
  }
  if (focus) get(next === "tasks" ? "settings-button" : "back").focus();
  refresh();
}

function option(text, handler, detail, disabled = false) {
  const element = node(handler ? "button" : "div", handler ? "settings-row" : "settings-row info");
  element.append(node("span", "", text));
  if (detail === "›") {
    const chevron = node("i", "chevron");
    chevron.setAttribute("aria-hidden", "true");
    element.append(chevron);
  } else if (detail) {
    element.append(node("span", "version", detail));
  }
  if (handler) {
    element.disabled = disabled;
    element.dataset.focus = text;
    element.onclick = handler;
  }
  return element;
}

function confirmAction(kind, explanation, label) {
  confirmation = { kind, explanation, label };
  render(true);
  get("cancel-operation")?.focus();
}

function renderSettings() {
  const data = snapshot.shell || {};
  const locked = snapshot.busy || pending;
  const options = [];
  if (confirmation) {
    const block = node("div", "confirm");
    block.append(node("p", "", confirmation.explanation));
    const controls = node("div", "confirm-controls");
    const cancel = node("button", "control", "取消");
    cancel.id = "cancel-operation";
    cancel.onclick = () => { confirmation = undefined; render(true); };
    const accept = node("button", "control accept", confirmation.label);
    accept.disabled = locked;
    accept.onclick = () => {
      const { kind } = confirmation;
      confirmation = undefined;
      action(kind);
      render(true);
    };
    controls.append(cancel, accept);
    block.append(controls);
    options.push(block);
  } else if (page === "settings") {
    options.push(
      option("关于与更新", () => go("about", true), "›"),
      option("高级", () => go("advanced", true), "›"),
      option("连接官方 Orca…", () => action("open_paired"), "", locked),
    );
    if (data.importOfficial) options.push(option("从官方 Orca 导入…", () =>
      confirmAction("import_official", "导入当前资料的项目和官方 Orca 内置聊天记录。Orca-Wails 会先备份原数据，重启内核时会结束正在运行的 Orca-Wails 任务。", "导入"), "", locked));
    options.push(option("开机启动", () => action("autostart"), data.autostart ? "已开启" : "已关闭", locked));
    if (data.windowOpen) options.push(option("关闭 Orca 窗口", () => action("close_orca")));
    options.push(option("退出 Orca-Wails", () => confirmAction("quit", "退出会停止后台运行的任务。", "退出")));
  } else if (page === "about") {
    const versions = node("div", "settings-section");
    versions.append(option("Orca-Wails", null, data.appVersion), option("内核", null, data.coreVersion || "尚未安装"));
    options.push(versions);
    const updates = node("div", "settings-section");
    for (const [family, allowed, version] of [["app", data.checkApp, data.pendingApp], ["core", data.checkCore, data.pendingCore]]) {
      if (family === "core" && data.bundledCore) continue;
      const label = family === "app" ? "应用" : "内核";
      if (!allowed) {
        updates.append(option(`${label}更新`, null, family === "app" ? "未启用" : "未配置更新源"));
      } else if (version) {
        const disruption = family === "app" ? "安装会停止后台任务并重启应用。" : "安装会停止后台任务并重启内核。";
        updates.append(option(`安装${label}更新…`, () => confirmAction(`install_${family}`, disruption, "安装并重启"), version, locked));
      } else {
        updates.append(option(`检查${label}更新`, () => action(`check_${family}`), "", locked));
      }
    }
    updates.append(option("查询官方最新版本", () => action("check_official"), data.officialVersion || "", locked));
    options.push(updates);
  } else {
    options.push(
      option("重启内核 / 重新配对", () => confirmAction("restart", "重启会停止此内核运行中的任务。", "重启"), "", locked || !data.coreInstalled),
      option("停止内核", () => confirmAction("stop", "停止内核会结束正在运行的任务。", "停止"), "", locked || !data.running),
    );
    if (!data.bundledCore) {
      options.push(option("回退内核", () => confirmAction("rollback", "回退会停止正在运行的任务。", "回退"), "", locked || !data.rollbackAvailable));
    }
    options.push(
      option("打开配置文件", () => action("config")),
      option("打开日志目录", () => action("logs")),
      option("打开数据目录", () => action("data")),
    );
  }
  get("settings-content").replaceChildren(...options);
  const message = ["窗口已打开", "窗口已关闭", "内核运行中", "官方版本查询完成"].includes(snapshot.message) ? "" : snapshot.message;
  showNotice("settings-message", error || message);
}

function render(force = false) {
  if (!snapshot) return;
  const signature = JSON.stringify([page, tab, snapshot, error, confirmation, page === "tasks" && Math.floor(Date.now() / 60000)]);
  if (!force && signature === rendered) return;
  rendered = signature;
  const focused = document.activeElement?.dataset.focus;
  if (page === "tasks") renderTasks(); else renderSettings();
  if (focused) [...panel.querySelectorAll("[data-focus]")].find(element => element.dataset.focus === focused)?.focus({ preventScroll: true });
}

async function refresh() {
  clearTimeout(poll);
  const id = ++request;
  try {
    const response = await invoke("tray_state", { page });
    if (id !== request) return;
    snapshot = response;
    document.documentElement.style.setProperty("--available", `${snapshot.availableHeight}px`);
    render();
  } catch {
    if (id !== request) return;
    showNotice(page === "tasks" ? "task-message" : "settings-message", "暂时无法读取，请重新打开弹窗");
  }
  if (id === request) {
    ready = true;
    measure();
    poll = setTimeout(refresh, 1200);
  }
}

for (const name of ["active", "recent"]) {
  get(name).onclick = () => {
    tab = name;
    list.scrollTop = 0;
    render();
  };
}
get("settings-button").onclick = () => go("settings", true);
get("back").onclick = () => go(page === "settings" ? "tasks" : "settings", true);
get("open").onclick = () => action("open");
document.addEventListener("keydown", event => {
  if (event.key === "Escape") {
    event.preventDefault();
    invoke("tray_action", { action: { kind: "close" } }).catch(() => {});
  }
});

function measure() {
  if (!ready) return;
  wantedHeight = Math.ceil(panel.getBoundingClientRect().height);
  void resize();
}

async function resize() {
  if (resizing || wantedHeight === appliedHeight) return;
  resizing = true;
  try {
    while (wantedHeight !== appliedHeight) {
      const height = wantedHeight;
      await invoke("tray_action", { action: { kind: "resize", height } });
      appliedHeight = height;
    }
  } catch {
    // Closing the panel cancels its pending native resize.
  } finally {
    resizing = false;
  }
}
new ResizeObserver(measure).observe(panel);
refresh();
