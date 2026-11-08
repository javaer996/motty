import Foundation

/// One saved destination. The fields match `mobilecore.Host` on the Go side.
struct Host: Codable, Identifiable, Hashable {
    var id: String = ""
    var label: String = ""
    var group: String?
    var tags: [String]?
    var icon: String?
    var color: String?
    var address: String = ""
    var user: String = ""
    var port: UInt16 = 22
    var `protocol`: String?
    var auth: String?
    var identity: String?
    var proxyJump: String?
    var loginCommand: String?
    var noHistory: Bool?
    var viaSsh: String?
    var clipboard: Bool?
    var console: Bool?
    var scale: Double?
    /// The outbound proxy this host's traffic leaves through, nil for a direct
    /// connection. Its password is kept in the vault, never here.
    var proxy: HostProxy?
    var createdAt: Int64?
    var updatedAt: Int64?
    // Desktop catalog fields this app does not edit yet. They are kept so a
    // host written on the desktop survives being saved here.
    var system: String?
    var line: String?
    var flow: String?
    var algorithms: String?
    var algorithmList: String?
    var forwardX11: Bool?
    var agentForward: Bool?
    var transfer: String?
    var chromeless: Bool?

    static func empty() -> Host { Host() }

    static func defaultPort(for protocolName: String) -> UInt16 {
        switch protocolName {
        case "vnc": return 5900
        case "rdp": return 3389
        default: return 22
        }
    }

    var protocolName: String {
        let value = self.`protocol`?.lowercased() ?? ""
        return value.isEmpty ? "ssh" : value
    }

    var isDesktop: Bool {
        protocolName == "vnc" || protocolName == "rdp"
    }

    var protocolText: String {
        switch protocolName {
        case "vnc": return "VNC"
        case "rdp": return "RDP"
        default: return "SSH"
        }
    }

    var clipboardEnabled: Bool {
        clipboard ?? isDesktop
    }

    var target: String {
        port == Host.defaultPort(for: protocolName) ? address : "\(address):\(port)"
    }
}

/// A host's outbound proxy. The kinds are the desktop application's strings,
/// so a host edited on either side means the same thing.
struct HostProxy: Codable, Hashable {
    static let socks5 = "socks5"
    static let http = "http"

    var kind: String = ""
    var address: String = ""
    var user: String?
    var bypass: String?

    init(kind: String = "", address: String = "", user: String? = nil, bypass: String? = nil) {
        self.kind = kind
        self.address = address
        self.user = user
        self.bypass = bypass
    }

    var enabled: Bool { !kind.isEmpty }

    /// What the form shows: "Off" or "SOCKS5 proxy.example.com:1080".
    var summary: String {
        switch kind {
        case HostProxy.socks5: return "SOCKS5 \(address)"
        case HostProxy.http: return "HTTP \(address)"
        default: return ""
        }
    }
}

/// The front end's view of one terminal tab.
struct SessionInfo: Codable, Identifiable, Hashable {
    var id: UInt32
    var hostId: String
    var title: String
    var phase: String
    var address: String
    var user: String
    var cols: Int
    var rows: Int
    var connectedAt: Int64?
    var error: String?
    var method: String?
    var serverVersion: String?

    var isConnected: Bool { phase == "connected" }
    var isWaiting: Bool {
        ["hostkey", "password", "passphrase", "authenticating", "connecting"].contains(phase)
    }
}

struct RPCError: Codable, Error, LocalizedError {
    var code: String
    var message: String
    var errorDescription: String? { message }
}

struct RPCEnvelope<T: Decodable>: Decodable {
    var ok: Bool
    var result: T?
    var error: RPCError?
}

struct HostsResult: Decodable {
    var hosts: [Host]
}

struct HostResult: Decodable {
    var host: Host
}

struct GroupsResult: Decodable {
    var groups: [String]
}

struct SessionsResult: Decodable {
    var sessions: [SessionInfo]
}

