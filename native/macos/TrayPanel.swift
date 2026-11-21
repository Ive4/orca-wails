import SwiftUI

@MainActor
final class TrayNavigation: ObservableObject {
    enum Page { case tasks, bots, quickReply, settings, entries, notifications, about, advanced }
    struct Pending {
        enum Kind { case listBots, openBot, readQuestion, sendQuestion }
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
    @Published var confirmation: Confirmation?
    @Published var pending: Pending?
    @Published var bots: [TrayBot] = []
    @Published var task: TrayTask?
    @Published var question: TrayQuestion?
    @Published var draft: String = ""
    @Published var other = false
    @Published var error: String?
    @Published var receipt: String?
    var back = false
}

struct TrayPanel: View {
    @ObservedObject var tray: Tray
    @StateObject private var navigation: TrayNavigation
    private let management: Bool
    private let notchWidth: CGFloat?
    var onHeightChange: ((CGFloat) -> Void)?
    @FocusState private var focusReply: Bool
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency
    @Environment(\.colorScheme) private var colorScheme
    private var state: TraySnapshot { tray.snapshot }
    private var page: TrayNavigation.Page { navigation.page }
    private var isNotch: Bool { notchWidth != nil }
    private var transitionAnimation: Animation? { reduceMotion ? nil : .easeOut(duration: 0.16) }

