import AppKit
import SwiftUI

@MainActor
final class TrayNavigation: ObservableObject {
    enum Page { case tasks, launch, bots, detail, quickReply, settings, entries, shortcuts, notifications, wechat, computerUse, about, advanced }
    struct Pending {
        enum Kind {
            case listBots, openBot, readPrompt, sendQuestion, checkAnswer, readDetail, sendFollowUp, checkFollowUp
            case launchCatalog, launch, checkLaunch, openLaunchedSurface, readChanges, readDiff

            var writing: Bool {
                self == .openBot || self == .sendQuestion || self == .sendFollowUp || self == .launch || self == .openLaunchedSurface
            }
        }
        let id: UUID
        let kind: Kind
    }
    struct Confirmation: Identifiable {
        let title: String
        let message: String
        let action: String
        var id: String { action }
    }

    @Published var page: Page = .tasks
    @Published var scope: TraySnapshot.TaskScope = .current
    @Published var workspaceKey: String?
    @Published var lastMetricExpiry = Date.distantPast
    @Published var shortcut = ""
    @Published var confirmation: Confirmation?
    @Published var pending: Pending?
    @Published var bots: [TrayBot] = []
    @Published var task: TrayTask?
    @Published var taskID: String?
    @Published var question: TrayQuestion?
    @Published var approval: TrayApproval?
    @Published var approvalSelection: String?
    @Published var promptCount = 0
    @Published var detail: TrayTaskDetail?
    @Published var detailReadAt: Int64?
    @Published var detailReadError: String?
    @Published var promptFresh = false
    @Published var changes: TrayChanges?
    @Published var changesExpanded = false
    @Published var change: TrayChange?
    @Published var diff: TrayDiff?
    var backgroundDetailID: UUID?
    var viewedDetailID: String?
    @Published var followUp = ""
    @Published var followUpExpanded = false
    @Published var catalog: TrayLaunchCatalog?
    @Published var launch = TrayLaunchInput()
    var usesLaunchPreset = false
    @Published var drafts: [String: TrayQuestionDraft] = [:]
    @Published var computerUseDraft = TrayComputerUseSettings()
    @Published var error: String?
    @Published var receipt: String?
    var origin: Page = .tasks
    var detailOrigin: Page = .tasks
    var back = false
}

struct TrayPanel: View {
    @ObservedObject var tray: Tray
    @StateObject private var navigation: TrayNavigation
    private let management: Bool
    private let notchWidth: CGFloat?
    var onHeightChange: ((CGFloat) -> Void)?
    @FocusState private var focusReply: String?
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency
    @Environment(\.colorScheme) private var colorScheme
    private var state: TraySnapshot { tray.snapshot }
    private var computerUse: TrayComputerUseSettings { state.computerUse ?? .init() }
    private var page: TrayNavigation.Page { navigation.page }
    private var isNotch: Bool { notchWidth != nil }
    private var connected: Bool { state.running && state.taskState == "ready" && !state.busy }
    private var canInteract: Bool { navigation.pending == nil && connected }
    private var canSendFollowUp: Bool {
        canInteract && navigation.detailReadAt != nil && navigation.detailReadError == nil
    }
    private var refreshKey: String? {
        guard connected, let id = navigation.taskID else { return nil }
        if page == .detail { return "detail:" + id }
        if page == .quickReply { return "prompt:" + id }
        return nil
    }
    private var replyCheck: Tray.ReplyCheck? { navigation.taskID.flatMap { tray.replyChecks[$0] } }
    private var transitionAnimation: Animation? { reduceMotion ? nil : .easeOut(duration: 0.16) }

    init(tray: Tray, page: TrayNavigation.Page = .tasks, management: Bool = false,
         notchWidth: CGFloat? = nil, question: TrayTask? = nil, taskID: String? = nil) {
        self.tray = tray
        self.management = management
        self.notchWidth = notchWidth
        let navigation = TrayNavigation()
        navigation.page = page
        navigation.scope = notchWidth == nil ? .current : .all
        navigation.task = question
        navigation.taskID = taskID ?? question?.id
        if page == .computerUse { navigation.computerUseDraft = tray.snapshot.computerUse ?? .init() }
        _navigation = StateObject(wrappedValue: navigation)
    }

