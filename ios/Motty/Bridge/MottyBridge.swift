import Foundation
import UIKit

/// The Swift side of the C ABI. Control calls and desktop input use separate
/// queues so a blocked network write cannot hide login or disconnect state. The
/// published state is only touched on the main queue; `@unchecked Sendable`
/// acknowledges that split to the compiler.
final class MottyBridge: ObservableObject, @unchecked Sendable {
    static let shared = MottyBridge()

    @Published private(set) var hosts: [Host] = []
    @Published private(set) var keys: [KeyInfo] = []
    @Published private(set) var sessions: [SessionInfo] = []
    @Published private(set) var prompts: [CoreEvent] = []
    @Published private(set) var syncStatus = SyncStatus()
    @Published var syncTestMessage: String?
    @Published var lastError: String?

    private let queue = DispatchQueue(label: "dev.motty.core", qos: .userInitiated)
    /// SFTP calls may wait on a slow server. Keeping them off `queue` means
    /// terminal input and event polling keep flowing while a directory loads.
    private let filesQueue = DispatchQueue(label: "dev.motty.files", qos: .userInitiated)
    private let desktopInputQueue = DispatchQueue(label: "dev.motty.desktop.input", qos: .userInitiated)
    private let desktopInputLock = NSLock()
    private struct QueuedDesktopInput {
        let id: UInt32
        let epoch: UInt64
        let method: String
        var params: any Encodable
        var createdAt: TimeInterval
        let isMotion: Bool
    }
    private var pendingDesktopInput: [QueuedDesktopInput] = []
    private var desktopInputEpochs: [UInt32: UInt64] = [:]
    private var disabledDesktopInput: Set<UInt32> = []
    private var desktopQueuedButtons: [UInt32: UInt8] = [:]
    private var desktopInputDraining = false
    // Main queue only. Late state events from a closed connection cannot
    // revive its surface or overwrite a freshly opened login prompt.
    private var closedDesktopIDs: Set<UInt32> = []
    /// The app's writable core directory. SFTP paths must stay inside it.
    private(set) var dataDirectory: URL?
    private var eventBuffer = [UInt8](repeating: 0, count: 32 * 1024)
    private var ready = false
    private var syncBackgroundTask: UIBackgroundTaskIdentifier = .invalid

    private init() {}

    // MARK: Lifecycle

    func start() {
        let fileManager = FileManager.default
        guard let support = fileManager.urls(for: .applicationSupportDirectory, in: .userDomainMask).first else {
            lastError = AppLanguage.localized("no application support directory")
            return
        }
        let directory = support.appendingPathComponent("Motty", isDirectory: true)
        try? fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
        dataDirectory = directory
        try? fileManager.createDirectory(
            at: directory.appendingPathComponent("downloads", isDirectory: true),
            withIntermediateDirectories: true
        )
        try? fileManager.createDirectory(
            at: directory.appendingPathComponent("inbox", isDirectory: true),
            withIntermediateDirectories: true
        )
        let version = Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "0"

        let response: String = directory.path.withCString { path in
            version.withCString { versionCString in
                guard let result = MottyStart(UnsafeMutablePointer(mutating: path),
                                              UnsafeMutablePointer(mutating: versionCString)) else {
                    return ""
                }
                defer { MottyFree(result) }
                return String(cString: result)
            }
        }
        guard let data = response.data(using: .utf8),
              let envelope = try? JSONDecoder().decode(RPCEnvelope<StartResult>.self, from: data) else {
            lastError = AppLanguage.localized("the core did not start")
            return
        }
        if let error = envelope.error {
            lastError = error.message
            return
        }
        ready = true
        refreshHosts()
        refreshKeys()
        refreshAISettings()
        refreshAIPrivacy()
        refreshSyncStatus { _ in self.syncOnForeground() }
    }

    struct StartResult: Decodable {
        var reused: Bool
    }

    // MARK: Hosts

    struct EmptyParams: Encodable {}

    func refreshHosts(completion: (() -> Void)? = nil) {
        queue.async {
            do {
                let result: HostsResult = try self.callSync("hosts.list", params: EmptyParams())
                DispatchQueue.main.async {
                    self.hosts = result.hosts
                    completion?()
                }
            } catch {
                self.publish(error)
                DispatchQueue.main.async { completion?() }
            }
        }
    }

    func saveHost(_ host: Host, completion: ((Result<Host, Error>) -> Void)? = nil) {
        queue.async {
            do {
                let saved: Host = try self.callSync("hosts.save", params: host)
                DispatchQueue.main.async {
                    if let index = self.hosts.firstIndex(where: { $0.id == saved.id }) {
                        self.hosts[index] = saved
                    } else {
                        self.hosts.append(saved)
                        self.hosts.sort { ($0.group ?? "", $0.label.lowercased()) < ($1.group ?? "", $1.label.lowercased()) }
                    }
                    completion?(.success(saved))
                }
            } catch {
                self.publish(error)
                DispatchQueue.main.async { completion?(.failure(error)) }
            }
        }
    }

    func deleteHost(_ host: Host) {
        struct Params: Encodable { let id: String }
        queue.async {
            do {
                let _: DeletedResult = try self.callSync("hosts.delete", params: Params(id: host.id))
                DispatchQueue.main.async { self.hosts.removeAll { $0.id == host.id } }
            } catch {
                self.publish(error)
            }
        }
    }

    struct PasswordStatus: Decodable {
        var saved: Bool
    }

    /// Whether the host has a password in the vault. The value itself is never
    /// sent back to the interface. `kind` is "proxy" for the outbound proxy's
    /// password and empty for the one the server asks for.
    func hostPasswordStatus(id: String, kind: String = "", completion: @escaping (Bool) -> Void) {
        struct Params: Encodable {
            let id: String
            let kind: String?
        }
        queue.async {
            do {
                let status: PasswordStatus = try self.callSync(
                    "hosts.passwordStatus",
                    params: Params(id: id, kind: kind.isEmpty ? nil : kind)
                )
                DispatchQueue.main.async { completion(status.saved) }
            } catch {
                DispatchQueue.main.async { completion(false) }
            }
        }
    }

    func setHostPassword(
        id: String,
        password: String,
        remove: Bool = false,
        kind: String = "",
        completion: ((Error?) -> Void)? = nil
    ) {
        struct Params: Encodable {
            let id: String
            let password: String
            let remove: Bool
            let kind: String?
        }
        queue.async {
            do {
                let _: PasswordStatus = try self.callSync(
                    "hosts.password",
                    params: Params(id: id, password: password, remove: remove, kind: kind.isEmpty ? nil : kind)
                )
                DispatchQueue.main.async { completion?(nil) }
            } catch {
                self.publish(error)
                DispatchQueue.main.async { completion?(error) }
            }
        }
    }

    func importSSHConfig(path: String, completion: ((Result<ImportResult, Error>) -> Void)? = nil) {
        struct Params: Encodable { let path: String }
        queue.async {
            do {
                let result: ImportResult = try self.callSync("hosts.importSSHConfig", params: Params(path: path))
                DispatchQueue.main.async {
                    self.refreshHosts()
                    completion?(.success(result))
                }
            } catch {
                self.publish(error)
                DispatchQueue.main.async { completion?(.failure(error)) }
            }
        }
    }

    // MARK: Keys

    func refreshKeys() {
        queue.async {
            do {
                let result: KeysResult = try self.callSync("keys.list", params: EmptyParams())
                DispatchQueue.main.async { self.keys = result.keys }
            } catch {
                self.publish(error)
            }
        }
    }

