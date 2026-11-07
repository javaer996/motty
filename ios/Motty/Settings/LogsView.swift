import SwiftUI

/// The local, private journal of connection lifecycles. It holds no terminal
/// content: only which host connected, when, and how the session ended.
struct LogsView: View {
    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.locale) private var locale

    @State private var records: [SessionLogRecord] = []
    @State private var stats: SessionLogStats?
    @State private var search = ""
    @State private var favoritesOnly = false
    @State private var outcome = "all"
    @State private var more = false
    @State private var nextStartedAt: Int64?
    @State private var nextID: String?
    @State private var selected: SessionLogRecord?
    @State private var errorText: String?
    @State private var confirmingClear: ClearScope?

    private enum ClearScope: Identifiable {
        case unfavorited
        case everything

        var id: String {
            switch self {
            case .unfavorited: return "unfavorited"
            case .everything: return "everything"
            }
        }
    }

    private let pageSize = 50

    var body: some View {
        List {
            if records.isEmpty {
                ContentUnavailableView {
                    Label("No sessions yet", systemImage: "clock.arrow.circlepath")
                } description: {
                    Text("Connection history appears here after the first host opens.")
                }
            }
            ForEach(records) { record in
                row(record)
                    .contentShape(Rectangle())
                    .onTapGesture { selected = record }
                    .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                        Button(role: .destructive) {
                            delete(record)
                        } label: {
                            Label("Delete", systemImage: "trash")
                        }
                        Button {
                            toggleFavorite(record)
                        } label: {
                            Label(
                                LocalizedStringKey(record.favorite ? "Unfavorite" : "Favorite"),
                                systemImage: "star"
                            )
                        }
                        .tint(.yellow)
                    }
            }
            if more {
                Button {
                    loadMore()
                } label: {
                    Label("Load older sessions", systemImage: "arrow.down.circle")
                }
            }
        }
        .listStyle(.insetGrouped)
        .searchable(text: $search, prompt: "Search host, address or account")
        .onSubmit(of: .search) { reload() }
        .onChange(of: favoritesOnly) { _, _ in reload() }
        .onChange(of: outcome) { _, _ in reload() }
        .refreshable { reload() }
        .onAppear { reload() }
        .navigationTitle("Session log")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    Picker("Show", selection: $favoritesOnly) {
                        Text("All sessions").tag(false)
                        Text("Favorites only").tag(true)
                    }
                    Picker("Outcome", selection: $outcome) {
                        Text("Any outcome").tag("all")
                        Text("Normal").tag("normal")
                        Text("Abnormal").tag("abnormal")
                    }
                    Divider()
                    Button {
                        confirmingClear = .unfavorited
                    } label: {
                        Label("Clear un-favorited", systemImage: "trash")
                    }
                    Button(role: .destructive) {
                        confirmingClear = .everything
                    } label: {
                        Label("Clear everything", systemImage: "trash.fill")
                    }
                } label: {
                    Image(systemName: "line.3.horizontal.decrease.circle")
                }
            }
        }
        .safeAreaInset(edge: .bottom) {
            if let stats {
                Text("\(stats.records) sessions · \(stats.favorites) favorites · \(ByteCountFormatter.string(fromByteCount: stats.bytes, countStyle: .file))")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .padding(8)
                    .frame(maxWidth: .infinity)
                    .background(.bar)
            }
        }
        .sheet(item: $selected) { record in
            LogDetailView(record: record) { reload() }
                .environmentObject(bridge)
        }
        .confirmationDialog(
            clearTitle,
            isPresented: clearBinding,
            titleVisibility: .visible
        ) {
            if let confirmingClear {
                Button("Clear", role: .destructive) {
                    clear(confirmingClear)
                    self.confirmingClear = nil
                }
            }
            Button("Cancel", role: .cancel) { confirmingClear = nil }
        } message: {
            Text("Session journal rows are deleted from this device only.")
        }
        .alert("Session log", isPresented: errorBinding) {
            Button("OK") { errorText = nil }
        } message: {
            Text(errorText ?? "")
        }
    }

    private var clearTitle: LocalizedStringKey {
        switch confirmingClear {
        case .unfavorited: return "Clear un-favorited sessions?"
        case .everything: return "Clear every session?"
        case nil: return ""
        }
    }

    private func row(_ record: SessionLogRecord) -> some View {
        HStack(spacing: 10) {
            Image(systemName: record.favorite ? "star.fill" : statusIcon(record))
                .foregroundStyle(record.favorite ? .yellow : statusColor(record))
                .frame(width: 22)
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(record.host)
                        .font(.headline)
                        .lineLimit(1)
                    if let account = record.account, !account.isEmpty {
                        Text(account)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                Text(subtitle(record))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer()
            VStack(alignment: .trailing, spacing: 3) {
                Text(LocalizedStringKey(record.statusText))
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(statusColor(record))
                Text(record.localizedDuration)
                    .font(.caption2.monospaced())
                    .foregroundStyle(.tertiary)
            }
        }
        .padding(.vertical, 2)
    }

    private func subtitle(_ record: SessionLogRecord) -> String {
        var parts: [String] = []
        if let address = record.address, !address.isEmpty { parts.append(address) }
        parts.append(relative(record.startedDate))
        if let reason = record.reason, !reason.isEmpty {
            parts.append(reason.replacingOccurrences(of: "-", with: " "))
        }
        return parts.joined(separator: " · ")
    }

    private func statusIcon(_ record: SessionLogRecord) -> String {
        switch record.status {
        case "active": return "bolt.horizontal.circle.fill"
        case "failed": return "exclamationmark.triangle.fill"
        case "interrupted": return "pause.circle.fill"
        case "ended": return "checkmark.circle.fill"
        default: return "circle.dashed"
        }
    }

    private func statusColor(_ record: SessionLogRecord) -> Color {
        switch record.status {
        case "active": return .green
        case "failed": return .red
        case "interrupted": return .orange
        case "ended": return .secondary
        default: return .secondary
        }
    }

    private func relative(_ date: Date) -> String {
        let formatter = RelativeDateTimeFormatter()
        formatter.locale = locale
        formatter.unitsStyle = .abbreviated
        return formatter.localizedString(for: date, relativeTo: Date())
    }

    private func reload() {
        nextStartedAt = nil
        nextID = nil
        load(reset: true)
        bridge.logStats { result in
            if case .success(let value) = result { stats = value }
        }
    }

    private func loadMore() {
        load(reset: false)
    }

    private func load(reset: Bool) {
        bridge.listLogs(
            search: search,
            favoriteOnly: favoritesOnly,
            outcome: outcome,
            limit: pageSize,
            cursorStartedAt: reset ? nil : nextStartedAt,
            cursorID: reset ? nil : nextID
        ) { result in
            switch result {
            case .success(let page):
                if reset {
                    records = page.records
                } else {
                    records.append(contentsOf: page.records)
                }
                more = page.more
                nextStartedAt = page.nextStartedAt
                nextID = page.nextId
            case .failure(let error):
                errorText = error.localizedDescription
            }
        }
    }

    private func toggleFavorite(_ record: SessionLogRecord) {
        bridge.setLogFavorite(id: record.id, favorite: !record.favorite) { error in
            if let error {
                errorText = error.localizedDescription
            } else {
                reload()
            }
        }
    }

    private func delete(_ record: SessionLogRecord) {
        bridge.deleteLog(id: record.id) { error in
            if let error {
                errorText = error.localizedDescription
            } else {
                reload()
            }
        }
    }

    private func clear(_ scope: ClearScope) {
        bridge.clearLogs(includeFavorites: scope == .everything) { error in
            if let error {
                errorText = error.localizedDescription
            } else {
                reload()
            }
        }
    }

    private var clearBinding: Binding<Bool> {
        Binding(
            get: { confirmingClear != nil },
            set: { if !$0 { confirmingClear = nil } }
        )
    }

    private var errorBinding: Binding<Bool> {
        Binding(
            get: { errorText != nil },
            set: { if !$0 { errorText = nil } }
        )
    }
}

