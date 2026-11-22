import AppKit

@MainActor
final class DesktopMenu: NSObject, NSMenuDelegate, NSMenuItemValidation {
    private weak var tray: Tray?
    private let root = NSMenu()
    private let sessions = NSMenu(title: "会话")

    init(tray: Tray) {
        self.tray = tray
        super.init()
        let app = NSMenu(title: "Orca-Wails")
        app.addItem(item("关于 Orca-Wails", "about"))
        app.addItem(.separator())
        app.addItem(item("Orca Wails 设置…", "open_shell_settings"))
        app.addItem(.separator())
        app.addItem(NSMenuItem(title: "隐藏 Orca-Wails", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h"))
        let hideOthers = NSMenuItem(title: "隐藏其他", action: #selector(NSApplication.hideOtherApplications(_:)), keyEquivalent: "h")
        hideOthers.keyEquivalentModifierMask = [.command, .option]
        app.addItem(hideOthers)
        app.addItem(NSMenuItem(title: "全部显示", action: #selector(NSApplication.unhideAllApplications(_:)), keyEquivalent: ""))
        app.addItem(.separator())
        app.addItem(item("退出 Orca-Wails", "quit", key: "q"))
        append(app)

        let edit = NSMenu(title: "编辑")
        edit.addItem(NSMenuItem(title: "撤销", action: Selector(("undo:")), keyEquivalent: "z"))
        let redo = NSMenuItem(title: "重做", action: Selector(("redo:")), keyEquivalent: "z")
        redo.keyEquivalentModifierMask = [.command, .shift]
        edit.addItem(redo)
        edit.addItem(.separator())
        for (title, selector, key) in [("剪切", "cut:", "x"), ("复制", "copy:", "c"), ("粘贴", "paste:", "v"), ("全选", "selectAll:", "a")] {
            edit.addItem(NSMenuItem(title: title, action: Selector(selector), keyEquivalent: key))
        }
        append(edit)

        let workspace = NSMenu(title: "工作区")
        workspace.addItem(item("快速搜索…", "open_workspace_search"))
        workspace.addItem(item("新建工作区…", "open_new_workspace"))
        workspace.addItem(.separator())
        workspace.addItem(item("连接官方 Orca…", "open_paired"))
        workspace.addItem(item("从官方 Orca 导入…", "import_official"))
        append(workspace)

        sessions.delegate = self
        append(sessions)
        menuNeedsUpdate(sessions)

        let window = NSMenu(title: "窗口")
        window.addItem(item("最小化", "minimise_orca", key: "m"))
        window.addItem(item("进入或退出全屏", "fullscreen_orca", key: "f", modifiers: [.command, .control]))
        window.addItem(item("关闭 Orca 窗口", "close_orca", key: "w", modifiers: [.command, .shift]))
        window.addItem(.separator())
        window.addItem(item("打开 Orca", "open"))
        append(window)

        let help = NSMenu(title: "帮助")
        help.addItem(item("打开配置文件", "config"))
        help.addItem(item("打开日志目录", "logs"))
        help.addItem(item("打开数据目录", "data"))
        append(help)
    }

    func install() {
        if NSApp.mainMenu !== root { NSApp.mainMenu = root }
    }

    func menuNeedsUpdate(_ menu: NSMenu) {
        guard menu === sessions, let tray else { return }
        let state = tray.snapshot
        menu.removeAllItems()
        let attention = state.tasks.filter { $0.taskStatus == .waiting || $0.taskStatus == .blocked }
        if state.taskState == "unavailable" {
            menu.addItem(NSMenuItem(title: "暂时读不到任务状态", action: nil, keyEquivalent: ""))
            menu.addItem(.separator())
        } else if !attention.isEmpty || state.partial {
            for task in attention.prefix(3) {
                menu.addItem(taskItem(task, prefix: task.taskStatus.label + " · "))
            }
            if state.partial {
                menu.addItem(NSMenuItem(title: "仅显示可读取的工作区", action: nil, keyEquivalent: ""))
            }
            menu.addItem(.separator())
        }
        menu.addItem(item("查看任务…", "tasks"))
        menu.addItem(item("新建 Bot 对话…", "pick_bot", key: "t", modifiers: [.command, .option]))
        let recent = state.taskRows(recent: true)
        if !recent.isEmpty {
            let finished = NSMenu(title: "最近结束")
            for task in recent.prefix(5) { finished.addItem(taskItem(task)) }
            append(finished, to: menu)
        }
        menu.addItem(item("打开机器人", "open_bots"))
    }

    private func append(_ menu: NSMenu, to parent: NSMenu? = nil) {
        let branch = NSMenuItem(title: menu.title, action: nil, keyEquivalent: "")
        branch.submenu = menu
        (parent ?? root).addItem(branch)
    }

    private func item(_ title: String, _ action: String, key: String = "",
                      modifiers: NSEvent.ModifierFlags = .command) -> NSMenuItem {
        let item = NSMenuItem(title: title, action: #selector(performCommand(_:)), keyEquivalent: key)
        item.target = self
        item.representedObject = action
        item.keyEquivalentModifierMask = modifiers
        return item
    }

    private func taskItem(_ task: TrayTask, prefix: String = "") -> NSMenuItem {
        var title = prefix + String(task.title.prefix(46))
        if task.title.count > 46 { title += "…" }
        if !task.workspace.isEmpty { title += " · " + String(task.workspace.prefix(18)) }
        let item = NSMenuItem(title: title, action: #selector(openTask(_:)), keyEquivalent: "")
        item.target = self
        item.representedObject = task.id
        item.toolTip = task.title + (task.workspace.isEmpty ? "" : " · " + task.workspace)
        return item
    }

    func validateMenuItem(_ item: NSMenuItem) -> Bool {
        guard let state = tray?.snapshot else { return false }
        if item.action == #selector(openTask(_:)) { return state.canOpen }
        switch item.representedObject as? String {
        case "open", "open_bots": return state.canOpen
        case "pick_bot": return state.running && !state.busy
        case "open_new_workspace", "open_workspace_search": return state.localWindowReady
        case "open_paired", "import_official": return !state.busy
        case "close_orca", "minimise_orca", "fullscreen_orca": return state.windowOpen
        default: return true
        }
    }

    @objc private func openTask(_ item: NSMenuItem) {
        guard let id = item.representedObject as? String else { return }
        tray?.send("open_task", id, close: true)
    }

    @objc private func performCommand(_ sender: NSMenuItem) {
        guard let tray, let action = sender.representedObject as? String else { return }
        switch action {
        case "tasks": tray.show()
        case "pick_bot": tray.show(.bots)
        case "about":
            let state = tray.snapshot
            NSApp.orderFrontStandardAboutPanel(options: [
                .applicationName: "Orca-Wails",
                .applicationVersion: state.appVersion,
                .credits: NSAttributedString(string: "Orca Core  " + (state.coreVersion.isEmpty ? "未安装" : state.coreVersion)),
            ])
        case "import_official":
            confirm(sender.title, message: "会先备份原数据并重启内核。正在运行的任务会结束。", button: "导入", action: action)
        case "quit":
            if tray.snapshot.taskRows(recent: false).isEmpty && !tray.snapshot.busy {
                tray.send(action)
            } else {
                confirm(sender.title, message: "退出会停止后台运行的任务。", button: "退出", action: action)
            }
        default: tray.send(action, close: true)
        }
    }

    private func confirm(_ title: String, message: String, button: String, action: String) {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = title
        alert.informativeText = message
        let cancel = alert.addButton(withTitle: "取消")
        cancel.keyEquivalent = "\r"
        let proceed = alert.addButton(withTitle: button)
        proceed.keyEquivalent = ""
        if alert.runModal() == .alertSecondButtonReturn { tray?.send(action) }
    }
}
