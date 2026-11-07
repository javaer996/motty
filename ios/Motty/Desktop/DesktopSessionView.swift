import SwiftUI
import UIKit

/// One VNC or RDP tab. The picture lives in DesktopSurface; this view adds the
/// connection states, the password and certificate questions, the key bar and
/// the clipboard.
struct DesktopSessionView: View {
    let host: Host

    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var scenePhase

    @State private var desktop: DesktopState?
    @State private var errorText: String?
    @State private var password = ""
    @State private var rememberPassword = true
    @State private var heldKeys: Set<String> = []
    @State private var viewSize = CGSize.zero
    @State private var resizeTask: Task<Void, Never>?
    @State private var showingClipboard = false
    @State private var localClipboardText = ""
    @State private var surfaceHandle = DesktopSurfaceHandle()

    private var state: DesktopState? {
        if let desktop, let live = bridge.desktops[desktop.id] {
            return live
        }
        return desktop
    }

    private var prompt: DesktopPrompt? {
        guard let id = desktop?.id else { return nil }
        return bridge.desktopPrompts[id]
    }

    private var connected: Bool { state?.isConnected == true }

    var body: some View {
        NavigationStack {
            GeometryReader { proxy in
                ZStack {
                    Color.black.ignoresSafeArea()

                    if let state, state.isConnected {
                        DesktopSurface(desktopID: state.id, handle: surfaceHandle, bridge: bridge)
                            .ignoresSafeArea(edges: .bottom)
                    }

                    if let prompt {
                        promptOverlay(prompt)
                    } else if let state, state.isWaiting {
                        statusOverlay(state, failed: false)
                    } else if let state, state.state == "failed" {
                        statusOverlay(state, failed: true)
                    } else if errorText != nil {
                        statusOverlay(DesktopState(
                            id: 0, hostId: host.id, label: host.label, address: host.address,
                            protocol: host.protocolName, state: "failed", error: errorText,
                            name: nil, encrypted: false, width: 0, height: 0, attempt: nil
                        ), failed: true)
                    }
                }
                .onAppear {
                    if viewSize == .zero { viewSize = proxy.size }
                }
                .onChange(of: proxy.size) { _, size in
                    guard size != viewSize else { return }
                    viewSize = size
                    scheduleResize()
                }
            }
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    if let state, state.isConnected {
                        Label {
                            if state.encrypted {
                                Text("\(state.protocolText) · encrypted")
                            } else {
                                Text(verbatim: state.protocolText)
                            }
                        } icon: {
                            Image(systemName: state.encrypted ? "lock.fill" : "exclamationmark.triangle")
                        }
                        .font(.caption)
                        .foregroundStyle(state.encrypted ? Color.secondary : Color.orange)
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        disconnect()
                    } label: {
                        Image(systemName: "xmark")
                    }
                }
            }
        }
        .safeAreaInset(edge: .bottom) {
            if connected {
                keyBar
            }
        }
        .sheet(isPresented: $showingClipboard) {
            clipboardSheet
        }
        .task {
            await start()
        }
        .onDisappear {
            releaseHeldKeys()
            resizeTask?.cancel()
            if let id = desktop?.id {
                bridge.closeDesktop(id)
            }
        }
        .onChange(of: scenePhase) { _, phase in
            guard let id = desktop?.id else { return }
            bridge.desktopActive(desktopID: id, active: phase == .active)
        }
        .onChange(of: state?.state) { _, value in
            if value == "closed" { dismiss() }
        }
        .onReceive(Timer.publish(every: 1.0, on: .main, in: .common).autoconnect()) { _ in
            // Events carry every transition; this poll is the safety net for one
            // that the queue dropped while the app was busy.
            guard let id = desktop?.id, let state, state.isWaiting else { return }
            bridge.refreshDesktop(id) { _ in }
        }
        .preferredColorScheme(.dark)
    }

    private var title: String {
        if let name = state?.name, !name.isEmpty { return name }
        return host.label
    }

    // MARK: Connection

    private func start() async {
        errorText = nil
        let scale = Double(UIScreen.main.scale)
        let source = viewSize == .zero ? UIScreen.main.bounds.size : viewSize
        let width = min(max(Int(source.width * UIScreen.main.scale), 320), 2732)
        let height = min(max(Int(source.height * UIScreen.main.scale), 240), 2048)
        do {
            desktop = try await bridge.openDesktop(host: host, width: width, height: height, scale: scale)
        } catch {
            errorText = error.localizedDescription
        }
    }

    private func scheduleResize() {
        guard let id = desktop?.id, connected, viewSize.width > 0, viewSize.height > 0 else { return }
        resizeTask?.cancel()
        let size = viewSize
        resizeTask = Task {
            // A rotation settles before the server is asked for a new desktop.
            try? await Task.sleep(for: .milliseconds(350))
            if Task.isCancelled { return }
            let width = min(Int(size.width * UIScreen.main.scale), 2732)
            let height = min(Int(size.height * UIScreen.main.scale), 2048)
            bridge.desktopResize(
                desktopID: id,
                width: width,
                height: height,
                scale: Double(UIScreen.main.scale)
            )
        }
    }

    private func disconnect() {
        releaseHeldKeys()
        if let id = desktop?.id {
            bridge.closeDesktop(id)
        }
        dismiss()
    }

    // MARK: Overlays

    @ViewBuilder
    private func statusOverlay(_ state: DesktopState, failed: Bool) -> some View {
        VStack(spacing: 14) {
            if failed {
                Image(systemName: "exclamationmark.triangle.fill")
                    .font(.largeTitle)
                    .foregroundStyle(.orange)
            } else {
                ProgressView()
                    .controlSize(.large)
            }
            Text(LocalizedStringKey(failed ? "Connection failed" : state.statusText))
                .font(.headline)
            if failed, let message = state.error ?? errorText, !message.isEmpty {
                Text(message)
                    .font(.callout)
                    .multilineTextAlignment(.center)
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: 420)
            }
            if let name = state.name, !name.isEmpty {
                Text(name)
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            }
            if failed {
                HStack {
                    Button("Close") { disconnect() }
                    Button("Reconnect") {
                        if let id = desktop?.id { bridge.closeDesktop(id) }
                        desktop = nil
                        Task { await start() }
                    }
                    .buttonStyle(.borderedProminent)
                }
            } else {
                Button("Cancel") { disconnect() }
                    .buttonStyle(.bordered)
            }
        }
        .padding(24)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 20))
        .padding()
    }

    @ViewBuilder
    private func promptOverlay(_ prompt: DesktopPrompt) -> some View {
        VStack(spacing: 14) {
            Image(systemName: prompt.kind == .password ? "lock.fill" : "checkmark.shield.fill")
                .font(.largeTitle)
                .foregroundStyle(.tint)
            if prompt.kind == .password {
                Text("Password required")
                    .font(.headline)
                Text(host.address)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                SecureField("Password", text: $password)
                    .textFieldStyle(.roundedBorder)
                    .textContentType(.password)
                    .frame(maxWidth: 320)
                    .onSubmit { submitPassword() }
                Toggle("Remember password", isOn: $rememberPassword)
                    .frame(maxWidth: 320)
                HStack {
                    Button("Cancel") { disconnect() }
                    Button("Connect") { submitPassword() }
                        .buttonStyle(.borderedProminent)
                        .disabled(password.isEmpty)
                }
                if let name = state?.name, !name.isEmpty {
                    Text(name)
                        .font(.caption2)
                        .foregroundStyle(.tertiary)
                }
            } else {
                Text(LocalizedStringKey(prompt.changed == true ? "The certificate changed" : "New certificate"))
                    .font(.headline)
                Text(prompt.address ?? host.address)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                Text(prompt.fingerprint ?? "")
                    .font(.caption2.monospaced())
                    .textSelection(.enabled)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: 420)
                if prompt.changed == true, let known = prompt.known?.first {
                    Text("Previously trusted: \(known)")
                        .font(.caption2.monospaced())
                        .foregroundStyle(.orange)
                        .textSelection(.enabled)
                        .multilineTextAlignment(.center)
                    Text("Only continue if you know why the server's certificate changed.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                        .frame(maxWidth: 420)
                }
                HStack {
                    Button("Cancel", role: .cancel) { rejectCertificate() }
                    Button("Trust Once") { acceptCertificate(save: false) }
                    Button("Trust & Save") { acceptCertificate(save: true) }
                        .buttonStyle(.borderedProminent)
                }
            }
        }
        .padding(24)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 20))
        .padding()
    }

    private func submitPassword() {
        guard let id = desktop?.id, !password.isEmpty else { return }
        // The core writes the password only after the server accepts it, so a
        // refusal never overwrites the saved one.
        bridge.desktopAnswer(
            desktopID: id, kind: "password", value: password, save: rememberPassword
        )
        password = ""
    }

    private func acceptCertificate(save: Bool) {
        guard let id = desktop?.id else { return }
        bridge.desktopAnswer(desktopID: id, kind: "certificate", accept: true, save: save)
    }

    private func rejectCertificate() {
        guard let id = desktop?.id else { return }
        bridge.desktopAnswer(desktopID: id, kind: "certificate", accept: false)
    }

    // MARK: Key bar

    private var keyBar: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) {
                symbolButton("keyboard", label: "Keyboard") {
                    surfaceHandle.toggleKeyboard()
                }
                Divider().frame(height: 22)
                textButton("esc") { sendKey("escape") }
                textButton("tab") { sendKey("tab") }
                modifierButton("control", label: "ctrl")
                modifierButton("alt", label: "alt")
                modifierButton("meta", label: "cmd")
                Divider().frame(height: 22)
                symbolButton("arrow.left", label: "Left") { sendKey("left") }
                symbolButton("arrow.down", label: "Down") { sendKey("down") }
                symbolButton("arrow.up", label: "Up") { sendKey("up") }
                symbolButton("arrow.right", label: "Right") { sendKey("right") }
                Menu {
                    ForEach(1...12, id: \.self) { number in
                        Button("F\(number)") { sendKey("f\(number)") }
                    }
                    Divider()
                    Button("Home") { sendKey("home") }
                    Button("End") { sendKey("end") }
                    Button("Page Up") { sendKey("pageup") }
                    Button("Page Down") { sendKey("pagedown") }
                    Button("Insert") { sendKey("insert") }
                    Button("Delete") { sendKey("delete") }
                    Divider()
                    Button("Ctrl+Alt+Del") {
                        chord(modifiers: ["control", "alt"], key: "delete")
                    }
                    Button("Ctrl+C") { chord(modifiers: ["control"], key: "c") }
                    Button("Ctrl+V") { chord(modifiers: ["control"], key: "v") }
                    Button("Ctrl+Z") { chord(modifiers: ["control"], key: "z") }
                } label: {
                    Image(systemName: "ellipsis.circle")
                        .font(.system(size: 16))
                        .padding(.horizontal, 8)
                        .frame(height: 34)
                }
                Divider().frame(height: 22)
                symbolButton("doc.on.clipboard", label: "Clipboard") {
                    localClipboardText = UIPasteboard.general.string ?? ""
                    showingClipboard = true
                }
            }
            .padding(.horizontal, 12)
        }
        .frame(height: 44)
        .background(.bar)
    }

    private func symbolButton(_ systemImage: String, label: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.system(size: 16))
                .frame(minWidth: 30, minHeight: 34)
                .padding(.horizontal, 4)
                .contentShape(Rectangle())
        }
        .buttonStyle(.borderless)
        .accessibilityLabel(label)
    }

    private func textButton(_ title: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title)
                .font(.caption.monospaced())
                .padding(.horizontal, 8)
                .frame(height: 30)
                .overlay(
                    RoundedRectangle(cornerRadius: 6)
                        .stroke(Color.secondary.opacity(0.35))
                )
        }
        .buttonStyle(.plain)
    }

    private func modifierButton(_ name: String, label: String) -> some View {
        Button {
            toggleModifier(name)
        } label: {
            Text(label)
                .font(.caption.monospaced())
                .padding(.horizontal, 8)
                .frame(height: 30)
                .background(
                    heldKeys.contains(name) ? Color.accentColor.opacity(0.35) : Color.clear,
                    in: RoundedRectangle(cornerRadius: 6)
                )
                .overlay(
                    RoundedRectangle(cornerRadius: 6)
                        .stroke(heldKeys.contains(name) ? Color.accentColor : Color.secondary.opacity(0.35))
                )
        }
        .buttonStyle(.plain)
    }

    private func sendKey(_ name: String) {
        guard let id = desktop?.id else { return }
        bridge.desktopKey(desktopID: id, key: name, down: true)
        bridge.desktopKey(desktopID: id, key: name, down: false)
    }

    private func toggleModifier(_ name: String) {
        guard let id = desktop?.id else { return }
        if heldKeys.contains(name) {
            bridge.desktopKey(desktopID: id, key: name, down: false)
            heldKeys.remove(name)
        } else {
            bridge.desktopKey(desktopID: id, key: name, down: true)
            heldKeys.insert(name)
        }
    }

    private func releaseHeldKeys() {
        guard let id = desktop?.id else { return }
        for name in heldKeys {
            bridge.desktopKey(desktopID: id, key: name, down: false)
        }
        heldKeys.removeAll()
    }

    /// Types one shortcut as modifier presses around a key or character.
    private func chord(modifiers names: [String], key: String) {
        guard let id = desktop?.id else { return }
        for name in names {
            bridge.desktopKey(desktopID: id, key: name, down: true)
        }
        if key.count == 1 {
            bridge.desktopKey(desktopID: id, rune: key, typed: key, down: true)
            bridge.desktopKey(desktopID: id, rune: key, typed: key, down: false)
        } else {
            bridge.desktopKey(desktopID: id, key: key, down: true)
            bridge.desktopKey(desktopID: id, key: key, down: false)
        }
        for name in names.reversed() {
            bridge.desktopKey(desktopID: id, key: name, down: false)
        }
    }

    // MARK: Clipboard

    private var clipboardSheet: some View {
        NavigationStack {
            Form {
                Section("Remote clipboard") {
                    if let id = desktop?.id, let text = bridge.desktopClipboard[id], !text.isEmpty {
                        Text(text)
                            .font(.callout.monospaced())
                            .lineLimit(6)
                            .textSelection(.enabled)
                        Button("Copy to this iPhone") {
                            UIPasteboard.general.string = text
                        }
                    } else {
                        Text("The remote system has not put any text on its clipboard yet.")
                            .foregroundStyle(.secondary)
                    }
                    Button("Read again") {
                        guard let id = desktop?.id else { return }
                        bridge.desktopGetClipboard(desktopID: id) { result in
                            if case .success(let text) = result, !text.isEmpty {
                                UIPasteboard.general.string = text
                            }
                        }
                    }
                }
                Section("Send to the remote") {
                    TextEditor(text: $localClipboardText)
                        .frame(minHeight: 120)
                        .font(.body.monospaced())
                    Button("Use this iPhone's clipboard") {
                        localClipboardText = UIPasteboard.general.string ?? ""
                    }
                    Button("Send") {
                        guard let id = desktop?.id else { return }
                        bridge.desktopSetClipboard(desktopID: id, text: localClipboardText)
                    }
                    .disabled(localClipboardText.isEmpty)
                }
            }
            .navigationTitle("Clipboard")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                Button("Done") { showingClipboard = false }
            }
        }
    }
}
