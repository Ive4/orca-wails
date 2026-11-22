import AppKit
import SwiftUI

struct TrayTask: Decodable, Equatable, Identifiable {
    enum Status: String {
        case waiting, blocked, working, monitoring, done, unknown

        var label: String {
            switch self {
            case .waiting: "等待输入"
            case .blocked: "受阻"
            case .working: "运行中"
            case .monitoring: "监测中"
            case .done: "已结束"
            case .unknown: "待确认"
            }
        }
    }

    struct OutputMetrics: Decodable, Equatable {
        let turnOutputTokens: UInt64?
        let visibleTokensPerSecond: UInt64?
        let visibleUntil: UInt64?
        let requestRate: Bool
        let toolActive: Bool

        private enum CodingKeys: String, CodingKey {
            case turnOutputTokens, visibleTokensPerSecond, visibleUntil, requestRate, toolActive
        }

        init(from decoder: any Decoder) throws {
            let values = try? decoder.container(keyedBy: CodingKeys.self)
            turnOutputTokens = try? values?.decode(UInt64.self, forKey: .turnOutputTokens)
            visibleTokensPerSecond = try? values?.decode(UInt64.self, forKey: .visibleTokensPerSecond)
            visibleUntil = try? values?.decode(UInt64.self, forKey: .visibleUntil)
            requestRate = (try? values?.decode(Bool.self, forKey: .requestRate)) == true
            toolActive = (try? values?.decode(Bool.self, forKey: .toolActive)) == true
        }
    }

    let id: String
    let title: String
    let workspace: String
    let status: String
    let startedAt: UInt64?
    let updatedAt: UInt64?
    let completedDurationSeconds: UInt64?
    let quickReply: Bool?
    var outcome: String?
    var launchKey: String?
    var provider: String?
    var workspaceKey: String?
    var currentTool: String?
    var outputMetrics: OutputMetrics?

    var taskStatus: Status { Status(rawValue: status) ?? .unknown }

    var statusLabel: String {
        guard taskStatus == .done else { return taskStatus.label }
        return switch outcome {
        case "success": "成功"
        case "failure": "失败"
        case "cancellation": "已取消"
        case "superseded": "已被替代"
        case "interruption": "已中断"
        case "unconfirmed": "结果待确认"
        default: "已结束"
        }
    }

    func completionTiming(now: Date = .now, calendar: Calendar = .current) -> (finishedAt: String?, duration: String?) {
        let duration = completedDurationSeconds.map { seconds in
            let parts = [(seconds / 3600, "小时"), (seconds / 60 % 60, "分"), (seconds % 60, "秒")]
            let time = parts.compactMap { value, unit in value == 0 ? nil : "\(value)\(unit)" }.joined()
            return "耗时 \(time.isEmpty ? "0秒" : time)"
        }
        // For a done row, Go sends the recorded finish or the start of the done state.
        guard let completion = taskDate(startedAt, now: now) else { return (nil, duration) }
        let parts = calendar.dateComponents([.month, .day, .hour, .minute], from: completion)
        let clock = String(format: "%02d:%02d", parts.hour ?? 0, parts.minute ?? 0)
        let prefix: String
        if calendar.isDate(completion, inSameDayAs: now) {
            prefix = ""
        } else if let yesterday = calendar.date(byAdding: .day, value: -1, to: now),
                  calendar.isDate(completion, inSameDayAs: yesterday) {
            prefix = "昨天 "
        } else {
            prefix = "\(parts.month ?? 0)月\(parts.day ?? 0)日 "
        }
        return ("\(prefix)\(clock) 结束", duration)
    }

    func activeTiming(now: Date = .now) -> (elapsedSince: Date?, updatedAt: Date?) {
        let start = taskDate(startedAt, now: now)
        let update = taskDate(updatedAt, now: now)
        if let start, let update, update.timeIntervalSince(start) < 1 {
            return (start, nil)
        }
        return (start, update)
    }

