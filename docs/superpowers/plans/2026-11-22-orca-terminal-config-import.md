# Orca-Wails 终端配置导入实施计划

> 按 Superpowers 的 `executing-plans` 或 `subagent-driven-development` 逐项执行，用复选框记录实测结果。

**目标** 在 macOS 本机 Wails 提供 Ghostty 和 Kitty 单文件的选择、预览及应用。

**实现** 将上游可直接在 Web 运行的解析、映射和差异计算移到共享层。已有终端配置弹窗负责选择和预览，确认后继续走现有的设置保存流程。

**环境** Orca `v1.4.268`、TypeScript、React、WebKit、Vitest、macOS ARM64。

**设计** [终端配置导入首版设计](../specs/2026-11-22-orca-terminal-config-import-design.md) · [桌面能力清单 01A](2026-11-22-orca-wails-desktop-capabilities.md#01-配置导入)

## 范围和验证

- 本次交付限定 macOS 本机 Wails。官方 Electron 端继续使用原有 IPC，普通 Web 和已配对的远程环境隐藏 Wails 本机入口。
- 读取用户选中的 UTF-8 单文件，上限 1,000,000 字节。带路径引用、命令和环境变量的条目只列出键名。先预览，再确认写入。
- 源码路径 `src/…` 指锁定的上游 Orca 源码。提交到本仓库的改动是补丁及构建接入，构建使用两份脚本当前统一的补丁顺序。
- 分别验证相同设置、取消文件选择、超限或读失败、已关闭弹窗的迟到结果、切换到配对环境和保存失败。保存失败保留可重试的预览。

## 任务 1 — 单文件预览

上游主要涉及 `src/main/ghostty/{parser,mapper,numeric-config-values}.ts`、`src/main/kitty/{parser,mapper}.ts` 和 `src/main/terminal-config-import/{settings-changes,config-file-reader}.ts`。把纯函数移到 `src/shared/terminal/appearance/config-import/`，让原上游桌面导入和 Wails 共用。Kitty 的递归读取保留在主进程解析模块，复用共享的 `parseKittyLines`。

- [ ] 新建 `src/shared/terminal/appearance/config-import/preview-terminal-config-text.test.ts`。用 `getDefaultSettings('')` 构造当前设置，并将字号设为 12。用以下断言固定接口。

  ```ts
  const ghostty = previewTerminalConfigText(
    'ghostty',
    'config',
    'font-size = 16\nbackground = #112233\nconfig-file = other.conf',
    settings,
    true
  )
  expect(ghostty.diff).toMatchObject({
    terminalFontSize: 16,
    terminalColorOverrides: { background: '#112233' }
  })
  expect(ghostty.unsupportedKeys).toContain('config-file')

  const kitty = previewTerminalConfigText(
    'kitty',
    'kitty.conf',
    'font_size 17\ninclude another.conf\ngeninclude echo',
    settings,
    true
  )
  expect(kitty.diff).toEqual({ terminalFontSize: 17 })
  expect(kitty.unsupportedKeys).toEqual(expect.arrayContaining(['include', 'geninclude']))
  expect(previewTerminalConfigText('ghostty', 'config', 'font-size = 12', settings, true).diff)
    .toEqual({})
  expect(settings.terminalFontSize).toBe(12)
  ```

- [ ] 在上游工作目录运行 `pnpm exec vitest run --config config/vitest.config.ts src/shared/terminal/appearance/config-import/preview-terminal-config-text.test.ts`。预期首次失败在缺少实现。
- [ ] 实现 `previewTerminalConfigText(format: TerminalConfigImportFormat, filename: string, content: string, currentSettings: GlobalSettings, isMacOS: boolean): TerminalConfigImportPreview`，放在同目录的 `preview-terminal-config-text.ts`。两种格式共用 `settingsChanges`。首版将 Kitty 的 `include`、`globinclude`、`geninclude`、`envinclude` 和 Ghostty 的 `config-file`、`theme` 列入未支持项。传入平台参数，确保浏览器执行路径无需 Node 或 Electron 运行时。
- [ ] 重跑该测试及 `src/main/ghostty/index.test.ts`。预期 Ghostty、Kitty 单文件预览通过新增用例，官方桌面的 Ghostty 发现和预览通过原有用例。

## 任务 2 — 设置页、文件选择及状态

修改上游的 `src/renderer/src/components/settings/terminal/configuration/{useTerminalConfigImport.ts,TerminalConfigImportModal.tsx}`、`terminal/appearance/TerminalAppearanceSection.tsx`、`terminal/terminal-search.ts`、`appearance/appearance-search.ts` 和设置导航。复用 `src/renderer/src/runtime/hosts/display/wails-local-runtime.ts` 的 `getWailsLocalRuntimeId()`。导航搜索和设置页使用相同的本机判定，只为 Ghostty、Kitty 增加入口。

- [ ] 新建 `src/renderer/src/components/settings/terminal/configuration/terminal-config-import-wails.test.tsx`。默认字号 12，使用可控制的 `updateSettings`。选择内容 `font_size 17` 后保存调用次数为零，点击应用后收到 `{ terminalFontSize: 17 }`，写入完成前没有成功状态。补上取消再选、1,000,001 字节、读取拒绝、关闭后返回结果、切到配对环境后应用、设置写入拒绝的用例。用可见入口和搜索结果验证本机、普通 Web、配对环境三种状态。
- [ ] 运行 `pnpm exec vitest run --config config/vitest.config.ts src/renderer/src/components/settings/terminal/configuration/terminal-config-import-wails.test.tsx`。预期首次在 Wails 入口或选中本地文件的处理尚未接入处失败。
- [ ] Wails 本机弹窗打开时进入文件待选状态，复用「选择配置文件」触发单选文件输入。收到 `File` 后先检查 `size` 再读内容，调用任务 1 的预览。取消时保留上一份预览。复用请求序号，关联发起时的本机运行时 ID，并在异步结果返回与应用前确认上下文。其他界面的导入按钮与搜索结果沿用原行为。
- [ ] 重跑任务 1 和 2 的测试，再执行 `pnpm run build:web`。预期用例全部通过，打包后的浏览器模块可加载。

## 任务 3 — 打包和 macOS 验收

- [ ] 将已验证的上游差异存为 `scripts/macos-terminal-config-import.patch`，在 `scripts/extract-macos-core.py` 与 `scripts/follow-orca.py` 的补丁列表同步登记，置于现有补丁之后。
- [ ] 执行 `python3 scripts/test-follow-orca.py` 和 `python3 scripts/follow-orca.py --tag v1.4.268 --prepare`。预期测试通过，准备脚本报告所有补丁可应用。
- [ ] 用临时产物目录打包，通过 Core 和 WebKit 的已有冒烟流程。

  ```sh
  orca_config_artifacts=$(mktemp -d /tmp/orca-wails-config.XXXXXX)
  python3 scripts/extract-macos-core.py --download-orca --output "$orca_config_artifacts"
  ./scripts/build-macos.sh --smoke-test --archive "$orca_config_artifacts/bootstrap-core.zip"
  python3 scripts/verify-bun-macos.py \
    --core-dir "$orca_config_artifacts" --app 'bin/Orca-Wails Smoke.app'
  ```

- [ ] 在独立测试数据目录和端口打开冒烟应用，走设计中的 Ghostty、Kitty、取消、超限、配对和重启用例。确认默认数据目录中的现有配置没有被测试写入。在总清单的 01A 记录生效的平台和版本，以及自动发现、主题和 Windows 仍由 01B、01C 验收。
