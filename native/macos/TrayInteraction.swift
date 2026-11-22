import Foundation

struct TrayBot: Decodable, Identifiable {
    let id: String
    let name: String
    let specialty: String?
    let paused: Bool
}

struct TrayQuestion: Decodable {
    struct Option: Decodable, Identifiable {
        let id: String
        let label: String
        let description: String?
        let preview: String?
        let intent: String?
    }

    struct Part: Decodable, Identifiable {
        let id: String
        let text: String
        let multiSelect: Bool
        let options: [Option]
        let acceptsText: Bool
        let allowEmpty: Bool
        let placeholder: String?
        let initialText: String?

        var initialDraft: TrayQuestionDraft {
            TrayQuestionDraft(useOther: acceptsText && options.isEmpty, text: initialText ?? "")
        }

        func selection(from draft: TrayQuestionDraft) -> TrayQuestionAnswer.Selection? {
            let options = options.map(\.id).filter { draft.options.contains($0) }
            let text = draft.useOther && acceptsText ? draft.text : nil
            if let text, text.utf8.count > 64 * 1024 ||
                (!allowEmpty && text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty) {
                return nil
            }
            let count = options.count + (text == nil ? 0 : 1)
            guard options.count <= 64, count > 0, multiSelect || count == 1 else { return nil }
            return .init(questionId: id, optionIds: options, other: text)
        }
    }

    let id: String
    let revision: UInt64
    let parts: [Part]

    var sendsOnSelection: Bool { parts.count == 1 && !parts[0].multiSelect }

    func selections(from drafts: [String: TrayQuestionDraft]) -> [TrayQuestionAnswer.Selection]? {
        let selections = parts.compactMap { $0.selection(from: drafts[$0.id] ?? $0.initialDraft) }
        return !parts.isEmpty && selections.count == parts.count ? selections : nil
    }
}

struct TrayQuestionDraft {
    var options: Set<String> = []
    var useOther: Bool
    var text: String
}

struct TrayQuestionAnswer: Encodable {
    struct Selection: Encodable {
        let questionId: String
        let optionIds: [String]
        let other: String?
    }

    let id: String
    let revision: UInt64
    let selections: [Selection]
    var approvalOption: String?
}

struct TrayPrompt: Decodable {
    let question: TrayQuestion?
    let approval: TrayApproval?
    let count: Int
}

struct TrayApproval: Decodable {
    struct Subject: Decodable {
        let text: String
        let filePath: String
    }

    struct Rule: Decodable {
        let source: String
        let toolName: String
        let ruleContent: String
    }

    let id: String
    let revision: UInt64
    let title: String
    let toolName: String
    let detail: String
    let description: String
    let decisionReason: String
    let blockedPath: String
    let subject: Subject?
    let matchedAskRule: Rule?
    let options: [TrayQuestion.Option]
}

struct TrayTaskDetail: Decodable {
    struct Entry: Decodable {
        struct Tool: Decodable { let name: String }
        let kind: String
        let role: String?
        let text: String?
        let name: String?
        let state: String?
        let tools: [Tool]?

        var label: String {
            switch kind {
            case "message": role == "user" ? "你" : (role == "assistant" ? "Agent" : "会话记录")
            case "tool": name ?? "工具"
            case "approval": "审批"
            case "question": "提问"
            case "error": "错误"
            default: "会话记录"
            }
        }
    }

    let task: TrayTask
    let source: String?
    let result: String?
    let resultLabel: String?
    let output: String?
    let currentTool: String?
    let entries: [Entry]
    let limited: Bool
    let unavailable: Bool
    let canFollowUp: Bool
    let canReadChanges: Bool?
    let launchPreset: TrayLaunchInput?
    let readAt: Int64?
}

struct TrayChange: Decodable, Identifiable {
    let path: String
    let oldPath: String?
    let area: String
    let status: String
    let conflictStatus: String?

    var id: String { area + "\u{0}" + path }

    var summary: String {
        if conflictStatus == "unresolved" { return "冲突" }
        return switch area {
        case "staged": "已暂存"
        case "untracked": "未跟踪"
        default: "未暂存"
        }
    }
}

struct TrayChanges: Decodable {
    let entries: [TrayChange]
    let limited: Bool
}

struct TrayDiff: Decodable {
    let originalContent: String
    let modifiedContent: String
    let kind: String
    let tooLarge: Bool?
}

struct TrayLaunchCatalog: Decodable {
    struct Workspace: Decodable, Identifiable {
        let id: String
        let name: String
        let path: String
    }

    let workspaces: [Workspace]
    let agents: [String]
    let defaultAgent: String?
    let partial: Bool
}

struct TrayLaunchInput: Codable {
    var workspaceID = ""
    var agent = ""
    var text = ""

    var canSubmit: Bool {
        !workspaceID.isEmpty && !agent.isEmpty && text.utf8.count <= 64 * 1024 &&
            !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }
}

struct TrayLaunchLookup: Codable {
    let workspaceID: String
    let paneKey: String
    let sessionID: String
}

struct TrayLaunchReceipt: Decodable {
    let key: String?
    let taskID: String?
    let lookup: TrayLaunchLookup?
    let kind: String
    let prompt: String
    let warning: String?
    let modeDetail: String?

    var label: String {
        switch prompt {
        case "journaled": "任务已启动，指令已写入会话"
        case "handed-to-terminal": "任务已启动，指令已交给终端"
        case "not-delivered": "任务已创建，指令尚未发送"
        default: "任务已创建，指令状态待确认"
        }
    }
}

struct TrayInteractionRequest: Encodable {
    let requestID: UUID
    let taskID: String?
    let botID: String?
    let answer: TrayQuestionAnswer?
    var since: Int64?
    var text: String?
    var launch: TrayLaunchInput?
    var launchCheck: TrayLaunchLookup?
    var operationID: String?
    var file: String?
    var area: String?
}

struct TrayInteractionResult: Decodable {
    let requestID: UUID
    let bots: [TrayBot]?
    let prompt: TrayPrompt?
    let detail: TrayTaskDetail?
    let catalog: TrayLaunchCatalog?
    let launch: TrayLaunchReceipt?
    let launchCheck: TrayLaunchLookup?
    let operationID: String?
    let changes: TrayChanges?
    let diff: TrayDiff?
    let message: String?
    let receipt: String?
    let uncertain: Bool?
    let reopenRequired: Bool?
    let repliedTaskID: String?
    let repliedQuestionID: String?
    let settledTaskID: String?
    let settledQuestionID: String?
}