    var body: some View {
        VStack(spacing: 0) {
            if isNotch {
                header.frame(height: 33).padding(.bottom, 5)
                    .padding(.horizontal, 14)
            } else {
                header.padding(.horizontal, 12).padding(.vertical, 7)
                Divider()
            }
            if let receipt = navigation.receipt, page == .tasks {
                Label(receipt, systemImage: "checkmark.circle")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(statusColor(.working))
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 14).padding(.vertical, 6)
                    .accessibilityAddTraits(.updatesFrequently)
                    .task(id: receipt) {
                        do { try await Task.sleep(for: .seconds(3)) } catch { return }
                        navigation.receipt = nil
                    }
            }
            ZStack(alignment: .top) {
                content
                    .id(page)
                    .transition(reduceMotion ? .identity : .asymmetric(
                        insertion: .offset(x: navigation.back ? -6 : 6).combined(with: .opacity),
                        removal: .identity
                    ))
            }
            .clipped()
            .animation(transitionAnimation, value: page)
            .animation(transitionAnimation, value: navigation.scope)
        }
        .frame(width: notchWidth ?? 336)
        .onGeometryChange(for: CGFloat.self, of: { $0.size.height }) { height in
            onHeightChange?(height)
        }
        .fixedSize(horizontal: false, vertical: notchWidth != nil)
        .background {
            if !management && notchWidth == nil && !reduceTransparency {
                RoundedRectangle(cornerRadius: TrayWindow.cornerRadius)
                    .fill(LinearGradient(colors: [
                        Color(red: 0.34, green: 0.48, blue: 0.83).opacity(colorScheme == .dark ? 0.14 : 0.09),
                        .clear,
                    ], startPoint: .topLeading, endPoint: .bottomTrailing))
            }
        }
        .onAppear {
            if page == .bots { loadBots() }
            if page == .quickReply { loadPrompt() }
            if page == .detail { loadDetail() }
            if page == .launch { openLaunch() }
            if page == .detail, let id = navigation.taskID {
                navigation.followUp = tray.followUpDrafts[id] ?? ""
            }
        }
        .onReceive(tray.$interaction) { if let result = $0 { receive(result) } }
        .task(id: refreshKey) { await refreshVisibleTask() }
        .onExitCommand {
            if management { tray.closeManagement() }
            else if page != .tasks { back() }
            else { tray.close() }
        }
        .alert(item: $navigation.confirmation) { item in
            Alert(title: Text(item.title), message: Text(item.message),
                  primaryButton: .destructive(Text("确认")) { tray.send(item.action) },
                  secondaryButton: .cancel(Text("取消")))
        }
    }

    private var header: some View {
        HStack(spacing: 4) {
            if page == .tasks {
                HStack(spacing: isNotch ? 3 : 2) {
                    if isNotch { tab("全部", scope: .all) }
                    tab("当前", scope: .current)
                    tab("最近", scope: .recent)
                }
                Spacer()
                icon("plus", "新建任务") { openLaunch() }
                    .keyboardShortcut("t", modifiers: [.command, .option])
                    .disabled(!state.running || state.busy)
                icon("macwindow", "打开窗口") { tray.send("open", close: true) }
                    .disabled(!state.canOpen)
                moreMenu
            } else {
                if !management || page != .settings {
                    icon("chevron.left", "返回") { back() }
                        .disabled(navigation.pending?.kind.writing == true)
                }
                Text(pageTitle).font(isNotch ? .system(size: 13, weight: .semibold) : .headline)
                    .foregroundStyle(isNotch ? NotchPalette.text : .primary)
                Spacer()
                if page == .detail && navigation.detail != nil {
                    icon("arrow.clockwise", "刷新任务") { loadDetail() }.disabled(!canInteract)
                }
                if (page == .quickReply || page == .detail), let id = navigation.taskID {
                    icon("macwindow", "打开原会话") { openOriginal(id) }
                        .disabled(!state.canOpen || navigation.pending?.kind.writing == true)
                }
            }
        }
    }

    private var moreMenu: some View {
        let workspaces = Dictionary(grouping: state.tasks.filter { $0.workspaceKey != nil }) { $0.workspaceKey ?? "" }
            .values.compactMap(\.first).sorted { $0.workspace.localizedStandardCompare($1.workspace) == .orderedAscending }
        let recent = tray.viewedTaskIDs.compactMap { id in state.tasks.first { $0.id == id } }
        return Menu {
            if !workspaces.isEmpty || navigation.workspaceKey != nil {
                Picker("筛选工作区", selection: $navigation.workspaceKey) {
                    Text("所有工作区").tag(String?.none)
                    ForEach(workspaces) { task in
                        let duplicate = workspaces.contains { $0.id != task.id && $0.workspace == task.workspace }
                        Text(duplicate ? task.workspace + " · " + task.title : task.workspace).tag(task.workspaceKey)
                    }
                }
                .pickerStyle(.menu)
            }
            if !recent.isEmpty {
                Menu("最近查看") {
                    ForEach(recent) { task in
                        Button(task.title + " · " + task.workspace) { openDetail(task.id, task: task) }
                    }
                }
            }
            Button("机器人") { tray.send("open_bots", close: true) }
                .disabled(!state.canOpen)
            Divider()
            Button("设置") { tray.send("open_shell_settings", close: true) }
        } label: {
            Image(systemName: "ellipsis.circle")
                .font(.system(size: 14, weight: .medium))
                .frame(width: isNotch ? 30 : 29, height: isNotch ? 30 : 29)
                .foregroundStyle(isNotch ? NotchPalette.control : .primary)
        }
        .menuStyle(.borderlessButton)
        .menuIndicator(.hidden)
        .fixedSize()
        .help("更多")
        .accessibilityLabel("更多")
    }

    private func tab(_ title: String, scope: TraySnapshot.TaskScope) -> some View {
        let selected = navigation.scope == scope
        return Button {
            if !selected { navigation.scope = scope }
        } label: {
            Text(title).font(isNotch ? .system(size: 13, weight: selected ? .semibold : .regular) : .subheadline.weight(.semibold))
                .padding(.horizontal, isNotch ? 10 : 12).frame(height: isNotch ? 27 : 29)
                .foregroundStyle(isNotch ? (selected ? NotchPalette.text : NotchPalette.secondary) : (selected ? .primary : .secondary))
                .background {
                    if selected {
                        if isNotch { RoundedRectangle(cornerRadius: 8).fill(.white.opacity(0.086)) }
                        else { Capsule().fill(.primary.opacity(0.10)) }
                    }
                }
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }

    private func icon(_ symbol: String, _ label: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: symbol).font(.system(size: 14, weight: .medium))
                .frame(width: isNotch ? 30 : 29, height: isNotch ? 30 : 29)
                .foregroundStyle(isNotch ? NotchPalette.control : .primary)
        }
        .buttonStyle(.borderless)
        .help(label)
        .accessibilityLabel(label)
    }

    @ViewBuilder private var content: some View {
        switch page {
        case .tasks:
            taskList(scope: navigation.scope)
                .id(navigation.scope)
                .transition(reduceMotion ? .identity : .asymmetric(insertion: .opacity, removal: .identity))
        case .bots:
            botPicker
        case .launch:
            launchPage
        case .detail:
            detailPage
        case .quickReply:
            VStack(spacing: 4) {
                ViewThatFits(in: .vertical) {
                    quickReply.fixedSize(horizontal: false, vertical: true)
                    ScrollView { quickReply }
                }
                replySubmit
            }
            .frame(maxHeight: 338)
        default:
            if management {
                ScrollView { settingsContent }
                    .frame(height: page == .entries ? 132 : 300)
            } else {
                ViewThatFits(in: .vertical) {
                    settingsContent.fixedSize(horizontal: false, vertical: true)
                    ScrollView { settingsContent }
                }
                .frame(maxHeight: 330)
            }
        }
    }

    private var settingsContent: some View {
        VStack(alignment: .leading, spacing: 5) {
            switch page {
            case .settings: settings
            case .entries: entrySettings
            case .shortcuts: shortcutSettings
            case .notifications:
                NotificationSettings(tray: tray, notifications: tray.notifications)
                if let status = state.wechat {
                    Divider()
                    row("微信", detail: status.label, next: true) {
                        if management { navigate(.wechat) }
                        else { tray.showManagement(.wechat) }
                    }
                }
            case .wechat:
                if let status = state.wechat {
                    TrayWeChatSettings(status: status, send: { tray.send($0, $1) }) {
                        confirm("解除微信绑定", "Orca 将停止向这个微信会话发送任务通知，旧配对码和回复码也会失效。", "wechat_disconnect")
                    }
                } else {
                    Text("微信连接暂不可用。").foregroundStyle(.secondary).padding(.horizontal, 7)
                }
            case .computerUse: computerUseSettings
            case .about: about
            case .advanced: advanced
            case .tasks, .launch, .bots, .quickReply, .detail: EmptyView()
            }
            if !state.message.isEmpty {
                Text(state.message).font(.caption).foregroundStyle(.secondary).padding(.horizontal, 7)
            }
        }
        .padding(10)
    }

    private func taskList(scope: TraySnapshot.TaskScope) -> some View {
        let workspace = navigation.workspaceKey
        let rows = state.taskRows(scope).filter { workspace == nil || $0.workspaceKey == workspace }
        let notchRowsHeight = rows.reduce(CGFloat(0)) { height, task in height + notchRowHeight(task) }
        let now = max(navigation.lastMetricExpiry, .now)
        let nextMetricExpiry = rows.compactMap(\.visibleOutputExpiry).filter { $0 > now }.min()
        return VStack(spacing: 4) {
            if let workspace {
                HStack {
                    Label(state.tasks.first { $0.workspaceKey == workspace }?.workspace ?? "筛选工作区",
                          systemImage: "line.3.horizontal.decrease")
                        .font(.caption).foregroundStyle(isNotch ? NotchPalette.secondary : .secondary)
                        .lineLimit(1)
                    Spacer()
                    icon("xmark", "清除工作区筛选") { navigation.workspaceKey = nil }
                }
                .padding(.horizontal, 7)
            }
            if state.taskState == "ready", !rows.isEmpty {
                ScrollView {
                    LazyVStack(spacing: isNotch ? 0 : 2) {
                        ForEach(Array(rows.enumerated()), id: \.element.id) { index, task in
                            taskRow(task, now: now)
                                .overlay(alignment: .bottom) {
                                    if isNotch && task.taskStatus != .waiting && index < rows.count - 1 {
                                        Rectangle().fill(NotchPalette.separator).frame(height: 1)
                                            .allowsHitTesting(false)
                                    }
                                }
                                .modifier(TaskRowArrival(animated: isNotch && !reduceMotion,
                                                         delay: Double(min(index, 4)) * 0.02))
                        }
                    }
                    .padding(isNotch ? 0 : 8)
                }
                .frame(height: min(314, isNotch ? notchRowsHeight : CGFloat(rows.count) * 44 + 14))
            } else {
                VStack(spacing: 9) {
                    let message = workspace != nil && state.taskState == "ready" ? "此工作区暂无符合条件的任务" : emptyMessage(scope: scope)
                    Text(message).foregroundStyle(isNotch ? NotchPalette.secondary : .secondary)
                    if state.taskState == "unavailable" {
                        Button("重试") { tray.send("retry_tasks") }
                    }
                }.frame(maxWidth: .infinity, minHeight: isNotch ? 53 : 104)
            }
            if state.partial {
                Text("仅显示可读取的工作区").font(.caption).foregroundStyle(.secondary).padding(.bottom, 8)
            }
        }
        .padding(.horizontal, isNotch ? 14 : 0)
        .padding(.bottom, isNotch ? 16 : 0)
        .task(id: nextMetricExpiry) {
            guard let nextMetricExpiry else { return }
            do { try await Task.sleep(for: .seconds(max(0, nextMetricExpiry.timeIntervalSinceNow))) }
            catch { return }
            navigation.lastMetricExpiry = nextMetricExpiry
        }
    }

    private func notchRowHeight(_ task: TrayTask) -> CGFloat { task.taskStatus == .waiting ? 51 : 50 }

    private func taskRow(_ task: TrayTask, now: Date) -> some View {
        let status = task.taskStatus
        let tint = statusColor(task)
        let metric = task.activeMetricLabel(now: now)
        let activity = metric.map { " · \($0)" } ?? ""
        return HStack(spacing: isNotch ? 7 : 4) {
            Button {
                openDetail(task.id, task: task)
            } label: {
                HStack(spacing: isNotch ? 7 : 8) {
                    TaskStatusMark(status: status, color: tint, notch: isNotch, animated: !reduceMotion, outcome: task.outcome)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(task.title).font(isNotch ? .system(size: 12, weight: .semibold) : .callout.weight(.semibold))
                            .foregroundStyle(isNotch ? NotchPalette.text : .primary).lineLimit(1)
                            .frame(height: isNotch ? 17 : nil, alignment: .leading)
                        if !task.workspace.isEmpty {
                            Text(task.workspace).font(isNotch ? .system(size: 11) : .caption)
                                .foregroundStyle(isNotch ? NotchPalette.secondary : .secondary).lineLimit(1)
                                .frame(height: isNotch ? 16 : nil, alignment: .leading)
                        }
                    }
                    Spacer(minLength: 6)
                    taskMetrics(task, primary: metric, now: now)
                        .layoutPriority(1)
                }
                .padding(.horizontal, isNotch ? 0 : 7).frame(minHeight: isNotch ? notchRowHeight(task) : 42)
                .contentShape(Rectangle())
                .modifier(TaskRowHover(ink: isNotch ? NotchPalette.text : .primary))
            }
            .buttonStyle(.borderless)
            .disabled(!state.running)
            .accessibilityValue(task.statusLabel + activity)
            .help("\(task.title) · \(task.statusLabel)\(activity)")
            if task.quickReply == true || tray.replyChecks[task.id] != nil {
                if isNotch {
                    Button { openQuestion(task) } label: {
                        Text("处理").font(.system(size: 13))
                            .padding(.horizontal, 10).frame(height: 31)
                            .foregroundStyle(NotchPalette.amber)
                            .background(NotchPalette.replyFill, in: RoundedRectangle(cornerRadius: 8))
                            .overlay(RoundedRectangle(cornerRadius: 8).strokeBorder(NotchPalette.replyBorder))
                            .contentShape(Rectangle())
                            .modifier(TaskRowHover(ink: NotchPalette.text))
                    }
                    .buttonStyle(.plain)
                    .disabled(!state.running || state.busy)
                    .accessibilityLabel("处理待办：\(task.title)")
                } else {
                    Button("处理") { openQuestion(task) }
                        .controlSize(.small)
                        .buttonStyle(.bordered)
                        .disabled(!state.running || state.busy)
                        .accessibilityLabel("处理待办：\(task.title)")
                        .padding(.trailing, 6)
                }
            }
        }
        .padding(.horizontal, isNotch ? (status == .waiting ? 8 : 7) : 0)
        .background {
            if isNotch && status == .waiting {
                RoundedRectangle(cornerRadius: 11).fill(NotchPalette.waitingFill)
                    .overlay(RoundedRectangle(cornerRadius: 11).strokeBorder(NotchPalette.replyBorder))
            }
        }
    }

    private func taskMetrics(_ task: TrayTask, primary: String?, now: Date) -> some View {
        VStack(alignment: .trailing, spacing: 2) {
            if task.taskStatus == .done {
                let completion = task.completionTiming()
                if let duration = completion.duration {
                    Text(duration).fontWeight(.medium)
                }
                if let finishedAt = completion.finishedAt {
                    Text(finishedAt)
                }
            } else {
                let timing = task.activeTiming(now: now)
                if let primary {
                    Text(primary).fontWeight(.medium)
                        .frame(maxWidth: 120, alignment: .trailing)
                        .accessibilityHidden(true)
                } else if let update = timing.updatedAt {
                    Text("\(update, style: .relative)前更新")
                }
                if let start = timing.elapsedSince {
                    Text(start, style: .relative).fontWeight(.medium)
                }
            }
        }
        .font(.caption2).monospacedDigit().lineLimit(1)
        .foregroundStyle(isNotch ? NotchPalette.secondary : .secondary)
    }

    private func statusColor(_ status: TrayTask.Status) -> Color {
        if isNotch {
            return switch status {
            case .blocked: NotchPalette.red
            case .waiting: NotchPalette.amber
            case .working, .monitoring: NotchPalette.blue
            case .done: NotchPalette.mint
            case .unknown: NotchPalette.secondary
            }
        }
        let dark = colorScheme == .dark
        switch status {
        case .blocked:
            return dark ? Color(red: 1, green: 0.70, blue: 0.73) : Color(red: 0.66, green: 0.12, blue: 0.22)
        case .waiting:
            return dark ? Color(red: 1, green: 0.82, blue: 0.53) : Color(red: 0.48, green: 0.29, blue: 0.07)
        case .working, .monitoring:
            return dark ? Color(red: 0.60, green: 0.81, blue: 1) : Color(red: 0.09, green: 0.30, blue: 0.67)
        case .done, .unknown:
            return .secondary
        }
    }

    private func statusColor(_ task: TrayTask) -> Color {
        switch task.outcome {
        case "failure", "interruption": statusColor(.blocked)
        case "success": statusColor(.done)
        default: statusColor(task.taskStatus == .done ? .unknown : task.taskStatus)
        }
    }

    private func emptyMessage(scope: TraySnapshot.TaskScope) -> String {
        switch state.taskState {
        case "loading": "正在读取任务…"
        case "unavailable", "offline": "暂时读不到任务状态"
        default:
            switch scope {
            case .all: "暂无任务"
            case .current: "没有正在进行的任务"
            case .recent: "过去 24 小时暂无已结束的任务"
            }
        }
    }

    private var pageTitle: String {
        switch page {
        case .about: "关于与更新"
        case .advanced: "内核维护"
        case .entries: "任务入口"
        case .shortcuts: "快捷键"
        case .notifications: "通知"
        case .wechat: "微信"
        case .computerUse: "电脑操控"
        case .bots: "新建 Bot 对话"
        case .launch: "新建任务"
        case .detail: "任务详情"
        case .quickReply: navigation.approval != nil ? "审批请求" : (navigation.question == nil ? "处理待办" : "回复问题")
        default: "设置"
        }
    }

    private var botPicker: some View {
        VStack(alignment: .leading, spacing: 8) {
            if navigation.pending?.kind == .listBots {
                progress("正在读取 Bot…")
            } else if navigation.bots.isEmpty, navigation.error == nil {
                Text("还没有 Bot").foregroundStyle(.secondary)
                Button("打开机器人") { tray.send("open_bots", close: true) }
                    .disabled(!state.canOpen)
            } else if !navigation.bots.isEmpty {
                ScrollView {
                    LazyVStack(spacing: 1) {
                        ForEach(navigation.bots) { bot in
                            Button {
                                send("open_bot_chat", kind: .openBot, botID: bot.id)
                            } label: {
                                HStack(spacing: 9) {
                                    Image(systemName: "bubble.left.and.text.bubble.right")
                                        .font(.system(size: 14))
                                        .frame(width: 24)
                                        .foregroundStyle(statusColor(.working))
                                        .accessibilityHidden(true)
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(bot.name).font(.callout.weight(.semibold)).lineLimit(1)
                                        if let specialty = bot.specialty {
                                            Text(specialty).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                                        }
                                    }
                                    Spacer()
                                    if bot.paused {
                                        Text("已暂停").font(.caption).foregroundStyle(.secondary)
                                    } else {
                                        Image(systemName: "chevron.right").font(.caption2).foregroundStyle(.secondary)
                                    }
                                }
                                .frame(minHeight: 40)
                                .contentShape(Rectangle())
                            }
                            .buttonStyle(.borderless)
                            .disabled(bot.paused || navigation.pending != nil || !state.running || state.busy)
                        }
                    }
                }
                .frame(height: min(247, CGFloat(navigation.bots.count) * 41))
            }
            if navigation.pending?.kind == .openBot { progress("正在打开对话…") }
            if let error = navigation.error {
                Text(error).font(.caption).foregroundStyle(.secondary)
                Button("重新读取", action: loadBots)
            }
        }
        .padding(.horizontal, isNotch ? 17 : 12)
        .padding(.top, isNotch ? 1 : 12)
        .padding(.bottom, isNotch ? 16 : 12)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var launchPage: some View {
        workbenchPage {
            switch tray.launchState {
            case .complete(let receipt):
                Label(receipt.label, systemImage: "arrow.up.right.circle")
                    .font(.callout.weight(.semibold))
                Text(receipt.kind == "terminal" ? "终端会话" : "原生对话")
                    .font(.caption).foregroundStyle(.secondary)
                if let modeDetail = receipt.modeDetail { Text(modeDetail).font(.caption).foregroundStyle(.secondary) }
                if let warning = receipt.warning { Text(warning).font(.caption).textSelection(.enabled) }
                if let taskID = receipt.taskID {
                    Button("查看新任务") { openDetail(taskID, task: state.tasks.first { $0.id == taskID }) }
                        .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: true))
                } else if let key = receipt.key,
                   let task = state.tasks.first(where: { $0.launchKey == key }) {
                    Button("查看新任务") { openDetail(task.id, task: task) }
                        .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: true))
                } else {
                    Text("等待 Agent 上报任务状态").font(.caption).foregroundStyle(.secondary)
                    if let lookup = receipt.lookup {
                        Button("打开新会话") {
                            send("open_launched_surface", kind: .openLaunchedSurface, launchCheck: lookup)
                        }
                        .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: true))
                        .disabled(!canInteract)
                    } else {
                        Button("返回任务列表") { back() }
                    }
                }
                if navigation.pending?.kind == .openLaunchedSurface { progress("正在打开新会话…") }
                if let error = navigation.error { Text(error).font(.caption).foregroundStyle(.secondary) }
                if receipt.prompt != "journaled", receipt.prompt != "handed-to-terminal", !tray.launchDraft.text.isEmpty {
                    Button("复制任务指令") { copyToPasteboard(tray.launchDraft.text) }
                }
                Button("新建另一项任务", action: startAnotherTask)
            case .uncertain(let message, let lookup):
                Text(message).font(.callout)
                if let lookup {
                    Button("核对启动结果") { send("check_launch", kind: .checkLaunch, launchCheck: lookup) }
                        .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: true))
                        .disabled(!canInteract)
                }
                if navigation.pending?.kind == .checkLaunch { progress("正在核对启动结果…") }
                Button("返回任务列表") { back() }
                Button("新建另一项任务", action: startAnotherTask)
                    .disabled(navigation.pending != nil)
            case .starting:
                progress("正在启动任务…")
            case nil:
                if let catalog = navigation.catalog {
                    let input = Binding(get: { navigation.launch }, set: { value in
                        navigation.launch = value
                        tray.launchDraft = value
                    })
                    TrayLaunchForm(catalog: catalog, input: input, notch: isNotch, enabled: canInteract) {
                        send("launch_task", kind: .launch, launch: navigation.launch)
                    }
                }
                if navigation.pending?.kind == .launchCatalog { progress("正在读取工作区和 Agent…") }
                if let error = navigation.error {
                    Text(error).font(.caption).foregroundStyle(.secondary)
                    Button("刷新工作区和 Agent", action: loadLaunch)
                }
                Divider()
                Button("新建 Bot 对话…", action: openBots)
                    .buttonStyle(ReplyChoiceStyle(notch: isNotch))
                    .disabled(!canInteract)
            }
        }
    }

    private var detailPage: some View {
        workbenchPage {
            if let detail = navigation.detail {
                let task = state.tasks.first(where: { $0.id == detail.task.id }) ?? detail.task
                Text(task.title).font(.callout.weight(.semibold)).textSelection(.enabled).lineLimit(2)
                HStack(spacing: 5) {
                    Text(task.statusLabel).fontWeight(.semibold).foregroundStyle(statusColor(task))
                    if !task.workspace.isEmpty {
                        Text("· " + task.workspace).foregroundStyle(.secondary).lineLimit(1)
                    }
                    if let provider = task.provider, !provider.isEmpty {
                        Text("· " + provider).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                .font(.caption)
                if let readAt = navigation.detailReadAt {
                    let time = Date(timeIntervalSince1970: Double(readAt) / 1000)
                    Text("上次读取 " + time.formatted(date: .omitted, time: .standard))
                        .font(.caption2).foregroundStyle(.secondary)
                }
                if !connected {
                    Text(state.busy ? "Orca 正忙" : "暂时无法连接任务数据")
                        .font(.caption).foregroundStyle(statusColor(.waiting))
                } else if let issue = navigation.detailReadError {
                    Text(issue).font(.caption).foregroundStyle(statusColor(.waiting))
                }
                if let tool = detail.currentTool, task.taskStatus == .working || task.taskStatus == .monitoring {
                    Label(tool, systemImage: "wrench").font(.caption).foregroundStyle(.secondary)
                }
                if task.quickReply == true || replyCheck != nil {
                    Button(replyCheck == nil ? "处理待办" : "核对刚才的回复") { openQuestion(task) }
                        .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: true))
                        .disabled(!canInteract)
                }
                let followUp = tray.followUps[task.id]
                switch followUp {
                case .sending:
                    progress("正在发送后续消息…")
                case .uncertain(let message, let operationID):
                    Text(message).font(.caption).foregroundStyle(.secondary)
                    if let operationID {
                        Button("核对后续消息") {
                            send("check_follow_up", kind: .checkFollowUp, taskID: task.id, operationID: operationID)
                        }
                        .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: true))
                        .disabled(!canInteract)
                    }
                    if navigation.pending?.kind == .checkFollowUp { progress("正在核对后续消息…") }
                    if let draft = tray.followUpDrafts[task.id], !draft.isEmpty {
                        Button("复制后续消息") { copyToPasteboard(draft) }
                    }
                case .recorded(let receipt):
                    Label(receipt, systemImage: "checkmark.circle")
                        .font(.caption.weight(.semibold)).foregroundStyle(statusColor(.working))
                    if detail.canFollowUp && replyCheck == nil { followUpComposer(task) }
                case nil:
                    if detail.canFollowUp && replyCheck == nil { followUpComposer(task) }
                }
                TrayDetailContent(detail: detail, task: task)
                if task.taskStatus == .done, let preset = detail.launchPreset {
                    Button("用此工作区和 Agent 新建任务") { openLaunch(from: preset) }
                        .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: true)).disabled(!canInteract)
                }
                if detail.canReadChanges == true { workspaceChanges(for: task) }
            }
            if let receipt = navigation.receipt {
                Label(receipt, systemImage: "checkmark.circle")
                    .font(.caption.weight(.semibold)).foregroundStyle(statusColor(.working))
            }
            if navigation.pending?.kind == .readDetail { progress("正在读取任务…") }
            if let error = navigation.error, !isFollowUpUncertain {
                Text(error).font(.caption).foregroundStyle(.secondary)
                if navigation.detail == nil {
                    Button("重新读取", action: loadDetail).disabled(!canInteract)
                }
            }
        }
    }

    private var isFollowUpUncertain: Bool {
        guard let id = navigation.taskID, case .uncertain = tray.followUps[id] else { return false }
        return true
    }

    @ViewBuilder private func followUpComposer(_ task: TrayTask) -> some View {
        let text = Binding(get: { navigation.followUp }, set: { value in
            navigation.followUp = value
            tray.followUpDrafts[task.id] = value
        })
        DisclosureGroup("给 Agent 追加消息", isExpanded: $navigation.followUpExpanded) {
            VStack(alignment: .leading, spacing: 8) {
                TrayWorkbenchEditor(text: text, label: "后续消息", notch: isNotch)
                    .disabled(!canSendFollowUp)
                Button(task.taskStatus == .working || task.taskStatus == .monitoring ? "排队发送" : "发送后续消息") {
                    send("send_follow_up", kind: .sendFollowUp, taskID: task.id, text: navigation.followUp)
                }
                .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: true))
                .disabled(!canSendFollowUp || navigation.followUp.utf8.count > 64 * 1024 ||
                          navigation.followUp.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
            .padding(.top, 6)
        }
        .font(.caption.weight(.semibold))
    }

    private func copyToPasteboard(_ text: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }

    private func workspaceChanges(for task: TrayTask) -> some View {
        let expanded = Binding(get: { navigation.changesExpanded }, set: { value in
            navigation.changesExpanded = value
            if value && navigation.changes == nil {
                send("read_changes", kind: .readChanges, taskID: task.id)
            }
        })
        return DisclosureGroup("工作区改动", isExpanded: expanded) {
            VStack(alignment: .leading, spacing: 7) {
                Text("当前工作区的未提交改动").font(.caption).foregroundStyle(.secondary)
                if let changes = navigation.changes {
                    TrayChangesContent(changes: changes, selected: navigation.change, diff: navigation.diff,
                                       enabled: canInteract) { entry in
                        if navigation.change?.id == entry.id {
                            navigation.change = nil
                            navigation.diff = nil
                        } else {
                            navigation.change = entry
                            navigation.diff = nil
                            send("read_diff", kind: .readDiff, taskID: task.id, change: entry)
                        }
                    }
                }
                if navigation.pending?.kind == .readChanges { progress("正在读取改动…") }
                if navigation.pending?.kind == .readDiff { progress("正在读取差异…") }
                Button("刷新改动") {
                    navigation.change = nil
                    navigation.diff = nil
                    send("read_changes", kind: .readChanges, taskID: task.id)
                }
                .disabled(!canInteract)
            }.padding(.top, 6)
        }
        .font(.caption.weight(.semibold))
    }

    private func workbenchPage<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        let stack = VStack(alignment: .leading, spacing: 9, content: content)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, isNotch ? 17 : 12)
            .padding(.top, isNotch ? 1 : 12)
            .padding(.bottom, isNotch ? 16 : 12)
        return TrayFitScroll(maxHeight: 338) { stack }
        .environment(\.colorScheme, isNotch ? .dark : colorScheme)
    }

    private var quickReply: some View {
        VStack(alignment: .leading, spacing: isNotch ? 8 : 9) {
            if let task = navigation.task {
                let context = task.workspace.isEmpty ? task.taskStatus.label : task.taskStatus.label + " · " + task.workspace
                VStack(alignment: .leading, spacing: 3) {
                    Text(task.title).font(isNotch ? .system(size: 14, weight: .semibold) : .callout.weight(.semibold))
                        .foregroundStyle(isNotch ? NotchPalette.text : .primary).lineLimit(2)
                    Text(context).font(isNotch ? .system(size: 11) : .caption)
                        .foregroundStyle(isNotch ? NotchPalette.secondary : .secondary).lineLimit(1)
                }
            }
            if let receipt = navigation.receipt {
                Label(receipt, systemImage: "checkmark.circle")
                    .font(.caption.weight(.semibold)).foregroundStyle(statusColor(.working))
            }
            if !connected {
                Text("暂时无法连接，恢复后会重新读取待办")
                    .font(.caption).foregroundStyle(statusColor(.waiting))
            }
            if navigation.promptCount > 1, replyCheck == nil {
                Text("还有 \(navigation.promptCount) 项待处理").font(.caption).foregroundStyle(.secondary)
            }
            if let question = navigation.question, replyCheck == nil {
                VStack(alignment: .leading, spacing: 10) {
                    ForEach(question.parts) { part in replyPart(part, question: question) }
                }
                .disabled(!canInteract || !navigation.promptFresh)
            }
            if let approval = navigation.approval, replyCheck == nil {
                TrayApprovalReview(approval: approval, notch: isNotch, selected: $navigation.approvalSelection) { selected in
                    guard let id = navigation.taskID, canInteract, navigation.promptFresh else { return }
                    send("answer_question", kind: .sendQuestion, taskID: id,
                         answer: .init(id: approval.id, revision: approval.revision, selections: [],
                                       approvalOption: selected))
                }
                .id(approval.id)
                .disabled(!canInteract || !navigation.promptFresh)
            }
            if navigation.pending?.kind == .readPrompt { progress("正在读取待办…") }
            if navigation.pending?.kind == .sendQuestion { progress("正在提交…") }
            if navigation.pending?.kind == .checkAnswer { progress("正在核对原会话…") }
            if let id = navigation.taskID, let check = replyCheck,
               navigation.pending == nil {
                if tray.replyInFlight(id) {
                    progress("正在提交…")
                } else {
                    if navigation.error == nil {
                        Text("上次提交的结果待确认").font(.caption).foregroundStyle(.secondary)
                    }
                    Button("核对回执") {
                        send("check_answer", kind: .checkAnswer, taskID: id, answer: check.answer, since: check.startedAt)
                    }
                    .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: true))
                    .disabled(!canInteract)
                }
            }
            if let error = navigation.error {
                Text(error).font(.caption).foregroundStyle(.secondary)
                if let id = navigation.taskID, navigation.question == nil && navigation.approval == nil && replyCheck == nil {
                    Button("重新读取待办", action: loadPrompt)
                        .buttonStyle(ReplyChoiceStyle(notch: isNotch))
                        .disabled(!canInteract)
                    Button("打开原会话") { openOriginal(id) }
                        .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: true))
                        .disabled(!state.canOpen)
                }
            }
            if navigation.pending == nil && navigation.error == nil &&
                navigation.question == nil && navigation.approval == nil && replyCheck == nil {
                if navigation.promptCount == 0 {
                    Text("当前没有待处理事项").font(.callout)
                    Button("查看任务详情") {
                        if let id = navigation.taskID { openDetail(id, task: navigation.task, keepReceipt: true) }
                    }
                    .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: true))
                } else if let id = navigation.taskID {
                    Text("这项待办需要在原会话中处理").font(.callout)
                    Button("打开原会话") { openOriginal(id) }
                        .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: true)).disabled(!state.canOpen)
                }
            }
        }
        .padding(.horizontal, isNotch ? 17 : 12)
        .padding(.top, isNotch ? 1 : 12)
        .padding(.bottom, isNotch ? 16 : 12)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder private var replySubmit: some View {
        if let question = navigation.question, replyCheck == nil,
           !question.sendsOnSelection || question.parts.contains(where: { $0.acceptsText && draft(for: $0).useOther }) {
            let selections = question.selections(from: navigation.drafts)
            Button("发送") {
                if let selections { submit(question, selections: selections) }
            }
            .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: true))
            .keyboardShortcut(.return, modifiers: .command)
            .help("发送（⌘↵）")
            .disabled(selections == nil || !canInteract || !navigation.promptFresh)
            .frame(maxWidth: .infinity, alignment: .trailing)
            .padding(.horizontal, isNotch ? 17 : 12)
            .padding(.bottom, isNotch ? 12 : 9)
        }
    }

    private func replyPart(_ part: TrayQuestion.Part, question: TrayQuestion) -> some View {
        VStack(alignment: .leading, spacing: 5) {
            Text(part.text).font(isNotch ? .system(size: 13) : .callout)
                .foregroundStyle(isNotch ? NotchPalette.text : .primary)
                .textSelection(.enabled)
                .padding(.vertical, isNotch ? 7 : 8)
                .padding(.horizontal, isNotch ? 9 : 8)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(.white.opacity(isNotch ? 0.044 : 0), in: RoundedRectangle(cornerRadius: 8))
                .background(.primary.opacity(isNotch ? 0 : 0.06), in: RoundedRectangle(cornerRadius: 7))
            if question.sendsOnSelection && part.options.allSatisfy({
                ($0.description ?? "").isEmpty && ($0.preview ?? "").isEmpty
            }) {
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: 5) { presets(part, question: question, detailed: false) }.fixedSize()
                    VStack(spacing: 5) { presets(part, question: question, detailed: true) }
                }
            } else {
                VStack(spacing: 5) { presets(part, question: question, detailed: true) }
            }
            if part.acceptsText && draft(for: part).useOther {
                customReply(part, autofocus: question.sendsOnSelection)
                if draft(for: part).text.utf8.count > 64 * 1024 {
                    Text("回复超过 64 KB，请缩短后发送").font(.caption).foregroundStyle(.secondary)
                }
            }
        }
    }

    @ViewBuilder private func customReply(_ part: TrayQuestion.Part, autofocus: Bool) -> some View {
        let field = TextField(part.placeholder ?? "输入你的回复", text: Binding(
            get: { draft(for: part).text },
            set: { text in updateDraft(for: part) { $0.text = text } }
        ), axis: .vertical)
            .accessibilityLabel("其他回复")
            .focused($focusReply, equals: part.id)
            .task {
                guard autofocus else { return }
                await Task.yield()
                if !Task.isCancelled { focusReply = part.id }
            }
        if isNotch {
            field.lineLimit(1...4).textFieldStyle(.plain)
                .font(.system(size: 13))
                .foregroundStyle(NotchPalette.text)
                .padding(.horizontal, 8).padding(.vertical, 5)
                .frame(minHeight: 31)
                .background(NotchPalette.field, in: RoundedRectangle(cornerRadius: 8))
                .overlay(RoundedRectangle(cornerRadius: 8)
                    .strokeBorder(focusReply == part.id ? NotchPalette.blue : NotchPalette.control.opacity(0.28)))
        } else {
            field.lineLimit(2...4).textFieldStyle(.roundedBorder)
        }
    }

    @ViewBuilder private func presets(_ part: TrayQuestion.Part, question: TrayQuestion, detailed: Bool) -> some View {
        ForEach(part.options) { option in
            let selected = draft(for: part).options.contains(option.id)
            VStack(alignment: .leading, spacing: 3) {
                Button {
                    if question.sendsOnSelection {
                        submit(question, selections: [.init(questionId: part.id, optionIds: [option.id], other: nil)])
                    } else {
                        updateDraft(for: part) { draft in
                            if part.multiSelect {
                                if selected { draft.options.remove(option.id) }
                                else { draft.options.insert(option.id) }
                            } else {
                                draft.options = [option.id]
                                draft.useOther = false
                            }
                        }
                    }
                } label: {
                    HStack(alignment: .firstTextBaseline, spacing: 7) {
                        if !question.sendsOnSelection {
                            Image(systemName: selectionSymbol(selected: selected, multiple: part.multiSelect))
                                .accessibilityHidden(true)
                        }
                        VStack(alignment: .leading, spacing: 2) {
                            Text(option.label)
                            if let description = option.description, !description.isEmpty {
                                Text(description).font(.caption)
                                    .foregroundStyle(isNotch ? NotchPalette.secondary : .secondary)
                            }
                        }
                    }
                    .frame(maxWidth: detailed ? .infinity : nil, alignment: .leading)
                    .padding(.vertical, detailed ? 3 : 0)
                }
                .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: selected))
                .accessibilityAddTraits(selected ? .isSelected : [])
                .help(question.sendsOnSelection ? "点击后立即发送" : "")
                if let preview = option.preview, !preview.isEmpty {
                    DisclosureGroup("预览") {
                        Text(preview).font(.system(.caption, design: .monospaced))
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .font(.caption)
                }
            }
        }
        if part.acceptsText, !part.options.isEmpty {
            let selected = draft(for: part).useOther
            Button {
                updateDraft(for: part) { draft in
                    draft.useOther.toggle()
                    if draft.useOther && !part.multiSelect { draft.options.removeAll() }
                }
                if !selected && !question.sendsOnSelection {
                    Task {
                        await Task.yield()
                        focusReply = part.id
                    }
                }
            } label: {
                HStack(spacing: 7) {
                    if !question.sendsOnSelection {
                        Image(systemName: selectionSymbol(selected: selected, multiple: part.multiSelect))
                            .accessibilityHidden(true)
                    }
                    Text("其他")
                }
                .frame(maxWidth: detailed ? .infinity : nil, alignment: .leading)
            }
            .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: selected))
            .accessibilityAddTraits(selected ? .isSelected : [])
        }
    }

    private func selectionSymbol(selected: Bool, multiple: Bool) -> String {
        if multiple { return selected ? "checkmark.square.fill" : "square" }
        return selected ? "largecircle.fill.circle" : "circle"
    }

    private func draft(for part: TrayQuestion.Part) -> TrayQuestionDraft {
        navigation.drafts[part.id] ?? part.initialDraft
    }

    private func updateDraft(for part: TrayQuestion.Part, _ update: (inout TrayQuestionDraft) -> Void) {
        guard let task = navigation.task, let question = navigation.question else { return }
        var value = draft(for: part)
        update(&value)
        navigation.drafts[part.id] = value
        tray.storeDrafts(navigation.drafts, taskID: task.id, questionID: question.id)
    }

    private func progress(_ label: String) -> some View {
        HStack(spacing: 7) {
            ProgressView().controlSize(.small)
            Text(label).font(.caption).foregroundStyle(.secondary)
        }
    }

    private func openBots() {
        navigate(.bots)
        loadBots()
    }

    private func loadBots() {
        navigation.bots = []
        send("list_bots", kind: .listBots)
    }

    private func openLaunch(from preset: TrayLaunchInput? = nil) {
        guard navigation.pending == nil else { return }
        if preset != nil, case .complete = tray.launchState { tray.beginAnotherLaunch() }
        navigation.usesLaunchPreset = preset != nil && tray.launchState == nil
        navigation.launch = navigation.usesLaunchPreset ? (preset ?? tray.launchDraft) : tray.launchDraft
        navigate(.launch)
        if tray.launchState == nil { loadLaunch() }
    }

    private func startAnotherTask() {
        tray.beginAnotherLaunch()
        openLaunch()
    }

    private func loadLaunch() {
        send("launch_catalog", kind: .launchCatalog)
    }

    private func openOriginal(_ id: String) {
        tray.send("open_task", id, close: true)
        if management { tray.closeManagement() }
    }

    private func openDetail(_ id: String, task: TrayTask?, keepReceipt: Bool = false) {
        guard navigation.pending == nil else { return }
        if page == .launch { navigation.detailOrigin = .launch }
        else if page == .tasks { navigation.detailOrigin = .tasks }
        if navigation.taskID != id {
            navigation.detail = nil
            navigation.detailReadAt = nil
            navigation.detailReadError = nil
            navigation.changes = nil
            navigation.change = nil
            navigation.diff = nil
            navigation.changesExpanded = false
            navigation.followUpExpanded = !(tray.followUpDrafts[id] ?? "").isEmpty
        }
        navigation.followUp = tray.followUpDrafts[id] ?? ""
        if !keepReceipt { navigation.receipt = nil }
        navigation.taskID = id
        navigation.task = task
        navigation.viewedDetailID = nil
        navigate(.detail)
        loadDetail()
    }

    private func loadDetail() {
        navigation.backgroundDetailID = nil
        if let id = navigation.taskID { send("read_task", kind: .readDetail, taskID: id) }
    }

    private func refreshVisibleTask() async {
        guard connected else {
            navigation.backgroundDetailID = nil
            navigation.promptFresh = false
            if navigation.detailReadAt != nil { navigation.detailReadError = "重新连接后会刷新记录" }
            if navigation.pending?.kind == .readPrompt { navigation.pending = nil }
            return
        }
        if page == .quickReply {
            if !navigation.promptFresh && navigation.pending == nil { loadPrompt() }
            return
        }
        guard page == .detail, let taskID = navigation.taskID else { return }
        while !Task.isCancelled, connected, page == .detail, navigation.taskID == taskID {
            if navigation.pending == nil && navigation.backgroundDetailID == nil {
                let id = UUID()
                navigation.backgroundDetailID = id
                tray.request("read_task", id: id, taskID: taskID)
            }
            do { try await Task.sleep(for: .seconds(6)) } catch { return }
        }
    }

    private func applyDetail(_ result: TrayInteractionResult) {
        if let detail = result.detail {
            if let readAt = detail.readAt {
                navigation.detailReadAt = readAt
                navigation.detailReadError = nil
            } else if navigation.detailReadAt != nil {
                navigation.detailReadError = "本次读取未完成，仍显示上次记录"
                return
            }
            navigation.detail = detail
            navigation.task = detail.task
            if navigation.viewedDetailID != detail.task.id {
                navigation.viewedDetailID = detail.task.id
                tray.recordTaskView(detail.task.id)
            }
        } else if let message = result.message {
            navigation.detailReadError = message
        }
    }

    private func openQuestion(_ task: TrayTask) {
        guard navigation.pending == nil else { return }
        tray.recordTaskView(task.id)
        navigation.origin = page == .detail ? .detail : .tasks
        navigation.task = task
        navigation.taskID = task.id
        navigation.receipt = nil
        navigation.error = nil
        navigate(.quickReply)
        loadPrompt()
    }

    private func loadPrompt() {
        guard let id = navigation.taskID, navigation.pending == nil else { return }
        navigation.promptFresh = false
        navigation.question = nil
        navigation.approval = nil
        navigation.approvalSelection = nil
        navigation.promptCount = 0
        navigation.drafts = [:]
        focusReply = nil
        if replyCheck == nil { send("read_prompt", kind: .readPrompt, taskID: id) }
    }

    private func submit(_ question: TrayQuestion, selections: [TrayQuestionAnswer.Selection]) {
        guard let id = navigation.taskID, canInteract, navigation.promptFresh, replyCheck == nil else { return }
        let answer = TrayQuestionAnswer(id: question.id, revision: question.revision, selections: selections)
        send("answer_question", kind: .sendQuestion, taskID: id, answer: answer)
    }

    private func send(_ action: String, kind: TrayNavigation.Pending.Kind, taskID: String? = nil,
                      botID: String? = nil, answer: TrayQuestionAnswer? = nil, since: Int64? = nil,
                      text: String? = nil, launch: TrayLaunchInput? = nil, launchCheck: TrayLaunchLookup? = nil,
                      operationID: String? = nil, change: TrayChange? = nil) {
        guard navigation.pending == nil, tray.canSubmit(action, taskID: taskID) else { return }
        let id = UUID()
        navigation.error = nil
        navigation.pending = .init(id: id, kind: kind)
        tray.request(action, id: id, taskID: taskID, botID: botID, answer: answer,
                     since: since, text: text, launch: launch, launchCheck: launchCheck,
                     operationID: operationID, change: change)
    }

    private func receive(_ result: TrayInteractionResult) {
        if navigation.backgroundDetailID == result.requestID {
            navigation.backgroundDetailID = nil
            if page == .detail && connected { applyDetail(result) }
            return
        }
        guard let pending = navigation.pending, pending.id == result.requestID else { return }
        navigation.pending = nil
        navigation.error = result.message
        if pending.kind == .readDetail {
            applyDetail(result)
            if navigation.detail != nil { navigation.error = nil }
            return
        }
        if result.reopenRequired == true {
            navigation.question = nil
            navigation.approval = nil
        }
        guard result.message == nil else { return }
        switch pending.kind {
        case .listBots:
            navigation.bots = result.bots ?? []
        case .openBot, .openLaunchedSurface:
            if management { tray.closeManagement() }
            else { tray.close() }
        case .readPrompt:
            navigation.promptFresh = connected && result.prompt != nil
            navigation.promptCount = result.prompt?.count ?? 0
            navigation.approval = result.prompt?.approval
            if let id = navigation.taskID, let question = result.prompt?.question {
                navigation.drafts = tray.drafts(taskID: id, questionID: question.id)
                navigation.question = question
            }
        case .sendQuestion, .checkAnswer:
            navigation.receipt = result.receipt ?? (pending.kind == .checkAnswer ? "已在原会话核对到记录" : "已提交")
            loadPrompt()
        case .readDetail:
            break
        case .sendFollowUp, .checkFollowUp:
            navigation.followUp = ""
            navigation.followUpExpanded = false
            loadDetail()
        case .launchCatalog:
            if let catalog = result.catalog {
                navigation.catalog = catalog
                if !catalog.workspaces.contains(where: { $0.id == navigation.launch.workspaceID }) {
                    navigation.launch.workspaceID = !navigation.usesLaunchPreset && catalog.workspaces.count == 1
                        ? catalog.workspaces[0].id : ""
                }
                if !catalog.agents.contains(navigation.launch.agent) {
                    let preferred = catalog.defaultAgent ?? ""
                    navigation.launch.agent = navigation.usesLaunchPreset ? ""
                        : (catalog.agents.contains(preferred) ? preferred : (catalog.agents.first ?? ""))
                }
                if !navigation.usesLaunchPreset { tray.launchDraft = navigation.launch }
            }
        case .readChanges:
            navigation.changes = result.changes
        case .readDiff:
            navigation.diff = result.diff
        case .launch, .checkLaunch:
            break
        }
    }

    private func back() {
        guard navigation.pending?.kind.writing != true else { return }
        navigation.pending = nil
        navigation.backgroundDetailID = nil
        navigation.error = nil
        navigation.receipt = nil
        switch page {
        case .bots:
            navigate(.launch, back: true)
        case .quickReply:
            navigate(navigation.origin, back: true)
        case .detail:
            navigate(navigation.detailOrigin, back: true)
        case .wechat:
            navigate(.notifications, back: true)
        case .settings, .launch:
            navigate(.tasks, back: true)
        default:
            navigate(.settings, back: true)
        }
    }

    private func navigate(_ newPage: TrayNavigation.Page, back: Bool = false) {
        navigation.back = back
        if newPage == .computerUse { navigation.computerUseDraft = computerUse }
        if newPage == .shortcuts { navigation.shortcut = state.globalShortcut ?? "" }
        navigation.page = newPage
    }

    private func row(_ title: String, detail: String = "", next: Bool = false,
                     enabled: Bool = true, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack {
                Text(title)
                Spacer()
                if !detail.isEmpty { Text(detail).foregroundStyle(.secondary).font(.caption) }
                if next { Image(systemName: "chevron.right").font(.caption2).foregroundStyle(.secondary) }
            }
            .padding(.horizontal, 7).frame(minHeight: 27)
            .contentShape(Rectangle())
        }
        .buttonStyle(.borderless)
        .disabled(!enabled)
    }

    private func confirm(_ title: String, _ message: String, _ action: String) {
        navigation.confirmation = .init(title: title, message: message, action: action)
    }

    private var settings: some View {
        Group {
            if management && !state.windowOpen {
                row("打开 Orca", enabled: state.canOpen) { tray.send("open") }
            }
            row("开机启动", detail: state.autostart ? "已开启" : "已关闭", enabled: !state.busy) { tray.send("autostart") }
            if state.windowOpen { row("关闭 Orca 窗口") { tray.send("close_orca") } }
            Divider()
            row("连接官方 Orca…", enabled: !state.busy) { tray.send("open_paired", close: true) }
            row("从官方 Orca 导入…", enabled: !state.busy) {
                confirm("从官方 Orca 导入", "会先备份原数据，并重启内核。正在运行的任务会结束。", "import_official")
            }
            Divider()
            row("任务入口", next: true) {
                if management { navigate(.entries) }
                else { tray.showManagement(.entries) }
            }
            row("快捷键", next: true) { navigate(.shortcuts) }
            row("通知", next: true) { navigate(.notifications) }
            row("电脑操控", detail: computerUse.backend.label, next: true) {
                if management { navigate(.computerUse) }
                else { tray.showManagement(.computerUse) }
            }
            row("关于与更新", next: true) { navigate(.about) }
            row("内核维护", next: true) { navigate(.advanced) }
            Divider()
            row("退出 Orca-Wails") { confirm("退出 Orca-Wails", "退出会停止后台运行的任务。", "quit") }
                .foregroundStyle(.red)
        }
    }

    private var entrySettings: some View {
        VStack(alignment: .leading, spacing: 12) {
            entryToggle("菜单栏猫标", enabled: tray.entries.menuBar, action: "menu_bar")
            entryToggle("刘海面板", enabled: tray.entries.notch, action: "notch")
            Text(tray.notchAvailable ? "也可以从 Orca 的「会话」菜单查看任务。" : "连接带刘海的显示器后显示刘海面板。")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .toggleStyle(.switch)
        .padding(.horizontal, 7)
        .padding(.vertical, 5)
    }

    private var shortcutSettings: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("打开或关闭 Orca").font(.callout.weight(.semibold))
            TextField("例如 Command+Shift+O", text: $navigation.shortcut)
                .textFieldStyle(.roundedBorder)
                .accessibilityLabel("打开或关闭 Orca 的全局快捷键")
            Button("保存快捷键") { tray.send("global_shortcut", navigation.shortcut) }
                .buttonStyle(ReplyChoiceStyle(notch: isNotch, primary: true))
                .disabled(state.busy || navigation.shortcut.trimmingCharacters(in: .whitespaces).isEmpty ||
                          navigation.shortcut == state.globalShortcut)
        }
        .padding(.horizontal, 7)
        .padding(.vertical, 5)
    }

    private func entryToggle(_ title: String, enabled: Bool, action: String) -> some View {
        HStack {
            Text(title).accessibilityHidden(true)
            Spacer(minLength: 8)
            Toggle(title, isOn: Binding(
                get: { enabled },
                set: { tray.send(action + ($0 ? "_on" : "_off")) }
            ))
            .labelsHidden()
            .accessibilityLabel(title)
        }
    }

    private var computerUseToSave: TrayComputerUseSettings {
        let draft = navigation.computerUseDraft
        let alias = draft.vmSSH.trimmingCharacters(in: .whitespacesAndNewlines)
        return .init(backend: draft.backend, vmSSH: alias)
    }

    private var canSaveComputerUse: Bool {
        let draft = computerUseToSave
        return draft != computerUse && (draft.backend != .cuaVM || !draft.vmSSH.isEmpty)
    }

    private var computerUseSettings: some View {
        VStack(alignment: .leading, spacing: 12) {
            Picker("默认后端", selection: $navigation.computerUseDraft.backend) {
                ForEach(TrayComputerUseSettings.Backend.allCases, id: \.self) { backend in
                    Text(backend.label).tag(backend)
                }
            }
            LabeledContent("Lume SSH 别名") {
                TextField("Lume SSH 别名", text: $navigation.computerUseDraft.vmSSH, prompt: Text("orca-lume"))
                    .labelsHidden()
                    .onSubmit(saveComputerUse)
            }
            Text("Agent 可以为单次命令指定后端。先在 ~/.ssh/config 配置来宾的主机别名，即可按需使用 Lume。")
                .font(.caption)
                .foregroundStyle(.secondary)
            Button("保存", action: saveComputerUse)
                .buttonStyle(.borderedProminent)
                .disabled(!canSaveComputerUse)
        }
        .padding(.horizontal, 7)
        .padding(.vertical, 5)
    }

    private func saveComputerUse() {
        guard canSaveComputerUse, let data = try? JSONEncoder().encode(computerUseToSave) else { return }
        tray.send("computer_use", String(decoding: data, as: UTF8.self))
    }

    private var about: some View {
        Group {
            LabeledContent("外壳版本", value: state.appVersion)
            LabeledContent("内核版本", value: state.coreVersion.isEmpty ? "未安装" : state.coreVersion)
            Divider()
            if state.checkApp {
                if state.pendingApp.isEmpty {
                    row("检查应用更新", enabled: !state.busy) { tray.send("check_app") }
                } else {
                    row("安装应用更新", detail: state.pendingApp, enabled: !state.busy) {
                        confirm("安装应用更新", "会下载并安装更新，然后重启 Orca。", "install_app")
                    }
                }
            }
            if state.checkCore && !state.bundledCore {
                if state.pendingCore.isEmpty {
                    row("检查内核更新", enabled: !state.busy) { tray.send("check_core") }
                } else {
                    row("安装内核更新", detail: state.pendingCore, enabled: !state.busy) {
                        confirm("安装内核更新", "更新会重启此内核，并结束正在运行的任务。", "install_core")
                    }
                }
            }
            row("查询官方最新版本", detail: state.officialVersion, enabled: !state.busy) { tray.send("check_official") }
        }
    }

    private var advanced: some View {
        Group {
            if state.running {
                row("重启内核 / 重新配对", enabled: !state.busy) {
                    confirm("重启内核", "重启会结束此内核正在运行的任务。", "restart")
                }
            } else {
                row("启动内核", enabled: !state.busy && state.coreInstalled) { tray.send("start") }
            }
            row("停止内核", enabled: !state.busy && state.running) {
                confirm("停止内核", "停止会结束此内核正在运行的任务。", "stop")
            }
            if !state.bundledCore {
                row("回退内核", enabled: !state.busy && state.rollbackAvailable) {
                    confirm("回退内核", "回退会结束此内核正在运行的任务。", "rollback")
                }
            }
            Divider()
            row("打开配置文件") { tray.send("config", close: true) }
            row("打开日志目录") { tray.send("logs", close: true) }
            row("打开数据目录") { tray.send("data", close: true) }
        }
    }
}