struct ImportResult: Decodable {
    struct Warning: Decodable {
        var code: String
        var path: String?
        var line: Int?
        var directive: String?
        var detail: String?
    }
    var hosts: [Host]
    var warnings: [Warning]?
}

/// One private key imported into the app's data directory.
struct KeyInfo: Decodable, Identifiable, Hashable {
    var name: String
    var path: String
    var publicKey: String?
    var encrypted: Bool
    var createdAt: Int64?

    var id: String { name }

    /// "ssh-ed25519 AAAAC3..." trimmed to something a list row can show.
    var shortPublicKey: String {
        guard let publicKey, !publicKey.isEmpty else {
            return encrypted ? "Encrypted key" : "No public key"
        }
        let parts = publicKey.split(separator: " ")
        guard parts.count >= 2 else { return publicKey }
        let body = String(parts[1])
        return parts[0] + " " + body.prefix(16) + "…"
    }
}

struct KeysResult: Decodable {
    var keys: [KeyInfo]
}

// MARK: - Assistant

/// One configured assistant endpoint. Matches `mobilecore.aiProvider`. The key
/// is stored in the app's private container.
struct AIProvider: Codable, Equatable, Identifiable, Hashable {
    var id: String = ""
    var name: String?
    var provider: String = "openai"
    var baseUrl: String?
    var apiKey: String?
    var model: String = ""
    var maxTokens: Int?
    var temperature: Double?
    var system: String?
    var mode: String = "agent"

    /// A name to show in lists: the user's label, else the protocol.
    var displayName: String {
        if let name, !name.trimmingCharacters(in: .whitespaces).isEmpty { return name }
        return kindTitle
    }

    var kindTitle: String {
        switch provider {
        case "anthropic": return "Anthropic"
        case "responses": return "OpenAI Responses"
        default: return "OpenAI compatible"
        }
    }

    /// The line under the name in the provider list.
    var summary: String {
        var parts: [String] = [kindTitle]
        if !model.trimmingCharacters(in: .whitespaces).isEmpty { parts.append(model) }
        return parts.joined(separator: " · ")
    }
}

/// Matches `mobilecore.aiSettings`: the configured providers and the active
/// one. Switching providers never loses the others' keys.
struct AISettings: Codable, Equatable {
    var providers: [AIProvider] = []
    var currentId: String?
    var privacy: AIPrivacySettings?

    var current: AIProvider? {
        providers.first { $0.id == currentId } ?? providers.first
    }
}

/// Matches `mobilecore.aiPrivacySettings`: what the assistant hides before a
/// message leaves the device. Addresses, account names and the current host
/// are on by default; host names and e-mail addresses are opt-in.
struct AIPrivacySettings: Codable, Equatable {
    var enabled: Bool = true
    var knownValues: Bool = true
    var secrets: Bool = true
    var ips: Bool = true
    var accounts: Bool = true
    var hosts: Bool = false
    var emails: Bool = false
    var custom: Bool = true
}

/// One value the guard replaced or restored.
struct AIPrivacyReplacement: Decodable, Identifiable, Equatable {
    var category: String
    var token: String
    var value: String

    var id: String { token }
}

/// One request or reply in the recent exchange log.
struct AIPrivacyAuditEntry: Decodable, Identifiable, Equatable {
    var id: String
    var time: Int64
    var direction: String
    var runId: UInt64?
    var sessionId: UInt32?
    var turn: Int?
    var preview: String?
    var count: Int?
    var replacements: [AIPrivacyReplacement]?
}

struct AIPrivacyInfo: Codable, Equatable {
    var settings: AIPrivacySettings = AIPrivacySettings()
    var customStrings: [String]? = []
}

struct AIPrivacyLogResult: Decodable {
    var entries: [AIPrivacyAuditEntry]?
}

struct AIPrivacyPreviewResult: Decodable {
    var enabled: Bool?
    var text: String
    var count: Int?
    var replacements: [AIPrivacyReplacement]?
}

struct AISendResult: Decodable {
    var runId: UInt64
}

struct AIModelsResult: Decodable {
    var models: [String]
}

