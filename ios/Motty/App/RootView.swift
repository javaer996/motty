import SwiftUI
import UniformTypeIdentifiers

struct RootView: View {
    @EnvironmentObject private var bridge: MottyBridge

    @State private var editingHost: Host?
    @State private var activeSession: SessionInfo?
    @State private var desktopHost: Host?
    @State private var importing = false
    @State private var importMessage: String?
    @State private var showingSettings = false
    @State private var showingForwards = false

    var body: some View {
        NavigationStack {
            HostListView(
                hosts: bridge.hosts,
                sessions: bridge.sessions,
                onOpenHost: open,
                onOpenSession: { activeSession = $0 },
                onCloseSession: { bridge.closeSession($0.id) },
                onCloseSessions: { sessions in
                    for session in sessions { bridge.closeSession(session.id) }
                },
                onEdit: { editingHost = $0 },
                onDelete: { bridge.deleteHost($0) },
                onImport: { importing = true }
            )
            .navigationTitle("Motty")
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button { showingSettings = true } label: {
                        Image(systemName: "gearshape")
                    }
                    .accessibilityIdentifier("settings")
                }
                ToolbarItem(placement: .topBarLeading) {
                    Button { showingForwards = true } label: {
                        Image(systemName: "network")
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { editingHost = Host.empty() } label: {
                        Image(systemName: "plus")
                    }
                }
            }
        }
        .sheet(item: $editingHost) { host in
            HostEditorView(host: host)
                .environmentObject(bridge)
        }
        .sheet(isPresented: $showingSettings) {
            SettingsView()
                .environmentObject(bridge)
        }
        .sheet(isPresented: $showingForwards) {
            ForwardsView()
                .environmentObject(bridge)
        }
        .fullScreenCover(item: $activeSession) { session in
            SessionView(sessionID: session.id)
                .environmentObject(bridge)
        }
        .fullScreenCover(item: $desktopHost) { host in
            DesktopSessionView(host: host)
                .environmentObject(bridge)
        }
        .fileImporter(
            isPresented: $importing,
            allowedContentTypes: [.item],
            allowsMultipleSelection: false
        ) { result in
            handleImport(result)
        }
        .onReceive(Timer.publish(every: 0.1, on: .main, in: .common).autoconnect()) { _ in
            bridge.pollEvents()
        }
        .alert("Error", isPresented: errorBinding) {
            Button("OK") { bridge.lastError = nil }
        } message: {
            Text(bridge.lastError ?? "")
        }
        .alert("Import", isPresented: importBinding) {
            Button("OK") { importMessage = nil }
        } message: {
            Text(importMessage ?? "")
        }
    }

    private var errorBinding: Binding<Bool> {
        Binding(
            get: { bridge.lastError != nil },
            set: { if !$0 { bridge.lastError = nil } }
        )
    }

    private var importBinding: Binding<Bool> {
        Binding(
            get: { importMessage != nil },
            set: { if !$0 { importMessage = nil } }
        )
    }

    private func open(_ host: Host) {
        if host.isDesktop {
            desktopHost = host
            return
        }
        // Every tap on a configured host opens another terminal. Existing
        // connections live in the Open sessions section so they can be
        // resumed without taking away the ability to open a second shell.
        Task {
            do {
                let info = try await bridge.openSession(host: host, cols: 80, rows: 24)
                await MainActor.run { activeSession = info }
            } catch {
                await MainActor.run { bridge.lastError = error.localizedDescription }
            }
        }
    }

    private func handleImport(_ result: Result<[URL], Error>) {
        switch result {
        case .failure(let error):
            importMessage = error.localizedDescription
        case .success(let urls):
            guard let url = urls.first else { return }
            let accessing = url.startAccessingSecurityScopedResource()
            bridge.importSSHConfig(path: url.path) { result in
                if accessing { url.stopAccessingSecurityScopedResource() }
                switch result {
                case .failure(let error):
                    importMessage = error.localizedDescription
                case .success(let imported):
                    let warnings = imported.warnings?.count ?? 0
                    if warnings > 0 {
                        importMessage = AppLanguage.localized(
                            "Imported \(imported.hosts.count) hosts with \(warnings) warnings."
                        )
                    } else {
                        importMessage = AppLanguage.localized("Imported \(imported.hosts.count) hosts.")
                    }
                }
            }
        }
    }
}