    init(tray: Tray, page: TrayNavigation.Page = .tasks, management: Bool = false,
         notchWidth: CGFloat? = nil, question: TrayTask? = nil) {
        self.tray = tray
        self.management = management
        self.notchWidth = notchWidth
        let navigation = TrayNavigation()
        navigation.page = page
        navigation.scope = notchWidth == nil ? .current : .all
        navigation.task = question
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
            if page == .quickReply, let task = navigation.task { openQuestion(task) }
        }
        .onReceive(tray.$interaction) { if let result = $0 { receive(result) } }
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
                icon("plus.bubble", "新建 Bot 对话") { openBots() }
                    .keyboardShortcut("t", modifiers: [.command, .option])
                    .disabled(!state.running || state.busy)
                icon("macwindow", "打开窗口") { tray.send("open", close: true) }
                    .disabled(!state.canOpen)
                moreMenu
            } else {
                if !management || page != .settings {
                    icon("chevron.left", "返回") { back() }
                        .disabled(navigation.pending?.kind == .sendQuestion || navigation.pending?.kind == .openBot)
                }
                Text(pageTitle).font(isNotch ? .system(size: 13, weight: .semibold) : .headline)
                    .foregroundStyle(isNotch ? NotchPalette.text : .primary)
                Spacer()
                if page == .quickReply, let task = navigation.task {
                    icon("macwindow", "打开原会话") { tray.send("open_task", task.id, close: true) }
                        .disabled(!state.canOpen || navigation.pending?.kind == .sendQuestion)
                }
            }
        }
    }

    private var moreMenu: some View {
        Menu {
            Button("机器人") { tray.send("open_bots", close: true) }
                .disabled(!state.canOpen)
            Divider()
            Button("设置") { navigate(.settings) }
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
        case .quickReply:
            ViewThatFits(in: .vertical) {
                quickReply.fixedSize(horizontal: false, vertical: true)
                ScrollView { quickReply }
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
            case .notifications: NotificationSettings(tray: tray, notifications: tray.notifications)
            case .about: about
            case .advanced: advanced
            case .tasks, .bots, .quickReply: EmptyView()
            }
            if !state.message.isEmpty {
                Text(state.message).font(.caption).foregroundStyle(.secondary).padding(.horizontal, 7)
            }
        }
        .padding(10)
    }

    private func taskList(scope: TraySnapshot.TaskScope) -> some View {
        let rows = state.taskRows(scope)
        let notchRowsHeight = rows.reduce(CGFloat(0)) { height, task in height + notchRowHeight(task) }
        return VStack(spacing: 4) {
            if state.taskState == "ready", !rows.isEmpty {
                ScrollView {
                    LazyVStack(spacing: isNotch ? 0 : 2) {
                        ForEach(Array(rows.enumerated()), id: \.element.id) { index, task in
                            taskRow(task)
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
                    Text(emptyMessage(scope: scope)).foregroundStyle(isNotch ? NotchPalette.secondary : .secondary)
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
    }

    private func notchRowHeight(_ task: TrayTask) -> CGFloat { task.taskStatus == .waiting ? 51 : 50 }

    private func taskRow(_ task: TrayTask) -> some View {
        let status = task.taskStatus
        return HStack(spacing: isNotch ? 7 : 4) {
            Button {
                tray.send("open_task", task.id, close: true)
                if management { tray.closeManagement() }
            } label: {
                HStack(spacing: isNotch ? 7 : 8) {
                    TaskStatusMark(status: status, color: statusColor(status),
                                   animated: isNotch && !reduceMotion)
                        .frame(width: isNotch ? 17 : 15)
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
                    VStack(alignment: .trailing, spacing: 2) {
                        Text(status.label).font(isNotch ? .system(size: 11) : .caption)
                            .foregroundStyle(isNotch ? NotchPalette.secondary : .primary.opacity(0.85))
                        if let time = task.startedAt ?? task.updatedAt {
                            Text(Date(timeIntervalSince1970: Double(time) / 1000), style: .relative)
                                .font(.caption2).monospacedDigit()
                                .foregroundStyle(isNotch ? NotchPalette.secondary : .secondary).lineLimit(1)
                        }
                    }
                    .layoutPriority(1)
                }
                .padding(.horizontal, isNotch ? 0 : 7).frame(minHeight: isNotch ? notchRowHeight(task) : 42)
                .contentShape(Rectangle())
            }
            .buttonStyle(.borderless)
            .disabled(!state.canOpen)
            .help(task.title)
            if task.quickReply == true {
                if isNotch {
                    Button { openQuestion(task) } label: {
                        Text("回复").font(.system(size: 13))
                            .padding(.horizontal, 10).frame(height: 31)
                            .foregroundStyle(NotchPalette.amber)
                            .background(NotchPalette.replyFill, in: RoundedRectangle(cornerRadius: 8))
                            .overlay(RoundedRectangle(cornerRadius: 8).strokeBorder(NotchPalette.replyBorder))
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .disabled(!state.running || state.busy)
                    .accessibilityLabel("快速回复：\(task.title)")
                } else {
                    Button("回复") { openQuestion(task) }
                        .controlSize(.small)
                        .buttonStyle(.bordered)
                        .disabled(!state.running || state.busy)
                        .accessibilityLabel("快速回复：\(task.title)")
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
        case .notifications: "通知"
        case .bots: "新建 Bot 对话"
        case .quickReply: "快速回复"
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

    private var quickReply: some View {
        VStack(alignment: .leading, spacing: isNotch ? 8 : 9) {
            if let task = navigation.task {
                if isNotch {
                    VStack(alignment: .leading, spacing: 3) {
                        Text(task.title).font(.system(size: 14, weight: .semibold))
                            .foregroundStyle(NotchPalette.text).lineLimit(2)
                        Text(task.workspace.isEmpty ? "等待输入" : "等待输入 · " + task.workspace)
                            .font(.system(size: 11)).foregroundStyle(NotchPalette.secondary).lineLimit(1)
                    }
                } else {
                    Text(task.title).font(.callout.weight(.semibold)).lineLimit(2)
                    Label(task.workspace.isEmpty ? "等待输入" : task.workspace + " · 等待输入",
                          systemImage: "pause.fill")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
            }
            if let question = navigation.question {
                Text(question.text).font(isNotch ? .system(size: 13) : .callout)
                    .foregroundStyle(isNotch ? NotchPalette.text : .primary)
                    .textSelection(.enabled)
                    .padding(.vertical, isNotch ? 7 : 8)
                    .padding(.horizontal, isNotch ? 9 : 8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(.white.opacity(isNotch ? 0.044 : 0), in: RoundedRectangle(cornerRadius: 8))
                    .background(.primary.opacity(isNotch ? 0 : 0.06), in: RoundedRectangle(cornerRadius: 7))
                VStack(alignment: .leading, spacing: 5) {
                    if question.options.contains(where: { !($0.description ?? "").isEmpty }) {
                        VStack(spacing: 5) { presets(question, detailed: true) }
                    } else {
                        ViewThatFits(in: .horizontal) {
                            HStack(spacing: 5) { presets(question, detailed: false) }.fixedSize()
                            VStack(spacing: 5) { presets(question, detailed: true) }
                        }
                    }
                    if question.acceptsText {
                        if navigation.other {
                            customReply(question)
                            if navigation.draft.utf8.count > 64 * 1024 {
                                Text("回复超过 64 KB，请缩短后发送").font(.caption).foregroundStyle(.secondary)
                            }
                        }
                    }
                }
                .disabled(navigation.pending != nil || !state.running || state.busy)
            }
            if navigation.pending?.kind == .readQuestion { progress("正在读取问题…") }
            if navigation.pending?.kind == .sendQuestion { progress("正在发送…") }
            if let error = navigation.error {
                Text(error).font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(.horizontal, isNotch ? 17 : 12)
        .padding(.top, isNotch ? 1 : 12)
        .padding(.bottom, isNotch ? 16 : 12)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder private func customReply(_ question: TrayQuestion) -> some View {
        let field = TextField(question.placeholder ?? "输入你的回复", text: replyText(question), axis: .vertical)
            .accessibilityLabel("其他回复")
            .focused($focusReply)
            .task {
                await Task.yield()
                if !Task.isCancelled { focusReply = true }
            }
        let send = Button("发送") { submit(question, text: navigation.draft) }
            .keyboardShortcut(.return, modifiers: .command)
            .help("发送（⌘↵）")
            .disabled(!canSendCustom(question))
        if isNotch {
            HStack(alignment: .bottom, spacing: 7) {
                field.lineLimit(1...4).textFieldStyle(.plain)
                    .font(.system(size: 13))
                    .foregroundStyle(NotchPalette.text)
                    .padding(.horizontal, 8).padding(.vertical, 5)
                    .frame(minHeight: 31)
                    .background(NotchPalette.field, in: RoundedRectangle(cornerRadius: 8))
                    .overlay(RoundedRectangle(cornerRadius: 8)
                        .strokeBorder(focusReply ? NotchPalette.blue : NotchPalette.control.opacity(0.28)))
                send.buttonStyle(ReplyChoiceStyle(notch: true, primary: true))
            }
            .padding(.top, 3)
        } else {
            field.lineLimit(2...4).textFieldStyle(.roundedBorder)
            send.buttonStyle(.borderedProminent)
        }
    }

    @ViewBuilder private func presets(_ question: TrayQuestion, detailed: Bool) -> some View {
        ForEach(question.options) { option in
            Button {
                submit(question, option: option.id)
            } label: {
                if detailed {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(option.label)
                        if let description = option.description, !description.isEmpty {
                            Text(description).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.vertical, 3)
                } else {
                    Text(option.label)
                }
            }
            .buttonStyle(ReplyChoiceStyle(notch: isNotch))
            .help("点击后立即发送")
        }
        if question.acceptsText, !question.options.isEmpty {
            Button {
                navigation.other.toggle()
            } label: {
                Text("其他").frame(maxWidth: detailed ? .infinity : nil, alignment: .leading)
            }
            .buttonStyle(ReplyChoiceStyle(notch: isNotch))
        }
    }

    private func replyText(_ question: TrayQuestion) -> Binding<String> {
        Binding(get: { navigation.draft }, set: { text in
            navigation.draft = text
            if let task = navigation.task {
                tray.storeDraft(text, taskID: task.id, question: question)
            }
        })
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

    private func openQuestion(_ task: TrayTask) {
        navigation.task = task
        navigation.question = nil
        navigation.other = false
        navigation.draft = ""
        navigate(.quickReply)
        send("read_question", kind: .readQuestion, taskID: task.id)
    }

    private func canSendCustom(_ question: TrayQuestion) -> Bool {
        navigation.draft.utf8.count <= 64 * 1024 &&
            (question.allowEmpty || !navigation.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
    }

    private func submit(_ question: TrayQuestion, option: String? = nil, text: String? = nil) {
        guard let task = navigation.task, state.running, text == nil || canSendCustom(question) else { return }
        let answer = TrayQuestionAnswer(id: question.id, revision: question.revision, optionId: option, text: text)
        send("answer_question", kind: .sendQuestion, taskID: task.id, answer: answer)
    }

    private func send(_ action: String, kind: TrayNavigation.Pending.Kind, taskID: String? = nil,
                      botID: String? = nil, answer: TrayQuestionAnswer? = nil) {
        guard navigation.pending == nil else { return }
        let id = UUID()
        navigation.error = nil
        navigation.pending = .init(id: id, kind: kind)
        tray.request(action, id: id, taskID: taskID, botID: botID, answer: answer)
    }

    private func receive(_ result: TrayInteractionResult) {
        guard let pending = navigation.pending, pending.id == result.requestID else { return }
        navigation.pending = nil
        navigation.error = result.message
        if result.reopenRequired == true { navigation.question = nil }
        guard result.message == nil else { return }
        switch pending.kind {
        case .listBots:
            navigation.bots = result.bots ?? []
        case .openBot:
            if management { tray.closeManagement() }
            else { tray.close() }
        case .readQuestion:
            if let task = navigation.task, let question = result.question {
                let draft = tray.draft(taskID: task.id, question: question)
                navigation.draft = draft ?? question.initialText ?? ""
                navigation.other = question.acceptsText && (draft != nil || question.options.isEmpty)
                navigation.question = question
            }
        case .sendQuestion:
            navigation.receipt = "已发送"
            if notchWidth != nil { navigation.scope = .all }
            navigate(.tasks, back: true)
        }
    }

    private func back() {
        guard navigation.pending?.kind != .sendQuestion, navigation.pending?.kind != .openBot else { return }
        navigation.pending = nil
        navigation.error = nil
        switch page {
        case .settings, .bots, .quickReply:
            navigate(.tasks, back: true)
        default:
            navigate(.settings, back: true)
        }
    }

    private func navigate(_ newPage: TrayNavigation.Page, back: Bool = false) {
        navigation.back = back
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
            row("通知", next: true) { navigate(.notifications) }
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

private struct ReplyChoiceStyle: PrimitiveButtonStyle {
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
    let animated: Bool

    var body: some View {
        Group {
            if animated && (status == .working || status == .monitoring) {
                TimelineView(.animation(minimumInterval: 1.0 / 30)) { context in
                    let time = context.date.timeIntervalSinceReferenceDate
                    if status == .working {
                        let phase = (1 - cos(time * .pi / 1.15)) / 2
                        ZStack {
                            Circle().strokeBorder(color.opacity(0.42 * (1 - phase)), lineWidth: 1)
                                .frame(width: 8, height: 8).scaleEffect(1 + 0.75 * phase)
                            Circle().fill(color.opacity(0.7 + 0.3 * phase)).frame(width: 6, height: 6)
                        }
                    } else {
                        let angle = time.truncatingRemainder(dividingBy: 2.8) / 2.8 * 360
                        ZStack {
                            glyph
                            Circle().fill(color).frame(width: 3, height: 3)
                                .offset(y: -6).rotationEffect(.degrees(angle))
                        }
                    }
                }
            } else {
                glyph
            }
        }
        .frame(width: 15, height: 16)
        .accessibilityHidden(true)
    }

    private var glyph: some View {
        Image(systemName: status.symbol)
            .font(.system(size: 11, weight: .semibold))
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
