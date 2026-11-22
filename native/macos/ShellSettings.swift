import AppKit

private struct ShellSettingsState: Encodable {
    let notchAvailable: Bool
    let sound: Bool
    let permission: String
    let canRequest: Bool
    let authorized: Bool
    let qrPNG: String?
}

@_cdecl("orca_tray_shell_settings")
public func desktopShellSettings(_ command: UnsafePointer<CChar>, _ argument: UnsafePointer<CChar>) -> UnsafeMutablePointer<CChar>? {
    let action = String(cString: command)
    let value = String(cString: argument)
    let payload = MainActor.assumeIsolated { () -> String? in
        guard let tray = Tray.shared else { return nil }
        let notifications = tray.notifications
        switch action {
        case "state":
            Task { await notifications.refresh() }
            let state = ShellSettingsState(notchAvailable: tray.notchAvailable, sound: notifications.sound,
                                           permission: notifications.status, canRequest: notifications.canRequest,
                                           authorized: notifications.authorized, qrPNG: qrPNG(value))
            guard let payload = try? JSONEncoder().encode(state) else { return nil }
            return String(decoding: payload, as: UTF8.self)
        case "notification_permission":
            if tray.snapshot.notifications && notifications.canRequest { notifications.requestPermission() }
        case "notification_system":
            notifications.openSystemSettings()
        case "notification_test":
            notifications.send(title: "Orca-Wails", body: "这是一条测试通知。")
        case "notification_sound":
            if value == "on" || value == "off" { notifications.sound = value == "on" }
        case "notification_preview":
            if let cue = Notifications.TaskSound(rawValue: value) { notifications.playTaskSounds([cue]) }
        default:
            break
        }
        return nil
    }
    return payload.flatMap { strdup($0) }
}

@MainActor
private func qrPNG(_ value: String) -> String? {
    guard let bitmap = makeWeChatQRCode(value) else { return nil }
    return NSBitmapImageRep(cgImage: bitmap).representation(using: .png, properties: [:])?.base64EncodedString()
}