    var visibleOutputExpiry: Date? {
        guard taskStatus == .working, let metrics = outputMetrics,
              let rate = metrics.visibleTokensPerSecond, rate > 0,
              let until = metrics.visibleUntil, until > 0 else { return nil }
        return Date(timeIntervalSince1970: Double(until) / 1000)
    }

    func activeMetricLabel(now: Date) -> String? {
        guard taskStatus == .working else { return taskStatus == .monitoring ? currentTool : nil }
        let freshRate = visibleOutputExpiry.flatMap { now < $0 ? outputMetrics?.visibleTokensPerSecond : nil }
        if let freshRate, outputMetrics?.requestRate != true {
            return "≈ \(freshRate) token/s"
        }
        if outputMetrics?.toolActive == true, let currentTool {
            return currentTool
        }
        if let freshRate {
            return "上次 \(freshRate) token/s"
        }
        guard let tokens = outputMetrics?.turnOutputTokens, tokens > 0 else { return currentTool }
        let count = tokens.formatted(.number.notation(.compactName).precision(.fractionLength(0...1))
            .locale(Locale(identifier: "en_US"))).lowercased()
        return "本轮输出 \(count) tokens"
    }

    private func taskDate(_ milliseconds: UInt64?, now: Date) -> Date? {
        guard let milliseconds, milliseconds > 0 else { return nil }
        let time = Date(timeIntervalSince1970: Double(milliseconds) / 1000)
        return time <= now ? time : nil
    }
}

struct TrayComputerUseSettings: Codable, Equatable {
    enum Backend: String, Codable, CaseIterable {
        case cua
        case cuaVM = "cua-vm"
        case orca

        var label: String {
            switch self {
            case .cua: "Cua（本机）"
            case .cuaVM: "Cua（Lume 虚拟机）"
            case .orca: "Orca"
            }
        }
    }

    var backend: Backend = .cua
    var vmSSH = ""

    enum CodingKeys: String, CodingKey {
        case backend = "computer_use_backend"
        case vmSSH = "computer_use_vm_ssh"
    }
}

struct TraySnapshot: Decodable, Equatable {
    enum TaskScope { case all, current, recent }
    struct Entries: Decodable, Equatable {
        var menuBar = true
        var notch = false

        enum CodingKeys: String, CodingKey {
            case menuBar = "menu_bar"
            case notch
        }
    }

    struct Summary: Decodable, Equatable {
        let rows: [TrayTask]
        let partial: Bool
    }

    var summary: Summary?
    var entries: Entries?
    var computerUse: TrayComputerUseSettings?
    var wechat: TrayWeChatStatus?
    var globalShortcut: String?
    var taskState = "stopped"
    var canOpen = true
    var busy = false
    var notifications = true
    var autostart = false
    var windowOpen = false
    var localWindowReady = false
    var appVersion = ""
    var coreVersion = ""
    var officialVersion = ""
    var pendingApp = ""
    var pendingCore = ""
    var checkApp = false
    var checkCore = false
    var bundledCore = false
    var coreInstalled = false
    var rollbackAvailable = false
    var running = false
    var message = ""

    var tasks: [TrayTask] { summary?.rows ?? [] }
    var partial: Bool { summary?.partial ?? false }

    func taskRows(_ scope: TaskScope) -> [TrayTask] {
        switch scope {
        case .all: taskRows(recent: false) + taskRows(recent: true)
        case .current: taskRows(recent: false)
        case .recent: taskRows(recent: true)
        }
    }

    func taskRows(recent: Bool) -> [TrayTask] {
        let now = Date().timeIntervalSince1970 * 1000
        return tasks.filter { task in
            if !recent { return task.taskStatus != .done }
            guard task.taskStatus == .done, let time = task.startedAt ?? task.updatedAt else { return false }
            return Double(time) <= now && now - Double(time) <= 86_400_000
        }
    }
}

struct TrayLights: Equatable {
    var working = 0
    var monitoring = 0
    var waiting = 0
    var blocked = 0
    var unconfirmed = false

