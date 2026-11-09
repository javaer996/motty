import SwiftUI
import UIKit

private struct DesktopChromeInsetsKey: PreferenceKey {
    static let defaultValue = UIEdgeInsets.zero

    static func reduce(value: inout UIEdgeInsets, nextValue: () -> UIEdgeInsets) {
        let next = nextValue()
        value = UIEdgeInsets(top: max(value.top, next.top), left: max(value.left, next.left),
                             bottom: max(value.bottom, next.bottom), right: max(value.right, next.right))
    }
}

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
    /// The picture goes edge to edge; the controls are an overlay a three-finger
    /// tap brings back, so nothing covers the remote menubar or taskbar.
    @State private var chromeVisible = false
    @State private var showsChromeHint = false
    @State private var chromeInsets = UIEdgeInsets.zero

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

    /// The core says it is waiting for a password but the prompt event never
    /// arrived (the queue drops while the app is busy, for one): the state is
    /// enough to show the form, so the session cannot sit on a spinner.
    private var needsPassword: Bool {
        prompt == nil && state?.state == "password"
    }

    private var connected: Bool { state?.isConnected == true }

    var body: some View {
        NavigationStack {
            GeometryReader { proxy in
                ZStack {
                    Color.black.ignoresSafeArea()

                    if let state, state.isConnected {
                        DesktopSurface(
                            desktopID: state.id,
                            handle: surfaceHandle,
                            bridge: bridge,
                            viewportInsets: UIEdgeInsets(
                                top: proxy.safeAreaInsets.top + chromeInsets.top,
                                left: proxy.safeAreaInsets.leading + chromeInsets.left,
                                bottom: proxy.safeAreaInsets.bottom + chromeInsets.bottom,
                                right: proxy.safeAreaInsets.trailing + chromeInsets.right
                            ),
                            onToggleChrome: { toggleChrome() },
                            onBeginScroll: { releaseHeldKeys() }
                        )
                        .ignoresSafeArea()
                    }

                    if connected, chromeVisible {
                        chromeOverlay
                            .transition(.opacity)
                    }

                    if connected, showsChromeHint, !chromeVisible {
                        Text("Three-finger tap shows controls")
                            .font(.caption)
                            .foregroundStyle(.white)
                            .padding(.horizontal, 14)
                            .padding(.vertical, 8)
                            .background(.ultraThinMaterial, in: Capsule())
                            .frame(maxHeight: .infinity, alignment: .top)
                            .padding(.top, 18)
                            .allowsHitTesting(false)
                            .transition(.opacity)
                    }

                    if let prompt {
                        promptOverlay(prompt)
                    } else if needsPassword, let state {
                        promptOverlay(DesktopPrompt(
                            id: state.id,
                            kind: .password,
                            attempt: state.attempt,
                            address: nil,
                            fingerprint: nil,
                            known: nil,
                            changed: nil
                        ))
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
                .onPreferenceChange(DesktopChromeInsetsKey.self) { chromeInsets = $0 }
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
            .toolbar(connected ? .hidden : .visible, for: .navigationBar)
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
        .statusBarHidden(connected)
        .persistentSystemOverlays(connected ? .hidden : .automatic)
        .defersSystemGestures(on: connected ? .all : [])
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
        .onChange(of: connected) { _, value in
            guard value else {
                heldKeys.removeAll()
                chromeVisible = false
                showsChromeHint = false
                return
            }
            // The controls are there to be used: show them on connection and
            // let the eye button put them away for a picture with no rails.
            chromeVisible = true
            showsChromeHint = false
        }
        .onReceive(Timer.publish(every: 1.0, on: .main, in: .common).autoconnect()) { _ in
            // Events carry every transition, but one dropped event must not
            // leave a connected tab showing a frozen picture, or a waiting one
            // without its password form: poll as long as the session exists.
            guard let id = desktop?.id else { return }
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
        let width = min(max(Int(source.width * UIScreen.main.scale), 320), 5120)
        let height = min(max(Int(source.height * UIScreen.main.scale), 240), 3456)
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
            let width = min(Int(size.width * UIScreen.main.scale), 5120)
            let height = min(Int(size.height * UIScreen.main.scale), 3456)
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

    private func toggleChrome() {
        withAnimation(.easeInOut(duration: 0.18)) {
            chromeVisible.toggle()
        }
        guard !chromeVisible else { return }
        // Hidden, the controls need a reminder of how they come back.
        showsChromeHint = true
        Task {
            try? await Task.sleep(for: .seconds(3))
            withAnimation(.easeOut(duration: 0.3)) { showsChromeHint = false }
        }
    }

    /// Close, the protocol badge and the key bar, laid over the picture rather
    /// than shrinking it. Portrait puts the rails above and below the picture;
    /// landscape puts them on its sides, so the remote menubar and taskbar keep
    /// the screen's top and bottom edges to themselves.
    @ViewBuilder
    private var chromeOverlay: some View {
        if isLandscape {
            HStack(spacing: 0) {
                VStack(spacing: 10) {
                    closeButton
                    refreshButton
                    hideChromeButton
                    Spacer(minLength: 0)
                }
                .padding(.vertical, 12)
                .padding(.horizontal, 8)
                .background(chromeMeasurement(.leading))
                .background(.ultraThinMaterial, ignoresSafeAreaEdges: [.top, .bottom, .leading])

                Spacer(minLength: 0)

                verticalKeyBar
                    .background(chromeMeasurement(.trailing))
                    .background(.bar, ignoresSafeAreaEdges: [.top, .bottom, .trailing])
            }
        } else {
            VStack(spacing: 0) {
                HStack(spacing: 12) {
                    closeButton
                    if let state {
                        protocolBadge(state)
                    }
                    Spacer()
                    refreshButton
                    hideChromeButton
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 8)
                .background(chromeMeasurement(.top))
                .background(.ultraThinMaterial, ignoresSafeAreaEdges: [.top, .leading, .trailing])

                Spacer(minLength: 0)

                horizontalKeyBar
                    .background(chromeMeasurement(.bottom))
                    .background(.bar, ignoresSafeAreaEdges: [.bottom, .leading, .trailing])
            }
        }
    }

    private var isLandscape: Bool { viewSize.width > viewSize.height }

    private func chromeMeasurement(_ edge: Edge) -> some View {
        GeometryReader { geometry in
            Color.clear.preference(key: DesktopChromeInsetsKey.self, value: UIEdgeInsets(
                top: edge == .top ? geometry.size.height : 0,
                left: edge == .leading ? geometry.size.width : 0,
                bottom: edge == .bottom ? geometry.size.height : 0,
                right: edge == .trailing ? geometry.size.width : 0
            ))
        }
    }

    private var closeButton: some View {
        Button {
            disconnect()
        } label: {
            chromeIcon("xmark")
        }
        .accessibilityLabel("Close")
    }

    private var refreshButton: some View {
        Button {
            if let id = state?.id {
                bridge.desktopRefresh(desktopID: id)
            }
        } label: {
            chromeIcon("arrow.clockwise")
        }
        .accessibilityLabel("Refresh picture")
    }

    private var hideChromeButton: some View {
        Button {
            toggleChrome()
        } label: {
            chromeIcon("eye.slash")
        }
        .accessibilityLabel("Hide controls")
    }

    private func chromeIcon(_ name: String) -> some View {
        Image(systemName: name)
            .font(.system(size: 15, weight: .semibold))
            .foregroundStyle(.white)
            .frame(width: 32, height: 32)
            .background(.black.opacity(0.45), in: Circle())
    }

    private func protocolBadge(_ state: DesktopState) -> some View {
        Label {
            Text(state.encrypted ? "\(state.protocolText) · encrypted" : state.protocolText)
                .font(.caption)
                .lineLimit(1)
        } icon: {
            Image(systemName: state.encrypted ? "lock.fill" : "exclamationmark.triangle")
        }
        .font(.caption)
        .foregroundStyle(.white.opacity(0.9))
    }

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

    /// The key bar of a portrait phone: a strip along the bottom.
    private var horizontalKeyBar: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) { keyBarButtons }
                .padding(.horizontal, 12)
        }
        .frame(height: 44)
    }

    /// The key bar of a landscape phone: a column down the side, which leaves
    /// the wide screen its full height.
    private var verticalKeyBar: some View {
        ScrollView(.vertical, showsIndicators: false) {
            VStack(spacing: 6) {
                keyBarButtons
            }
            .padding(.vertical, 12)
        }
        .frame(width: 46)
    }

    @ViewBuilder
    private var keyBarButtons: some View {
        symbolButton("keyboard", label: "Keyboard") {
            surfaceHandle.toggleKeyboard()
        }
        Divider()
        textButton("esc") { sendKey("escape") }
        textButton("tab") { sendKey("tab") }
        modifierButton("control", label: "ctrl")
        modifierButton("alt", label: "alt")
        modifierButton("meta", label: "cmd")
        Divider()
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
                .frame(minWidth: 30, minHeight: 34)
                .padding(.horizontal, 4)
                .contentShape(Rectangle())
        }
        .buttonStyle(.borderless)
        Divider()
        symbolButton("doc.on.clipboard", label: "Clipboard") {
            localClipboardText = UIPasteboard.general.string ?? ""
            showingClipboard = true
        }
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
