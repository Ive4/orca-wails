# Orca-Wails 终端配置导入首版设计

状态为待确认。对应 [桌面能力清单 01A](../plans/2026-11-22-orca-wails-desktop-capabilities.md#01-配置导入)。

## 现状与目标

官方 Orca 的终端外观设置通过 Electron IPC 预览 Ghostty、Kitty 的配置差异，用户确认后才写入设置。Wails 运行 Web 版，终端外观采用 Web 设置存储，导入入口按 Web 客户端隐藏。此次将「配置导入」落在终端外观。已有的官方 Orca 项目和历史导入由 `internal/official` 管理。[上游设置入口](https://github.com/stablyai/orca/blob/v1.4.268/src/renderer/src/components/settings/terminal/appearance/TerminalAppearanceSection.tsx) · [上游导入流程](https://github.com/stablyai/orca/blob/v1.4.268/src/main/terminal-config-import/preview.ts)

首版让 macOS ARM64 的 Wails 用户在本机设置里手动选取 Ghostty 或 Kitty 的单个配置文件，预览配置差异，再确认应用。支持扩展名为空的文件。自动发现、引用文件、Warp/YAML 主题和 Windows 接入依次列在 01B、01C。

## 实现方案

- **建议先做 WebView 单文件导入。** 复用上游纯解析、映射、差异计算、预览弹窗和设置写入。文件选择由现有 WebView 提供，解析只需要用户选中的文件内容。首版可随 macOS 已有的定制 Web 构建交付。
- **通过原生选择器和本地 Core 接入上游完整导入。** 这一方案允许解析本机默认配置路径、Kitty include 和 Ghostty 主题引用，还需要处理本地文件访问范围和跨平台打包。等 01B 再决定能复用的部分。

## 用户行为

- 只有连接 Wails 自己启动的本机 Core 时显示两个导入入口及对应搜索结果。已有 Electron 入口沿用上游逻辑。
- 打开导入弹窗后，使用「选择配置文件」选取一个 UTF-8 文本文件。大小上限沿用上游的 1,000,000 字节。读取失败或超限时显示错误。选择器取消时保留已有预览。
- 预览显示文件名、变化的设置和未支持的配置键。基础字段沿用上游映射，包括字体、字号和颜色。涉及另一个文件、命令或环境变量的选项只显示键名。
- 预览和取消只影响弹窗。点击「应用更改」才调用现有设置写入。没有差异时不提供应用按钮。设置保存完成后才显示成功，重开同一数据目录可以读回配置。切换到配对服务器或关闭弹窗后，失效的预览不允许保存。

## 验收样例

| 场景 | 预期 |
| --- | --- |
| 原字号为 12，选择内容为 `font-size = 16`、`background = #112233` 的 Ghostty 文件 | 预览显示字号 16 和背景色。当前字号在点击应用前仍为 12 |
| 选择含 `font_size 17`、`include another.conf` 和 `geninclude echo` 的 Kitty 文件 | 预览字号 17，列出 `include`、`geninclude`，单文件解析不涉及其他文件或命令 |
| 配置中的字号与当前一致 | 预览无差异，应用按钮不可用 |
| 已有预览时取消再次选择文件 | 预览保留，当前设置不变 |
| 文件超过 1,000,000 字节，或读取失败 | 显示错误，配置无法应用 |
| 点击应用后重开同一 macOS 测试数据目录 | 终端字号及颜色配置可读回 |
| 连上已配对的远程服务器，或在普通浏览器打开 Web 客户端 | 设置页和搜索中均无 Wails 本机导入入口 |