private struct LogDetailView: View {
    let record: SessionLogRecord
    let onChanged: () -> Void

    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss
    @Environment(\.locale) private var locale
    @State private var events: [SessionLogEvent] = []
    @State private var loading = true
    @State private var errorText: String?

    private var time: DateFormatter {
        let formatter = DateFormatter()
        formatter.locale = locale
        formatter.dateStyle = .medium
        formatter.timeStyle = .medium
        return formatter
    }

    var body: some View {
        NavigationStack {
            List {
                Section {
                    LabeledContent("Host", value: record.host)
                    if let account = record.account, !account.isEmpty {
                        LabeledContent("Account", value: account)
                    }
                    if let address = record.address, !address.isEmpty {
                        LabeledContent("Address", value: address)
                    }
                    LabeledContent("Started", value: time.string(from: record.startedDate))
                    if let ended = record.endedDate {
                        LabeledContent("Ended at", value: time.string(from: ended))
                    }
                    LabeledContent("Duration") { Text(record.localizedDuration) }
                    LabeledContent("Status") { Text(LocalizedStringKey(record.statusText)) }
                    if let reason = record.reason, !reason.isEmpty {
                        LabeledContent("Reason", value: reason.replacingOccurrences(of: "-", with: " "))
                    }
                }
                Section("Timeline") {
                    if loading {
                        ProgressView()
                    } else if events.isEmpty {
                        Text("No events were recorded.")
                            .foregroundStyle(.secondary)
                    }
                    ForEach(events) { event in
                        HStack {
                            Text(event.title)
                            Spacer()
                            Text(time.string(from: event.date))
                                .font(.caption.monospaced())
                                .foregroundStyle(.secondary)
                        }
                    }
                }
            }
            .navigationTitle("Session")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("Done") { dismiss() }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        Button {
                            bridge.setLogFavorite(id: record.id, favorite: !record.favorite) { error in
                                if let error {
                                    errorText = error.localizedDescription
                                } else {
                                    onChanged()
                                }
                            }
                        } label: {
                            Label(
                                LocalizedStringKey(record.favorite ? "Remove favorite" : "Favorite"),
                                systemImage: "star"
                            )
                        }
                        Button(role: .destructive) {
                            bridge.deleteLog(id: record.id) { error in
                                if let error {
                                    errorText = error.localizedDescription
                                } else {
                                    onChanged()
                                    dismiss()
                                }
                            }
                        } label: {
                            Label("Delete", systemImage: "trash")
                        }
                    } label: {
                        Image(systemName: "ellipsis.circle")
                    }
                }
            }
            .onAppear {
                bridge.logEvents(id: record.id) { result in
                    loading = false
                    switch result {
                    case .success(let list): events = list
                    case .failure(let error): errorText = error.localizedDescription
                    }
                }
            }
            .alert("Session log", isPresented: errorBinding) {
                Button("OK") { errorText = nil }
            } message: {
                Text(errorText ?? "")
            }
        }
    }

    private var errorBinding: Binding<Bool> {
        Binding(
            get: { errorText != nil },
            set: { if !$0 { errorText = nil } }
        )
    }
}

private extension SessionLogRecord {
    /// A short duration that follows the UI language, e.g. "2h 5m" or "2小时5分钟".
    var localizedDuration: LocalizedStringKey {
        let seconds = max(0, durationMillis / 1000)
        let hours = seconds / 3600
        let minutes = (seconds % 3600) / 60
        if hours > 0 { return "\(hours)h \(minutes)m" }
        if minutes > 0 { return "\(minutes)m \(seconds % 60)s" }
        return "\(seconds)s"
    }
}