/// One event from the assistant stream. Every field is optional because the
/// type carries several shapes.
struct AIEvent: Decodable {
    var type: String
    var runId: UInt64?
    var sessionId: UInt32?
    var state: String?
    var error: String?
    var text: String?
    var step: Int?
    var id: String?
    var name: String?
    var arguments: String?
    var isError: Bool?
    var denied: Bool?
    var approvalId: String?
    var tool: String?
    var kind: String?
    var summary: String?
    var level: String?
    var levelValue: Int?
    var reasons: [String]?
    var safer: String?
    var needsText: Bool?
    var confirm: String?
    var sessionLabel: String?
    var inputTokens: Int?
    var outputTokens: Int?
    var incomplete: Bool?
    var stopReason: String?
}

/// One row in the assistant conversation.
struct AIChatMessage: Identifiable, Equatable {
    enum Role: Equatable {
        case user
        case assistant
        case reasoning
        case tool
        case error
    }

    var id: String
    var role: Role
    var text: String
    /// The provider's tool-call id. `id` stays unique for SwiftUI; several
    /// runs may reuse the same call id.
    var toolCallID: String? = nil
    var toolName: String?
    var toolArguments: String?
    var toolResult: String?
    var toolRunning: Bool = false
    var isError: Bool = false
    var streaming: Bool = false
}

/// One tool call waiting for the user's decision.
struct AIApprovalRequest: Identifiable, Equatable {
    var id: String
    var runID: UInt64
    var tool: String
    var summary: String
    var level: String
    var reasons: [String]
    var safer: String?
    var needsText: Bool
    var confirm: String
    var sessionLabel: String?
}

struct AIConversation {
    var messages: [AIChatMessage] = []
    var runID: UInt64?
    var pendingApproval: AIApprovalRequest?
    /// Whether the saved conversation for this host has been read yet. The
    /// chat panel loads it once, so reconnecting or reopening the app shows
    /// the earlier messages.
    var historyLoaded = false

    var isRunning: Bool {
        if runID != nil { return true }
        return messages.last?.streaming == true
    }
}

/// One saved assistant thread in the history list.
struct AIConversationSummary: Decodable, Identifiable, Equatable {
    var id: Int64
    var title: String
    var hostLabel: String?
    /// Unix milliseconds, from the Go store's conversation timestamp.
    var updated: Int64
    var active: Bool
}

struct AIConversationListResult: Decodable {
    var conversations: [AIConversationSummary]
}

struct AIHistoryToolCall: Decodable {
    var id: String
    var name: String
    var arguments: String
}

/// One persisted message from `ai.conversation.get`. Tool results refer back
/// to the call they answer by id.
struct AIHistoryMessage: Decodable {
    var role: String
    var text: String?
    var toolCalls: [AIHistoryToolCall]?
    var toolCallId: String?
}

struct AIConversationView: Decodable {
    var id: Int64
    var messages: [AIHistoryMessage]
}

struct DeletedResult: Decodable {
    var deleted: Bool?
    var closed: Bool?
    var sent: Bool?
    var resized: Bool?
    var answered: Bool?
    var offset: Int?
    /// Set by `ai.conversation.delete` when the removed thread was the one
    /// the host was continuing.
    var active: Bool?
}

struct HostKeyPrompt: Codable, Hashable {
    var address: String
    var type: String
    var fingerprint: String
    var known: [String]?
    var file: String?
    var changed: Bool
}

struct Question: Codable, Hashable {
    var text: String
    var secret: Bool
}

/// One event from the core's queue. Fields are optional because the queue
/// carries several shapes under one envelope.
struct CoreEvent: Decodable {
    var type: String
    var session: SessionInfo?
    var sessionID: UInt32?
    var kind: String?
    var hostKey: HostKeyPrompt?
    var questions: [Question]?
    var attempt: Int?
    var identityPath: String?

    // Remote desktop events.
    var desktop: DesktopState?
    var desktopId: UInt32?
    var address: String?
    var fingerprint: String?
    var known: [String]?
    var changed: Bool?
    var text: String?

