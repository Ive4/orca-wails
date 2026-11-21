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
    }

    let id: String
    let revision: UInt64
    let text: String
    let options: [Option]
    let acceptsText: Bool
    let allowEmpty: Bool
    let placeholder: String?
    let initialText: String?
}

struct TrayQuestionAnswer: Encodable {
    let id: String
    let revision: UInt64
    let optionId: String?
    let text: String?
}

struct TrayInteractionRequest: Encodable {
    let requestID: UUID
    let taskID: String?
    let botID: String?
    let answer: TrayQuestionAnswer?
}

struct TrayInteractionResult: Decodable {
    let requestID: UUID
    let bots: [TrayBot]?
    let question: TrayQuestion?
    let message: String?
    let reopenRequired: Bool?
    let repliedTaskID: String?
    let repliedQuestionID: String?
}
