# Orca-Wails

Orca-Tauri 的 Wails 3 / Go 桌面实现。托盘常驻，按需创建 Orca Web 窗口，关闭时销毁窗口。内核直接运行随包的 `orcad-server.js`，macOS 使用 Bun，Windows 使用 Node。

## Apple Silicon macOS

需要 Go 1.26+、macOS 13.5+ 和 Xcode Command Line Tools。打包 Core 还需要 Python 3、Node.js 和锁定版本的官方 Orca。可以从官方应用提取，也可以使用已经验证的 Core 包。

```sh
./scripts/build-macos.sh
open bin/Orca-Wails.app
```

使用已有的 Orca-Tauri 打包产物时，传入对应的 ZIP 和同名 JSON。也可以传 `--no-core` 只构建外壳。

```sh
./scripts/build-macos.sh --archive /path/to/bootstrap-core.zip
open bin/Orca-Wails.app
```

内核提取参数见 `python3 scripts/extract-macos-core.py --help`。本地构建默认临时签名。使用稳定的 Developer ID 签名可以让 WebKit 在升级后继续使用已有钥匙串授权。通过 `ORCA_WAILS_SIGN_IDENTITY` 传入构建签名身份。

## Windows 11 x64

需要 Go 1.26+ 和 WebView2 Runtime。提取和验证 Core 需要 PowerShell 7.2+、7-Zip，步骤使用 `core-version.json` 锁定的安装包和 Node 版本。

```powershell
pwsh -File scripts/build-windows.ps1
.\bin\Orca-Wails.exe
```

如果已有 `scripts/extract-headless.ps1` 在 Windows 上生成并验证的 ZIP 和 `.sha256`，可用 `scripts/build-windows.ps1 -Archive <zip>` 生成带 Core 的 `bin/Orca-Wails/` 便携目录。分发该目录时同时保留引导 ZIP 和 JSON。

## 使用

- 左键点击托盘图标显示任务，右键打开原生菜单。选择任务进入对应会话。
- macOS 默认全局快捷键为 **Command+Shift+O**，Windows 为 **Ctrl+Shift+O**。关闭主窗口后，托盘与 Core 继续运行。
- 用 `--background` 启动时，Core 可在没有窗口的情况下运行。通过托盘设置切换开机启动、连接已配对服务器或在 macOS 导入官方 Orca 的项目与历史。导入需要匹配 Core 版本，原数据保存在同级备份目录。
- 默认数据目录为 `~/.orca-wails`。可用 `--data-dir` 或 `ORCA_WAILS_HOME` 指定其他绝对路径。首次运行创建 `config.json`。macOS 默认使用端口 16768，Windows 默认使用 6768。
- 配置格式见 [config.example.json](config.example.json)。可以查看应用、内核和官方最新版本。Core 首次安装与回退采用独立版本目录，候选 Core 完成启动、PTY 和 Web 配对检查后才会切换活动指针。Core 回退只切换二进制版本，数据格式由 Core 管理。

## 发布和更新

`update_repository` 配置为公开的 GitHub `owner/repo`。独立 Core 更新读取平台匹配的 Core Release ZIP 和 SHA-256。随包 Core 的 macOS 发行版通过更新应用带入新 Core。

应用更新走 Wails 签名机制。使用 `ORCA_WAILS_UPDATE_PUBLIC_KEY`（macOS）或 `-PublicKey`（Windows）在构建时写入 Base64 Ed25519 公钥，构建版本通过 `ORCA_WAILS_VERSION` 或 `-Version` 设置。通过 Wails 3 的 `wails3 updater manifest` 为 macOS 的应用 ZIP 或 Windows 的替换 EXE 生成签名清单，以 `wails-update.json` 连同产物上传到稳定 Release。Windows 的应用更新替换 EXE，随附 Core 可通过重新分发便携目录更新。

## 验证

```sh
go test ./...
go vet ./...
for test in scripts/test-*.mjs; do node "$test" || exit; done
```

Apple Silicon 真机可用 `./scripts/build-macos.sh --smoke-test --archive <zip>` 创建具有独立 Bundle ID 的测试应用。使用新数据目录和独立端口验证首次启动。Windows Job、WebView2 与托盘交互需要在 Windows 运行验证。Windows 的外链拦截使用 WebView2 页面导航事件，macOS 使用原生 WebKit 导航代理。
