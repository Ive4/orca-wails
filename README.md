# Orca-Wails

Orca-Tauri 的 Wails 3 / Go 桌面实现。托盘常驻，按需创建 Orca Web 窗口，关闭时销毁窗口。内核直接运行随包的 `orcad-server.js`，macOS 使用 Bun，Windows 使用 Node。

## Apple Silicon macOS

需要 Go 1.26+、macOS 13.5+ 和包含 Swift 6 的 Xcode Command Line Tools。打包 Core 还需要 Python 3、Node.js 和锁定版本的官方 Orca。可以从官方应用提取，也可以使用已经验证的 Core 包。

```sh
./scripts/build-macos.sh
open bin/Orca-Wails.app
```

使用已有的 Orca-Tauri 打包产物时，传入对应的 ZIP 和同名 JSON。也可以传 `--no-core` 只构建外壳。

```sh
./scripts/build-macos.sh --archive /path/to/bootstrap-core.zip
open bin/Orca-Wails.app
```

内核提取参数见 `python3 scripts/extract-macos-core.py --help`。本机可在登录钥匙串中保存专用代码签名身份，并将证书的 SHA-1 指纹（40 位十六进制字符，无冒号）写入 `~/Library/Application Support/Orca-Wails/signing-identity`。普通构建会自动复用该身份，macOS 可在更新后继续使用已有的钥匙串和隐私权限授权。从临时签名切换时，需要重新授权一次。

对外发布时，通过 `ORCA_WAILS_SIGN_IDENTITY` 指定 Developer ID 签名身份。单独提取供 `--archive` 使用的 Core 时，也设置此变量以签名随包的 Computer Use 组件。未配置签名身份时使用临时签名，也可通过 `ORCA_WAILS_SIGN_IDENTITY=-` 显式选择临时签名。

## Windows 11 x64

需要 Go 1.26+ 和 WebView2 Runtime。提取和验证 Core 需要 PowerShell 7.2+、7-Zip，步骤使用 `core-version.json` 锁定的安装包和 Node 版本。

```powershell
pwsh -File scripts/build-windows.ps1
.\bin\Orca-Wails.exe
```

如果已有 `scripts/extract-headless.ps1` 在 Windows 上生成并验证的 ZIP 和 `.sha256`，可用 `scripts/build-windows.ps1 -Archive <zip>` 生成带 Core 的 `bin/Orca-Wails/` 便携目录。分发该目录时同时保留引导 ZIP 和 JSON。

## 使用

