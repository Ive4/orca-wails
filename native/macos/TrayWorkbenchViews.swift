import SwiftUI

struct TrayApprovalReview: View {
    let approval: TrayApproval
    let notch: Bool
    @Binding var selected: String?
    let submit: (String) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(approval.title).font(.callout.weight(.semibold))
            if !approval.toolName.isEmpty, approval.toolName != approval.title {
                LabeledContent("工具", value: approval.toolName).font(.caption)
            }
            if !approval.description.isEmpty { Text(approval.description).font(.caption).textSelection(.enabled) }
            if !approval.detail.isEmpty {
                Text(approval.detail).font(.system(.caption, design: .monospaced))
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(8).background(.primary.opacity(0.07), in: RoundedRectangle(cornerRadius: 7))
            }
            if !approval.blockedPath.isEmpty {
                Label(approval.blockedPath, systemImage: "doc").font(.caption).textSelection(.enabled)
            }
            if !approval.decisionReason.isEmpty {
                Text(approval.decisionReason).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
            }
            if let rule = approval.matchedAskRule {
                Text([rule.source, rule.toolName, rule.ruleContent].filter { !$0.isEmpty }.joined(separator: " · "))
                    .font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
            }
            if let subject = approval.subject {
                if !subject.filePath.isEmpty { Text(subject.filePath).font(.caption).textSelection(.enabled) }
                Text(subject.text).font(.callout).textSelection(.enabled)
            }
            Divider()
            if let option = approval.options.first(where: { $0.id == selected }) {
                VStack(alignment: .leading, spacing: 6) {
                    Text(option.label).font(.caption.weight(.semibold))
                    if let description = option.description { Text(description).font(.caption) }
                    if let preview = option.preview { Text(preview).font(.caption).textSelection(.enabled) }
                    HStack {
                        Button("返回选择") { selected = nil }.buttonStyle(ReplyChoiceStyle(notch: notch))
                        Spacer()
                        Button(decisionLabel(option, confirm: true)) { submit(option.id) }
                            .buttonStyle(ReplyChoiceStyle(notch: notch, primary: true))
                    }
                }
            } else {
                HStack {
                    ForEach(approval.options) { option in
                        Button(decisionLabel(option, confirm: false)) {
                            selected = option.id
                        }
                        .buttonStyle(ReplyChoiceStyle(notch: notch))
                        .help(option.label)
                    }
                }
            }
        }
        .environment(\.colorScheme, notch ? .dark : colorScheme)
    }

    @Environment(\.colorScheme) private var colorScheme

    private func decisionLabel(_ option: TrayQuestion.Option, confirm: Bool) -> String {
        let allow = option.intent == "allow_once"
        if approval.subject != nil {
            if allow { return confirm ? "确认批准计划" : "批准计划" }
            return confirm ? "确认继续规划" : "继续规划"
        }
        if allow { return confirm ? "确认允许" : "仅本次允许" }
        return confirm ? "确认拒绝" : "仅本次拒绝"
    }
}

struct TrayFitScroll<Content: View>: View {
    let maxHeight: CGFloat
    @ViewBuilder let content: () -> Content
    private var height = State(initialValue: CGFloat(338))

    var body: some View {
        ScrollView {
            content()
                .fixedSize(horizontal: false, vertical: true)
                .onGeometryChange(for: CGFloat.self, of: { $0.size.height }) {
                    height.wrappedValue = min($0, maxHeight)
                }
        }
        .frame(height: height.wrappedValue)
    }
}

struct TrayWorkbenchEditor: View {
    @Binding var text: String
    let label: String
    let notch: Bool

    var body: some View {
        TextEditor(text: $text)
            .font(.callout)
            .scrollContentBackground(.hidden)
            .padding(5)
            .frame(height: 71)
            .background(.primary.opacity(notch ? 0.07 : 0.045), in: RoundedRectangle(cornerRadius: 7))
            .overlay(RoundedRectangle(cornerRadius: 7).strokeBorder(.primary.opacity(0.14)))
            .accessibilityLabel(label)
    }
}

struct TrayLaunchForm: View {
    let catalog: TrayLaunchCatalog
    @Binding var input: TrayLaunchInput
    let notch: Bool
    let enabled: Bool
    let launch: () -> Void