private enum NotchPalette {
    static let text = Color(red: 240.0 / 255, green: 244.0 / 255, blue: 253.0 / 255)
    static let secondary = Color(red: 174.0 / 255, green: 185.0 / 255, blue: 204.0 / 255)
    static let control = Color(red: 203.0 / 255, green: 215.0 / 255, blue: 234.0 / 255)
    static let amber = Color(red: 1, green: 215.0 / 255, blue: 146.0 / 255)
    static let red = Color(red: 1, green: 152.0 / 255, blue: 165.0 / 255)
    static let blue = Color(red: 154.0 / 255, green: 212.0 / 255, blue: 1)
    static let mint = Color(red: 184.0 / 255, green: 226.0 / 255, blue: 204.0 / 255)
    static let waitingFill = Color(red: 0.60, green: 0.46, blue: 0.26).opacity(0.125)
    static let replyFill = Color(red: 0.64, green: 0.45, blue: 0.21).opacity(0.21)
    static let replyBorder = Color(red: 0.85, green: 0.69, blue: 0.43).opacity(0.19)
    static let separator = Color(red: 0.73, green: 0.8, blue: 0.94).opacity(0.09)
    static let field = Color(red: 12.0 / 255, green: 17.0 / 255, blue: 27.0 / 255)
}