private struct HostListView: View {
    let hosts: [Host]
    let sessions: [SessionInfo]
    let onOpenHost: (Host) -> Void
    let onOpenSession: (SessionInfo) -> Void
    let onCloseSession: (SessionInfo) -> Void
    let onCloseSessions: ([SessionInfo]) -> Void
    let onEdit: (Host) -> Void
    let onDelete: (Host) -> Void
    let onImport: () -> Void

    @State private var search = ""

    private func liveSessions(for host: Host) -> [SessionInfo] {
        sessions.filter { $0.hostId == host.id }.sorted { $0.id < $1.id }
    }

    private var orderedSessions: [SessionInfo] {
        sessions.sorted { $0.id > $1.id }
    }

    private var visibleSessions: [SessionInfo] {
        guard !search.isEmpty else { return orderedSessions }
        let needle = search.lowercased()
        return orderedSessions.filter { session in
            let hostLabel = hosts.first { $0.id == session.hostId }?.label ?? ""
            return session.title.lowercased().contains(needle) ||
                session.address.lowercased().contains(needle) ||
                session.user.lowercased().contains(needle) ||
                hostLabel.lowercased().contains(needle)
        }
    }

    /// Per-host terminal numbers. Two shells on the same server otherwise look
    /// identical when the shell's title is the host label.
    private var sessionOrdinals: [UInt32: Int] {
        var counts: [String: Int] = [:]
        var ordinals: [UInt32: Int] = [:]
        for session in sessions.sorted(by: { $0.id < $1.id }) {
            let ordinal = (counts[session.hostId] ?? 0) + 1
            counts[session.hostId] = ordinal
            ordinals[session.id] = ordinal
        }
        return ordinals
    }

    private func sessionCount(forHostID id: String) -> Int {
        sessions.filter { $0.hostId == id }.count
    }

    private var filtered: [Host] {
        guard !search.isEmpty else { return hosts }
        let needle = search.lowercased()
        return hosts.filter {
            $0.label.lowercased().contains(needle) ||
            $0.address.lowercased().contains(needle) ||
            $0.user.lowercased().contains(needle) ||
            ($0.group ?? "").lowercased().contains(needle)
        }
    }

    private var sections: [(String, [Host])] {
        var groups: [String: [Host]] = [:]
        for host in filtered {
            groups[host.group ?? "", default: []].append(host)
        }
        return groups.keys.sorted { left, right in
            if left.isEmpty { return false }
            if right.isEmpty { return true }
            return left.localizedStandardCompare(right) == .orderedAscending
        }.map { key in
            (key, groups[key]!.sorted { $0.label.localizedCaseInsensitiveCompare($1.label) == .orderedAscending })
        }
    }

    var body: some View {
        List {
            if !visibleSessions.isEmpty {
                Section("Open sessions") {
                    ForEach(visibleSessions) { session in
                        Button {
                            onOpenSession(session)
                        } label: {
                            SessionRow(
                                session: session,
                                ordinal: sessionOrdinals[session.id] ?? 1,
                                showsOrdinal: sessionCount(forHostID: session.hostId) > 1
                            )
                        }
                        .buttonStyle(.plain)
                        .contextMenu {
                            Button(role: .destructive) {
                                onCloseSession(session)
                            } label: {
                                Label("Close connection", systemImage: "xmark.circle")
                            }
                        }
                        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                            Button(role: .destructive) {
                                onCloseSession(session)
                            } label: {
                                Label("Close connection", systemImage: "xmark.circle")
                            }
                        }
                    }
                }
            }

            ForEach(sections, id: \.0) { section in
                Section(section.0.isEmpty ? "Hosts" : section.0) {
                    ForEach(section.1) { host in
                        let live = liveSessions(for: host)
                        Button { onOpenHost(host) } label: {
                            HostRow(host: host, sessions: live)
                        }
                        .buttonStyle(.plain)
                        .contextMenu {
                            if !live.isEmpty {
                                Button(role: .destructive) {
                                    onCloseSessions(live)
                                } label: {
                                    Label("Close all connections", systemImage: "xmark.circle")
                                }
                            }
                            Button { onEdit(host) } label: {
                                Label("Edit", systemImage: "pencil")
                            }
                            Button(role: .destructive) { onDelete(host) } label: {
                                Label("Delete", systemImage: "trash")
                            }
                        }
                        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                            Button(role: .destructive) { onDelete(host) } label: {
                                Label("Delete", systemImage: "trash")
                            }
                            if !live.isEmpty {
                                Button {
                                    onCloseSessions(live)
                                } label: {
                                    Label("Close all connections", systemImage: "xmark.circle")
                                }
                                .tint(.orange)
                            }
                            Button { onEdit(host) } label: {
                                Label("Edit", systemImage: "pencil")
                            }
                            .tint(.blue)
                        }
                    }
                }
            }
        }
        .listStyle(.insetGrouped)
        .searchable(text: $search, prompt: "Search hosts")
        .overlay {
            if hosts.isEmpty && sessions.isEmpty {
                ContentUnavailableView {
                    Label("No hosts yet", systemImage: "server.rack")
                } description: {
                    Text("Add a destination, or import an OpenSSH config.")
                } actions: {
                    Button("Import SSH config", action: onImport)
                }
            } else if filtered.isEmpty && visibleSessions.isEmpty {
                ContentUnavailableView.search(text: search)
            }
        }
    }

}