    var running: Int { working + monitoring }

    init(_ snapshot: TraySnapshot) {
        for task in snapshot.tasks {
            switch task.taskStatus {
            case .working: working += 1
            case .monitoring: monitoring += 1
            case .waiting: waiting += 1
            case .blocked: blocked += 1
            case .done, .unknown: break
            }
        }
        switch snapshot.taskState {
        case "loading", "unavailable", "offline":
            unconfirmed = true
        default:
            unconfirmed = running + waiting + blocked == 0 &&
                (snapshot.partial || snapshot.tasks.contains { $0.taskStatus == .unknown })
        }
    }

    func statusDescription(for snapshot: TraySnapshot) -> String {
        switch snapshot.taskState {
        case "loading": return "正在读取任务…"
        case "unavailable", "offline": return "暂时读不到任务状态"
        case "stopped": return "内核未运行"
        default: break
        }
        let counts: [(Int, TrayTask.Status)] = [(working, .working), (monitoring, .monitoring),
                                                 (waiting, .waiting), (blocked, .blocked)]
        var parts = counts.compactMap { count, status in count > 0 ? "\(count) 项\(status.label)" : nil }
        if parts.isEmpty { parts.append(unconfirmed ? "任务状态待确认" : "当前无活动任务") }
        if snapshot.partial { parts.append("部分工作区") }
        return parts.joined(separator: " · ")
    }
}

public typealias OrcaTrayCallback = @convention(c) @Sendable (UnsafePointer<CChar>, UnsafePointer<CChar>?) -> Void

@MainActor
final class Tray: NSObject, ObservableObject, NSWindowDelegate {
    static var shared: Tray?
    @Published private(set) var snapshot = TraySnapshot()
    @Published private(set) var interaction: TrayInteractionResult?
    @Published private(set) var notchAvailable = false
    enum LaunchState { case starting, complete(TrayLaunchReceipt), uncertain(String, TrayLaunchLookup?) }
    enum FollowUpState { case sending, recorded(String), uncertain(String, String?) }
    @Published private(set) var launchState: LaunchState?
    @Published private(set) var followUps: [String: FollowUpState] = [:]
    @Published private(set) var viewedTaskIDs = UserDefaults.standard.stringArray(forKey: "orca.tray.viewedTasks") ?? []
    var launchDraft = TrayLaunchInput(
        workspaceID: UserDefaults.standard.string(forKey: "orca.tray.lastLaunchWorkspace") ?? "",
        agent: UserDefaults.standard.string(forKey: "orca.tray.lastLaunchAgent") ?? ""
    )
    var followUpDrafts: [String: String] = [:]
    var entries: TraySnapshot.Entries { snapshot.entries ?? .init() }
    let notifications = Notifications()
    private let callback: OrcaTrayCallback
    private let pointerLocation: () -> NSPoint
    private let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
    private var notchLauncher: NotchLauncher?
    private var panel: NSPanel?
    private var notchHover = false
    private var notchTracking: Task<Void, Never>?
    private var placement: TrayWindow.Placement? {
        guard let panel else { return nil }
        return panel === notchLauncher ? .notch : .menuBar
    }
    private var managementWindow: NSWindow?
    private lazy var applicationMenu = DesktopMenu(tray: self)
    private var outsideClick: Any?
    private var localClick: Any?
    private let mark: NSImage?
    private var timer: Timer?
    private var ring = TrayRing()
    private var lights = TrayLights(TraySnapshot())
    private var lastFrame: TrayIcon.Frame?
    private var lastNotchFrame: TrayIcon.Frame?
    private var replyDrafts: [String: [String: [String: TrayQuestionDraft]]] = [:]
    struct ReplyCheck {
        let answer: TrayQuestionAnswer
        let startedAt: Int64
    }
    private(set) var replyChecks: [String: ReplyCheck] = [:]
    private var pendingReplies: [UUID: String] = [:]
    private var pendingFollowUps: [UUID: String] = [:]
    private var pendingLaunch: UUID?