struct ReplyChoiceStyle: PrimitiveButtonStyle {
    let notch: Bool
    var primary = false
    @Environment(\.isEnabled) private var enabled

    func makeBody(configuration: Configuration) -> some View {
        if notch {
            Button(action: configuration.trigger) {
                configuration.label
                    .font(.system(size: 13))
                    .foregroundStyle(primary ? NotchPalette.amber : NotchPalette.text)
                    .padding(.horizontal, 9)
                    .padding(.vertical, 5)
                    .frame(minHeight: primary ? 31 : 28)
                    .background(primary ? NotchPalette.replyFill : NotchPalette.control.opacity(0.11),
                                in: RoundedRectangle(cornerRadius: 8))
                    .overlay(RoundedRectangle(cornerRadius: 8)
                        .strokeBorder(primary ? NotchPalette.replyBorder : NotchPalette.control.opacity(0.12)))
                    .opacity(enabled ? 1 : 0.45)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
        } else {
            Button(configuration).buttonStyle(.bordered)
        }
    }
}

private struct TaskRowHover: ViewModifier {
    let ink: Color
    @Environment(\.isEnabled) private var enabled
    private var hovered = State(initialValue: false)

    func body(content: Content) -> some View {
        content
            .background(ink.opacity(enabled && hovered.wrappedValue ? 0.10 : 0), in: RoundedRectangle(cornerRadius: 8))
            .background {
                if enabled {
                    TaskRowTrackingArea { hovered.wrappedValue = $0 }
                }
            }
            .onChange(of: enabled) { _ in hovered.wrappedValue = false }
    }
}

private struct TaskRowTrackingArea: NSViewRepresentable {
    let onHover: (Bool) -> Void

