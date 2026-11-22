# Orca-Wails 版本跟进规划

更新于 2026-11-22 12:04（北京时间）。官方最新稳定版为 **`v1.4.269`**，发布于 2026-11-22 01:57:28（北京时间）。[官方发布](https://github.com/stablyai/orca/releases/tag/v1.4.269) · [发布 API](https://api.github.com/repos/stablyai/orca/releases/latest) · [macOS 已安装版本 → `.269`](https://github.com/stablyai/orca/compare/v1.4.268...v1.4.269) · [Windows 正式锁 → `.269`](https://github.com/stablyai/orca/compare/v1.4.267...v1.4.269)

## 当前状态

| 平台 | 工作区正式锁 | 候选产物和验收 | 安装或活动 Core |
| --- | --- | --- | --- |
| macOS ARM64 | `.269 / 0.0.24`，`HEAD` 中为 `.268 / 0.0.13`，新锁在工作区改动中。[锁文件](../core-version.macos-arm64.json) | **基础冒烟已通过，发布待验收。** `.269` 已有本地包。三份 `0.0.24` 的 SHA-256 不同，发布候选待确定。Bot、配对登录和浏览器 UI 仍有缺口。 | 本机 `/Applications/Orca-Wails.app` 与默认数据目录活动 Core 均为 `.268 / 0.0.13`。 |
| Windows x64 | `.267 / 0.0.1`，已静态校验。[锁文件](../core-version.json) | **阻塞。** `.269 / 0.0.1` 候选仅登记官方安装包摘要，状态为 `awaiting-windows-validation`。需要 Windows 11 x64 运行环境。 | 本次未核实。 |

`[x]` 表示该项列出的范围已有验证结果。`[ ]` 后注明 **待开发**、**待验收** 或 **阻塞**。版本号采用 `Core tag / package_revision`。

## 已完成及证据

- [x] **查版与候选锁。** 运行了 `python3 scripts/follow-orca.py`，通过发布 API 核对发布时间。对照根目录锁、候选锁、本地包清单、已安装应用和活动 Core 指针。Windows 候选仍只在 `dist-core/follow/v1.4.269/`。
- [x] **macOS Core 和共享 Web 基础构建。** 发布标签对应源码 `af41be45`，官方 macOS 应用记录的构建提交为 `e2d67466`，两者分别锁定。现有实施记录覆盖 `.269` Wails 应用冒烟、Python 和 Node 脚本测试、35 个上游定向用例。本次重跑 `go test ./...` 与 `python3 scripts/test-follow-orca.py` 均通过。`tc:node` 和 `tc:web` 的 Cua RPC 和标签拖拽类型检查仍有失败记录。[macOS 锁](../core-version.macos-arm64.json) · [实施记录](superpowers/plans/2026-11-22-orca-wails-desktop-capabilities.md#当前进度)
- [x] **macOS 导入门槛及测试数据。** 导入目标已切到 `.269`。本次 Go 测试覆盖项目及历史、SQLite WAL、未发送的 connector replies、备份及失败恢复。官方 `.269` Profile 的端到端验收见下方任务。[导入版本](../internal/official/official.go) · [导入测试](../internal/official/import_darwin_test.go)
- [x] **桌面能力首批子项。** 串流、扩展、模拟器和状态栏插件已有对应验证结果，范围见本页 05–08。[详细验收记录](superpowers/plans/2026-11-22-orca-wails-desktop-capabilities.md#05-远程串流)

## 待办及验收

### P0 · Windows x64 的 `.269` Core 与桌面冒烟

- [ ] **阻塞于 Windows 11 x64 实机或桌面 CI。** 使用 `.269` 候选锁执行下列命令。脚本检查 Node `24.22.1`、包哈希、ConPTY 和 Job。确认候选中的 Codex 历史引用和占用统计 worker 能实际调用，再启动便携包验收 WebView2 页面、配对和重连、关闭重开、托盘交互，以及取消 Git 后清理子进程。当前提取流程使用官方包的 Web 资源，Wails 定制资源的构建跟踪见 04。记录 Windows 与 WebView2 版本、候选包 SHA-256 和运行结果。通过后更新候选验证记录并转正 `core-version.json`。[Windows 提取](../scripts/extract-headless.ps1) · [Core 校验](../scripts/verify-core.ps1) · [Wails 构建](../scripts/build-windows.ps1)

  ```powershell
  $candidate = 'dist-core/follow/v1.4.269'
  pwsh -File scripts/extract-headless.ps1 -VersionFile "$candidate/core-version.json" -OutputDirectory "$candidate/windows"
  # 使用上一条命令输出的 Qualified core archive 路径
  pwsh -File scripts/build-windows.ps1 -Archive '<已验证的 ZIP 路径>'
  ```

### P0 · macOS ARM64 的下一份可发布候选

- [ ] **待开发和验收。** 三份本地 `.269 / 0.0.24` 包摘要前缀分别为 `144b79fc34a3`、`4fe530cc19b5`、`60f372897b6b`。更新检查按 tag 和 revision 排序，同号重包不会替换现有 Core。选定一份源码与补丁快照，有内容变化时递增 `package_revision`，构建后记录完整 SHA-256、签名和测试结果。用隔离数据目录验证从 `.268 / 0.0.13` 及已装 `.269 / 0.0.24` 升级，覆盖首次启动、真实 PTY、WebKit 配对、关窗重开、退出清理和回退。两个起点均应激活同一份候选 Core。实际交付后再更新安装状态。[Core 激活](../desktop.go) · [版本排序](../internal/update/manifest.go)

  ```sh
  candidate=dist-core/follow/v1.4.269
  python3 scripts/follow-orca.py --tag v1.4.269 --prepare
  # 核对候选锁的 package_revision 后再提取
  python3 scripts/extract-macos-core.py --download-orca \
    --version-file "$candidate/core-version.macos-arm64.json" --output "$candidate/macos"
  ./scripts/build-macos.sh --smoke-test --archive "$candidate/macos/bootstrap-core.zip"
  python3 scripts/verify-bun-macos.py --version-file "$candidate/core-version.macos-arm64.json" \
    --core-dir "$candidate/macos" --app 'bin/Orca-Wails Smoke.app'
  ```

### P1 · `.269` Core、CLI 与数据导入

- [ ] **待验收。** 上游 `.269` 新增八种 Bot Agent，Chat 新增 Kiro、Auggie、Goose 入口。用锁定源码和候选包核对 Agent 枚举、启动参数及缺依赖提示。用可用测试 Agent 走 Bot 创建、发送首条消息、续跑及结果显示。覆盖 Codex 历史引用读取和磁盘占用统计的实际 worker 调用，并检查空闲终端与 Git 后台 worker 的进程表现。按平台记录通过范围及外部 Agent 依赖。[官方发布](https://github.com/stablyai/orca/releases/tag/v1.4.269) · [Core 打包](../scripts/extract-macos-core.py)
- [ ] **待验收。** 用官方 `.269` 生成的 Profile 副本验证导入和再开，检查 profile index、`profile-state`、`agent-session-domain`、附件及 `took_ms`。执行 `go test ./internal/official -count=1`，再分别验证新目录、已有数据的备份恢复、不支持的 schema 以及旧 Core 使用保留数据回退。通过条件为导入后的项目和历史可打开，源目录完好，失败能恢复。Windows 的官方数据导入当前无支持范围。[导入实现](../internal/official/data_darwin.go) · [历史端到端记录](superpowers/plans/2026-11-21-orca-v1.4.268-upgrade.md#实施记录)

### P1 · `.269` Web、配对与桌面 UI

- [ ] **待开发。** 核对 Wails 本机承载界面对远端 Bot 主机名与全局 Bots 设置的 gating。验证本机、远端 A 和远端 B 的列表与设置，远端 Bot 显示对应主机名，开关按目标主机读写，设置页与搜索入口一致。修改点从 `use-bot-host-label.ts`、设置导航和现有 Wails 本机判定开始。[上游主机标签](https://github.com/stablyai/orca/blob/v1.4.269/src/renderer/src/components/bots/use-bot-host-label.ts) · [本机 Web 补丁](../scripts/macos-local-runtime.patch)
- [ ] **待开发和验收。** 核对配对环境的 Antigravity Google / agy 登录入口、回调及设备归属。覆盖正常完成、取消、重连和选错主机，用测试账户确认状态只更新到发起的主机。工作区 WebKit 目前将 popup 加载到原视图，另用测试页验收依赖 `window.opener` 的回传及关闭行为。两个流程分别验证。[上游登录变更](https://github.com/stablyai/orca/pull/46125) · [工作区浏览器](../workspace_browser_darwin.h)
- [ ] **待验收。** 核对 native chat 的 worker 审批订阅、同意或拒绝及回执在本机 Wails 和配对 Web 的可达性。用隔离任务验证授权范围、断线重连和过期处理。运行 `node scripts/test-pairing-init.mjs`、`node scripts/test-window-init.mjs`、`node scripts/test-workspace-browser.mjs`，在应用中走通对应流程。在锁定的上游源码运行 `pnpm run tc:node`、`pnpm run tc:web` 和 `pnpm run build:web`，记录 Cua RPC 和拖拽错误的处理结果。[上游审批 UI](https://github.com/stablyai/orca/tree/v1.4.269/src/renderer/src/components/native-chat/worker-approvals) · [WebKit 导航](../window_darwin.go)

### P2 · 跨版本桌面能力

现有任务按 05–08、01–04 的顺序推进。移动端的问答、审批和工具设置依赖官方手机客户端。官方 Electron 的窗口行为按 Wails 的原生实现做功能验收。

| 任务 | macOS ARM64 | Windows x64 |
| --- | --- | --- |
| 05 远程串流 | 无头 Chromium 提供流的双标签用例已验收。待测官方远端供流及客户端关窗、重连。 | 待验证平台提供方和配对客户端。 |
| 06 浏览器扩展 | 15.4+ 本地解包扩展的站点授权和重启存储已验收。设置页管理和商店安装待评估。 | 待工作区浏览器及扩展 API 验证。 |
| 07 模拟器 | iOS 已接入，模拟帧流及缺少 `simctl` 的提示已测。缺少完整 Xcode 和 iOS Runtime，真实模拟器画面与输入待验收。另验 Android。 | 待 Android SDK 和对应平台验证。 |
| 08 高级插件 | `statusBarItems` 首个插件的安装、授权、更新、拒绝越权、禁用及清理已验收。其他桌面接口待按插件验证。 | 待验证插件宿主能力和 UI。 |
| 01 终端配置导入 | Ghostty / Kitty 单文件已有设计和实施清单，待开发。自动发现、引用和 Warp 主题有独立任务。 | 待开发并验证 WebView2 文件选择与定制 Web 打包。 |
| 02 语音听写 | 待接通麦克风权限、转写后端和输入框。 | 待核实运行时及权限支持。 |
| 03 多 Profile | 待实现创建、切换、重启恢复与数据隔离。当前官方数据导入覆盖单个活动 Profile。 | 待实现并验证存储隔离。 |
| 04 Windows 工作区浏览器 | 本机 WebKit 标签已有实现。 | 待接入独立 WebView2 子视图、下载、导航和生命周期。 |

实现路径、测试和通过条件见 [桌面能力 01–08](superpowers/plans/2026-11-22-orca-wails-desktop-capabilities.md) · [终端配置导入 01A](superpowers/plans/2026-11-22-orca-terminal-config-import.md)。

## 历史版本

| 官方版本 | 已完成 | 当时未完成或缺少记录 |
| --- | --- | --- |
| [`.266`](https://github.com/stablyai/orca/releases/tag/v1.4.266) | `c3b2f49` 两平台均锁定 `.266`。macOS 有 Core 构建，Windows 核验了官方安装包、433 个 Core 文件和 1,513 个 Web 资源。 | Windows 运行时、PTY、Job、WebView2 待验收。完整功能及 UI 同步缺少逐项记录。 |
| [`.267`](https://github.com/stablyai/orca/releases/tag/v1.4.267) | 两平台曾锁定 `.267`。macOS 有 Core 构建，Windows 核验了官方安装包、433 个 Core 文件和 1,515 个 Web 资源。 | Windows 仍只完成静态检查。完整功能及 UI 同步缺少逐项记录。 |
| [`.268`](https://github.com/stablyai/orca/releases/tag/v1.4.268) | macOS `0.0.13` 已通过 Bun、双 Codex worker、真实 PTY、WebKit 配对、官方数据导入及回退。Windows 外壳交叉构建与安装包静态检查通过。 | Windows 官方包缺两个 Codex worker，且无实机运行验证，正式锁留在 `.267`。桌面能力另有待办。[实施记录](superpowers/plans/2026-11-21-orca-v1.4.268-upgrade.md#实施记录) · [.267 → .268](https://github.com/stablyai/orca/compare/v1.4.267...v1.4.268) |
| [`.269`](https://github.com/stablyai/orca/releases/tag/v1.4.269) | macOS 工作区和本地构建已跟进，Windows 已生成候选锁。 | 见本页待办。macOS 发布、Windows 实机、各项 UI 流程分别记状态。 |

## 每次跟进怎么更新本页

1. 按 README 查版，更新查询时间及官方发布时间。查询失败时写明原因并标为「上游未确认」，保留已核实的本地锁和活动版本。两平台版本已追平时记录无需安排升级，原有功能和验收待办继续跟踪。[跟进流程](../README.md#跟进官方-orca)
2. 有新 tag 时，从两个平台的锁各附比较链接。按 Core/CLI、运行时和产物、Web/配对、导入记录有证据的变化及待验证影响。新任务附代码路径、复现或测试命令和通过条件，跨版本待办链接到已有实施清单。
3. 每项通过后再勾选，记录日期、平台、tag、revision、测试结果以及产物摘要或提交链接。分别更新工作区锁、候选与运行状态，将结束的版本批次移入历史表。