    // Sync and backup events.
    var running: Bool?
    var phase: String?
    var message: String?
    var ok: Bool?
    var error: String?
}

// MARK: - Sync and backup

struct SyncS3Settings: Codable, Hashable {
    var endpoint: String = ""
    var region: String = ""
    var bucket: String = ""
    var key: String = ""
    var accessKey: String = ""
    var pathStyle: Bool = false
}

struct SyncGistSettings: Codable, Hashable {
    var id: String = ""
    var file: String = ""
    var api: String = ""
}

/// Mirrors the status object the core returns; it never holds a password.
struct SyncStatus: Decodable, Equatable {
    var kind: String?
    var url: String?
    var user: String?
    var path: String?
    var s3: SyncS3Settings?
    var gist: SyncGistSettings?
    var etag: String?
    var lastSync: String?
    var manual: Bool?
    var allowInsecureHTTP: Bool?
    var configured: Bool?
    var running: Bool?
    var error: String?
    var hasSecret: Bool?
    var hasBackupPassword: Bool?

    var isConfigured: Bool { configured ?? false }
    var isManual: Bool { manual ?? false }

    var kindText: String {
        switch kind ?? "" {
        case "s3": return "S3"
        case "gist": return "GitHub Gist"
        default: return "WebDAV"
        }
    }
}

struct BackupExportResult: Decodable {
    var path: String
    var name: String
    var bytes: Int
}

struct BackupInspectResult: Decodable {
    var backup: Bool
    var version: Int?
    var bytes: Int?
}

struct RestoreResult: Decodable {
    var hosts: Int
    var records: Int?
    var created: String?
    var device: String?
    var tag: String?
}

struct CoreEventEnvelope: Decodable {
    var type: String
}

// MARK: - Files

/// One remote SFTP directory item. Mirrors `mobilecore.fileEntry`.
struct RemoteEntry: Decodable, Identifiable, Hashable {
    var name: String
    var size: Int64
    var mode: UInt32
    var modTime: Int64?
    var isDir: Bool
    var isLink: Bool
    var linkTarget: String?
    var linkDir: Bool?
    var linkBroken: Bool?
    var owner: String?
    var group: String?

    var id: String { name }

    var permissions: String {
        let bits = mode & 0o777
        let chars: [Character] = ["r", "w", "x"]
        var text = ""
        for shift in stride(from: 6, through: 0, by: -3) {
            for (index, char) in chars.enumerated() {
                text.append((bits >> UInt32(shift + (2 - index))) & 1 == 1 ? char : "-")
            }
        }
        return text
    }

    var modified: Date? {
        guard let modTime, modTime > 0 else { return nil }
        return Date(timeIntervalSince1970: TimeInterval(modTime))
    }

    var sizeText: String {
        ByteCountFormatter.string(fromByteCount: size, countStyle: .file)
    }
}

struct FilesListResult: Decodable {
    var path: String
    var entries: [RemoteEntry]
}

/// A remote UTF-8 file opened in the built-in editor. `revision` is the
/// content identity returned by the core; saving with it prevents silently
/// overwriting a file someone else changed in the meantime.
struct RemoteTextDocument: Decodable {
    var text: String
    var revision: String
    var bom: Bool
    var size: Int
}

/// One transfer row. Mirrors `mobilecore.fileTransfer`.
struct FileTransfer: Decodable, Identifiable, Hashable {
    var id: UInt64
    var operation: String
    var source: String
    var destination: String
    var recursive: Bool
    var state: String
    var filesDone: Int
    var filesTotal: Int
    var bytesDone: Int64
    var bytesTotal: Int64
    var bytesPerSecond: Int64
    var resumed: Int64
    var error: String?
    var retryable: Bool
    var queuedAt: Int64?
    var startedAt: Int64?
    var finishedAt: Int64?

    var isActive: Bool {
        state == "queued" || state == "running" || state == "paused"
    }

    var isDownload: Bool { operation == "download" }

    var displayName: String {
        (isDownload ? source : destination).split(separator: "/").last.map(String.init) ?? source
    }