    func makeNSView(context: Context) -> TrackingView {
        let view = TrackingView()
        // The notch panel can open on hover while another window is key.
        let options: NSTrackingArea.Options = [.activeAlways, .inVisibleRect, .mouseEnteredAndExited, .mouseMoved]
        view.addTrackingArea(NSTrackingArea(rect: .zero, options: options, owner: view, userInfo: nil))
        return view
    }

    func updateNSView(_ view: TrackingView, context: Context) { view.onHover = onHover }

    final class TrackingView: NSView {
        var onHover: ((Bool) -> Void)?
        private var hovered = false

        override func hitTest(_ point: NSPoint) -> NSView? { nil }

        override func mouseEntered(with event: NSEvent) {
            hovered = true
            NSCursor.pointingHand.set()
            onHover?(true)
        }

        override func mouseMoved(with event: NSEvent) { NSCursor.pointingHand.set() }

        override func mouseExited(with event: NSEvent) {
            clearCursor()
            onHover?(false)
        }

        override func viewWillMove(toWindow newWindow: NSWindow?) {
            if newWindow == nil { clearCursor() }
            super.viewWillMove(toWindow: newWindow)
        }

        private func clearCursor() {
            if hovered && NSCursor.current == .pointingHand { NSCursor.arrow.set() }
            hovered = false
        }
    }
}

private struct TaskRowArrival: ViewModifier {
    let animated: Bool
    let delay: Double
    private var appeared = State(initialValue: false)