    var body: some View {
        if catalog.workspaces.isEmpty {
            Text("没有可用的本地工作区").font(.callout)
        } else if catalog.agents.isEmpty {
            Text("没有可用的 Agent").font(.callout)
        } else {
            VStack(alignment: .leading, spacing: 8) {
                Picker("工作区", selection: $input.workspaceID) {
                    if input.workspaceID.isEmpty { Text("选择工作区").tag("") }
                    ForEach(catalog.workspaces) { workspace in
                        Text(workspace.name).tag(workspace.id)
                    }
                }
                if let workspace = catalog.workspaces.first(where: { $0.id == input.workspaceID }),
                   !workspace.path.isEmpty {
                    Text(workspace.path).font(.caption2).lineLimit(1).truncationMode(.middle)
                        .foregroundStyle(.secondary).help(workspace.path)
                }
                Picker("Agent", selection: $input.agent) {
                    if input.agent.isEmpty { Text("选择 Agent").tag("") }
                    ForEach(catalog.agents, id: \.self) { agent in
                        Text(agentName(agent)).tag(agent)
                    }
                }
                Text("任务指令").font(.caption.weight(.semibold))
                TrayWorkbenchEditor(text: $input.text, label: "任务指令", notch: notch)
                if enabled && input.text.utf8.count > 64 * 1024 {
                    Text("指令超过 64 KB，请缩短后启动").font(.caption).foregroundStyle(.secondary)
                }
                Button("启动任务", action: launch)
                    .buttonStyle(ReplyChoiceStyle(notch: notch, primary: true))
                    .disabled(!input.canSubmit)
            }
            .disabled(!enabled)
        }
        if catalog.partial {
            Text("当前仅显示部分工作区").font(.caption).foregroundStyle(.secondary)
        }
    }

    private func agentName(_ agent: String) -> String {
        switch agent {
        case "codex": "Codex"
        case "claude": "Claude Code"
        case "opencode": "OpenCode"
        case "gemini": "Gemini CLI"
        default: agent
        }
    }
}

struct TrayChangesContent: View {
    let changes: TrayChanges
    let selected: TrayChange?
    let diff: TrayDiff?
    let enabled: Bool
    let select: (TrayChange) -> Void

    var body: some View {
        if changes.entries.isEmpty {
            Text("当前没有未提交的改动").foregroundStyle(.secondary)
        }
        VStack(alignment: .leading, spacing: 4) {
            ForEach(changes.entries) { entry in
                Button { select(entry) } label: {
                    HStack(spacing: 6) {
                        Image(systemName: selected?.id == entry.id ? "chevron.down" : "chevron.right")
                            .font(.caption2).accessibilityHidden(true)
                        Text(entry.path).lineLimit(1).truncationMode(.middle)
                        Spacer(minLength: 4)
                        Text(entry.summary).foregroundStyle(.secondary)
                    }
                    .padding(.vertical, 4)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.borderless)
                .help(entry.oldPath.map { $0 + " → " + entry.path } ?? entry.path)
                .disabled(!enabled || entry.conflictStatus == "unresolved")
                if selected?.id == entry.id, let diff { TrayDiffContent(diff: diff) }
            }
        }
        if changes.limited {
            Text("仅显示部分改动，完整列表可在原工作区查看").foregroundStyle(.secondary)
        }
    }
}

private struct TrayDiffContent: View {
    let diff: TrayDiff
    @Environment(\.colorScheme) private var colorScheme

    var body: some View {
        if diff.tooLarge == true {
            Text("文件超出预览范围，可在原工作区查看").foregroundStyle(.secondary)
        } else if diff.kind == "binary" {
            Text("二进制文件已变化").foregroundStyle(.secondary)
        } else {
            let lines = trayDiffLines(before: diff.originalContent, after: diff.modifiedContent)
            if lines.isEmpty {
                Text("没有文本差异").foregroundStyle(.secondary)
            } else {
                Text(render(lines))
                    .font(.system(.caption, design: .monospaced))
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(7)
                    .background(.primary.opacity(0.065), in: RoundedRectangle(cornerRadius: 7))
            }
        }
    }

    private func render(_ lines: [String]) -> AttributedString {
        var text = AttributedString()
        for line in lines {
            var item = AttributedString(line + "\n")
            if line.hasPrefix("+ ") {
                item.foregroundColor = colorScheme == .dark
                    ? Color(red: 0.58, green: 0.91, blue: 0.72) : Color(red: 0.09, green: 0.35, blue: 0.18)
            } else if line.hasPrefix("− ") {
                item.foregroundColor = colorScheme == .dark
                    ? Color(red: 1, green: 0.65, blue: 0.68) : Color(red: 0.62, green: 0.14, blue: 0.21)
            } else {
                item.foregroundColor = .secondary
            }
            text.append(item)
        }
        return text
    }
}