private struct HostRow: View {
    let host: Host
    let sessions: [SessionInfo]

    private var connectedCount: Int {
        sessions.filter { $0.isConnected }.count
    }

    private var waiting: Bool {
        sessions.contains { $0.isWaiting }
    }

    private var sessionStatus: LocalizedStringKey {
        if connectedCount > 0 { return "Connected" }
        if waiting { return "Connecting…" }
        return "Disconnected"
    }

    private var sessionColor: Color {
        if connectedCount > 0 { return .green }
        if waiting { return .orange }
        return .secondary
    }

    private var accent: Color {
        guard let value = host.color?.trimmingCharacters(in: CharacterSet(charactersIn: "#")),
              let number = UInt32(value, radix: 16) else {
            return .accentColor
        }
        return Color(
            red: Double((number >> 16) & 0xFF) / 255,
            green: Double((number >> 8) & 0xFF) / 255,
            blue: Double(number & 0xFF) / 255
        )
    }

    var body: some View {
        HStack(spacing: 12) {
            RoundedRectangle(cornerRadius: 3)
                .fill(accent)
                .frame(width: 4, height: 30)
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(host.label)
                        .font(.headline)
                        .foregroundStyle(.primary)
                    if host.isDesktop {
                        Text(host.protocolText)
                            .font(.caption2.bold())
                            .padding(.horizontal, 5)
                            .padding(.vertical, 1)
                            .background(Color.accentColor.opacity(0.18), in: Capsule())
                            .foregroundStyle(.tint)
                    }
                }
                Text(host.user.isEmpty ? host.target : "\(host.user)@\(host.target)")
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
            }
            Spacer()
            if !sessions.isEmpty {
                HStack(spacing: 5) {
                    Circle()
                        .fill(sessionColor)
                        .frame(width: 7, height: 7)
                    Text(sessionStatus)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                    if connectedCount > 1 {
                        Text(verbatim: "×\(connectedCount)")
                            .font(.caption2.monospacedDigit())
                            .foregroundStyle(.secondary)
                    }
                }
            }
            Image(systemName: "plus.circle")
                .font(.caption)
                .foregroundStyle(.tertiary)
                .accessibilityLabel(Text("New terminal"))
        }
        .contentShape(Rectangle())
        .padding(.vertical, 2)
    }
}

private struct SessionRow: View {
    let session: SessionInfo
    let ordinal: Int
    let showsOrdinal: Bool

    private var statusText: LocalizedStringKey {
        if session.isConnected { return "Connected" }
        if session.isWaiting { return "Connecting…" }
        return "Disconnected"
    }

    private var statusColor: Color {
        if session.isConnected { return .green }
        if session.isWaiting { return .orange }
        return .secondary
    }

    private var target: String {
        session.user.isEmpty ? session.address : "\(session.user)@\(session.address)"
    }

    var body: some View {
        HStack(spacing: 12) {
            Circle()
                .fill(statusColor)
                .frame(width: 8, height: 8)
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(session.title.isEmpty ? session.address : session.title)
                        .font(.headline)
                        .lineLimit(1)
                    if showsOrdinal {
                        Text(verbatim: "\(AppLanguage.localized("Terminal")) \(ordinal)")
                            .font(.caption2.weight(.semibold))
                            .padding(.horizontal, 6)
                            .padding(.vertical, 2)
                            .background(.tint.opacity(0.15), in: Capsule())
                            .foregroundStyle(.tint)
                    }
                }
                Text(verbatim: target)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer()
            Text(statusText)
                .font(.caption2)
                .foregroundStyle(.secondary)
            Image(systemName: "chevron.right")
                .font(.caption)
                .foregroundStyle(.tertiary)
        }
        .contentShape(Rectangle())
        .padding(.vertical, 2)
    }
}