- macOS 本机工作区可从 Orca CLI 使用 `orca tab create`、`orca snapshot`、`orca click` 和 `orca screenshot` 操作同一个内嵌浏览器。用 `--worktree` 和 `--page` 指定工作区及页面。
- macOS 会自动查找已安装的 Chrome 或 Chromium。也可以在 `config.json` 的 `environment` 中设置 `ORCA_BROWSER_EXECUTABLE` 指定可执行文件，重启后生效。工作区标签栏的「＋ → 新建浏览器标签页」会在 Wails 窗口内打开页面。
- 用 `orca tab create --worktree current --url https://example.com --json` 创建的 Chromium 页面会同步到当前工作区的标签栏。关闭 Wails 窗口后，Core 里的页面继续运行。重开窗口可以回到同一页，CLI 也可以用 `--page` 和原来的 `browserPageId` 继续操作。
- Chromium 页面支持键盘、纯文本粘贴和中文输入。点击「接管页面」会等正在执行的浏览器命令结束，随后暂停该页的 Agent 浏览器操作，直到点击「交还控制」。选取网页元素后可使用「Attach to AI」，把选取内容、截图和标注发送到选定的对话。
- macOS 本机工作区可从标签栏「+」打开移动模拟器，在面板中选取 iOS 设备。设备由已安装 Xcode 的 iOS Simulator Runtime 提供。
- macOS 本机插件可以通过 `contributes.statusBarItems` 添加状态栏项。启用并授权后，插件 worker 可调用 `orca.host.call('statusBar.update', …)` 更新自己声明的项。
- macOS 的 `orca computer` 默认使用 Cua 本机，可在「设置 → Orca Wails → 电脑操控」更改默认后端。Agent 可用 `--backend cua`、`--backend cua-vm` 或 `--backend orca` 为当次命令选择后端。使用前可运行 `orca computer capabilities --json` 检查连接。
- 左键点击托盘图标显示任务，右键打开原生菜单。选择任务进入对应会话。
- macOS 菜单栏和任务面板使用 Swift、AppKit 与 SwiftUI。猫标外的蓝环表示运行或监测，黄环表示等待输入，红环表示受阻。一种状态占满整圈并呼吸，两种或三种状态均分整圈并旋转。灰色虚线环表示正在读取或状态待确认，空闲时只显示猫标。悬停可查看运行、监测及待处理任务的计数。开启系统的「减少动态效果」后，色环和猫标静止显示。Windows 托盘用小圆点提示待处理任务，悬停可查看各类计数。
- macOS 任务面板优先居中于菜单栏猫标，靠近屏幕边缘时向内调整。点任务可查看近期消息、只读终端输出或结束结果。「处理」打开本地结构化待办，单题单选一点即发，多选和分组问题统一发送。一次性审批先展示具体操作，再确认。提交后会继续读取下一项，结果待确认时可核对原会话记录。点「＋」可选择本地工作区和 Agent，在后台启动任务，也可以进入 Bot 列表。
- macOS 任务详情打开时自动刷新，断线时保留上次记录。追加消息和后台启动可在面板内核对。结束任务可以沿用原工作区和 Agent 新建任务，也可以展开该工作区的未提交文件与只读 diff。「更多」提供工作区筛选和最近查看记录。
- macOS 的「设置 → Orca Wails → 桌面与任务入口」可分别开启菜单栏猫标和刘海面板。默认开启菜单栏猫标。刘海左侧的猫标在空闲时也可用，悬停或点击直接展开「全部任务」。有任务时，刘海右侧显示待处理数或运行数。两个入口关闭时，可从 Orca 的「会话 → 查看任务」打开。
- macOS 的任务开始、恢复、结束、等待输入和受阻各有专属提示音。菜单栏的「设置 → 通知」可逐个试听、开关声音、授权系统通知和发送测试通知。等待输入或受阻时的系统通知打开原生任务详情，任务恢复后自动清除。Windows 通过系统通知提醒这些状态变化，可用 `notifications` 配置项切换。
- macOS 默认全局快捷键为 **Command+Shift+O**，可在「设置 → Orca Wails → 桌面与任务入口」修改。Windows 默认为 **Ctrl+Shift+O**。关闭主窗口后，托盘与 Core 继续运行。
- 用 `--background` 启动时，Core 可在没有窗口的情况下运行。通过托盘设置切换开机启动、连接已配对服务器或在 macOS 导入官方 Orca 的项目与历史。导入需要匹配 Core 版本，原数据保存在同级备份目录。
- 默认数据目录为 `~/.orca-wails`。可用 `--data-dir` 或 `ORCA_WAILS_HOME` 指定其他绝对路径。首次运行创建 `config.json`。macOS 默认使用端口 16768，Windows 默认使用 6768。
- 配置格式见 [config.example.json](config.example.json)。可以查看应用、内核和官方最新版本。Core 首次安装与回退采用独立版本目录，候选 Core 完成启动、PTY 和 Web 配对检查后才会切换活动指针。Core 回退只切换二进制版本，数据格式由 Core 管理。

### 在 Lume 里测试 Orca Wails

使用 Cua 本机后端时，在 macOS「系统设置 → 隐私与安全性」为 Orca Wails 授予辅助功能和屏幕录制，授权后重新打开应用。使用 Orca 后端时，运行 `orca computer permissions --backend orca`，按提示授权随包的 Orca Computer Use。