    func importKey(
        name: String,
        data: Data,
        overwrite: Bool = false,
        completion: ((Result<KeyInfo, Error>) -> Void)? = nil
    ) {
        struct Params: Encodable {
            let name: String
            let data: String
            let overwrite: Bool
        }
        queue.async {
            do {
                let info: KeyInfo = try self.callSync(
                    "keys.import",
                    params: Params(name: name, data: data.base64EncodedString(), overwrite: overwrite)
                )
                DispatchQueue.main.async {
                    if let index = self.keys.firstIndex(where: { $0.name == info.name }) {
                        self.keys[index] = info
                    } else {
                        self.keys.append(info)
                        self.keys.sort { $0.name < $1.name }
                    }
                    completion?(.success(info))
                }
            } catch {
                // A name conflict is the caller's decision to resolve, so it is
                // reported through the completion instead of a global alert.
                if let rpc = error as? RPCError, rpc.code == "conflict" {
                    DispatchQueue.main.async { completion?(.failure(error)) }
                    return
                }
                self.publish(error)
                DispatchQueue.main.async { completion?(.failure(error)) }
            }
        }
    }

    func deleteKey(_ key: KeyInfo) {
        struct Params: Encodable { let name: String }
        queue.async {
            do {
                let _: DeletedResult = try self.callSync("keys.delete", params: Params(name: key.name))
                DispatchQueue.main.async { self.keys.removeAll { $0.name == key.name } }
            } catch {
                self.publish(error)
            }
        }
    }

    // MARK: Assistant

    @Published private(set) var aiSettings = AISettings()
    @Published private(set) var aiConversations: [UInt32: AIConversation] = [:]
    @Published private(set) var aiPrivacy = AIPrivacyInfo()

    func aiConversation(_ sessionID: UInt32) -> AIConversation {
        aiConversations[sessionID] ?? AIConversation()
    }

    func refreshAISettings() {
        queue.async {
            do {
                let settings: AISettings = try self.callSync("ai.settings.get", params: EmptyParams())
                DispatchQueue.main.async { self.aiSettings = settings }
            } catch {
                self.publish(error)
            }
        }
    }

    func saveAISettings(_ settings: AISettings, completion: ((Result<AISettings, Error>) -> Void)? = nil) {
        queue.async {
            do {
                let saved: AISettings = try self.callSync("ai.settings.save", params: settings)
                DispatchQueue.main.async {
                    self.aiSettings = saved
                    completion?(.success(saved))
                }
            } catch {
                self.publish(error)
                DispatchQueue.main.async { completion?(.failure(error)) }
            }
        }
    }

    func refreshAIPrivacy(completion: ((AIPrivacyInfo) -> Void)? = nil) {
        queue.async {
            do {
                let info: AIPrivacyInfo = try self.callSync("ai.privacy.get", params: EmptyParams())
                DispatchQueue.main.async {
                    self.aiPrivacy = info
                    completion?(info)
                }
            } catch {
                self.publish(error)
            }
        }
    }

    func saveAIPrivacy(_ settings: AIPrivacySettings, customStrings: [String]) {
        struct Params: Encodable {
            let settings: AIPrivacySettings
            let customStrings: [String]
        }
        queue.async {
            do {
                let info: AIPrivacyInfo = try self.callSync(
                    "ai.privacy.save",
                    params: Params(settings: settings, customStrings: customStrings)
                )
                DispatchQueue.main.async {
                    self.aiPrivacy = info
                    self.aiSettings.privacy = info.settings
                }
            } catch {
                self.publish(error)
            }
        }
    }