    var progress: Double {
        guard bytesTotal > 0 else { return state == "completed" ? 1 : 0 }
        return min(1, max(0, Double(bytesDone) / Double(bytesTotal)))
    }

    var speedText: String {
        guard state == "running", bytesPerSecond > 0 else { return "" }
        return ByteCountFormatter.string(fromByteCount: bytesPerSecond, countStyle: .file) + "/s"
    }

    var localURL: URL? {
        guard isDownload, state == "completed", destination.hasPrefix("/") else { return nil }
        return URL(fileURLWithPath: destination)
    }
}

struct TransfersResult: Decodable {
    var transfers: [FileTransfer]
}

struct TransferIDResult: Decodable {
    var transferId: UInt64
}

/// Decoded form of the small `{"created":true}` style results.
struct OKResult: Decodable {}

// MARK: - Port forwards

/// Mirrors `mobilecore.forwardState`.
struct PortForward: Decodable, Identifiable, Hashable {
    var id: String
    var label: String?
    var hostId: String
    var mode: String
    var listenAddress: String
    var destination: String?
    var autoStart: Bool
    var sessionId: UInt32?
    var state: String
    var error: String?
    var active: Int
    var accepted: UInt64
    var uploaded: UInt64
    var downloaded: UInt64

    var modeText: String {
        switch mode {
        case "remote": return "Remote"
        case "socks5": return "SOCKS5"
        case "http-connect": return "HTTP CONNECT"
        default: return "Local"
        }
    }

    var targetText: String {
        switch mode {
        case "socks5", "http-connect": return "dynamic destination"
        default: return destination ?? ""
        }
    }

    var isRunning: Bool { state == "running" }
    var isStarting: Bool { state == "starting" }
    var showsActivity: Bool { state == "running" || state == "starting" }

}

struct ForwardsResult: Decodable {
    var forwards: [PortForward]
}

struct ForwardResult: Decodable {
    var forward: PortForward
}

// MARK: - Remote desktop

/// Mirrors `mobilecore.desktopState`.
struct DesktopState: Decodable, Identifiable, Hashable {
    var id: UInt32
    var hostId: String
    var label: String
    var address: String
    var `protocol`: String
    var state: String
    var error: String?
    var name: String?
    var encrypted: Bool
    var width: Int
    var height: Int
    var attempt: Int?

    var isConnected: Bool { state == "connected" }
    var isFinished: Bool { state == "failed" || state == "closed" }
    var isWaiting: Bool { state == "connecting" || state == "password" || state == "certificate" }

    var protocolText: String { self.`protocol`.uppercased() }

    var statusText: String {
        switch state {
        case "connecting": return "Connecting…"
        case "password": return "Waiting for the password"
        case "certificate": return "Check the certificate"
        case "connected": return "Connected"
        case "failed": return error ?? "Connection failed"
        case "closed": return "Disconnected"
        default: return state
        }
    }
}

struct DesktopResult: Decodable {
    var desktop: DesktopState
}

struct DesktopsResult: Decodable {
    var desktops: [DesktopState]
}

/// The question a VNC or RDP handshake is waiting on.
struct DesktopPrompt: Identifiable, Hashable {
    enum Kind: String {
        case password
        case certificate
    }

    var id: UInt32
    var kind: Kind
    var attempt: Int?
    var address: String?
    var fingerprint: String?
    var known: [String]?
    var changed: Bool?
}

// MARK: - Session journal and history

/// Mirrors `mobilecore.logRecordJSON`.
struct SessionLogRecord: Decodable, Identifiable, Hashable {
    var id: String
    var host: String
    var address: String?
    var account: String?
    var `protocol`: String
    var startedAt: Int64
    var endedAt: Int64?
    var status: String
    var favorite: Bool
    var reason: String?
    var durationMillis: Int64
    var truncated: Bool?

    var startedDate: Date { Date(timeIntervalSince1970: TimeInterval(startedAt) / 1000) }
    var endedDate: Date? {
        guard let endedAt, endedAt > 0 else { return nil }
        return Date(timeIntervalSince1970: TimeInterval(endedAt) / 1000)
    }

