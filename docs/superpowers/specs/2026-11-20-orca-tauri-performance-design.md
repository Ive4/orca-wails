# Orca Tauri v2 性能验证设计

## 目标和范围

在 Apple Silicon macOS 上验证 Tauri v2 能否缩短 Orca 启动及会话切换时间，并降低本机整套进程的内存占用。第一阶段交付一个可运行的 Tauri 窗口和一份可复测的性能报告。Orca runtime 继续拥有工作树、终端会话、Agent 与持久化状态。首轮只覆盖三个对照组都能执行的工作树列表、会话打开和切换操作。桌面专属功能、跨平台适配、安装包分发和前端重构留待后续决策。

## 方案选择

用户选定直接复用 Orca Web 客户端，桌面宿主采用 Tauri v2。Tauri 和 Chromium 加载同一版本、同一来源的 Web 资源，连接同一个本地 Orca runtime，轮流操作相同的测试会话。这个对照控制了前端代码和服务端差异，所得差异反映桌面宿主与渲染引擎的共同影响。另用原版 Orca 桌面应用衡量实际体验，比较结果包含桌面与 Web 的界面实现和数据链路差异。

Wails V3 也适合承载 Web 客户端，两者在 macOS 均使用 `WKWebView`。采用 Tauri v2 是桌面宿主的技术选型。其窗口支持直接加载 Orca 提供的本机 HTTP URL，首轮只需要很少的 Rust 代码。首轮基准聚焦原版 Orca、Chromium 与 Tauri 这三组，Tauri 和 Wails 之间的性能排序需要另做对照。[Tauri 窗口 URL 配置](https://v2.tauri.app/reference/config/#webviewurl) · [Wails 窗口配置](https://v3.wails.io/features/windows/options/)

另一个方案是直接做精简 UI，通过 runtime 接口实现同样的操作。它能同时改进界面加载与渲染，但需要先实现客户端交互，首轮也更难量化更换宿主带来的收益。

## 客户端与 runtime

- 以本机 Orca `1.4.265`（`3534932c2d0ee3305df97c9689ea23efa207abdd`）为初始基线。记录实际测试的 Orca 构建、Tauri、Chromium 和 macOS 版本。若必须更换 Orca 版本，所有对照组一起更换。
- 首选用 `ORCA_SERVE_RUNTIME=orcad` 启动原版 `orca serve` 并绑定 `127.0.0.1`。macOS 打包版默认使用 Electron 服务端，实验前核对实际进程。若 `orcad` 无法同时提供 Web 资源或所需操作，可用同版本 Electron 无窗口服务端继续实验。两组 Web 对照使用同一种后端，Electron 服务端计入整套内存。若两种模式都无法提供核心流程，记录缺口和实际可测范围。
- Tauri 在 `WKWebView` 中直接打开 Orca 提供的本机 Web 页面，沿用 Web 客户端的配对、连接与终端 RPC。Rust 端只负责窗口和启动入口，runtime 作为独立进程运行。Web 页面通过 Orca 自有协议通信，Tauri 命令和插件权限保持关闭。Tauri 退出后，用同一客户端重新打开并接回测试会话。用隔离的 Orca 数据目录和独立客户端配置做实验。连接凭据由原客户端的配对流程处理，性能日志仅记录时间、版本、资源占用和脱敏的错误信息。
- 先确认隔离配置下 Chromium 与 Tauri 均能配对、显示工作树列表、打开已有终端会话、看到终端回显并切换会话。若只在 `WKWebView` 中失败，首轮按原版 Web 资源报告兼容性问题，继续测量可比场景。修补前端兼容性留待下一轮决定。

## 测量协议

用同一台机器、相同窗口尺寸和匹配的测试数据。建立固定工作树和三个可复现的测试终端，使用同样的本地脚本预填确定性输出，避免模型响应时间干扰测量。各组使用等价初始状态，关掉无关工作负载并轮流前台运行。首次配对只做一次并单独记录。每种场景先预演，再按交错顺序测至少 10 次，保留原始样本并报告中位数、范围和失败次数。

| 指标 | 测量边界 |
| --- | --- |
| 本机全冷启动 | 客户端与该组所需 runtime、终端后台进程均未运行，从启动整套进程计到工作树列表出现且可操作。保留已配对配置。无法安全隔离后台进程时，标记该组全冷启动不可测。 |
| runtime 已就绪时启动 | 同一 runtime 已就绪，Chromium 和 Tauri 分别从客户端进程启动计到相同的列表可操作。 |
| 打开和切换会话 | 从相同的输入动作计到目标终端显示预设标记并可交互。分别记录首次打开已有会话与已打开会话之间的切换。各组使用相同的外部观测方法，启动和交互的完成点使用同一判据。 |
| 空闲与三会话内存 | 应用可操作或打开三个会话后各等待 60 秒，采样 macOS physical footprint。记录客户端及渲染、GPU、网络辅助进程，连同本机 runtime 和 Orca 终端后台进程。单独列客户端小计与整套占用，避免重复计数。WebKit 服务可能由系统启动，按归属核对进程，不能归属时标记数据不完整。 |

## 验收与决策

首轮验收看对照能否运行、报告能否解释结果。报告提供运行步骤、版本和进程清单、原始样本、对照汇总及功能缺口。测量条件不齐的指标注明原因，保留可比结果。

建议把原版桌面作为是否继续投入的基准。若 Tauri 整套进程在空闲和三个会话时的内存中位数都至少降低 20%，全冷启动或会话切换的中位数至少缩短 15%，且其余交互指标的延迟增幅在 10% 以内，建议扩大验证范围。只改善一个维度时，报告收益和代价，供下一阶段决策。这些百分比是本设计提出的判断标准。若差异与样本波动相当，增加重复次数或报告证据不足。

实现阶段决定具体的 Tauri v2 版本、启动命令包装和采样脚本。三组运行方式与测量边界以此设计为准。

## 参考

- [Orca CLI 的 runtime 命令](https://orca.dev/docs/cli/reference)
- [Orca 无窗口服务与运行模式](https://github.com/stablyai/orca/blob/3534932c2d0ee3305df97c9689ea23efa207abdd/docs/site/content/docs/remote-servers.mdx)
- [Tauri v2 的进程模型](https://v2.tauri.app/concept/process-model/)
- [Tauri v2 的能力权限](https://v2.tauri.app/security/capabilities/)
- [Apple 的内存 footprint 测量说明](https://developer.apple.com/documentation/xcode/analyzing-the-memory-usage-of-your-metal-app)
