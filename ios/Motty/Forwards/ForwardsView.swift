import SwiftUI

/// Saved tunnels and their live state. The Go core owns the listeners; this
/// screen starts and stops them and polls `forwards.status` for traffic.
struct ForwardsView: View {
    @EnvironmentObject private var bridge: MottyBridge
    @Environment(\.dismiss) private var dismiss

    @State private var forwards: [PortForward] = []
    @State private var errorText: String?
    @State private var target: EditorTarget?

    private let timer = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    private enum EditorTarget: Identifiable {
        case new
        case existing(PortForward)

        var id: String {
            switch self {
            case .new: return "new"
            case .existing(let forward): return forward.id
            }
        }
    }

    var body: some View {
        NavigationStack {
            List {
                if forwards.isEmpty {
                    ContentUnavailableView {
                        Label("No port forwards", systemImage: "network")
                    } description: {
                        Text("Tunnels appear here once you add one.")
                    } actions: {
                        Button("Add a forward") { target = .new }
                    }
                }
                ForEach(forwards) { forward in
                    row(forward)
                }
            }
            .navigationTitle("Port forwards")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("Done") { dismiss() }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { target = .new } label: {
                        Image(systemName: "plus")
                    }
                }
            }
            .refreshable { refresh() }
            .onAppear { refresh() }
            .onReceive(timer) { _ in refresh() }
            .sheet(item: $target) { target in
                switch target {
                case .new:
                    ForwardEditorView(forward: nil, hosts: bridge.hosts) { save($0, id: nil) }
                        .environmentObject(bridge)
                case .existing(let forward):
                    ForwardEditorView(forward: forward, hosts: bridge.hosts) { save($0, id: forward.id) }
                        .environmentObject(bridge)
                }
            }
            .alert("Port forwards", isPresented: errorBinding) {
                Button("OK") { errorText = nil }
            } message: {
                Text(errorText ?? "")
            }
        }
    }

    private func row(_ forward: PortForward) -> some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack(spacing: 8) {
                Circle()
                    .fill(stateColor(forward))
                    .frame(width: 8, height: 8)
                Group {
                    if let label = forward.label, !label.isEmpty {
                        Text(verbatim: label)
                    } else {
                        Text(LocalizedStringKey(forward.modeText))
                    }
                }
                .font(.headline)
                Text(LocalizedStringKey(forward.modeText))
                    .font(.caption2.weight(.semibold))
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(.quaternary, in: Capsule())
                    .foregroundStyle(.secondary)
                Spacer()
                Button {
                    toggle(forward)
                } label: {
                    Image(systemName: forward.showsActivity ? "stop.circle" : "play.circle")
                        .font(.title3)
                }
                .buttonStyle(.borderless)
            }
            (Text(forward.listenAddress) + Text(" → ") + Text(LocalizedStringKey(forward.targetText)))
                .font(.caption.monospaced())
                .foregroundStyle(.secondary)
                .lineLimit(1)
            HStack(spacing: 6) {
                hostLabelView(forward.hostId)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
                Text(stateText(forward))
                    .font(.caption)
                    .foregroundStyle(stateColor(forward))
            }
            if forward.isRunning {
                Text(trafficText(forward))
                    .font(.caption2.monospaced())
                    .foregroundStyle(.tertiary)
            }
            if let message = forward.error, !message.isEmpty {
                Text(message)
                    .font(.caption2)
                    .foregroundStyle(.red)
                    .lineLimit(3)
            }
        }
        .padding(.vertical, 3)
        .contentShape(Rectangle())
        .onTapGesture { target = .existing(forward) }
        .swipeActions(edge: .trailing, allowsFullSwipe: false) {
            Button(role: .destructive) {
                remove(forward)
            } label: {
                Label("Delete", systemImage: "trash")
            }
            Button {
                target = .existing(forward)
            } label: {
                Label("Edit", systemImage: "pencil")
            }
            .tint(.blue)
        }
    }

    private func stateColor(_ forward: PortForward) -> Color {
        switch forward.state {
        case "running": return .green
        case "starting": return .orange
        case "failed": return .red
        default: return .secondary
        }
    }

    private func stateText(_ forward: PortForward) -> LocalizedStringKey {
        switch forward.state {
        case "running":
            return forward.active > 0 ? "Running · \(forward.active) active" : "Running"
        case "starting": return "Starting…"
        case "failed": return "Failed"
        default: return forward.autoStart ? "Stopped · auto-start" : "Stopped"
        }
    }

    private func trafficText(_ forward: PortForward) -> LocalizedStringKey {
        let up = ByteCountFormatter.string(fromByteCount: Int64(forward.uploaded), countStyle: .file)
        let down = ByteCountFormatter.string(fromByteCount: Int64(forward.downloaded), countStyle: .file)
        let streams = Int(forward.accepted)
        return streams == 1
            ? "↑\(up) ↓\(down) · \(streams) stream"
            : "↑\(up) ↓\(down) · \(streams) streams"
    }

    @ViewBuilder
    private func hostLabelView(_ id: String) -> some View {
        if let label = bridge.hosts.first(where: { $0.id == id })?.label, !label.isEmpty {
            Text(verbatim: label)
        } else {
            Text("Missing host")
        }
    }

    private func toggle(_ forward: PortForward) {
        if forward.showsActivity {
            bridge.stopForward(id: forward.id) { error in
                if let error { errorText = error.localizedDescription } else { refresh() }
            }
            return
        }
        let sessionID = bridge.sessions.first {
            $0.hostId == forward.hostId && $0.isConnected
        }?.id ?? 0
        bridge.startForward(id: forward.id, sessionID: sessionID) { result in
            switch result {
            case .success(let updated):
                if updated.state == "failed", let message = updated.error, !message.isEmpty {
                    errorText = message
                }
                refresh()
            case .failure(let error):
                errorText = error.localizedDescription
            }
        }
    }

    private func save(_ params: MottyBridge.ForwardParams, id: String?) {
        let completion: (Result<PortForward, Error>) -> Void = { result in
            target = nil
            if case .failure(let error) = result {
                errorText = error.localizedDescription
            }
            refresh()
        }
        if let id {
            bridge.updateForward(id: id, params, completion: completion)
        } else {
            bridge.addForward(params, completion: completion)
        }
    }

    private func remove(_ forward: PortForward) {
        bridge.removeForward(id: forward.id) { error in
            if let error { errorText = error.localizedDescription } else { refresh() }
        }
    }

    private func refresh() {
        bridge.listForwards { result in
            if case .success(let list) = result {
                forwards = list
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

private struct ForwardEditorView: View {
    let forward: PortForward?
    let hosts: [Host]
    let onSave: (MottyBridge.ForwardParams) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var label = ""
    @State private var hostID = ""
    @State private var mode = "local"
    @State private var localPort = ""
    @State private var destination = ""
    @State private var autoStart = false
    @State private var errorText: String?

    private let modes = ["local", "remote", "socks5", "http-connect"]

    private var usesDestination: Bool {
        mode == "local" || mode == "remote"
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Picker("Host", selection: $hostID) {
                        ForEach(hosts) { host in
                            Text("\(host.label) (\(host.user)@\(host.target))")
                                .tag(host.id)
                        }
                    }
                    Picker("Mode", selection: $mode) {
                        Text("Local").tag("local")
                        Text("Remote").tag("remote")
                        Text("SOCKS5").tag("socks5")
                        Text("HTTP CONNECT").tag("http-connect")
                    }
                    TextField("Label", text: $label)
                }
                Section {
                    TextField("Local port", text: $localPort)
                        .keyboardType(.numberPad)
                    if usesDestination {
                        TextField("Destination host:port", text: $destination)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                    }
                } footer: {
                    if usesDestination {
                        Text("The listener binds 127.0.0.1 on this device and reaches the destination through SSH.")
                    } else {
                        Text("The listener speaks the proxy protocol; the client picks each destination.")
                    }
                }
                Section {
                    Toggle("Start when the host connects", isOn: $autoStart)
                }
            }
            .navigationTitle(forward == nil ? "New forward" : "Edit forward")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { save() }
                        .disabled(hostID.isEmpty)
                }
            }
            .onAppear(perform: load)
            .alert("Port forward", isPresented: errorBinding) {
                Button("OK") { errorText = nil }
            } message: {
                Text(errorText ?? "")
            }
        }
    }

    private func load() {
        guard let forward else {
            hostID = hosts.first?.id ?? ""
            return
        }
        label = forward.label ?? ""
        hostID = forward.hostId
        mode = forward.mode
        localPort = String(forward.listenAddress.split(separator: ":").last ?? "")
        destination = forward.destination ?? ""
        autoStart = forward.autoStart
    }

    private func save() {
        guard !hostID.isEmpty else {
            errorText = AppLanguage.localized("Choose a host first.")
            return
        }
        guard let port = UInt16(localPort), port > 0 else {
            errorText = AppLanguage.localized("Enter a port between 1 and 65535.")
            return
        }
        let trimmedDestination = destination.trimmingCharacters(in: .whitespaces)
        if usesDestination {
            let parts = trimmedDestination.split(separator: ":", omittingEmptySubsequences: false)
            guard parts.count == 2,
                  !parts[0].isEmpty,
                  let destinationPort = UInt16(parts[1]),
                  destinationPort > 0 else {
                errorText = AppLanguage.localized("Enter the destination as host:port.")
                return
            }
        }
        let params = MottyBridge.ForwardParams(
            label: label.trimmingCharacters(in: .whitespaces),
            hostId: hostID,
            mode: mode,
            localPort: port,
            destination: usesDestination ? trimmedDestination : "",
            autoStart: autoStart
        )
        onSave(params)
        dismiss()
    }

    private var errorBinding: Binding<Bool> {
        Binding(
            get: { errorText != nil },
            set: { if !$0 { errorText = nil } }
        )
    }
}