    init(callback: @escaping OrcaTrayCallback, icon: Data,
         pointerLocation: @escaping () -> NSPoint = { NSEvent.mouseLocation }) {
        self.callback = callback
        self.pointerLocation = pointerLocation
        self.mark = NSImage(data: icon)
        super.init()
        notifications.onOpen = { [weak self] id in
            if let id, !id.isEmpty { self?.show(.detail, taskID: id) }
            else { self?.send("notification_open", id) }
        }
        if let button = statusItem.button {
            button.target = self
            button.action = #selector(toggle(_:))
            button.sendAction(on: [.leftMouseUp, .rightMouseUp])
        }
        NSWorkspace.shared.notificationCenter.addObserver(
            self, selector: #selector(displayOptionsChanged),
            name: NSWorkspace.accessibilityDisplayOptionsDidChangeNotification, object: nil
        )
        NotificationCenter.default.addObserver(
            self, selector: #selector(screenConfigurationChanged),
            name: NSApplication.didChangeScreenParametersNotification, object: nil
        )
        NSWorkspace.shared.notificationCenter.addObserver(
            self, selector: #selector(screenConfigurationChanged),
            name: NSWorkspace.activeSpaceDidChangeNotification, object: nil
        )
        syncNotch()
        drawIcon()
    }

    func apply(_ value: TraySnapshot) {
        applicationMenu.install()
        guard snapshot != value else { return }
        let previous = entries
        snapshot = value
        if previous.menuBar != entries.menuBar {
            statusItem.isVisible = entries.menuBar
            if !entries.menuBar && placement == .menuBar { close() }
        }
        if previous.notch != entries.notch { syncNotch() }
        lights = TrayLights(value)
        ring.update(lights, at: ProcessInfo.processInfo.systemUptime,
                    reduceMotion: NSWorkspace.shared.accessibilityDisplayShouldReduceMotion)
        notifications.update(enabled: value.notifications, attention: lights.waiting + lights.blocked)
        drawIcon()
    }