    var statusText: String {
        switch status {
        case "connecting": return "Connecting"
        case "active": return "Active"
        case "reconnecting": return "Reconnecting"
        case "ended": return "Ended"
        case "failed": return "Failed"
        case "interrupted": return "Interrupted"
        case "tracking-stopped": return "Stopped"
        default: return "Unknown"
        }
    }
}

struct SessionLogsResult: Decodable {
    var records: [SessionLogRecord]
    var matched: Int64
    var total: Int64
    var more: Bool
    var nextStartedAt: Int64?
    var nextId: String?
}

struct SessionLogEvent: Decodable, Identifiable, Hashable {
    var kind: String
    var at: Int64
    var attempt: Int?
    var reason: String?

    var id: String { "\(kind)-\(at)-\(attempt ?? 0)" }
    var date: Date { Date(timeIntervalSince1970: TimeInterval(at) / 1000) }
}

struct SessionLogEventsResult: Decodable {
    var events: [SessionLogEvent]
}

struct SessionLogStats: Decodable {
    var records: Int
    var favorites: Int
    var events: Int
    var bytes: Int64
}

struct HistoryEntry: Decodable, Identifiable, Hashable {
    var command: String
    var used: Int64
    var count: Int

    var id: String { command }
    var usedDate: Date { Date(timeIntervalSince1970: TimeInterval(used) / 1000) }
}

struct HistoryResult: Decodable {
    var entries: [HistoryEntry]
}

struct CompletionCandidate: Decodable, Identifiable, Hashable {
    var text: String
    var insert: String
    var desc: String?
    var kind: String

    var id: String { insert }

    var iconName: String {
        switch kind {
        case "option": return "switch.2"
        case "value": return "textformat"
        case "combo": return "wand.and.stars"
        default: return "terminal"
        }
    }
}

struct CompletionResult: Decodable {
    var line: String
    var candidates: [CompletionCandidate]
    var suppressed: Bool
    var derived: Bool?
}

// MARK: - Terminal snapshot

/// Decoded form of the binary screen snapshot `MottyScreenSnapshot` writes.
/// See docs/ios/architecture.md for the wire layout.
struct ScreenSnapshot {
    struct Cell {
        var rune: UInt32
        var fg: UInt32
        var bg: UInt32
        var attr: UInt16
        var width: UInt8
        var flags: UInt8
    }

    var version: UInt64 = 0
    var cols = 0
    var rows = 0
    var cursorX = 0
    var cursorY = 0
    var cursorStyle: UInt32 = 0
    var flags: UInt32 = 0
    var scrollback = 0
    var viewOffset = 0
    var cells: [[Cell]] = []

    var altScreen: Bool { flags & 1 != 0 }
    var cursorVisible: Bool { flags & 2 != 0 }
    var cursorSteady: Bool { flags & 4 != 0 }
    var bracketedPaste: Bool { flags & 8 != 0 }
    var appCursorKeys: Bool { flags & 16 != 0 }
    var mouseEnabled: Bool { flags & 64 != 0 }

    static let headerSize = 48
    static let cellSize = 16

    init() {}

