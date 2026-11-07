import SwiftUI
import UIKit

/// Commands remembered from the terminal. The file is local-only; secrets are
/// filtered by the core before anything is written.
struct HistoryView: View {
    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.locale) private var locale

    @State private var entries: [HistoryEntry] = []
    @State private var search = ""
    @State private var hostID = ""
    @State private var errorText: String?
    @State private var confirmingClear = false

    private var filtered: [HistoryEntry] {
        guard !search.isEmpty else { return entries }
        let needle = search.lowercased()
        return entries.filter { $0.command.lowercased().contains(needle) }
    }

    var body: some View {
        List {
            if filtered.isEmpty {
                ContentUnavailableView {
                    Label("No commands yet", systemImage: "text.append")
                } description: {
                    Text("Commands typed into a connected shell appear here.")
                }
            }
            ForEach(filtered) { entry in
                VStack(alignment: .leading, spacing: 4) {
                    Text(entry.command)
                        .font(.callout.monospaced())
                        .lineLimit(3)
                        .textSelection(.enabled)
                    HStack {
                        Text(relative(entry.usedDate))
                        if entry.count > 1 {
                            Text("· used \(entry.count)×")
                        }
                    }
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                }
                .padding(.vertical, 2)
                .swipeActions(edge: .trailing, allowsFullSwipe: true) {
                    Button(role: .destructive) {
                        delete(entry)
                    } label: {
                        Label("Delete", systemImage: "trash")
                    }
                }
                .contextMenu {
                    Button {
                        UIPasteboard.general.string = entry.command
                    } label: {
                        Label("Copy", systemImage: "doc.on.doc")
                    }
                    Button(role: .destructive) {
                        delete(entry)
                    } label: {
                        Label("Delete", systemImage: "trash")
                    }
                }
            }
        }
        .listStyle(.insetGrouped)
        .searchable(text: $search, prompt: "Search commands")
        .refreshable { reload() }
        .onAppear { reload() }
        .onChange(of: hostID) { _, _ in reload() }
        .navigationTitle("Command history")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    Picker("Host", selection: $hostID) {
                        Text("All hosts").tag("")
                        ForEach(bridge.hosts) { host in
                            Text(host.label).tag(host.id)
                        }
                    }
                    Divider()
                    Button(role: .destructive) {
                        confirmingClear = true
                    } label: {
                        Label("Clear history", systemImage: "trash")
                    }
                } label: {
                    Image(systemName: "line.3.horizontal.decrease.circle")
                }
            }
        }
        .confirmationDialog(
            "Clear the command history?",
            isPresented: $confirmingClear,
            titleVisibility: .visible
        ) {
            Button("Clear everything", role: .destructive) { clear() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Commands are removed from this device only.")
        }
        .alert("Command history", isPresented: errorBinding) {
            Button("OK") { errorText = nil }
        } message: {
            Text(errorText ?? "")
        }
    }

    private func relative(_ date: Date) -> String {
        let formatter = RelativeDateTimeFormatter()
        formatter.locale = locale
        formatter.unitsStyle = .abbreviated
        return formatter.localizedString(for: date, relativeTo: Date())
    }

    private func reload() {
        bridge.listHistory(hostID: hostID) { result in
            switch result {
            case .success(let list): entries = list
            case .failure(let error): errorText = error.localizedDescription
            }
        }
    }

    private func delete(_ entry: HistoryEntry) {
        bridge.deleteHistory(hostID: hostID, command: entry.command) { error in
            if let error {
                errorText = error.localizedDescription
            } else {
                reload()
            }
        }
    }

    private func clear() {
        bridge.clearHistory { error in
            if let error {
                errorText = error.localizedDescription
            } else {
                reload()
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