    func body(content: Content) -> some View {
        content
            .opacity(appeared.wrappedValue || !animated ? 1 : 0.25)
            .offset(y: appeared.wrappedValue || !animated ? 0 : -4)
            .onAppear {
                if animated {
                    withAnimation(.easeOut(duration: 0.16).delay(delay)) { appeared.wrappedValue = true }
                } else {
                    appeared.wrappedValue = true
                }
            }
    }
}

private struct TaskStatusMark: View {
    let status: TrayTask.Status
    let color: Color
    let notch: Bool
    let animated: Bool
    let outcome: String?

    var body: some View {
        Group {
            if notch {
                Group {
                    if animated && (status == .working || status == .monitoring) {
                        TimelineView(.animation(minimumInterval: 1.0 / 30)) { context in
                            notchGlyph(at: context.date.timeIntervalSinceReferenceDate)
                        }
                    } else {
                        notchGlyph()
                    }
                }
                .frame(width: 24, height: 24)
                .background {
                    if status != .done && status != .unknown {
                        RoundedRectangle(cornerRadius: 7).fill(color.opacity(0.15))
                    }
                }
            } else {
                glyph(size: 11).frame(width: 15, height: 16)
            }
        }
        .accessibilityHidden(true)
    }

    @ViewBuilder
    private func notchGlyph(at time: TimeInterval? = nil) -> some View {
        switch status {
        case .working:
            ZStack {
                if let time {
                    let phase = (1 - cos(time * .pi / 1.15)) / 2
                    Circle().strokeBorder(color.opacity(0.58 - 0.40 * phase), lineWidth: 1.25)
                        .frame(width: 17, height: 17).scaleEffect(1 + 0.34 * phase)
                }
                Circle().strokeBorder(color, lineWidth: 1.75).frame(width: 15, height: 15)
                Circle().fill(color).frame(width: 6, height: 6)
            }
        case .monitoring:
            ZStack {
                glyph(size: 12)
                if let time {
                    let angle = time.truncatingRemainder(dividingBy: 2.8) / 2.8 * 360
                    Circle().trim(from: 0, to: 0.23)
                        .stroke(color, style: StrokeStyle(lineWidth: 2, lineCap: .round))
                        .frame(width: 19, height: 19).rotationEffect(.degrees(angle))
                }
            }
        default:
            glyph(size: 13)
        }
    }

