import SwiftUI

/// Saved assistant threads for the host behind this session. Starting a new
/// conversation never deletes the old one; it stays here to reopen and
/// continue.
struct AIHistoryView: View {
    let sessionID: UInt32
    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss

    @State private var conversations: [AIConversationSummary] = []
    @State private var loading = true
    @State private var opening: Int64?
    @State private var message: String?

    var body: some View {
        NavigationStack {
            List {
                if conversations.isEmpty {
                    if loading {
                        HStack {
                            Spacer()
                            ProgressView()
                            Spacer()
                        }
                    } else {
                        ContentUnavailableView {
                            Label("No saved conversations", systemImage: "clock")
                        } description: {
                            Text("Conversations are saved on this iPhone. Starting a new conversation keeps the old one here.")
                        }
                    }
                } else {
                    Section {
                        ForEach(conversations) { conversation in
                            Button {
                                open(conversation)
                            } label: {
                                row(conversation)
                            }
                            .buttonStyle(.plain)
                            .disabled(opening != nil)
                            .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                                Button(role: .destructive) {
                                    delete(conversation)
                                } label: {
                                    Label("Delete", systemImage: "trash")
                                }
                            }
                        }
                    } footer: {
                        Text("Opening a saved conversation continues it. New messages are added to that thread.")
                    }
                }
            }
            .navigationTitle("Conversation history")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .refreshable { refresh() }
            .task { refresh() }
            .alert("Conversation history", isPresented: messageBinding) {
                Button("OK") { message = nil }
            } message: {
                Text(message ?? "")
            }
        }
    }

    private func row(_ conversation: AIConversationSummary) -> some View {
        HStack(spacing: 12) {
            Image(systemName: "bubble.left.and.bubble.right")
                .foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(conversation.title)
                        .font(.headline)
                        .lineLimit(1)
                    if conversation.active {
                        Text("Current")
                            .font(.caption2.weight(.semibold))
                            .padding(.horizontal, 6)
                            .padding(.vertical, 2)
                            .background(.tint.opacity(0.15), in: Capsule())
                            .foregroundStyle(.tint)
                    }
                }
                HStack(spacing: 8) {
                    if let label = conversation.hostLabel, !label.isEmpty {
                        Text(label)
                            .lineLimit(1)
                    }
                    Text(
                        Date(timeIntervalSince1970: TimeInterval(conversation.updated) / 1000),
                        format: .dateTime.month(.abbreviated).day().hour().minute()
                    )
                }
                .font(.caption)
                .foregroundStyle(.secondary)
            }
            Spacer()
            if opening == conversation.id {
                ProgressView()
                    .controlSize(.small)
            }
        }
        .contentShape(Rectangle())
    }

    private func refresh() {
        loading = true
        bridge.loadAIHistory(sessionID: sessionID) { result in
            loading = false
            switch result {
            case .success(let list):
                conversations = list
            case .failure(let error):
                message = error.localizedDescription
            }
        }
    }

    private func open(_ conversation: AIConversationSummary) {
        opening = conversation.id
        bridge.loadAIConversation(sessionID: sessionID, conversationID: conversation.id) { result in
            opening = nil
            switch result {
            case .success:
                dismiss()
            case .failure(let error):
                message = error.localizedDescription
            }
        }
    }

    private func delete(_ conversation: AIConversationSummary) {
        bridge.deleteAIConversation(sessionID: sessionID, conversationID: conversation.id) { result in
            switch result {
            case .success:
                conversations.removeAll { $0.id == conversation.id }
                refresh()
            case .failure(let error):
                message = error.localizedDescription
            }
        }
    }

    private var messageBinding: Binding<Bool> {
        Binding(get: { message != nil }, set: { if !$0 { message = nil } })
    }
}
