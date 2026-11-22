import CoreImage
import CoreImage.CIFilterBuiltins
import SwiftUI

struct TrayWeChatStatus: Codable, Equatable {
    enum Phase: String {
        case unbound, starting, scan, pin, verify, connected, reconnecting, expired
    }

    let phase: String
    let qr: String?
    let code: String?
    let message: String?
    let lastReceived: Int64?
    let lastSent: Int64?

    var connectionPhase: Phase? { Phase(rawValue: phase) }

    var label: String {
        switch connectionPhase {
        case .unbound: "未连接"
        case .starting: "正在连接"
        case .scan: "等待扫码"
        case .pin: "等待验证码"
        case .verify: code?.isEmpty == false ? "等待私聊配对" : "等待手机确认"
        case .connected: "已连接"
        case .reconnecting: "正在重连"
        case .expired: "连接已失效"
        case nil: "状态不可用"
        }
    }
}

struct TrayWeChatSettings: View {
    let status: TrayWeChatStatus
    let send: (String, String?) -> Void
    let onDisconnect: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 7) {
                statusMark.frame(width: 16, height: 16).accessibilityHidden(true)
                Text(status.label).font(.subheadline.weight(.semibold))
            }
            .accessibilityAddTraits(.updatesFrequently)

            if let message = status.message, !message.isEmpty {
                instruction(message)
            }

            switch status.connectionPhase {
            case .unbound:
                instruction("连接个人微信后，可查询 Orca 任务并测试消息收发。")
                startButton("连接微信")
            case .starting:
                cancelButton
            case .scan:
                if let qr = status.qr, let image = WeChatQRCode(content: qr) {
                    instruction("用手机微信扫一扫，按提示确认连接。")
                    image.frame(maxWidth: .infinity)
                } else {
                    instruction("无法显示二维码。取消连接后可重新获取。")
                }
                cancelButton
            case .pin:
                instruction("输入手机微信显示的数字验证码。")
                WeChatPINField { send("wechat_pin", $0) }
                cancelButton
            case .verify:
                if let code = status.code, !code.isEmpty {
                    instruction("在微信中打开刚连接的 Bot 私聊，发送以下配对码。")
                    Text(code)
                        .font(.system(.title2, design: .monospaced).weight(.semibold))
                        .textSelection(.enabled)
                        .accessibilityLabel("配对码")
                        .accessibilityValue(code)
                } else {
                    instruction("在手机微信中按提示完成确认。")
                }
                cancelButton
            case .connected, .reconnecting:
                instruction("阻塞和完成提醒尚待真实账号验证。")
                VStack(spacing: 8) {
                    LabeledContent("上次收信", value: activityDate(status.lastReceived))
                    LabeledContent("上次发信", value: activityDate(status.lastSent))
                }
                Divider()
                HStack {
                    Button("测试通知") { send("wechat_test", nil) }
                        .disabled(status.connectionPhase != .connected)
                    Spacer()
                    Button("解除绑定", role: .destructive, action: onDisconnect)
                }
            case .expired:
                startButton("重新连接")
            case nil:
                instruction("暂时无法读取微信连接状态。")
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 7)
        .padding(.vertical, 4)
    }

    @ViewBuilder private var statusMark: some View {
        switch status.connectionPhase {
        case .starting, .reconnecting:
            ProgressView().controlSize(.small)
        case .connected:
            Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
        case .expired, nil:
            Image(systemName: "exclamationmark.circle").foregroundStyle(.orange)
        case .scan:
            Image(systemName: "qrcode.viewfinder").foregroundStyle(.secondary)
        case .pin:
            Image(systemName: "lock").foregroundStyle(.secondary)
        case .unbound, .verify:
            Image(systemName: "bubble.left").foregroundStyle(.secondary)
        }
    }

    private func instruction(_ value: String) -> some View {
        Text(value)
            .font(.callout)
            .foregroundStyle(.primary.opacity(0.8))
            .fixedSize(horizontal: false, vertical: true)
    }

    private func startButton(_ title: String) -> some View {
        Button(title) { send("wechat_start", nil) }
            .buttonStyle(.borderedProminent)
    }

    private var cancelButton: some View {
        Button("取消连接") { send("wechat_cancel", nil) }
    }

    private func activityDate(_ seconds: Int64?) -> String {
        guard let seconds, seconds > 0 else { return "尚无记录" }
        return Date(timeIntervalSince1970: Double(seconds)).formatted(date: .numeric, time: .shortened)
    }
}

private struct WeChatPINField: View {
    // Select the property wrapper on toolchains without the SwiftUI State macro plugin.
    private typealias PINState = SwiftUI.State<String>
    let onSubmit: (String) -> Void
    @PINState private var pin = ""

    private var validPIN: String? {
        let digits = pin.trimmingCharacters(in: .whitespacesAndNewlines)
        guard (4...8).contains(digits.utf8.count),
              digits.utf8.allSatisfy({ (48...57).contains($0) }) else { return nil }
        return digits
    }

    var body: some View {
        HStack(spacing: 8) {
            SecureField("微信验证码", text: $pin)
                .textFieldStyle(.roundedBorder)
                .accessibilityLabel("微信验证码")
                .onSubmit(submit)
            Button("提交验证码", action: submit)
                .buttonStyle(.borderedProminent)
                .disabled(validPIN == nil)
        }
        .onDisappear { pin = "" }
    }

    private func submit() {
        guard let digits = validPIN else { return }
        pin = ""
        onSubmit(digits)
    }
}

@MainActor private let weChatQRContext = CIContext()

@MainActor
func makeWeChatQRCode(_ content: String) -> CGImage? {
    guard !content.isEmpty else { return nil }
    let filter = CIFilter.qrCodeGenerator()
    filter.message = Data(content.utf8)
    guard let output = filter.outputImage else { return nil }
    return weChatQRContext.createCGImage(output, from: output.extent)
}

private struct WeChatQRCode: View {
    private let image: CGImage
    private let moduleSize: CGFloat

    init?(content: String) {
        guard let image = makeWeChatQRCode(content) else { return nil }
        self.image = image
        moduleSize = max(1, floor(184 / (CGFloat(image.width) + 8)))
    }

    var body: some View {
        Image(image, scale: 1, label: Text("微信连接二维码"))
            .interpolation(.none)
            .resizable()
            .frame(width: CGFloat(image.width) * moduleSize, height: CGFloat(image.height) * moduleSize)
            .padding(4 * moduleSize)
            .background(.white)
    }
}
