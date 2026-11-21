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

    let id: String
    let title: String
    let workspace: String
    let status: String
    let startedAt: UInt64?
    let updatedAt: UInt64?
    let quickReply: Bool?

    var taskStatus: Status { Status(rawValue: status) ?? .unknown }
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
    var running = 0
    var waiting = 0
    var blocked = 0
    var unconfirmed = false

    init(_ snapshot: TraySnapshot) {
        for task in snapshot.tasks {
            switch task.status {
            case "working", "monitoring": running += 1
            case "waiting": waiting += 1
            case "blocked": blocked += 1
            default: break
            }
        }
        unconfirmed = snapshot.taskState == "unavailable" || (running + waiting + blocked == 0 &&
            (snapshot.partial || snapshot.tasks.contains { $0.status == "unknown" }))
    }
}

public typealias OrcaTrayCallback = @convention(c) @Sendable (UnsafePointer<CChar>, UnsafePointer<CChar>?) -> Void

@MainActor
final class Tray: NSObject, ObservableObject, NSWindowDelegate {
    static var shared: Tray?
    @Published private(set) var snapshot = TraySnapshot()
    @Published private(set) var interaction: TrayInteractionResult?
    @Published private(set) var notchAvailable = false
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
    private var replyDrafts: [String: [String: String]] = [:]

    init(callback: @escaping OrcaTrayCallback, icon: Data,
         pointerLocation: @escaping () -> NSPoint = { NSEvent.mouseLocation }) {
        self.callback = callback
        self.pointerLocation = pointerLocation
        self.mark = NSImage(data: icon)
        super.init()
        notifications.onOpen = { [weak self] id in self?.send("notification_open", id) }
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

    func showManagement(_ page: TrayNavigation.Page = .settings) {
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
        window.contentViewController = NSHostingController(rootView: TrayPanel(tray: self, page: page, management: true).background(.background))
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
                ("桌面设置…", "management", true),
            ] {
                if action == "management" { menu.addItem(.separator()) }
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
        case "management": showManagement()
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
              let task = snapshot.tasks.first(where: { $0.taskStatus == .waiting && $0.quickReply == true }) else {
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
              hovering: Bool = false, question: TrayTask? = nil) {
        let target = requested ?? placement ?? (menuBarLocation != nil ? .menuBar : .notch)
        if page == .tasks && placement == target {
            if notchHover && !hovering { pinNotch() }
            return
        }
        switch target {
        case .notch:
            guard let launcher = notchLauncher else {
                if requested == nil { showManagement(page) }
                return
            }
            if panel !== launcher { close() }
            panel = launcher
            notchHover = hovering
            launcher.expand(TrayPanel(tray: self, page: page, notchWidth: launcher.geometry.panelWidth,
                                      question: question), key: !hovering)
        case .menuBar:
            guard let (anchor, screen) = menuBarLocation else {
                if requested == nil { showManagement(page) }
                return
            }
            close()
            let window = TrayWindow(anchor: anchor, screen: screen, content: TrayPanel(tray: self, page: page))
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
                 answer: TrayQuestionAnswer? = nil) {
        let request = TrayInteractionRequest(requestID: id, taskID: taskID, botID: botID, answer: answer)
        guard let data = try? JSONEncoder().encode(request) else { return }
        send(action, String(decoding: data, as: UTF8.self))
    }

    func receive(_ value: TrayInteractionResult) {
        if let taskID = value.repliedTaskID, let questionID = value.repliedQuestionID {
            replyDrafts[taskID]?[questionID] = nil
            ring.confirmReply(at: ProcessInfo.processInfo.systemUptime)
            drawIcon()
        }
        interaction = value
    }

    func draft(taskID: String, question: TrayQuestion) -> String? { replyDrafts[taskID]?[question.id] }

    func storeDraft(_ text: String, taskID: String, question: TrayQuestion) {
        replyDrafts[taskID, default: [:]][question.id] = text
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
        let scope = snapshot.partial ? " · 部分工作区" : ""
        let text = snapshot.taskState == "unavailable" ? "暂时读不到任务状态" :
            "执行与监测 \(lights.running) · 等待输入 \(lights.waiting) · 受阻 \(lights.blocked)\(scope)"
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