    init?(data: Data) {
        guard data.count >= Self.headerSize else { return nil }
        var parsedVersion: UInt64 = 0
        var parsedCols = 0
        var parsedRows = 0
        var parsedCursorX = 0
        var parsedCursorY = 0
        var parsedCursorStyle: UInt32 = 0
        var parsedFlags: UInt32 = 0
        var parsedScrollback = 0
        var parsedViewOffset = 0
        var parsedCells: [[Cell]] = []

        let ok = data.withUnsafeBytes { raw -> Bool in
            func u32(_ offset: Int) -> UInt32 {
                raw.loadUnaligned(fromByteOffset: offset, as: UInt32.self)
            }
            parsedVersion = raw.loadUnaligned(fromByteOffset: 0, as: UInt64.self)
            parsedCols = Int(u32(8))
            parsedRows = Int(u32(12))
            parsedCursorX = Int(u32(16))
            parsedCursorY = Int(u32(20))
            parsedCursorStyle = u32(24)
            parsedFlags = u32(28)
            parsedScrollback = Int(u32(32))
            parsedViewOffset = Int(u32(36))

            var offset = Self.headerSize
            parsedCells.reserveCapacity(parsedRows)
            for _ in 0..<parsedRows {
                guard offset + 4 <= raw.count else { return false }
                let count = Int(u32(offset))
                offset += 4
                guard count >= 0, offset + count * Self.cellSize <= raw.count else { return false }
                var row: [Cell] = []
                row.reserveCapacity(count)
                for _ in 0..<count {
                    row.append(Cell(
                        rune: raw.loadUnaligned(fromByteOffset: offset, as: UInt32.self),
                        fg: raw.loadUnaligned(fromByteOffset: offset + 4, as: UInt32.self),
                        bg: raw.loadUnaligned(fromByteOffset: offset + 8, as: UInt32.self),
                        attr: raw.loadUnaligned(fromByteOffset: offset + 12, as: UInt16.self),
                        width: raw.loadUnaligned(fromByteOffset: offset + 14, as: UInt8.self),
                        flags: raw.loadUnaligned(fromByteOffset: offset + 15, as: UInt8.self)
                    ))
                    offset += Self.cellSize
                }
                parsedCells.append(row)
            }
            return true
        }
        guard ok else { return nil }
        version = parsedVersion
        cols = parsedCols
        rows = parsedRows
        cursorX = parsedCursorX
        cursorY = parsedCursorY
        cursorStyle = parsedCursorStyle
        flags = parsedFlags
        scrollback = parsedScrollback
        viewOffset = parsedViewOffset
        cells = parsedCells
    }
}

// MARK: - Terminal attributes

enum TerminalAttr {
    static let bold: UInt16 = 1 << 0
    static let dim: UInt16 = 1 << 1
    static let italic: UInt16 = 1 << 2
    static let underline: UInt16 = 1 << 3
    static let blink: UInt16 = 1 << 4
    static let inverse: UInt16 = 1 << 5
    static let hidden: UInt16 = 1 << 6
    static let strike: UInt16 = 1 << 7
    static let wideHead: UInt16 = 1 << 8
    static let wideTail: UInt16 = 1 << 9
}

// MARK: - Colors

enum TerminalColor {
    /// term.Color packs its kind in the top byte: 0 default, 1 palette, 2 RGB.
    enum Kind: UInt32 {
        case defaultValue = 0
        case palette = 1
        case trueColor = 2
    }

    static func kind(_ raw: UInt32) -> Kind {
        Kind(rawValue: raw >> 24) ?? .defaultValue
    }

    static func rgb(_ raw: UInt32) -> (UInt8, UInt8, UInt8) {
        (UInt8((raw >> 16) & 0xFF), UInt8((raw >> 8) & 0xFF), UInt8(raw & 0xFF))
    }

    static func index(_ raw: UInt32) -> Int {
        Int(raw & 0xFF)
    }

    /// The standard xterm palette: 16 system colors, a 6×6×6 cube, and grays.
    static let palette: [UInt32] = {
        var colors: [UInt32] = [
            0x000000, 0xCD0000, 0x00CD00, 0xCDCD00, 0x0000EE, 0xCD00CD, 0x00CDCD, 0xE5E5E5,
            0x7F7F7F, 0xFF0000, 0x00FF00, 0xFFFF00, 0x5C5CFF, 0xFF00FF, 0x00FFFF, 0xFFFFFF,
        ]
        let levels: [UInt32] = [0, 95, 135, 175, 215, 255]
        for r in levels {
            for g in levels {
                for b in levels {
                    colors.append((r << 16) | (g << 8) | b)
                }
            }
        }
        for i in 0..<24 {
            let v = UInt32(8 + i * 10)
            colors.append((v << 16) | (v << 8) | v)
        }
        return colors
    }()
}
