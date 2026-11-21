import AppKit
import Combine
import OSLog
import UserNotifications

@MainActor
final class Notifications: NSObject, ObservableObject, UNUserNotificationCenterDelegate {
    private enum Key {
        static let task = "orca.task"
        static let general = "orca.general"
        static let open = "orca.open"
        static let clear = "orca.clear"
    }

    @Published private(set) var status = "正在读取系统状态…"
    @Published private(set) var canRequest = false
    @Published private(set) var authorized = false
    @Published var sound = UserDefaults.standard.object(forKey: "orca.notificationSound") as? Bool ?? true {
        didSet { UserDefaults.standard.set(sound, forKey: "orca.notificationSound") }
    }
    var onOpen: ((String?) -> Void)?
    private let center = UNUserNotificationCenter.current()
    private let logger = Logger(subsystem: "dev.orca-wails", category: "notifications")
    private var enabled = true
    private var attention = 0
    private var revisions: [String: UUID] = [:]

    override init() {
        super.init()
        center.delegate = self
        let clear = UNNotificationAction(identifier: Key.clear, title: "清除此通知")
        let openTask = UNNotificationAction(identifier: Key.open, title: "打开任务", options: [.foreground])
        let openApp = UNNotificationAction(identifier: Key.open, title: "打开 Orca", options: [.foreground])
        center.setNotificationCategories([
            UNNotificationCategory(identifier: Key.task, actions: [openTask, clear], intentIdentifiers: []),
            UNNotificationCategory(identifier: Key.general, actions: [openApp, clear], intentIdentifiers: []),
        ])
    }

    func update(enabled: Bool, attention: Int) {
        guard self.enabled != enabled || self.attention != attention else { return }
        self.enabled = enabled
        self.attention = attention
        if !enabled {
            revisions.removeAll()
            center.removeAllPendingNotificationRequests()
            center.removeAllDeliveredNotifications()
        }
        Task { await refresh() }
    }

    func refresh() async {
        let settings = await center.notificationSettings()
        canRequest = settings.authorizationStatus == .notDetermined
        authorized = [.authorized, .provisional].contains(settings.authorizationStatus)
        if canRequest {
            status = "尚未授权"
        } else if authorized {
            var options = [String]()
            if settings.alertSetting == .enabled { options.append("提醒") }
            if settings.notificationCenterSetting == .enabled { options.append("通知中心") }
            if settings.soundSetting == .enabled { options.append("声音") }
            if settings.badgeSetting == .enabled { options.append("角标") }
            status = options.isEmpty ? "已授权" : options.joined(separator: " · ")
        } else {
            status = "系统通知已关闭"
        }
        try? await center.setBadgeCount(enabled && settings.badgeSetting == .enabled ? attention : 0)
    }

    func requestPermission() {
        Task {
            do { _ = try await center.requestAuthorization(options: [.alert, .sound, .badge]) }
            catch { logger.error("Notification authorization failed: \(error.localizedDescription)") }
            await refresh()
        }
    }

    func openSystemSettings() {
        if let url = URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension") {
            NSWorkspace.shared.open(url)
        }
    }

    func send(title: String, body: String, taskID: String? = nil, group: String = Key.general) {
        guard enabled else { return }
        let revision = UUID()
        if let taskID { revisions[taskID] = revision }
        Task {
            let settings = await center.notificationSettings()
            guard enabled, [.authorized, .provisional].contains(settings.authorizationStatus),
                  taskID.map({ revisions[$0] == revision }) ?? true else { return }
            let content = UNMutableNotificationContent()
            content.title = title
            content.body = body
            content.threadIdentifier = group
            content.categoryIdentifier = taskID == nil ? Key.general : Key.task
            if let taskID { content.userInfo = ["taskID": taskID] }
            if sound && settings.soundSetting == .enabled { content.sound = .default }
            let identifier = taskID.map { "orca.task.\($0)" } ?? revision.uuidString
            // Replace an older delivered alert for this task when its attention state changes.
            if taskID != nil { center.removeDeliveredNotifications(withIdentifiers: [identifier]) }
            do {
                try await center.add(UNNotificationRequest(identifier: identifier, content: content, trigger: nil))
                if !enabled || (taskID.map { revisions[$0] == nil } ?? false) {
                    center.removePendingNotificationRequests(withIdentifiers: [identifier])
                    center.removeDeliveredNotifications(withIdentifiers: [identifier])
                }
            } catch { logger.error("Notification delivery failed: \(error.localizedDescription)") }
        }
    }

    func clear(_ taskIDs: [String]) {
        for id in taskIDs { revisions[id] = nil }
        let identifiers = taskIDs.map { "orca.task.\($0)" }
        center.removePendingNotificationRequests(withIdentifiers: identifiers)
        center.removeDeliveredNotifications(withIdentifiers: identifiers)
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter, willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        let hasSound = notification.request.content.sound != nil
        return await MainActor.run {
            guard enabled else { return [] }
            return hasSound ? [.banner, .list, .sound] : [.banner, .list]
        }
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse
    ) async {
        let action = response.actionIdentifier
        let id = response.notification.request.identifier
        let task = response.notification.request.content.userInfo["taskID"] as? String
        if action == Key.clear || action == Key.open || action == UNNotificationDefaultActionIdentifier {
            center.removeDeliveredNotifications(withIdentifiers: [id])
        }
        if action == Key.open || action == UNNotificationDefaultActionIdentifier {
            await MainActor.run { onOpen?(task) }
        }
    }
}