    func showManagement(_ page: TrayNavigation.Page = .settings, taskID: String? = nil) {
        close()
        let window: NSWindow
        if let existing = managementWindow {
            window = existing
        } else {
            window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 336, height: 300),
                              styleMask: [.titled, .closable, .miniaturizable], backing: .buffered, defer: false)
            window.identifier = NSUserInterfaceItemIdentifier("orca-management")
            window.title = "Orca-Wails"
            window.isReleasedWhenClosed = false
            window.delegate = self
            window.center()
            managementWindow = window
        }
        window.contentViewController = NSHostingController(rootView:
            TrayPanel(tray: self, page: page, management: true, taskID: taskID).background(.background))
        NSApp.setActivationPolicy(.regular)
        NSApp.activate(ignoringOtherApps: true)
        window.deminiaturize(nil)
        window.makeKeyAndOrderFront(nil)
    }

    func closeManagement() { managementWindow?.close() }

    func windowWillClose(_ notification: Notification) {
        managementWindow?.contentViewController = nil
        managementWindow = nil
        send("management_closed")
    }

    func send(_ action: String, _ argument: String? = nil, close: Bool = false) {
        if close { self.close() }
        action.withCString { key in
            if let argument { argument.withCString { callback(key, $0) } }
            else { callback(key, nil) }
        }
    }

    @objc private func toggle(_ button: NSStatusBarButton) {
        if NSApp.currentEvent?.type == .rightMouseUp {
            let menu = NSMenu()
            for (title, action, enabled) in [
                ("查看任务", "tasks", true),
                ("打开 Orca", "open", snapshot.canOpen),
                ("Orca Wails 设置…", "open_shell_settings", true),
            ] {
                if action == "open_shell_settings" { menu.addItem(.separator()) }
                let item = NSMenuItem(title: title, action: #selector(menuAction(_:)), keyEquivalent: "")
                item.target = self
                item.representedObject = action
                item.isEnabled = enabled
                menu.addItem(item)
            }
            menu.autoenablesItems = false
            close()
            menu.popUp(positioning: nil, at: NSPoint(x: 0, y: button.bounds.maxY), in: button)
        } else if placement == .menuBar {
            close()
        } else {
            show(from: .menuBar)
        }
    }

    @objc private func menuAction(_ item: NSMenuItem) {
        guard let action = item.representedObject as? String else { return }
        switch action {
        case "tasks": show(from: .menuBar)
        default: send(action, close: true)
        }
    }

    private func toggleNotch() {
        if placement != .notch { show(from: .notch) }
        else if notchHover { pinNotch() }
        else { close() }
    }

    private func selectNotchCount() {
        guard placement == .notch, !notchHover, snapshot.running, !snapshot.busy,
              let task = snapshot.tasks.first(where: { $0.quickReply == true }) else {
            if placement == .notch { pinNotch() }
            else { show(from: .notch) }
            return
        }
        show(.quickReply, from: .notch, question: task)
    }

    private func pinNotch() {
        notchTracking?.cancel()
        notchHover = false
        notchLauncher?.pin()
    }

    private func trackNotch(_ inside: Bool) {
        notchTracking?.cancel()
        guard let launcher = notchLauncher, inside ? panel == nil : notchHover else { return }
        notchTracking = Task { @MainActor [weak self, weak launcher] in
            do { try await Task.sleep(for: .milliseconds(inside ? 170 : 130)) } catch { return }
            guard let self, let launcher, notchLauncher === launcher, launcher.isVisible,
                  launcher.frame.contains(pointerLocation()) == inside else { return }
            if inside, panel == nil { show(from: .notch, hovering: true) }
            if !inside, notchHover, launcher.attachedSheet == nil { close() }
        }
    }

    private var menuBarLocation: (NSRect, NSScreen)? {
        guard entries.menuBar, statusItem.isVisible,
              let button = statusItem.button, let window = button.window,
              let screen = window.screen else { return nil }
        let anchor = window.convertToScreen(button.convert(button.bounds, to: nil))
        return screen.frame.intersects(anchor) ? (anchor, screen) : nil
    }

    func show(_ page: TrayNavigation.Page = .tasks, from requested: TrayWindow.Placement? = nil,
              hovering: Bool = false, question: TrayTask? = nil, taskID: String? = nil) {
        let target = requested ?? placement ?? (menuBarLocation != nil ? .menuBar : .notch)
        if page == .tasks && placement == target {
            if notchHover && !hovering { pinNotch() }
            return
        }
        switch target {
        case .notch:
            guard let launcher = notchLauncher else {
                if requested == nil { showManagement(page, taskID: taskID) }
                return
            }
            if panel !== launcher { close() }
            panel = launcher
            notchHover = hovering
            launcher.expand(TrayPanel(tray: self, page: page, notchWidth: launcher.geometry.panelWidth,
                                      question: question, taskID: taskID), key: !hovering)
        case .menuBar:
            guard let (anchor, screen) = menuBarLocation else {
                if requested == nil { showManagement(page, taskID: taskID) }
                return
            }
            close()
            let window = TrayWindow(anchor: anchor, screen: screen,
                                    content: TrayPanel(tray: self, page: page, question: question, taskID: taskID))
            panel = window
            window.onClose = { [weak self] in self?.close() }
            window.present()
        }
        monitorPanelClicks()
        send("refresh")
    }

    private func monitorPanelClicks() {
        guard localClick == nil else { return }
        localClick = NSEvent.addLocalMonitorForEvents(matching: [.leftMouseDown, .rightMouseDown]) { [weak self] event in
            if let self, notchHover, let launcher = notchLauncher, event.window === launcher,
               !launcher.hitControl(event.locationInWindow) { pinNotch() }
            if event.window !== self?.panel && event.window !== self?.statusItem.button?.window &&
                event.window !== self?.notchLauncher &&
                self?.panel?.attachedSheet == nil {
                self?.close()
            }
            return event
        }
        outsideClick = NSEvent.addGlobalMonitorForEvents(matching: [.leftMouseDown, .rightMouseDown]) { [weak self] _ in
            Task { @MainActor in
                if self?.panel?.attachedSheet == nil { self?.close() }
            }
        }
    }

    func request(_ action: String, id: UUID, taskID: String? = nil, botID: String? = nil,
                 answer: TrayQuestionAnswer? = nil, since: Int64? = nil, text: String? = nil,
                 launch: TrayLaunchInput? = nil, launchCheck: TrayLaunchLookup? = nil,
                 operationID: String? = nil, change: TrayChange? = nil) {
        let request = TrayInteractionRequest(requestID: id, taskID: taskID, botID: botID, answer: answer,
                                             since: since, text: text, launch: launch, launchCheck: launchCheck,
                                             operationID: operationID, file: change?.path, area: change?.area)
        guard let data = try? JSONEncoder().encode(request) else { return }
        switch action {
        case "answer_question":
            if let taskID, let answer {
                pendingReplies[id] = taskID
                replyChecks[taskID] = .init(answer: answer, startedAt: Int64(Date.now.timeIntervalSince1970 * 1000))
            }
        case "send_follow_up":
            if let taskID {
                pendingFollowUps[id] = taskID
                followUps[taskID] = .sending
            }
        case "check_follow_up":
            if let taskID { pendingFollowUps[id] = taskID }
        case "launch_task":
            pendingLaunch = id
            launchState = .starting
            if let launch { launchDraft = launch }
        case "check_launch":
            pendingLaunch = id
        default:
            break
        }
        send(action, String(decoding: data, as: UTF8.self))
    }

    func receive(_ value: TrayInteractionResult) {
        if pendingLaunch == value.requestID {
            pendingLaunch = nil
            if let receipt = value.launch {
                launchState = .complete(receipt)
                UserDefaults.standard.set(launchDraft.workspaceID, forKey: "orca.tray.lastLaunchWorkspace")
                UserDefaults.standard.set(launchDraft.agent, forKey: "orca.tray.lastLaunchAgent")
                if receipt.prompt == "journaled" || receipt.prompt == "handed-to-terminal" { launchDraft.text = "" }
            } else if value.uncertain == true {
                launchState = .uncertain(value.message ?? "启动结果待确认", value.launchCheck)
            } else if case .uncertain(let message, let lookup) = launchState {
                launchState = .uncertain(value.message ?? message, lookup)
            } else {
                launchState = nil
            }
        }
        if let taskID = pendingFollowUps.removeValue(forKey: value.requestID) {
            if let receipt = value.receipt {
                followUpDrafts[taskID] = nil
                followUps[taskID] = .recorded(receipt)
            } else if value.uncertain == true {
                followUps[taskID] = .uncertain(value.message ?? "后续消息结果待确认", value.operationID)
            } else if case .uncertain(let message, let operationID) = followUps[taskID] {
                followUps[taskID] = .uncertain(value.message ?? message, operationID)
            } else {
                followUps[taskID] = nil
            }
        }
        if let taskID = pendingReplies.removeValue(forKey: value.requestID), value.uncertain != true {
            replyChecks[taskID] = nil
        }
        if let taskID = value.repliedTaskID, let questionID = value.repliedQuestionID {
            replyChecks[taskID] = nil
            replyDrafts[taskID]?[questionID] = nil
            ring.confirmReply(at: ProcessInfo.processInfo.systemUptime)
            drawIcon()
        }
        if let taskID = value.settledTaskID, let questionID = value.settledQuestionID {
            replyChecks[taskID] = nil
            replyDrafts[taskID]?[questionID] = nil
        }
        interaction = value
    }

    func replyInFlight(_ taskID: String) -> Bool { pendingReplies.values.contains(taskID) }

    func recordTaskView(_ id: String) {
        guard viewedTaskIDs.first != id else { return }
        viewedTaskIDs = Array(([id] + viewedTaskIDs.filter { $0 != id }).prefix(12))
        UserDefaults.standard.set(viewedTaskIDs, forKey: "orca.tray.viewedTasks")
    }

    func canSubmit(_ action: String, taskID: String?) -> Bool {
        switch action {
        case "launch_task":
            return launchState == nil
        case "check_launch":
            return pendingLaunch == nil
        case "answer_question":
            return taskID.map { replyChecks[$0] == nil } ?? false
        case "send_follow_up":
            guard let taskID else { return false }
            switch followUps[taskID] {
            case .sending, .uncertain: return false
            default: return true
            }
        case "check_follow_up":
            return taskID.map { !pendingFollowUps.values.contains($0) } ?? false
        default:
            return true
        }
    }

    func beginAnotherLaunch() {
        guard pendingLaunch == nil else { return }
        launchDraft.text = ""
        launchState = nil
    }

    func drafts(taskID: String, questionID: String) -> [String: TrayQuestionDraft] {
        replyDrafts[taskID]?[questionID] ?? [:]
    }

    func storeDrafts(_ drafts: [String: TrayQuestionDraft], taskID: String, questionID: String) {
        replyDrafts[taskID, default: [:]][questionID] = drafts
    }

    func close() {
        if let monitor = localClick { NSEvent.removeMonitor(monitor) }
        if let monitor = outsideClick { NSEvent.removeMonitor(monitor) }
        localClick = nil
        outsideClick = nil
        notchTracking?.cancel()
        notchTracking = nil
        notchHover = false
        if placement == .notch { notchLauncher?.collapse() }
        else { panel?.orderOut(nil) }
        panel = nil
    }

    @objc nonisolated private func displayOptionsChanged() {
        Task { @MainActor [weak self] in
            self?.notchLauncher?.finishAnimation()
            self?.drawIcon()
        }
    }

    @objc nonisolated private func screenConfigurationChanged() {
        Task { @MainActor [weak self] in
            guard let self else { return }
            close()
            syncNotch()
            drawIcon()
        }
    }

    private func syncNotch() {
        let geometry = NSScreen.screens.lazy.compactMap { NotchGeometry(screen: $0) }.first
        if notchAvailable != (geometry != nil) { notchAvailable = geometry != nil }
        guard entries.notch, let geometry else {
            if placement == .notch { close() }
            notchLauncher?.orderOut(nil)
            notchLauncher = nil
            lastNotchFrame = nil
            return
        }
        if notchLauncher?.geometry == geometry { return }
        if placement == .notch { close() }
        notchLauncher?.orderOut(nil)
        let launcher = NotchLauncher(geometry: geometry)
        launcher.onToggle = { [weak self] in self?.toggleNotch() }
        launcher.onCount = { [weak self] in self?.selectNotchCount() }
        launcher.onHover = { [weak self] inside in self?.trackNotch(inside) }
        launcher.onClose = { [weak self] in self?.close() }
        notchLauncher = launcher
        lastNotchFrame = nil
        launcher.orderFrontRegardless()
    }

    private func scheduleFrames(every interval: TimeInterval) {
        guard timer?.timeInterval != interval else { return }
        timer?.invalidate()
        let timer = Timer(timeInterval: interval, repeats: true) { [weak self] timer in
            guard let self else { timer.invalidate(); return }
            MainActor.assumeIsolated { self.drawIcon() }
        }
        timer.tolerance = interval * 0.1
        RunLoop.main.add(timer, forMode: .common)
        self.timer = timer
    }

    private func drawIcon() {
        let button = entries.menuBar && statusItem.isVisible ? statusItem.button : nil
        let launcher = notchLauncher
        guard button != nil || launcher != nil else {
            if entries.menuBar { scheduleFrames(every: 1) }
            else {
                timer?.invalidate()
                timer = nil
            }
            return
        }
        let time = ProcessInfo.processInfo.systemUptime
        let reduceMotion = NSWorkspace.shared.accessibilityDisplayShouldReduceMotion
        scheduleFrames(every: ring.frameInterval(at: time, reduceMotion: reduceMotion))
        let ringFrame = ring.frame(at: time, reduceMotion: reduceMotion)
        let text = lights.statusDescription(for: snapshot)
        if let button {
            let dark = button.effectiveAppearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua
            let frame = TrayIcon.Frame(dark: dark, ring: ringFrame)
            if lastFrame != frame {
                lastFrame = frame
                button.image = TrayIcon.image(mark, frame: frame)
            }
            if button.toolTip != text {
                button.toolTip = text
                button.setAccessibilityLabel("Orca · \(text)")
            }
        }
        if let launcher {
            let frame = TrayIcon.Frame(dark: true, ring: ringFrame)
            if lastNotchFrame != frame {
                lastNotchFrame = frame
                launcher.button.image = lastFrame == frame ? button?.image ?? TrayIcon.image(mark, frame: frame) :
                    TrayIcon.image(mark, frame: frame)
            }
            launcher.updateLabel(lights: lights, summary: text)
        }
    }
}