func trayDiffLines(before: String, after: String) -> [String] {
    let old = before.isEmpty ? [] : before.components(separatedBy: "\n")
    let new = after.isEmpty ? [] : after.components(separatedBy: "\n")
    let changes = new.difference(from: old)
    guard !changes.isEmpty else { return [] }

    var removed: Set<Int> = []
    var inserted: Set<Int> = []
    for change in changes {
        switch change {
        case .remove(let offset, _, _): removed.insert(offset)
        case .insert(let offset, _, _): inserted.insert(offset)
        }
    }
    var lines: [String] = []
    var changed: [Int] = []
    var left = 0
    var right = 0
    while left < old.count || right < new.count {
        if removed.contains(left) {
            changed.append(lines.count)
            lines.append("− " + old[left])
            left += 1
        } else if inserted.contains(right) {
            changed.append(lines.count)
            lines.append("+ " + new[right])
            right += 1
        } else {
            lines.append("  " + old[left])
            left += 1
            right += 1
        }
    }
    var context: Set<Int> = []
    for offset in changed {
        for line in max(0, offset - 2)...min(lines.count - 1, offset + 2) { context.insert(line) }
    }
    var preview: [String] = []
    for (offset, line) in lines.enumerated() {
        if context.contains(offset) {
            preview.append(line)
        } else if preview.last != "⋯" {
            preview.append("⋯")
        }
    }
    return preview
}

struct TrayDetailContent: View {
    let detail: TrayTaskDetail
    let task: TrayTask
    private var historyExpanded = State(initialValue: false)

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if task.taskStatus == .done, let result = detail.result {
                Divider()
                Text(detail.resultLabel ?? "最近结果").font(.caption.weight(.semibold))
                Text(result).font(.callout).textSelection(.enabled)
                ForEach(links(in: result), id: \.self) { url in
                    Link(destination: url) {
                        Label(url.absoluteString, systemImage: "link").font(.caption)
                            .lineLimit(1).truncationMode(.middle)
                    }
                    .help(url.absoluteString)
                }
            }
            if let output = detail.output {
                Divider()
                Text("近期终端输出（只读）").font(.caption.weight(.semibold))
                Text(output).font(.system(.caption, design: .monospaced)).textSelection(.enabled)
            }
            if detail.unavailable && detail.output == nil {
                Text("目前无法读取会话记录").font(.caption).foregroundStyle(.secondary)
            } else if !detail.unavailable && detail.entries.isEmpty && detail.output == nil {
                Text("暂无会话记录").font(.caption).foregroundStyle(.secondary)
            } else if !detail.entries.isEmpty && task.taskStatus == .done && detail.source == "chat" {
                DisclosureGroup("近期消息与进度", isExpanded: historyExpanded.projectedValue) {
                    timeline.padding(.top, 6)
                }
                .font(.caption.weight(.semibold))
            } else if !detail.entries.isEmpty {
                Divider()
                Text(detail.source == "transcript" ? "终端会话记录（只读）" : "近期消息与进度")
                    .font(.caption.weight(.semibold))
                timeline
            }
        }
    }

    private var timeline: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(detail.entries.enumerated()), id: \.offset) { _, entry in
                VStack(alignment: .leading, spacing: 3) {
                    Text([entry.label, entry.state].compactMap { $0 }.joined(separator: " · "))
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.secondary)
                    if let text = entry.text, !text.isEmpty {
                        Text(text).font(.callout).textSelection(.enabled)
                    }
                    if let tools = entry.tools, !tools.isEmpty {
                        Text(tools.map(\.name).joined(separator: " · ")).font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
            if detail.limited {
                Text("仅显示近期记录，可在原会话查看完整内容").font(.caption2).foregroundStyle(.secondary)
            }
        }
    }

    private func links(in text: String) -> [URL] {
        guard let detector = try? NSDataDetector(types: NSTextCheckingResult.CheckingType.link.rawValue) else {
            return []
        }
        var seen: Set<URL> = []
        let matches = detector.matches(in: text, range: NSRange(text.startIndex..., in: text))
        let urls = matches.compactMap(\.url).filter { url in
            guard url.scheme == "http" || url.scheme == "https" else { return false }
            return seen.insert(url).inserted
        }
        return Array(urls.prefix(5))
    }
}
