import SwiftUI

struct SessionView: View {
    let sessionID: UInt32
    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss
    @State private var showingAssistant = false
    @State private var showingFiles = false
    @State private var showingCompletion = false

    private var session: SessionInfo? {
        bridge.sessions.first { $0.id == sessionID }
    }

    private var prompt: CoreEvent? {
        bridge.prompts.first { $0.sessionID == sessionID }
    }

    var body: some View {
        NavigationStack {
            ZStack(alignment: .bottom) {
                TerminalView(sessionID: sessionID)
                    .ignoresSafeArea(edges: .bottom)

                if let session, !session.isConnected, let text = statusText(session) {
                    statusOverlay(session, text: text)
                }

                if let prompt {
                    PromptView(prompt: prompt)
                        .environmentObject(bridge)
                        .padding()
                        .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
            .animation(.easeOut(duration: 0.18), value: prompt != nil)
            .navigationTitle(session?.title ?? "Session")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    if let session {
                        Text(session.address)
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { showingCompletion = true } label: {
                        Image(systemName: "lightbulb")
                    }
                    .disabled(session?.isConnected != true)
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { showingFiles = true } label: {
                        Image(systemName: "folder")
                    }
                    .disabled(session?.isConnected != true)
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { showingAssistant = true } label: {
                        Image(systemName: "sparkles")
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        bridge.closeSession(sessionID)
                        dismiss()
                    } label: {
                        Image(systemName: "xmark")
                    }
                }
            }
        }
        .sheet(isPresented: $showingAssistant) {
            AIChatView(sessionID: sessionID)
                .environmentObject(bridge)
        }
        .sheet(isPresented: $showingFiles) {
            SFTPBrowserView(sessionID: sessionID)
                .environmentObject(bridge)
        }
        .sheet(isPresented: $showingCompletion) {
            CompletionView(sessionID: sessionID)
                .environmentObject(bridge)
        }
    }

    private func statusText(_ session: SessionInfo) -> LocalizedStringKey? {
        switch session.phase {
        case "connecting": return "Connecting to \(session.address)…"
        case "authenticating": return "Authenticating…"
        case "hostkey": return "Confirm the host key below"
        case "password": return "Waiting for a password"
        case "passphrase": return "Waiting for a key passphrase"
        case "failed": return session.error.map { LocalizedStringKey($0) } ?? "Connection failed"
        case "closed": return session.error.map { LocalizedStringKey($0) } ?? "Connection closed"
        default: return nil
        }
    }

    @ViewBuilder
    private func statusOverlay(_ session: SessionInfo, text: LocalizedStringKey) -> some View {
        VStack(spacing: 10) {
            statusBanner(text, failed: session.phase == "failed" || session.phase == "closed")
            if session.phase == "failed" || session.phase == "closed" {
                Button {
                    bridge.reconnectSession(sessionID)
                } label: {
                    Label("Reconnect", systemImage: "arrow.clockwise")
                }
                .buttonStyle(.borderedProminent)
                .padding(.bottom, 20)
            }
        }
    }

    private func statusBanner(_ text: LocalizedStringKey, failed: Bool) -> some View {
        Text(text)
            .font(.footnote)
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .background(.regularMaterial, in: Capsule())
            .foregroundStyle(failed ? .red : .secondary)
            .padding(.bottom, 12)
    }
}