在 Lume 中启动来宾并登录桌面，安装 Cua Driver。macOS 来宾可在桌面终端运行 `cua-driver permissions grant`，为来宾的驱动授予权限。Linux 来宾的 SSH 用户需能访问图形会话和辅助功能服务，可先在来宾桌面运行 `cua-driver doctor` 检查。 [Lume](https://github.com/trycua/lume) · [Cua Driver 0.56.4](https://github.com/trycua/cua/releases/tag/cua-driver-rs-v0.56.4)

在宿主的 `~/.ssh/config` 配置别名（例如 `orca-lume`），用已确认的来宾主机密钥和 SSH 密钥连接。来宾的非交互式 SSH 命令需要能找到 `cua-driver`。在「设置 → Orca Wails → 电脑操控」保存该 SSH 别名后，即使默认后端选了本机，开发 Agent 也可以这样操作来宾中的 Orca Wails。

```sh
orca computer capabilities --backend cua-vm --json
orca computer list-apps --backend cua-vm --json
orca computer get-app-state --backend cua-vm --app dev.orca-wails.desktop --json
```

同一任务有多条命令时，可以在该任务的 shell 中设置 `ORCA_COMPUTER_USE_BACKEND=cua-vm`。显式的 `--backend` 优先。截图、窗口 ID 和元素编号要从同一后端获取。Lume 连接失败时命令会报错。

来宾首次设置、Remote Login、SSH 主机密钥校验和 CUA 授权完成后，后续测试可以复用同一块 VM 磁盘。宿主只需保持 Orca Wails 运行，并配置好对应的 SSH 别名。

```sh
python3 scripts/verify-lume-desktop.py --vm orca --ssh orca \
  --app 'bin/Orca-Wails Smoke.app'
```

如果来宾在自定义目录，传入 `--storage /path/to/vms`。脚本按需启动 VM，等待来宾桌面和 CUA，部署测试包，使用独立 Profile 加载测试项目，并检查终端归属和命令输出。测试证据保存在 `dist-core/lume-desktop-smoke/`。

切换后端需要随此版本附带的 Core。回退到旧版 Core 时，电脑操控会停用，升级 Core 后可恢复。

## 跟进官方 Orca

当前版本、已验收范围、平台阻塞和后续任务见 [Orca 版本跟进规划](docs/orca-version-plan.md)。

在 macOS 从仓库根目录执行。查版只请求 GitHub 的发布元数据。准备时按稳定 tag 锁定源码，验证官方 macOS ZIP 的 SHA-256，检查 macOS Core 补丁能否应用，并在 `dist-core/follow/<tag>/` 生成两份候选锁文件。

```sh
python3 scripts/follow-orca.py
python3 scripts/follow-orca.py --prepare
# 重跑指定稳定版本时，加 --tag v1.4.268
```

下载默认保存在 `dist-core/upstream-cache/`。可以用 `ORCA_CORE_DOWNLOAD_CACHE` 指向跨工作区共用的目录。构建会校验并复用已有文件。macOS 官方 ZIP 可用 `aria2c` 分段续传，未安装时使用 `curl` 续传。源码、Bun、SQLite 和 Windows 提取所需的下载也会缓存。Windows 安装包在实际提取时下载。已有文件可用其锁定哈希作为文件名放入缓存。

用候选锁直接构建和验证。把下例的 tag 换成准备命令输出的版本。

```sh
dir=dist-core/follow/v1.4.268
python3 scripts/extract-macos-core.py --download-orca \
  --version-file "$dir/core-version.macos-arm64.json" --output "$dir/macos"
python3 scripts/verify-bun-macos.py \
  --version-file "$dir/core-version.macos-arm64.json" --core-dir "$dir/macos"
./scripts/build-macos.sh --smoke-test --archive "$dir/macos/bootstrap-core.zip"
```

在 Windows 11 x64 上把候选 `core-version.json` 传给 `scripts/extract-headless.ps1 -VersionFile <path>`。脚本会验证运行时、PTY 和 Job 后产出可供 `build-windows.ps1 -Archive` 使用的 Core ZIP。在 Windows 验证 WebView2 后，再更新候选锁的验证记录。

如果补丁检查失败，按输出的路径维护对应的 macOS Core 补丁，然后重跑准备命令。遇到上游 Node pin 变化时，核对上游的 `node-runtime-pin.ts`，在独立维护分支同步两份锁文件的运行时 pin 再重试。审阅并验证各平台的候选锁后，将其复制到仓库根目录的同名文件。macOS 导入只支持核实过的数据格式，检查 profile index、profile-state 和 agent-session-domain 的 schema 后，再更新 `internal/official/official.go` 中的版本门槛。

## 发布和更新

`update_repository` 配置为公开的 GitHub `owner/repo`。独立 Core 更新读取平台匹配的 Core Release ZIP 和 SHA-256。随包 Core 的 macOS 发行版通过更新应用带入新 Core。

应用更新走 Wails 签名机制。使用 `ORCA_WAILS_UPDATE_PUBLIC_KEY`（macOS）或 `-PublicKey`（Windows）在构建时写入 Base64 Ed25519 公钥，构建版本通过 `ORCA_WAILS_VERSION` 或 `-Version` 设置。通过 Wails 3 的 `wails3 updater manifest` 为 macOS 的应用 ZIP 或 Windows 的替换 EXE 生成签名清单，以 `wails-update.json` 连同产物上传到稳定 Release。Windows 的应用更新替换 EXE，随附 Core 可通过重新分发便携目录更新。

## 验证

```sh
go test ./...
go vet ./...
python3 scripts/test-follow-orca.py
for test in scripts/test-*.mjs; do node "$test" || exit; done
```

Apple Silicon 真机可用 `./scripts/build-macos.sh --smoke-test --archive <zip>` 创建具有独立 Bundle ID 的测试应用。运行 `python3 scripts/verify-bun-macos.py --core-dir <core 目录> --app 'bin/Orca-Wails Smoke.app'`，可在独立数据目录和端口验证 Core 首次启动及 Computer Use 握手。Windows Job、WebView2 与托盘交互需要在 Windows 运行验证。Windows 的外链拦截使用 WebView2 页面导航事件，macOS 使用原生 WebKit 导航代理。