    private func glyph(size: CGFloat) -> some View {
        let symbol = if status == .done {
            switch outcome {
            case "success": "checkmark"
            case "failure": "xmark"
            case "interruption", "cancellation", "superseded": "stop.fill"
            default: "minus"
            }
        } else { status.symbol }
        return Image(systemName: symbol)
            .font(.system(size: size, weight: .semibold))
            .symbolRenderingMode(.monochrome)
            .foregroundStyle(color)
    }
}

private extension TrayTask.Status {
    var symbol: String {
        switch self {
        case .working: "circle.fill"
        case .monitoring: "scope"
        case .waiting: "pause.fill"
        case .blocked: "exclamationmark.octagon"
        case .done: "checkmark"
        case .unknown: "questionmark"
        }
    }
}

private struct NotificationSettings: View {
    @ObservedObject var tray: Tray
    @ObservedObject var notifications: Notifications

    var body: some View {
        Group {
            Toggle("任务与应用通知", isOn: Binding(
                get: { tray.snapshot.notifications },
                set: { tray.send("notifications", $0 ? "on" : "off") }
            )).disabled(tray.snapshot.busy)
            Toggle("播放声音", isOn: $notifications.sound).disabled(!tray.snapshot.notifications)
                .help("任务提示音由 Orca 播放，可独立于系统通知授权使用。")
            Menu("试听任务提示音") {
                ForEach(Notifications.TaskSound.allCases, id: \.self) { cue in
                    Button(cue.title) { notifications.playTaskSounds([cue]) }
                }
            }
            .disabled(!tray.snapshot.notifications || !notifications.sound)
            Divider()
            LabeledContent("系统权限", value: notifications.status)
            if notifications.canRequest {
                Button("请求通知权限") { notifications.requestPermission() }.disabled(!tray.snapshot.notifications)
            }
            Button("打开系统通知设置") { notifications.openSystemSettings() }
            Button("发送测试通知") { notifications.send(title: "Orca-Wails", body: "这是一条测试通知。") }
                .disabled(!tray.snapshot.notifications || !notifications.authorized)
        }
        .padding(.horizontal, 7)
        .task { await notifications.refresh() }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
            Task { await notifications.refresh() }
        }
    }
}