    func loadAIPrivacyLog(
        limit: Int = 10,
        completion: @escaping (Result<[AIPrivacyAuditEntry], Error>) -> Void
    ) {
        struct Params: Encodable { let limit: Int }
        queue.async {
            do {
                let result: AIPrivacyLogResult = try self.callSync(
                    "ai.privacy.log",
                    params: Params(limit: limit)
                )
                DispatchQueue.main.async { completion(.success(result.entries ?? [])) }
            } catch {
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    func clearAIPrivacyLog(completion: (() -> Void)? = nil) {
        queue.async {
            do {
                let _: OKResult = try self.callSync("ai.privacy.clear", params: EmptyParams())
                DispatchQueue.main.async { completion?() }
            } catch {
                self.publish(error)
            }
        }
    }

    /// Runs the current rules on a draft without sending it, so the user can
    /// see exactly which values the assistant will not receive.
    func previewAIPrivacy(
        sessionID: UInt32?,
        text: String,
        completion: @escaping (Result<AIPrivacyPreviewResult, Error>) -> Void
    ) {
        struct Params: Encodable {
            let sessionId: UInt32?
            let text: String
        }
        queue.async {
            do {
                let result: AIPrivacyPreviewResult = try self.callSync(
                    "ai.privacy.preview",
                    params: Params(sessionId: sessionID, text: text)
                )
                DispatchQueue.main.async { completion(.success(result)) }
            } catch {
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    /// Lists models for the settings currently on screen. The draft is not
    /// saved: this lets the form test a provider or key the user just typed.
    func loadAIModels(_ provider: AIProvider, completion: @escaping (Result<[String], Error>) -> Void) {
        queue.async {
            do {
                let result: AIModelsResult = try self.callSync("ai.models", params: provider)
                DispatchQueue.main.async { completion(.success(result.models)) }
            } catch {
                self.publish(error)
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    func sendAIMessage(sessionID: UInt32, text: String) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        var conversation = aiConversation(sessionID)
        conversation.messages.append(AIChatMessage(id: UUID().uuidString, role: .user, text: trimmed))
        conversation.pendingApproval = nil
        aiConversations[sessionID] = conversation

        struct Params: Encodable {
            let sessionId: UInt32
            let text: String
        }
        queue.async {
            do {
                let result: AISendResult = try self.callSync(
                    "ai.send",
                    params: Params(sessionId: sessionID, text: trimmed)
                )
                DispatchQueue.main.async {
                    var current = self.aiConversation(sessionID)
                    current.runID = result.runId
                    self.aiConversations[sessionID] = current
                }
            } catch {
                DispatchQueue.main.async {
                    var current = self.aiConversation(sessionID)
                    current.messages.append(AIChatMessage(
                        id: UUID().uuidString,
                        role: .error,
                        text: error.localizedDescription
                    ))
                    self.aiConversations[sessionID] = current
                }
            }
        }
    }

    func cancelAIRun(sessionID: UInt32) {
        guard let runID = aiConversation(sessionID).runID else { return }
        struct Params: Encodable { let runId: UInt64 }
        queue.async {
            let _: DeletedResult? = try? self.callSync("ai.cancel", params: Params(runId: runID))
        }
    }

    func approveAI(
        sessionID: UInt32,
        request: AIApprovalRequest,
        approved: Bool,
        confirmText: String = "",
        always: Bool = false
    ) {
        var conversation = aiConversation(sessionID)
        if conversation.pendingApproval?.id == request.id {
            conversation.pendingApproval = nil
            aiConversations[sessionID] = conversation
        }
        struct Params: Encodable {
            let runId: UInt64
            let approvalId: String
            let approved: Bool
            let confirmText: String
            let always: Bool
        }
        queue.async {
            do {
                let _: DeletedResult = try self.callSync(
                    "ai.approve",
                    params: Params(
                        runId: request.runID,
                        approvalId: request.id,
                        approved: approved,
                        confirmText: confirmText,
                        always: always
                    )
                )
            } catch {
                self.publish(error)
            }
        }
    }

    func resetAIConversation(sessionID: UInt32) {
        struct Params: Encodable { let sessionId: UInt32 }
        queue.async {
            let _: DeletedResult? = try? self.callSync("ai.reset", params: Params(sessionId: sessionID))
        }
        var conversation = AIConversation()
        conversation.runID = aiConversation(sessionID).runID
        conversation.historyLoaded = true
        aiConversations[sessionID] = conversation
    }

    /// Restores the thread the host was last using. The panel calls this once
    /// when it opens, so a reconnect or a fresh app launch still shows the
    /// earlier conversation.
    func loadAIConversationIfNeeded(sessionID: UInt32) {
        DispatchQueue.main.async {
            let conversation = self.aiConversation(sessionID)
            guard !conversation.historyLoaded,
                  conversation.messages.isEmpty,
                  conversation.runID == nil else { return }
            self.loadAIConversation(sessionID: sessionID, conversationID: 0)
        }
    }

    /// Loads one saved conversation. Zero means the host's active thread;
    /// loading another one makes it active so the next message continues it.
    func loadAIConversation(
        sessionID: UInt32,
        conversationID: Int64,
        completion: ((Result<Void, Error>) -> Void)? = nil
    ) {
        struct Params: Encodable {
            let sessionId: UInt32
            let conversationId: Int64
        }
        queue.async {
            do {
                let result: AIConversationView = try self.callSync(
                    "ai.conversation.get",
                    params: Params(sessionId: sessionID, conversationId: conversationID)
                )
                DispatchQueue.main.async {
                    var current = self.aiConversation(sessionID)
                    guard current.runID == nil else {
                        completion?(.success(()))
                        return
                    }
                    // A message typed while the read was in flight outranks
                    // the saved copy; never replace what the user just sent.
                    if conversationID == 0 && !current.messages.isEmpty {
                        current.historyLoaded = true
                        self.aiConversations[sessionID] = current
                        completion?(.success(()))
                        return
                    }
                    current.messages = Self.chatMessages(from: result.messages)
                    current.historyLoaded = true
                    current.pendingApproval = nil
                    self.aiConversations[sessionID] = current
                    completion?(.success(()))
                }
            } catch {
                DispatchQueue.main.async { completion?(.failure(error)) }
            }
        }
    }

    func loadAIHistory(
        sessionID: UInt32,
        completion: @escaping (Result<[AIConversationSummary], Error>) -> Void
    ) {
        struct Params: Encodable { let sessionId: UInt32 }
        queue.async {
            do {
                let result: AIConversationListResult = try self.callSync(
                    "ai.conversations",
                    params: Params(sessionId: sessionID)
                )
                DispatchQueue.main.async { completion(.success(result.conversations)) }
            } catch {
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    func deleteAIConversation(
        sessionID: UInt32,
        conversationID: Int64,
        completion: @escaping (Result<Void, Error>) -> Void
    ) {
        struct Params: Encodable {
            let sessionId: UInt32
            let conversationId: Int64
        }
        queue.async {
            do {
                let result: DeletedResult = try self.callSync(
                    "ai.conversation.delete",
                    params: Params(sessionId: sessionID, conversationId: conversationID)
                )
                DispatchQueue.main.async {
                    if result.active == true {
                        var conversation = AIConversation()
                        conversation.historyLoaded = true
                        self.aiConversations[sessionID] = conversation
                    }
                    completion(.success(()))
                }
            } catch {
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    /// Turns stored roles back into the rows the chat panel draws. Tool
    /// results fill the card of the call they answer.
    private static func chatMessages(from history: [AIHistoryMessage]) -> [AIChatMessage] {
        var messages: [AIChatMessage] = []
        // Tool call ids repeat across runs; SwiftUI row ids must not.
        var toolCards: [String: Int] = [:]
        for item in history {
            switch item.role {
            case "user":
                guard let text = item.text, !text.isEmpty else { continue }
                messages.append(AIChatMessage(id: UUID().uuidString, role: .user, text: text))
            case "assistant":
                if let text = item.text, !text.isEmpty {
                    messages.append(AIChatMessage(id: UUID().uuidString, role: .assistant, text: text))
                }
                for call in item.toolCalls ?? [] {
                    messages.append(AIChatMessage(
                        id: UUID().uuidString,
                        role: .tool,
                        text: "",
                        toolCallID: call.id,
                        toolName: call.name,
                        toolArguments: call.arguments,
                        toolRunning: false
                    ))
                    if !call.id.isEmpty {
                        toolCards[call.id] = messages.count - 1
                    }
                }
            case "tool":
                guard let id = item.toolCallId, let index = toolCards[id] else { continue }
                messages[index].toolResult = item.text
                messages[index].toolRunning = false
                messages[index].isError = item.text?.hasPrefix("Error: ") == true
            default:
                break
            }
        }
        return messages
    }

    private func handleAIEvent(_ event: AIEvent) {
        guard let sessionID = event.sessionId else { return }
        var conversation = aiConversation(sessionID)
        switch event.type {
        case "ai.state":
            switch event.state {
            case "running":
                conversation.runID = event.runId
            case "done", "cancelled", "error":
                conversation.runID = nil
                conversation.pendingApproval = nil
                for index in conversation.messages.indices {
                    conversation.messages[index].streaming = false
                }
                if event.state == "error", let message = event.error, !message.isEmpty {
                    conversation.messages.append(AIChatMessage(
                        id: UUID().uuidString,
                        role: .error,
                        text: message
                    ))
                }
            default:
                break
            }
        case "ai.text":
            let text = event.text ?? ""
            if let last = conversation.messages.indices.last,
               conversation.messages[last].role == .assistant,
               conversation.messages[last].streaming {
                conversation.messages[last].text += text
            } else {
                conversation.messages.append(AIChatMessage(
                    id: UUID().uuidString,
                    role: .assistant,
                    text: text,
                    streaming: true
                ))
            }
        case "ai.reasoning":
            let text = event.text ?? ""
            if let last = conversation.messages.indices.last,
               conversation.messages[last].role == .reasoning,
               conversation.messages[last].streaming {
                conversation.messages[last].text += text
            } else {
                conversation.messages.append(AIChatMessage(
                    id: UUID().uuidString,
                    role: .reasoning,
                    text: text,
                    streaming: true
                ))
            }
        case "ai.tool":
            for index in conversation.messages.indices {
                conversation.messages[index].streaming = false
            }
            conversation.messages.append(AIChatMessage(
                id: UUID().uuidString,
                role: .tool,
                text: "",
                toolCallID: event.id,
                toolName: event.name,
                toolArguments: event.arguments,
                toolRunning: true
            ))
        case "ai.toolResult":
            if let id = event.id,
               let index = conversation.messages.lastIndex(where: { $0.role == .tool && $0.toolCallID == id }) {
                conversation.messages[index].toolResult = event.text
                conversation.messages[index].toolRunning = false
                conversation.messages[index].isError = event.isError ?? false
            }
        case "ai.approval":
            conversation.pendingApproval = AIApprovalRequest(
                id: event.approvalId ?? "",
                runID: event.runId ?? 0,
                tool: event.tool ?? "",
                summary: event.summary ?? "",
                level: event.level ?? "",
                reasons: event.reasons ?? [],
                safer: event.safer,
                needsText: event.needsText ?? false,
                confirm: event.confirm ?? "CONFIRM",
                sessionLabel: event.sessionLabel
            )
        case "ai.error":
            conversation.messages.append(AIChatMessage(
                id: UUID().uuidString,
                role: .error,
                text: event.error ?? "The assistant failed."
            ))
        case "ai.modeRequest":
            conversation.messages.append(AIChatMessage(
                id: UUID().uuidString,
                role: .reasoning,
                text: "The assistant asked to use a tool. Send the request again in Agent mode to allow it."
            ))
        default:
            break
        }
        aiConversations[sessionID] = conversation
    }

    // MARK: Sessions

    struct OpenParams: Encodable {
        let hostId: String
        let cols: Int
        let rows: Int
    }

    func openSession(host: Host, cols: Int, rows: Int) async throws -> SessionInfo {
        try await withCheckedThrowingContinuation { continuation in
            queue.async {
                do {
                    let info: SessionInfo = try self.callSync(
                        "sessions.open",
                        params: OpenParams(hostId: host.id, cols: cols, rows: rows)
                    )
                    DispatchQueue.main.async {
                        // The state event may arrive before the call returns.
                        if let index = self.sessions.firstIndex(where: { $0.id == info.id }) {
                            self.sessions[index] = info
                        } else {
                            self.sessions.append(info)
                        }
                        continuation.resume(returning: info)
                    }
                } catch {
                    continuation.resume(throwing: error)
                }
            }
        }
    }

    func reconnectSession(_ id: UInt32) {
        struct Params: Encodable { let id: UInt32 }
        queue.async {
            do {
                let info: SessionInfo = try self.callSync("sessions.reconnect", params: Params(id: id))
                DispatchQueue.main.async {
                    if let index = self.sessions.firstIndex(where: { $0.id == info.id }) {
                        self.sessions[index] = info
                    }
                }
            } catch {
                self.publish(error)
            }
        }
    }

    func closeSession(_ id: UInt32) {
        struct Params: Encodable { let id: UInt32 }
        queue.async {
            let _: DeletedResult? = try? self.callSync("sessions.close", params: Params(id: id))
            DispatchQueue.main.async { self.sessions.removeAll { $0.id == id } }
        }
    }

    func input(sessionID: UInt32, data: Data) {
        struct Params: Encodable { let id: UInt32; let data: String }
        let encoded = data.base64EncodedString()
        queue.async {
            let _: DeletedResult? = try? self.callSync("sessions.input", params: Params(id: sessionID, data: encoded))
        }
    }

    func resize(sessionID: UInt32, cols: Int, rows: Int) {
        struct Params: Encodable { let id: UInt32; let cols: Int; let rows: Int }
        queue.async {
            let _: DeletedResult? = try? self.callSync("sessions.resize", params: Params(id: sessionID, cols: cols, rows: rows))
        }
    }

    struct AnswerParams: Encodable {
        let id: UInt32
        let kind: String
        var value: String?
        var save: Bool = false
        var answers: [String]?
        var reject: Bool = false
        var skip: Bool = false
    }

    func answer(_ params: AnswerParams) {
        queue.async {
            let _: DeletedResult? = try? self.callSync("sessions.answer", params: params)
        }
    }

    func dismissPrompt(_ prompt: CoreEvent) {
        DispatchQueue.main.async {
            self.prompts.removeAll { $0.sessionID == prompt.sessionID && $0.kind == prompt.kind }
        }
    }

    func scroll(sessionID: UInt32, lines: Int) {
        struct Params: Encodable { let id: UInt32; let lines: Int }
        queue.async {
            let _: DeletedResult? = try? self.callSync("sessions.scroll", params: Params(id: sessionID, lines: lines))
        }
    }

    /// Sets the scrollback view to an absolute line offset. The terminal
    /// coalesces touch updates and sends only the latest target every frame,
    /// so a fast fling cannot queue a long tail of stale scroll calls.
    func scrollTo(sessionID: UInt32, offset: Int) {
        struct Params: Encodable { let id: UInt32; let offset: Int }
        queue.async {
            let _: DeletedResult? = try? self.callSync("sessions.scrollTo", params: Params(id: sessionID, offset: offset))
        }
    }

    // MARK: Files

    private func filesCall<T: Decodable>(
        _ method: String,
        params: Encodable,
        publishError: Bool = true,
        completion: @escaping (Result<T, Error>) -> Void
    ) {
        filesQueue.async {
            do {
                let result: T = try self.callSync(method, params: params)
                DispatchQueue.main.async { completion(.success(result)) }
            } catch {
                if publishError { self.publish(error) }
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    struct FilesListParams: Encodable {
        let sessionId: UInt32
        let path: String
    }

    func listFiles(
        sessionID: UInt32,
        path: String,
        completion: @escaping (Result<FilesListResult, Error>) -> Void
    ) {
        filesCall(
            "files.list",
            params: FilesListParams(sessionId: sessionID, path: path),
            completion: completion
        )
    }

    struct OpenTextParams: Encodable {
        let sessionId: UInt32
        let path: String
    }

    func openRemoteText(
        sessionID: UInt32,
        path: String,
        completion: @escaping (Result<RemoteTextDocument, Error>) -> Void
    ) {
        filesCall(
            "files.openText",
            params: OpenTextParams(sessionId: sessionID, path: path),
            publishError: false,
            completion: completion
        )
    }

    struct SaveTextParams: Encodable {
        let sessionId: UInt32
        let path: String
        let text: String
        let revision: String
        let bom: Bool
    }

    func saveRemoteText(
        sessionID: UInt32,
        path: String,
        text: String,
        revision: String,
        bom: Bool,
        completion: @escaping (Result<RemoteTextDocument, Error>) -> Void
    ) {
        filesCall(
            "files.saveText",
            params: SaveTextParams(
                sessionId: sessionID,
                path: path,
                text: text,
                revision: revision,
                bom: bom
            ),
            publishError: false,
            completion: completion
        )
    }

    struct DownloadParams: Encodable {
        let sessionId: UInt32
        let remote: String
        let local: String
        let recursive: Bool
        let policy: String
    }

    func downloadFile(
        sessionID: UInt32,
        remote: String,
        local: URL,
        recursive: Bool = false,
        policy: String = "resume",
        completion: @escaping (Result<UInt64, Error>) -> Void
    ) {
        filesCall(
            "files.download",
            params: DownloadParams(
                sessionId: sessionID,
                remote: remote,
                local: local.path,
                recursive: recursive,
                policy: policy
            )
        ) { (result: Result<TransferIDResult, Error>) in
            completion(result.map { $0.transferId })
        }
    }

    struct UploadParams: Encodable {
        let sessionId: UInt32
        let local: String
        let remote: String
        let recursive: Bool
        let policy: String
    }

    func uploadFile(
        sessionID: UInt32,
        local: URL,
        remote: String,
        recursive: Bool = false,
        policy: String = "resume",
        completion: @escaping (Result<UInt64, Error>) -> Void
    ) {
        filesCall(
            "files.upload",
            params: UploadParams(
                sessionId: sessionID,
                local: local.path,
                remote: remote,
                recursive: recursive,
                policy: policy
            )
        ) { (result: Result<TransferIDResult, Error>) in
            completion(result.map { $0.transferId })
        }
    }

    struct FilesPathParams: Encodable {
        let sessionId: UInt32
        let path: String
    }

    func createDirectory(
        sessionID: UInt32,
        path: String,
        all: Bool = false,
        completion: ((Error?) -> Void)? = nil
    ) {
        struct Params: Encodable {
            let sessionId: UInt32
            let path: String
            let all: Bool
        }
        filesCall(
            "files.mkdir",
            params: Params(sessionId: sessionID, path: path, all: all)
        ) { (result: Result<OKResult, Error>) in
            completion?(result.failureError)
        }
    }

    func createFile(
        sessionID: UInt32,
        path: String,
        completion: ((Error?) -> Void)? = nil
    ) {
        filesCall(
            "files.create",
            params: FilesPathParams(sessionId: sessionID, path: path),
            publishError: false
        ) { (result: Result<OKResult, Error>) in
            completion?(result.failureError)
        }
    }

    struct RemoveParams: Encodable {
        let sessionId: UInt32
        let path: String
        let dir: Bool
    }

    func removeFile(
        sessionID: UInt32,
        path: String,
        directory: Bool,
        completion: ((Error?) -> Void)? = nil
    ) {
        filesCall(
            "files.remove",
            params: RemoveParams(sessionId: sessionID, path: path, dir: directory)
        ) { (result: Result<OKResult, Error>) in
            completion?(result.failureError)
        }
    }

    struct RenameParams: Encodable {
        let sessionId: UInt32
        let oldPath: String
        let newPath: String
    }

    func renameFile(
        sessionID: UInt32,
        oldPath: String,
        newPath: String,
        completion: ((Error?) -> Void)? = nil
    ) {
        filesCall(
            "files.rename",
            params: RenameParams(sessionId: sessionID, oldPath: oldPath, newPath: newPath)
        ) { (result: Result<OKResult, Error>) in
            completion?(result.failureError)
        }
    }

    struct ChmodParams: Encodable {
        let sessionId: UInt32
        let path: String
        let mode: UInt32
    }

    func chmod(
        sessionID: UInt32,
        path: String,
        mode: UInt32,
        completion: ((Error?) -> Void)? = nil
    ) {
        filesCall(
            "files.chmod",
            params: ChmodParams(sessionId: sessionID, path: path, mode: mode)
        ) { (result: Result<OKResult, Error>) in
            completion?(result.failureError)
        }
    }

    struct SessionParam: Encodable {
        let sessionId: UInt32
    }

    func fileTransfers(
        sessionID: UInt32,
        completion: @escaping (Result<[FileTransfer], Error>) -> Void
    ) {
        filesCall(
            "files.transfers",
            params: SessionParam(sessionId: sessionID)
        ) { (result: Result<TransfersResult, Error>) in
            completion(result.map { $0.transfers })
        }
    }

    struct TransferControlParams: Encodable {
        let sessionId: UInt32
        let id: UInt64
        let policy: String
    }

    func controlTransfer(
        _ method: String,
        sessionID: UInt32,
        id: UInt64,
        policy: String = "",
        completion: ((Error?) -> Void)? = nil
    ) {
        filesCall(
            method,
            params: TransferControlParams(sessionId: sessionID, id: id, policy: policy)
        ) { (result: Result<OKResult, Error>) in
            completion?(result.failureError)
        }
    }

    func releaseFiles(sessionID: UInt32) {
        filesCall("files.release", params: SessionParam(sessionId: sessionID)) {
            (_: Result<OKResult, Error>) in
        }
    }

    // MARK: Forwards

    struct ForwardParams: Encodable {
        let label: String
        let hostId: String
        let mode: String
        let localPort: UInt16
        let destination: String
        let autoStart: Bool
    }

    func listForwards(completion: @escaping (Result<[PortForward], Error>) -> Void) {
        queue.async {
            do {
                let result: ForwardsResult = try self.callSync("forwards.list", params: EmptyParams())
                DispatchQueue.main.async { completion(.success(result.forwards)) }
            } catch {
                self.publish(error)
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    func addForward(
        _ forward: ForwardParams,
        completion: @escaping (Result<PortForward, Error>) -> Void
    ) {
        queue.async {
            do {
                let result: ForwardResult = try self.callSync("forwards.add", params: forward)
                DispatchQueue.main.async { completion(.success(result.forward)) }
            } catch {
                self.publish(error)
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    func updateForward(
        id: String,
        _ forward: ForwardParams,
        completion: @escaping (Result<PortForward, Error>) -> Void
    ) {
        struct Params: Encodable {
            let id: String
            let label: String
            let hostId: String
            let mode: String
            let localPort: UInt16
            let destination: String
            let autoStart: Bool
        }
        let params = Params(
            id: id,
            label: forward.label,
            hostId: forward.hostId,
            mode: forward.mode,
            localPort: forward.localPort,
            destination: forward.destination,
            autoStart: forward.autoStart
        )
        queue.async {
            do {
                let result: ForwardResult = try self.callSync("forwards.update", params: params)
                DispatchQueue.main.async { completion(.success(result.forward)) }
            } catch {
                self.publish(error)
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    func removeForward(id: String, completion: ((Error?) -> Void)? = nil) {
        struct Params: Encodable { let id: String }
        queue.async {
            do {
                let _: DeletedResult = try self.callSync("forwards.remove", params: Params(id: id))
                DispatchQueue.main.async { completion?(nil) }
            } catch {
                self.publish(error)
                DispatchQueue.main.async { completion?(error) }
            }
        }
    }

    func startForward(
        id: String,
        sessionID: UInt32,
        completion: @escaping (Result<PortForward, Error>) -> Void
    ) {
        struct Params: Encodable {
            let id: String
            let sessionId: UInt32
        }
        queue.async {
            do {
                let result: ForwardResult = try self.callSync(
                    "forwards.start",
                    params: Params(id: id, sessionId: sessionID)
                )
                DispatchQueue.main.async { completion(.success(result.forward)) }
            } catch {
                self.publish(error)
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    func stopForward(id: String, completion: ((Error?) -> Void)? = nil) {
        struct Params: Encodable { let id: String }
        queue.async {
            do {
                let _: ForwardResult = try self.callSync("forwards.stop", params: Params(id: id))
                DispatchQueue.main.async { completion?(nil) }
            } catch {
                self.publish(error)
                DispatchQueue.main.async { completion?(error) }
            }
        }
    }

    // MARK: Remote desktop

    private func setDesktopInputActive(_ id: UInt32, active: Bool) {
        desktopInputLock.lock()
        defer { desktopInputLock.unlock() }
        if active {
            disabledDesktopInput.remove(id)
        } else {
            disabledDesktopInput.insert(id)
            desktopInputEpochs[id, default: 0] &+= 1
            pendingDesktopInput.removeAll { $0.id == id }
            desktopQueuedButtons[id] = nil
        }
    }

    private func enqueueDesktopInput(_ method: String, id: UInt32, params: any Encodable) {
        desktopInputLock.lock()
        defer { desktopInputLock.unlock() }
        guard !disabledDesktopInput.contains(id) else { return }
        let now = ProcessInfo.processInfo.systemUptime
        var isMotion = method == "desktop.wheel"
        if let pointer = params as? DesktopPointerParams {
            isMotion = pointer.buttons == desktopQueuedButtons[id, default: 0]
            desktopQueuedButtons[id] = pointer.buttons
        }
        var command = QueuedDesktopInput(
            id: id, epoch: desktopInputEpochs[id, default: 0], method: method,
            params: params, createdAt: now, isMotion: isMotion
        )
        if let last = pendingDesktopInput.last, last.id == id, last.method == method {
            if let next = params as? DesktopPointerParams,
               let previous = last.params as? DesktopPointerParams,
               command.isMotion, last.isMotion, next.buttons == previous.buttons {
                pendingDesktopInput.removeLast()
            } else if let next = params as? DesktopWheelParams,
                      let previous = last.params as? DesktopWheelParams,
                      next.x == previous.x, next.y == previous.y, next.buttons == previous.buttons,
                      next.dx * previous.dx >= 0, next.dy * previous.dy >= 0,
                      abs(next.dx + previous.dx) <= 10, abs(next.dy + previous.dy) <= 10 {
                command.params = DesktopWheelParams(
                    id: id, x: next.x, y: next.y, dx: next.dx + previous.dx,
                    dy: next.dy + previous.dy, buttons: next.buttons
                )
                pendingDesktopInput.removeLast()
            }
        }
        // Motion loses its meaning once the connection falls behind. Keys
        // keep their press/release ordering; mouse traffic has a bounded queue.
        if command.isMotion {
            pendingDesktopInput.removeAll { $0.isMotion && now - $0.createdAt > 0.25 }
            if pendingDesktopInput.filter({ $0.isMotion }).count >= 32 { return }
        }
        pendingDesktopInput.append(command)
        guard !desktopInputDraining else { return }
        desktopInputDraining = true
        desktopInputQueue.async { self.drainDesktopInput() }
    }

    private func drainDesktopInput() {
        while true {
            desktopInputLock.lock()
            guard !pendingDesktopInput.isEmpty else {
                desktopInputDraining = false
                desktopInputLock.unlock()
                return
            }
            let command = pendingDesktopInput.removeFirst()
            let current = desktopInputEpochs[command.id, default: 0]
            desktopInputLock.unlock()
            guard current == command.epoch,
                  !command.isMotion || ProcessInfo.processInfo.systemUptime - command.createdAt <= 0.25 else { continue }
            let _: DeletedResult? = try? callSync(command.method, params: command.params)
        }
    }

    private func applyDesktopState(_ state: DesktopState) {
        guard !closedDesktopIDs.contains(state.id) || state.state == "closed" else { return }
        setDesktopInputActive(state.id, active: state.isConnected)
        // The status poll repeats the same answer every second. Writing it
        // anyway wakes every view that watches desktops, which rebuilds the
        // key bar's menu and drops its scroll position back to the top.
        if desktops[state.id] != state {
            desktops[state.id] = state
        }
        if state.state != "password" && state.state != "certificate", desktopPrompts[state.id] != nil {
            desktopPrompts[state.id] = nil
        }
    }

    @Published private(set) var desktops: [UInt32: DesktopState] = [:]
    @Published private(set) var desktopPrompts: [UInt32: DesktopPrompt] = [:]
    @Published private(set) var desktopClipboard: [UInt32: String] = [:]

    struct DesktopOpenParams: Encodable {
        let hostId: String
        let width: Int
        let height: Int
        let scale: Double
    }

    func openDesktop(host: Host, width: Int, height: Int, scale: Double) async throws -> DesktopState {
        try await withCheckedThrowingContinuation { continuation in
            queue.async {
                do {
                    let result: DesktopResult = try self.callSync(
                        "desktop.open",
                        params: DesktopOpenParams(hostId: host.id, width: width, height: height, scale: scale)
                    )
                    DispatchQueue.main.async {
                        self.applyDesktopState(result.desktop)
                        continuation.resume(returning: result.desktop)
                    }
                } catch {
                    continuation.resume(throwing: error)
                }
            }
        }
    }

    func closeDesktop(_ id: UInt32) {
        struct Params: Encodable { let id: UInt32 }
        closedDesktopIDs.insert(id)
        setDesktopInputActive(id, active: false)
        queue.async {
            let _: DeletedResult? = try? self.callSync("desktop.close", params: Params(id: id))
            DispatchQueue.main.async {
                self.desktopPrompts[id] = nil
                self.desktopClipboard[id] = nil
                if let state = self.desktops[id] {
                    var closed = state
                    closed.state = "closed"
                    closed.error = nil
                    self.desktops[id] = closed
                }
            }
        }
    }

    func refreshDesktop(_ id: UInt32, completion: @escaping (Result<DesktopState, Error>) -> Void) {
        struct Params: Encodable { let id: UInt32 }
        queue.async {
            do {
                let result: DesktopsResult = try self.callSync("desktop.status", params: Params(id: id))
                DispatchQueue.main.async {
                    if let state = result.desktops.first {
                        self.applyDesktopState(state)
                        completion(.success(state))
                    } else {
                        completion(.failure(RPCError(code: "not_found", message: "the remote desktop is gone")))
                    }
                }
            } catch {
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    struct DesktopPointerParams: Encodable {
        let id: UInt32
        let x: Int
        let y: Int
        let buttons: UInt8
    }

    func desktopPointer(desktopID: UInt32, x: Int, y: Int, buttons: UInt8) {
        enqueueDesktopInput("desktop.pointer", id: desktopID,
                            params: DesktopPointerParams(id: desktopID, x: x, y: y, buttons: buttons))
    }

    struct DesktopWheelParams: Encodable {
        let id: UInt32
        let x: Int
        let y: Int
        let dx: Int
        let dy: Int
        let buttons: UInt8
    }

    func desktopWheel(desktopID: UInt32, x: Int, y: Int, dx: Int, dy: Int, buttons: UInt8 = 0) {
        enqueueDesktopInput("desktop.wheel", id: desktopID,
                            params: DesktopWheelParams(id: desktopID, x: x, y: y, dx: dx, dy: dy, buttons: buttons))
    }

    struct DesktopKeyParams: Encodable {
        let id: UInt32
        var key: String?
        var rune: String?
        var typed: String?
        let down: Bool
        var `repeat`: Bool = false
    }

    func desktopKey(
        desktopID: UInt32,
        key: String? = nil,
        rune: String? = nil,
        typed: String? = nil,
        down: Bool,
        repeat isRepeat: Bool = false
    ) {
        enqueueDesktopInput("desktop.key", id: desktopID,
                            params: DesktopKeyParams(
                    id: desktopID,
                    key: key,
                    rune: rune,
                    typed: typed,
                    down: down,
                    repeat: isRepeat
                ))
    }

    struct DesktopTextParams: Encodable {
        let id: UInt32
        let text: String
    }

    func desktopText(desktopID: UInt32, text: String) {
        guard !text.isEmpty else { return }
        enqueueDesktopInput("desktop.text", id: desktopID,
                            params: DesktopTextParams(id: desktopID, text: text))
    }

    func desktopSetClipboard(desktopID: UInt32, text: String) {
        enqueueDesktopInput("desktop.clipboard.set", id: desktopID,
                            params: DesktopTextParams(id: desktopID, text: text))
    }

    func desktopGetClipboard(
        desktopID: UInt32,
        completion: @escaping (Result<String, Error>) -> Void
    ) {
        struct Params: Encodable { let id: UInt32 }
        struct ResultText: Decodable { let text: String }
        queue.async {
            do {
                let result: ResultText = try self.callSync("desktop.clipboard.get", params: Params(id: desktopID))
                DispatchQueue.main.async { completion(.success(result.text)) }
            } catch {
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    struct DesktopAnswerParams: Encodable {
        let id: UInt32
        let kind: String
        let value: String
        let accept: Bool
        let save: Bool
    }

    func desktopAnswer(desktopID: UInt32, kind: String, value: String = "", accept: Bool = false, save: Bool = false) {
        queue.async {
            do {
                let _: DeletedResult = try self.callSync(
                    "desktop.answer",
                    params: DesktopAnswerParams(id: desktopID, kind: kind, value: value, accept: accept, save: save)
                )
                DispatchQueue.main.async { self.desktopPrompts[desktopID] = nil }
            } catch {
                self.publish(error)
            }
        }
    }

    struct DesktopResizeParams: Encodable {
        let id: UInt32
        let width: Int
        let height: Int
        let scale: Double
    }

    func desktopResize(desktopID: UInt32, width: Int, height: Int, scale: Double) {
        queue.async {
            let _: DeletedResult? = try? self.callSync(
                "desktop.resize",
                params: DesktopResizeParams(id: desktopID, width: width, height: height, scale: scale)
            )
        }
    }

    /// Asks the core for a full repaint; the server may have stopped sending
    /// updates after a screen layout change.
    func desktopRefresh(desktopID: UInt32) {
        struct Params: Encodable { let id: UInt32 }
        queue.async {
            let _: DeletedResult? = try? self.callSync("desktop.refresh", params: Params(id: desktopID))
        }
    }

    func desktopActive(desktopID: UInt32, active: Bool) {
        struct Params: Encodable {
            let id: UInt32
            let active: Bool
        }
        queue.async {
            let _: DeletedResult? = try? self.callSync(
                "desktop.active",
                params: Params(id: desktopID, active: active)
            )
        }
    }

    private func handleDesktopEvent(_ event: CoreEvent) {
        if event.type == "desktop.state", let state = event.desktop {
            DispatchQueue.main.async {
                self.applyDesktopState(state)
            }
            return
        }
        guard let id = event.desktopId else { return }
        switch event.type {
        case "desktop.prompt":
            guard let kind = event.kind.flatMap(DesktopPrompt.Kind.init(rawValue:)) else { return }
            let prompt = DesktopPrompt(
                id: id,
                kind: kind,
                attempt: event.attempt,
                address: event.address,
                fingerprint: event.fingerprint,
                known: event.known,
                changed: event.changed
            )
            DispatchQueue.main.async {
                guard !self.closedDesktopIDs.contains(id),
                      self.desktops[id]?.state == kind.rawValue else { return }
                self.desktopPrompts[id] = prompt
            }
        case "desktop.clipboard":
            guard let text = event.text, !text.isEmpty else { return }
            DispatchQueue.main.async { self.desktopClipboard[id] = text }
        default:
            break
        }
    }

    // MARK: Journal and history

    private func callOnMain<T: Decodable>(
        _ method: String,
        params: Encodable,
        completion: @escaping (Result<T, Error>) -> Void
    ) {
        queue.async {
            do {
                let result: T = try self.callSync(method, params: params)
                DispatchQueue.main.async { completion(.success(result)) }
            } catch {
                self.publish(error)
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    struct LogsListParams: Encodable {
        let search: String?
        let favorite: Bool?
        let outcome: String?
        let limit: Int
        let cursorStartedAt: Int64?
        let cursorId: String?
    }

    func listLogs(
        search: String = "",
        favoriteOnly: Bool = false,
        outcome: String = "",
        limit: Int = 50,
        cursorStartedAt: Int64? = nil,
        cursorID: String? = nil,
        completion: @escaping (Result<SessionLogsResult, Error>) -> Void
    ) {
        let params = LogsListParams(
            search: search.isEmpty ? nil : search,
            favorite: favoriteOnly ? true : nil,
            outcome: outcome.isEmpty ? nil : outcome,
            limit: limit,
            cursorStartedAt: cursorStartedAt,
            cursorId: cursorID
        )
        callOnMain("logs.list", params: params, completion: completion)
    }

    func logEvents(id: String, completion: @escaping (Result<[SessionLogEvent], Error>) -> Void) {
        struct Params: Encodable { let id: String }
        callOnMain("logs.events", params: Params(id: id)) {
            (result: Result<SessionLogEventsResult, Error>) in
            completion(result.map { $0.events })
        }
    }

    func logStats(completion: @escaping (Result<SessionLogStats, Error>) -> Void) {
        callOnMain("logs.stats", params: EmptyParams(), completion: completion)
    }

    func setLogFavorite(
        id: String,
        favorite: Bool,
        completion: ((Error?) -> Void)? = nil
    ) {
        struct Params: Encodable {
            let id: String
            let favorite: Bool
        }
        callOnMain("logs.favorite", params: Params(id: id, favorite: favorite)) {
            (result: Result<DeletedResult, Error>) in
            completion?(result.failureError)
        }
    }

    func deleteLog(id: String, completion: ((Error?) -> Void)? = nil) {
        struct Params: Encodable { let id: String }
        callOnMain("logs.delete", params: Params(id: id)) {
            (result: Result<DeletedResult, Error>) in
            completion?(result.failureError)
        }
    }

    func clearLogs(includeFavorites: Bool, completion: ((Error?) -> Void)? = nil) {
        struct Params: Encodable { let includeFavorites: Bool }
        callOnMain("logs.clear", params: Params(includeFavorites: includeFavorites)) {
            (result: Result<DeletedResult, Error>) in
            completion?(result.failureError)
        }
    }

    struct HistoryListParams: Encodable {
        let hostId: String
        let limit: Int
    }

    func listHistory(
        hostID: String = "",
        limit: Int = 500,
        completion: @escaping (Result<[HistoryEntry], Error>) -> Void
    ) {
        callOnMain("history.list", params: HistoryListParams(hostId: hostID, limit: limit)) {
            (result: Result<HistoryResult, Error>) in
            completion(result.map { $0.entries })
        }
    }

    func deleteHistory(
        hostID: String,
        command: String,
        completion: ((Error?) -> Void)? = nil
    ) {
        struct Params: Encodable {
            let hostId: String
            let command: String
        }
        callOnMain("history.delete", params: Params(hostId: hostID, command: command)) {
            (result: Result<DeletedResult, Error>) in
            completion?(result.failureError)
        }
    }

    func clearHistory(completion: ((Error?) -> Void)? = nil) {
        callOnMain("history.clear", params: EmptyParams()) {
            (result: Result<DeletedResult, Error>) in
            completion?(result.failureError)
        }
    }

    struct CompletionParams: Encodable {
        let sessionId: UInt32
        let line: String?
        let typed: String?
        let locale: String?
    }

    func complete(
        sessionID: UInt32,
        line: String? = nil,
        typed: String? = nil,
        locale: String? = nil,
        completion: @escaping (Result<CompletionResult, Error>) -> Void
    ) {
        callOnMain(
            "completion.complete",
            params: CompletionParams(sessionId: sessionID, line: line, typed: typed, locale: locale)
        ) { (result: Result<CompletionResult, Error>) in
            completion(result)
        }
    }

    // MARK: Sync and backup

    struct SyncConfigureParams: Encodable {
        var kind: String
        var url: String
        var user: String
        var path: String
        var s3: SyncS3Settings?
        var gist: SyncGistSettings?
        var secret: String
        var backupPassword: String
        var allowInsecureHTTP: Bool
        var manual: Bool?
    }

    struct SyncStarted: Decodable {
        var started: Bool?
        var running: Bool?
    }

    func refreshSyncStatus(completion: ((Result<SyncStatus, Error>) -> Void)? = nil) {
        queue.async {
            do {
                let status: SyncStatus = try self.callSync("sync.status", params: EmptyParams())
                DispatchQueue.main.async {
                    self.syncStatus = status
                    completion?(.success(status))
                }
            } catch {
                self.publish(error)
                DispatchQueue.main.async { completion?(.failure(error)) }
            }
        }
    }

    func configureSync(
        _ params: SyncConfigureParams,
        completion: ((Result<SyncStatus, Error>) -> Void)? = nil
    ) {
        queue.async {
            do {
                let status: SyncStatus = try self.callSync("sync.configure", params: params)
                DispatchQueue.main.async {
                    self.syncStatus = status
                    completion?(.success(status))
                }
            } catch {
                DispatchQueue.main.async { completion?(.failure(error)) }
            }
        }
    }

    func clearSync(completion: ((Result<SyncStatus, Error>) -> Void)? = nil) {
        queue.async {
            do {
                let status: SyncStatus = try self.callSync("sync.clear", params: EmptyParams())
                DispatchQueue.main.async {
                    self.syncStatus = status
                    completion?(.success(status))
                }
            } catch {
                DispatchQueue.main.async { completion?(.failure(error)) }
            }
        }
    }

    func testSync() {
        queue.async {
            do {
                let _: SyncStarted = try self.callSync("sync.test", params: EmptyParams())
            } catch {
                self.publish(error)
            }
        }
    }

    func syncNow() {
        queue.async {
            do {
                let _: SyncStarted = try self.callSync("sync.now", params: EmptyParams())
            } catch {
                self.publish(error)
            }
        }
    }

    /// Catches up after the app comes back to the foreground. The core already
    /// debounces and refuses overlapping rounds, so this is safe to call often.
    func syncOnForeground() {
        DispatchQueue.main.async {
            guard self.ready,
                  self.syncStatus.isConfigured,
                  !self.syncStatus.isManual,
                  self.syncStatus.running != true else { return }
            self.syncNow()
        }
    }

    func restoreFromSync(completion: @escaping (Result<RestoreResult, Error>) -> Void) {
        queue.async {
            do {
                let result: RestoreResult = try self.callSync("sync.restore", params: EmptyParams())
                DispatchQueue.main.async { completion(.success(result)) }
            } catch {
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    func exportBackup(
        password: String,
        completion: @escaping (Result<BackupExportResult, Error>) -> Void
    ) {
        struct Params: Encodable { let password: String }
        queue.async {
            do {
                let result: BackupExportResult = try self.callSync(
                    "backup.export",
                    params: Params(password: password)
                )
                DispatchQueue.main.async { completion(.success(result)) }
            } catch {
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    func inspectBackup(
        path: String,
        completion: @escaping (Result<BackupInspectResult, Error>) -> Void
    ) {
        struct Params: Encodable { let path: String }
        queue.async {
            do {
                let result: BackupInspectResult = try self.callSync("backup.inspect", params: Params(path: path))
                DispatchQueue.main.async { completion(.success(result)) }
            } catch {
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    func restoreBackup(
        path: String,
        password: String,
        completion: @escaping (Result<RestoreResult, Error>) -> Void
    ) {
        struct Params: Encodable {
            let path: String
            let password: String
        }
        queue.async {
            do {
                let result: RestoreResult = try self.callSync(
                    "backup.restore",
                    params: Params(path: path, password: password)
                )
                DispatchQueue.main.async {
                    self.refreshHosts()
                    self.refreshKeys()
                    self.refreshAISettings()
                    self.refreshSyncStatus()
                    completion(.success(result))
                }
            } catch {
                DispatchQueue.main.async { completion(.failure(error)) }
            }
        }
    }

    // MARK: Events

    /// Drains every queued event. Called from the UI timer; each drain runs on
    /// its own queue hop so a slow decode never blocks drawing.
    func pollEvents() {
        guard ready else { return }
        queue.async { self.drainEvents() }
    }

    private func drainEvents() {
        while true {
            let count = eventBuffer.withUnsafeMutableBytes { buffer in
                MottyPollEvent(buffer.baseAddress, Int32(buffer.count))
            }
            if count > 0 {
                let data = Data(eventBuffer[0..<Int(count)])
                handleEvent(data)
            } else if count < 0 {
                eventBuffer = [UInt8](repeating: 0, count: -Int(count))
            } else {
                break
            }
        }
    }

    private func handleEvent(_ data: Data) {
        guard let event = try? JSONDecoder().decode(CoreEvent.self, from: data) else { return }
        if event.type.hasPrefix("ai.") {
            if let aiEvent = try? JSONDecoder().decode(AIEvent.self, from: data) {
                DispatchQueue.main.async { self.handleAIEvent(aiEvent) }
            }
            return
        }
        switch event.type {
        case "session.state":
            guard let session = event.session else { return }
            DispatchQueue.main.async {
                if let index = self.sessions.firstIndex(where: { $0.id == session.id }) {
                    self.sessions[index] = session
                } else {
                    self.sessions.append(session)
                }
            }
        case "session.prompt":
            DispatchQueue.main.async { self.prompts.append(event) }
        case "desktop.state", "desktop.prompt", "desktop.clipboard":
            handleDesktopEvent(event)
        case "sync.state":
            let running = event.running ?? false
            updateSyncBackground(running: running)
            DispatchQueue.main.async { self.syncStatus.running = running }
        case "sync.done", "sync.restored":
            updateSyncBackground(running: false)
            refreshSyncStatus()
            refreshHosts()
            refreshKeys()
            refreshAISettings()
        case "sync.error":
            updateSyncBackground(running: false)
            refreshSyncStatus()
        case "sync.test":
            let message = event.ok == true
                ? "The service answered."
                : "Connection failed: \(event.error ?? "unknown error")"
            DispatchQueue.main.async { self.syncTestMessage = message }
        default:
            break
        }
    }

    /// Keeps a sync running for the short time iOS grants after the app leaves
    /// the screen; the system ends it when that time is up.
    private func updateSyncBackground(running: Bool) {
        DispatchQueue.main.async {
            if running, self.syncBackgroundTask == .invalid {
                self.syncBackgroundTask = UIApplication.shared.beginBackgroundTask(withName: "Motty sync") {
                    if self.syncBackgroundTask != .invalid {
                        UIApplication.shared.endBackgroundTask(self.syncBackgroundTask)
                        self.syncBackgroundTask = .invalid
                    }
                }
            } else if !running, self.syncBackgroundTask != .invalid {
                UIApplication.shared.endBackgroundTask(self.syncBackgroundTask)
                self.syncBackgroundTask = .invalid
            }
        }
    }

    // MARK: C ABI

    private func callSync<T: Decodable>(_ method: String, params: Encodable) throws -> T {
        let encoded = try JSONEncoder().encode(params)
        let paramsJSON = String(data: encoded, encoding: .utf8) ?? "{}"
        let response = cCall(method, paramsJSON)
        guard let data = response.data(using: .utf8) else {
            throw RPCError(code: "internal", message: "the core returned no data")
        }
        let envelope = try JSONDecoder().decode(RPCEnvelope<T>.self, from: data)
        if let error = envelope.error { throw error }
        guard let result = envelope.result else {
            throw RPCError(code: "internal", message: "the core returned an empty result")
        }
        return result
    }

    private func cCall(_ method: String, _ params: String) -> String {
        method.withCString { methodCString in
            params.withCString { paramsCString in
                guard let result = MottyCall(UnsafeMutablePointer(mutating: methodCString),
                                             UnsafeMutablePointer(mutating: paramsCString)) else {
                    return ""
                }
                defer { MottyFree(result) }
                return String(cString: result)
            }
        }
    }

    private func publish(_ error: Error) {
        DispatchQueue.main.async { self.lastError = error.localizedDescription }
    }
}

private extension Result {
    /// The error of a failed result, or nil when it succeeded.
    var failureError: Error? {
        if case .failure(let error) = self { return error }
        return nil
    }
}
