import SwiftUI

/// The assistant panel for one session: streamed answers, tool cards and the
/// approval card that guards anything that changes the remote host.
struct AIChatView: View {
    let sessionID: UInt32
    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss
    @State private var input = ""
    @State private var showingSettings = false
    @State private var showingPrivacy = false
    @State private var showingHistory = false
    @State private var confirmText = ""
    @FocusState private var composerFocused: Bool

    private var conversation: AIConversation {
        bridge.aiConversation(sessionID)
    }

    private var configured: Bool {
        guard let provider = bridge.aiSettings.current else { return false }
        return !provider.model.trimmingCharacters(in: .whitespaces).isEmpty
    }

    /// True while a run is in flight but it has not started streaming an
    /// answer yet. Tool cards carry their own progress indicator.
    private var waitingForReply: Bool {
        guard conversation.isRunning else { return false }
        guard let last = conversation.messages.last else { return true }
        if last.role == .tool { return false }
        if last.role == .assistant { return last.text.isEmpty }
        return true
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

                        if waitingForReply {
                            HStack(spacing: 8) {
                                ProgressView()
                                    .controlSize(.small)
                                Text("Replying…")
                                    .font(.footnote)
                                    .foregroundStyle(.secondary)
                            }
                            .id("replying")
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
                .defaultScrollAnchor(.bottom)
                .onAppear {
                    if conversation.historyLoaded {
                        scrollToBottom(proxy, animated: false, afterLayout: true)
                    }
                }
                .onChange(of: conversation.historyLoaded) { _, loaded in
                    if loaded {
                        scrollToBottom(proxy, animated: false, afterLayout: true)
                    }
                }
                .onChange(of: conversation.messages.count) { _, _ in
                    scrollToBottom(proxy)
                }
                .onChange(of: conversation.messages.last?.text) { _, _ in
                    // Streaming chunks arrive many times a second; animating
                    // each one makes the list lag behind the text.
                    scrollToBottom(proxy, animated: false)
                }
                .onChange(of: conversation.pendingApproval?.id) { _, _ in
                    scrollToBottom(proxy)
                }
                .onChange(of: conversation.isRunning) { _, running in
                    if running { scrollToBottom(proxy) }
                }
            }
            .safeAreaInset(edge: .bottom) {
                composer
            }
            .navigationTitle("Assistant")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItemGroup(placement: .topBarLeading) {
                    Button { showingPrivacy = true } label: {
                        Image(systemName: "hand.raised.fill")
                    }
                    .accessibilityLabel(Text("Privacy protection"))
                    Button { showingSettings = true } label: {
                        Image(systemName: "gearshape")
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        Button {
                            showingHistory = true
                        } label: {
                            Label("Conversation history", systemImage: "clock.arrow.circlepath")
                        }
                        Button {
                            bridge.resetAIConversation(sessionID: sessionID)
                        } label: {
                            Label("New conversation", systemImage: "square.and.pencil")
                        }
                        .disabled(conversation.isRunning)
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
            .sheet(isPresented: $showingPrivacy) {
                NavigationStack {
                    AIPrivacyView(sessionID: sessionID)
                        .environmentObject(bridge)
                }
            }
            .sheet(isPresented: $showingHistory) {
                AIHistoryView(sessionID: sessionID)
                    .environmentObject(bridge)
            }
            .task {
                bridge.loadAIConversationIfNeeded(sessionID: sessionID)
            }
        }
    }

    private func scrollToBottom(
        _ proxy: ScrollViewProxy,
        animated: Bool = true,
        afterLayout: Bool = false
    ) {
        if animated {
            withAnimation(.easeOut(duration: 0.15)) {
                proxy.scrollTo("bottom", anchor: .bottom)
            }
            return
        }
        DispatchQueue.main.async {
            proxy.scrollTo("bottom", anchor: .bottom)
            // After loading saved history the rows have not been laid out
            // yet. Repeat once after that layout pass; streaming chunks use
            // the immediate path so they cannot queue late scrolls.
            if afterLayout {
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.1) {
                    proxy.scrollTo("bottom", anchor: .bottom)
                }
            }
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
            Group {
                if message.text.isEmpty && message.streaming {
                    Text("…")
                } else {
                    MarkdownMessageView(text: message.text)
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 14))
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
                ExpandableCodeText(
                    text: arguments,
                    collapsedLineLimit: 6,
                    secondary: true
                )
            }
            if let result = message.toolResult, !result.isEmpty {
                Divider()
                ExpandableCodeText(
                    text: result,
                    collapsedLineLimit: 10,
                    secondary: false
                )
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
            HStack(alignment: .center, spacing: 8) {
                TextField(configured ? "Message the assistant" : "Configure the assistant first", text: $input, axis: .vertical)
                    .lineLimit(1...5)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 8)
                    .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 16))
                    .overlay {
                        if composerFocused {
                            RainbowGlowBorder(cornerRadius: 16)
                                .transition(.opacity)
                        }
                    }
                    .animation(.easeInOut(duration: 0.25), value: composerFocused)
                    .focused($composerFocused)
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

/// The ring around the composer while it holds the caret: a rainbow that
/// travels around the field, wrapped in a glow that slowly breathes. The
/// display link only runs while the view exists, and the view only exists
/// while the field is focused.
private struct RainbowGlowBorder: View {
    var cornerRadius: CGFloat

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        Group {
            if reduceMotion {
                border(phase: 0, breath: 0.6)
            } else {
                TimelineView(.animation(minimumInterval: 1.0 / 30.0)) { context in
                    let time = context.date.timeIntervalSinceReferenceDate
                    border(
                        phase: time.truncatingRemainder(dividingBy: 4) / 4 * 360,
                        breath: (sin(time * 2.2) + 1) / 2
                    )
                }
            }
        }
        .allowsHitTesting(false)
    }

