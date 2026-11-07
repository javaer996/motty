import SwiftUI

/// The assistant panel for one session: streamed answers, tool cards and the
/// approval card that guards anything that changes the remote host.
struct AIChatView: View {
    let sessionID: UInt32
    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss
    @State private var input = ""
    @State private var showingSettings = false
    @State private var confirmText = ""

    private var conversation: AIConversation {
        bridge.aiConversation(sessionID)
    }

    private var configured: Bool {
        !bridge.aiSettings.model.trimmingCharacters(in: .whitespaces).isEmpty
    }

    var body: some View {
        NavigationStack {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 12) {
                        if conversation.messages.isEmpty {
                            ContentUnavailableView {
                                Label("Ask about this session", systemImage: "sparkles")
                            } description: {
                                Text("The assistant can read the terminal and, with your approval, run commands.")
                            } actions: {
                                if !configured {
                                    Button("Configure the assistant") { showingSettings = true }
                                }
                            }
                        }

                        ForEach(conversation.messages) { message in
                            messageRow(message)
                                .id(message.id)
                        }

                        if let approval = conversation.pendingApproval {
                            approvalCard(approval)
                                .id("approval")
                        }

                        Color.clear
                            .frame(height: 1)
                            .id("bottom")
                    }
                    .padding()
                }
                .onChange(of: conversation.messages.count) { _, _ in
                    scrollToBottom(proxy)
                }
                .onChange(of: conversation.messages.last?.text) { _, _ in
                    scrollToBottom(proxy)
                }
                .onChange(of: conversation.pendingApproval?.id) { _, _ in
                    scrollToBottom(proxy)
                }
            }
            .safeAreaInset(edge: .bottom) {
                composer
            }
            .navigationTitle("Assistant")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button { showingSettings = true } label: {
                        Image(systemName: "gearshape")
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        Button("New conversation", role: .destructive) {
                            bridge.resetAIConversation(sessionID: sessionID)
                        }
                    } label: {
                        Image(systemName: "ellipsis.circle")
                    }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .sheet(isPresented: $showingSettings) {
                AISettingsView()
                    .environmentObject(bridge)
            }
        }
    }

    private func scrollToBottom(_ proxy: ScrollViewProxy) {
        withAnimation(.easeOut(duration: 0.15)) {
            proxy.scrollTo("bottom", anchor: .bottom)
        }
    }

    // MARK: Messages

    @ViewBuilder
    private func messageRow(_ message: AIChatMessage) -> some View {
        switch message.role {
        case .user:
            HStack {
                Spacer(minLength: 40)
                Text(message.text)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .background(.tint, in: RoundedRectangle(cornerRadius: 14))
                    .foregroundStyle(.white)
                    .textSelection(.enabled)
            }
        case .assistant:
            Text(message.text.isEmpty && message.streaming ? "…" : message.text)
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 14))
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        case .reasoning:
            Text(message.text)
                .font(.caption)
                .italic()
                .foregroundStyle(.secondary)
                .padding(.leading, 4)
        case .tool:
            toolCard(message)
        case .error:
            Label(message.text, systemImage: "exclamationmark.triangle")
                .font(.footnote)
                .foregroundStyle(.red)
        }
    }

    private func toolCard(_ message: AIChatMessage) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Image(systemName: "wrench.and.screwdriver")
                    .foregroundStyle(.secondary)
                Group {
                    if let name = message.toolName, !name.isEmpty {
                        Text(verbatim: name)
                    } else {
                        Text("Tool")
                    }
                }
                .font(.subheadline.weight(.semibold).monospaced())
                Spacer()
                if message.toolRunning {
                    ProgressView()
                        .controlSize(.small)
                } else if message.isError {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.red)
                } else {
                    Image(systemName: "checkmark.circle.fill")
                        .foregroundStyle(.green)
                }
            }
            if let arguments = message.toolArguments, arguments != "{}", !arguments.isEmpty {
                Text(arguments)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                    .lineLimit(4)
                    .textSelection(.enabled)
            }
            if let result = message.toolResult, !result.isEmpty {
                Divider()
                ScrollView(.vertical) {
                    Text(result)
                        .font(.caption.monospaced())
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .textSelection(.enabled)
                }
                .frame(maxHeight: 180)
            }
        }
        .padding(12)
        .background(.quaternary.opacity(0.35), in: RoundedRectangle(cornerRadius: 12))
    }

    // MARK: Approval

    private func approvalCard(_ approval: AIApprovalRequest) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("Approval needed")
                    .font(.headline)
                Spacer()
                Text(LocalizedStringKey(approval.level))
                    .font(.caption.weight(.semibold))
                    .padding(.horizontal, 8)
                    .padding(.vertical, 3)
                    .background(levelColor(approval.level).opacity(0.2), in: Capsule())
                    .foregroundStyle(levelColor(approval.level))
            }
            if let label = approval.sessionLabel, !label.isEmpty {
                Text(label)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Text(approval.summary)
                .font(.callout.monospaced())
                .textSelection(.enabled)
            if !approval.reasons.isEmpty {
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(approval.reasons, id: \.self) { reason in
                        Text("• \(reason)")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
            }
            if let safer = approval.safer, !safer.isEmpty {
                Text("Safer: \(safer)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            if approval.needsText {
                TextField("Type \(approval.confirm) to confirm", text: $confirmText)
                    .textFieldStyle(.roundedBorder)
                    .textInputAutocapitalization(.characters)
                    .autocorrectionDisabled()
            }
            HStack {
                Button("Reject", role: .cancel) {
                    answer(approval, approved: false)
                }
                Spacer()
                Button("Allow once") {
                    answer(approval, approved: true)
                }
                .buttonStyle(.borderedProminent)
                .disabled(approval.needsText && confirmText.uppercased() != approval.confirm.uppercased())
            }
            if !approval.needsText {
                Button("Always allow \(approval.tool) (\(approval.level))") {
                    answer(approval, approved: true, always: true)
                }
                .font(.caption)
            }
        }
        .padding(14)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 16))
        .overlay(
            RoundedRectangle(cornerRadius: 16)
                .stroke(levelColor(approval.level).opacity(0.5), lineWidth: 1)
        )
    }

    private func answer(_ approval: AIApprovalRequest, approved: Bool, always: Bool = false) {
        let text = approved ? confirmText : ""
        confirmText = ""
        bridge.approveAI(
            sessionID: sessionID,
            request: approval,
            approved: approved,
            confirmText: text,
            always: always
        )
    }

    private func levelColor(_ level: String) -> Color {
        switch level {
        case "Low": return .green
        case "Medium": return .orange
        case "High", "Critical": return .red
        default: return .secondary
        }
    }

    // MARK: Composer

    private var composer: some View {
        VStack(spacing: 0) {
            Divider()
            HStack(alignment: .bottom, spacing: 8) {
                TextField(configured ? "Message the assistant" : "Configure the assistant first", text: $input, axis: .vertical)
                    .lineLimit(1...5)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 8)
                    .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 16))
                    .disabled(!configured)
                if conversation.isRunning {
                    Button {
                        bridge.cancelAIRun(sessionID: sessionID)
                    } label: {
                        Image(systemName: "stop.circle.fill")
                            .font(.title2)
                    }
                } else {
                    Button {
                        send()
                    } label: {
                        Image(systemName: "arrow.up.circle.fill")
                            .font(.title2)
                    }
                    .disabled(!configured || input.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
        }
        .background(.bar)
    }

    private func send() {
        let text = input
        input = ""
        bridge.sendAIMessage(sessionID: sessionID, text: text)
    }
}