@_cdecl("orca_tray_show_management")
public func showManagement() {
    MainActor.assumeIsolated { Tray.shared?.showManagement() }
}

private struct Notice: Decodable {
    let title: String
    let body: String
    let taskID: String?
    let group: String
}

@_cdecl("orca_tray_start")
public func start(_ callback: OrcaTrayCallback, _ bytes: UnsafePointer<UInt8>, _ length: Int) -> Bool {
    guard Thread.isMainThread, Bundle.main.bundleURL.pathExtension == "app",
          Bundle.main.bundleIdentifier != nil else { return false }
    let icon = Data(bytes: bytes, count: length)
    MainActor.assumeIsolated {
        autoreleasepool {
            _ = NSApplication.shared
            Tray.shared = Tray(callback: callback, icon: icon)
        }
    }
    return true
}

@_cdecl("orca_tray_update")
public func update(_ bytes: UnsafePointer<UInt8>, _ length: Int) {
    let data = Data(bytes: bytes, count: length)
    guard let value = try? JSONDecoder().decode(TraySnapshot.self, from: data) else { return }
    MainActor.assumeIsolated { Tray.shared?.apply(value) }
}

@_cdecl("orca_tray_interaction")
public func receiveInteraction(_ bytes: UnsafePointer<UInt8>, _ length: Int) {
    let data = Data(bytes: bytes, count: length)
    guard let value = try? JSONDecoder().decode(TrayInteractionResult.self, from: data) else { return }
    MainActor.assumeIsolated { Tray.shared?.receive(value) }
}

@_cdecl("orca_tray_notify")
public func notify(_ bytes: UnsafePointer<UInt8>, _ length: Int) {
    let data = Data(bytes: bytes, count: length)
    guard let value = try? JSONDecoder().decode(Notice.self, from: data) else { return }
    MainActor.assumeIsolated {
        Tray.shared?.notifications.send(title: value.title, body: value.body, taskID: value.taskID, group: value.group)
    }
}

@_cdecl("orca_tray_clear")
public func clear(_ bytes: UnsafePointer<UInt8>, _ length: Int) {
    let data = Data(bytes: bytes, count: length)
    guard let ids = try? JSONDecoder().decode([String].self, from: data) else { return }
    MainActor.assumeIsolated { Tray.shared?.notifications.clear(ids) }
}

@_cdecl("orca_tray_play_task_sounds")
public func playTaskSounds(_ bytes: UnsafePointer<UInt8>, _ length: Int) {
    let data = Data(bytes: bytes, count: length)
    guard let cues = try? JSONDecoder().decode([Notifications.TaskSound].self, from: data) else { return }
    MainActor.assumeIsolated { Tray.shared?.notifications.playTaskSounds(cues) }
}