    private func border(phase: Double, breath: Double) -> some View {
        let shape = RoundedRectangle(cornerRadius: cornerRadius, style: .continuous)
        return ZStack {
            shape
                .strokeBorder(gradient(phase: phase), lineWidth: 3)
                .blur(radius: 4)
                .opacity(0.4 + 0.35 * breath)
                .scaleEffect(1 + 0.008 * breath)
            shape
                .strokeBorder(gradient(phase: phase), lineWidth: 1.5)
                .opacity(0.9)
        }
    }

    private func gradient(phase: Double) -> AngularGradient {
        AngularGradient(
            gradient: Gradient(colors: [
                Color(red: 1.00, green: 0.24, blue: 0.32),
                Color(red: 1.00, green: 0.62, blue: 0.12),
                Color(red: 1.00, green: 0.92, blue: 0.25),
                Color(red: 0.35, green: 0.90, blue: 0.40),
                Color(red: 0.20, green: 0.85, blue: 0.92),
                Color(red: 0.32, green: 0.52, blue: 1.00),
                Color(red: 0.72, green: 0.36, blue: 1.00),
                Color(red: 1.00, green: 0.24, blue: 0.32),
            ]),
            center: .center,
            startAngle: .degrees(phase),
            endAngle: .degrees(phase + 360)
        )
    }
}

/// Tool arguments and results can be much longer than a phone screen. Show a
/// readable excerpt first, with an explicit control to reveal every line
/// instead of an ellipsis the user cannot get past.
private struct ExpandableCodeText: View {
    let text: String
    let collapsedLineLimit: Int
    let secondary: Bool

    @State private var expanded = false

    private var needsExpansion: Bool {
        // Character count catches long single lines that wrap past the limit
        // without any newline; line count catches short but many-lined text.
        if text.count > collapsedLineLimit * 45 { return true }
        var lines = 1
        for byte in text.utf8 where byte == 10 {
            lines += 1
            if lines > collapsedLineLimit { return true }
        }
        return false
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(verbatim: text)
                .font(.caption.monospaced())
                .foregroundStyle(secondary ? Color.secondary : Color.primary)
                .lineLimit(expanded ? nil : collapsedLineLimit)
                .frame(maxWidth: .infinity, alignment: .leading)
                .textSelection(.enabled)
            if needsExpansion {
                Button(expanded ? "Show less" : "Show more") {
                    withAnimation(.easeInOut(duration: 0.15)) {
                        expanded.toggle()
                    }
                }
                .font(.caption.weight(.semibold))
                .buttonStyle(.borderless)
            }
        }
    }
}
